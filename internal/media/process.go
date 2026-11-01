package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"multiparse/internal/model"
)

const localFormats = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,mpegts,mpeg,avi,flv,ogg,mp3,aac,wav,flac,gif,image2,jpeg_pipe,png_pipe,webp_pipe,avif,heif"

type probeStream struct {
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	PixelFormat string `json:"pix_fmt"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Duration    string `json:"duration"`
}
type probeResult struct {
	Streams []probeStream `json:"streams"`
	Format  struct {
		Name     string `json:"format_name"`
		Duration string `json:"duration"`
	} `json:"format"`
}

func (r probeResult) video() *probeStream {
	for i := range r.Streams {
		if r.Streams[i].CodecType == "video" {
			return &r.Streams[i]
		}
	}
	return nil
}
func (r probeResult) audio() *probeStream {
	for i := range r.Streams {
		if r.Streams[i].CodecType == "audio" {
			return &r.Streams[i]
		}
	}
	return nil
}
func (r probeResult) duration() (float64, error) {
	duration := 0.0
	for _, raw := range append([]string{r.Format.Duration}, streamDurations(r.Streams)...) {
		if raw == "" || raw == "N/A" {
			continue
		}
		d, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(d) || math.IsInf(d, 0) || d < 0 {
			return 0, errors.New("媒体时长无效")
		}
		if d > duration {
			duration = d
		}
	}
	return duration, nil
}
func streamDurations(streams []probeStream) []string {
	out := make([]string, 0, len(streams))
	for _, s := range streams {
		out = append(out, s.Duration)
	}
	return out
}
func inputOptions(path string) []string {
	formats := localFormats
	if strings.HasSuffix(path, ".m3u8") {
		formats = "hls," + formats
	}
	options := []string{"-protocol_whitelist", "file,crypto", "-format_whitelist", formats}
	if strings.HasSuffix(path, ".m3u8") {
		// Segment demuxers must not interpret another playlist or local-file script.
		options = append(options, "-allowed_extensions", "ALL", "-seg_format_options", "format_whitelist="+localFormats+":protocol_whitelist=file,crypto")
	}
	return options
}
func processEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C", "HOME=/nonexistent"}
}

type cappedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if b.Len() < b.limit {
		b.Buffer.Write(data[:min(n, b.limit-b.Len())])
	}
	if b.Len() == b.limit {
		b.exceeded = true
	}
	return n, nil
}

func (p *preparation) probe(ctx context.Context, path string) (probeResult, error) {
	var result probeResult
	release, err := p.manager.acquire(ctx, true, p.cfg.Media.TranscodeConcurrency)
	if err != nil {
		return result, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-threads", "2"}
	args = append(args, inputOptions(path)...)
	args = append(args, "-show_entries", "stream=codec_type,codec_name,pix_fmt,width,height,duration:format=format_name,duration", "-of", "json", path)
	command := exec.CommandContext(ctx, "ffprobe", args...)
	command.Env = processEnv()
	stdout := &cappedBuffer{limit: 1 << 20}
	stderr := &cappedBuffer{limit: 8 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return result, errors.New("未安装ffprobe")
		}
		return result, errors.New("ffprobe无法识别媒体或媒体文件已损坏")
	}
	if stdout.exceeded {
		return result, errors.New("ffprobe输出超出限制")
	}
	if err = json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return result, errors.New("ffprobe返回无效媒体信息")
	}
	if len(result.Streams) == 0 || len(result.Streams) > 32 {
		return result, errors.New("媒体流数量无效")
	}
	duration, err := result.duration()
	if err != nil {
		return result, err
	}
	if duration > float64(p.cfg.Media.MaxDurationSeconds) {
		return result, errors.New("媒体实际时长超过限制")
	}
	if video := result.video(); video != nil {
		if video.Width < 1 || video.Height < 1 || video.Width > 16384 || video.Height > 16384 || int64(video.Width)*int64(video.Height) > 40_000_000 {
			return result, errors.New("媒体分辨率超出安全限制")
		}
	}
	return result, nil
}

type mediaWriter struct {
	file    *os.File
	prep    *preparation
	limit   int64
	written int64
	cancel  context.CancelFunc
	failure error
}

func (w *mediaWriter) Write(data []byte) (int, error) {
	if w.failure != nil {
		return 0, w.failure
	}
	if int64(len(data)) > w.limit-w.written {
		w.failure = errors.New("处理后的媒体超过大小限制")
		w.cancel()
		return 0, w.failure
	}
	if err := w.prep.claim(int64(len(data))); err != nil {
		w.failure = err
		w.cancel()
		return 0, err
	}
	n, err := w.file.Write(data)
	w.written += int64(n)
	w.prep.unclaim(int64(len(data) - n))
	if err != nil {
		w.failure = err
		w.cancel()
	}
	return n, err
}
func (p *preparation) ffmpeg(ctx context.Context, args []string, extension string, limit int64) (string, error) {
	release, err := p.manager.acquire(ctx, true, p.cfg.Media.TranscodeConcurrency)
	if err != nil {
		return "", err
	}
	defer release()
	timeout := time.Duration(p.cfg.Media.MaxDurationSeconds)*2*time.Second + time.Minute
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}
	if timeout < time.Minute {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	path := p.next(extension)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	writer := &mediaWriter{file: file, prep: p, limit: limit, cancel: cancel}
	success := false
	defer func() {
		file.Close()
		if !success {
			if os.Remove(path) == nil {
				p.unclaim(writer.written)
			}
		}
	}()
	commandArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "2", "-filter_threads", "1", "-filter_complex_threads", "1"}
	commandArgs = append(commandArgs, args...)
	commandArgs = append(commandArgs, "-threads", "2", "pipe:1")
	command := exec.CommandContext(ctx, "ffmpeg", commandArgs...)
	command.Env = processEnv()
	command.Stdout = writer
	command.Stderr = &cappedBuffer{limit: 8 << 10}
	if err = command.Run(); err != nil {
		if writer.failure != nil {
			return "", writer.failure
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("未安装ffmpeg")
		}
		return "", errors.New("ffmpeg媒体处理失败")
	}
	if writer.written == 0 {
		return "", errors.New("ffmpeg未产生媒体")
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	success = true
	return path, nil
}

func (p *preparation) prepareItem(ctx context.Context, item model.Media, headers map[string]string) (model.Artifact, error) {
	var empty model.Artifact
	if item.Kind == "livephoto" {
		return p.prepareLivePhoto(ctx, item, headers)
	}
	if item.URL == "" {
		return empty, errors.New("媒体缺少下载地址")
	}
	var input, finalURL string
	var err error
	urls := append([]string{item.URL}, item.BackupURLs...)
	if len(urls) > 9 {
		return empty, errors.New("媒体备用地址数量超过限制")
	}
	for _, candidate := range urls {
		input, finalURL, err = p.download(ctx, candidate, headers, p.hardLimit(), ".bin")
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
	}
	if err != nil {
		return empty, err
	}
	return p.prepareDownloaded(ctx, item, input, finalURL, headers, true)
}

// prepareLocal accepts only staged media, never a source URL or a remote companion.
func (p *preparation) prepareLocal(ctx context.Context, item model.Media, input string, headers map[string]string) (model.Artifact, error) {
	if err := p.validateLocal(input); err != nil {
		return model.Artifact{}, err
	}
	if item.AudioURL != "" || item.VideoURL != "" || item.Kind == "livephoto" {
		return model.Artifact{}, errors.New("本地提取媒体不能引用远程伴随媒体")
	}
	return p.prepareDownloaded(ctx, item, input, "", headers, false)
}

func (p *preparation) prepareDownloaded(ctx context.Context, item model.Media, input, finalURL string, headers map[string]string, remote bool) (model.Artifact, error) {
	var empty model.Artifact
	var err error
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	sniff := make([]byte, 512)
	file, err := os.Open(input)
	if err != nil {
		return empty, err
	}
	n, _ := file.Read(sniff)
	file.Close()
	sniff = sniff[:n]
	hls := bytes.HasPrefix(bytes.TrimSpace(sniff), []byte("#EXTM3U"))
	if hls {
		if !remote {
			return empty, errors.New("提取器必须返回已下载媒体，不能返回播放列表")
		}
		input, err = p.localizeHLS(ctx, input, finalURL, headers, 0)
		if err != nil {
			return empty, err
		}
	} else if bytes.HasPrefix(sniff, []byte("\xff\xd8\xff")) || bytes.HasPrefix(sniff, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(sniff, []byte("GIF87a")) || bytes.HasPrefix(sniff, []byte("GIF89a")) {
		if item.AudioURL != "" {
			return empty, errors.New("图片不能合并音频")
		}
		return p.prepareImage(ctx, input)
	} else if bytes.HasPrefix(bytes.TrimSpace(sniff), []byte("<")) || bytes.HasPrefix(bytes.TrimSpace(sniff), []byte("{")) || bytes.HasPrefix(bytes.TrimSpace(sniff), []byte("#")) {
		return empty, errors.New("媒体地址返回文本而非实际媒体")
	}
	info, err := p.probe(ctx, input)
	if err != nil {
		return empty, err
	}
	video := info.video()
	audio := info.audio()
	if video == nil && audio == nil {
		return empty, errors.New("下载内容不含音视频")
	}
	if item.Kind == "image" && !hls {
		if video == nil || audio != nil {
			return empty, errors.New("图片地址实际返回非图片媒体")
		}
		imageFormat := video.CodecName == "webp" || video.CodecName == "av1" || video.CodecName == "hevc" || video.CodecName == "mjpeg" || video.CodecName == "png"
		duration, _ := info.duration()
		if !imageFormat || duration > 1 {
			return empty, errors.New("图片格式不受支持或实际是视频")
		}
		args := append(inputOptions(input), "-i", input, "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=w='min(4096,iw)':h='min(4096,ih)':force_original_aspect_ratio=decrease", "-c:v", "mjpeg", "-q:v", "3", "-f", "image2pipe")
		imagePath, err := p.ffmpeg(ctx, args, ".jpg", min(p.hardLimit(), 20<<20))
		if err != nil {
			return empty, err
		}
		if err = p.discard(input); err != nil {
			return empty, err
		}
		return p.prepareImage(ctx, imagePath)
	}
	audioInput := ""
	audioInfo := info
	if item.AudioURL != "" {
		if video == nil {
			return empty, errors.New("音频合并缺少视频流")
		}
		var finalAudio string
		audioInput, finalAudio, err = p.download(ctx, item.AudioURL, headers, p.hardLimit(), ".bin")
		if err != nil {
			return empty, err
		}
		af, openErr := os.Open(audioInput)
		if openErr != nil {
			return empty, openErr
		}
		prefix := make([]byte, 16)
		count, _ := af.Read(prefix)
		af.Close()
		if bytes.HasPrefix(bytes.TrimSpace(prefix[:count]), []byte("#EXTM3U")) {
			audioInput, err = p.localizeHLS(ctx, audioInput, finalAudio, headers, 0)
			if err != nil {
				return empty, err
			}
		}
		audioInfo, err = p.probe(ctx, audioInput)
		if err != nil {
			return empty, err
		}
		audio = audioInfo.audio()
		if audio == nil {
			return empty, errors.New("独立音频地址不含音频流")
		}
	}
	duration, err := info.duration()
	if err != nil {
		return empty, err
	}
	if other, _ := audioInfo.duration(); other > duration {
		duration = other
	}
	if duration <= 0 {
		return empty, errors.New("无法确定媒体时长，拒绝无法限制的媒体")
	}
	quality := p.cfg.Media.Quality
	if quality < 1 {
		quality = 720
	}
	if quality > 4320 {
		quality = 4320
	}
	compatibleVideo := video == nil || (video.CodecName == "h264" && video.PixelFormat == "yuv420p" && min(video.Width, video.Height) <= quality)
	compatibleAudio := audio == nil || audio.CodecName == "aac"
	mp4 := strings.Contains(info.Format.Name, "mov") || strings.Contains(info.Format.Name, "mp4")
	expectedStreams := 0
	if video != nil {
		expectedStreams++
	}
	if info.audio() != nil {
		expectedStreams++
	}
	output := input
	if hls || audioInput != "" || !compatibleVideo || !compatibleAudio || !mp4 || len(info.Streams) != expectedStreams {
		args := append(inputOptions(input), "-i", input)
		if audioInput != "" {
			args = append(args, inputOptions(audioInput)...)
			args = append(args, "-i", audioInput)
		}
		if video != nil {
			args = append(args, "-map", "0:v:0")
		}
		if audio != nil {
			if audioInput != "" {
				args = append(args, "-map", "1:a:0")
			} else {
				args = append(args, "-map", "0:a:0")
			}
		}
		if video != nil {
			if compatibleVideo {
				args = append(args, "-c:v", "copy")
			} else {
				args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p", "-vf", fmt.Sprintf("scale=w='if(gte(iw,ih),-2,trunc(min(%d,iw)/2)*2)':h='if(gte(iw,ih),trunc(min(%d,ih)/2)*2,-2)'", quality, quality))
			}
		}
		if audio != nil {
			if compatibleAudio {
				// Fragmented MP4 needs explicit ADTS-to-ASC conversion for HLS AAC.
				args = append(args, "-c:a", "copy", "-bsf:a", "aac_adtstoasc")
			} else {
				args = append(args, "-c:a", "aac", "-b:a", "128k")
			}
		}
		args = append(args, "-sn", "-dn", "-map_metadata", "-1", "-max_muxing_queue_size", "1024", "-t", strconv.FormatFloat(float64(p.cfg.Media.MaxDurationSeconds)+0.25, 'f', 2, 64), "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-f", "mp4")
		extension := ".mp4"
		if video == nil {
			extension = ".m4a"
		}
		output, err = p.ffmpeg(ctx, args, extension, p.hardLimit())
		if err != nil {
			return empty, err
		}
		finalInfo, probeErr := p.probe(ctx, output)
		if probeErr != nil {
			return empty, probeErr
		}
		info = finalInfo
		if err = p.discard(input); err != nil {
			return empty, err
		}
		if audioInput != "" {
			if err = p.discard(audioInput); err != nil {
				return empty, err
			}
		}
	} else {
		extension := ".mp4"
		if video == nil {
			extension = ".m4a"
		}
		output = strings.TrimSuffix(input, filepath.Ext(input)) + extension
		if err = os.Rename(input, output); err != nil {
			return empty, err
		}
	}
	duration, err = info.duration()
	if err != nil {
		return empty, err
	}
	if duration <= 0 {
		return empty, errors.New("输出媒体时长无效")
	}
	stat, err := os.Stat(output)
	if err != nil {
		return empty, err
	}
	if stat.Size() <= 0 || stat.Size() > p.hardLimit() {
		return empty, errors.New("输出媒体大小超过安全限制")
	}
	kind := "audio"
	width, height := 0, 0
	thumbnail := ""
	if v := info.video(); v != nil {
		if v.CodecName != "h264" || v.PixelFormat != "yuv420p" {
			return empty, errors.New("输出视频不是兼容H.264/yuv420p格式")
		}
		kind = "video"
		width, height = v.Width, v.Height
		thumbArgs := append(inputOptions(output), "-i", output, "-map", "0:v:0", "-frames:v", "1", "-vf", "scale=w=640:h=640:force_original_aspect_ratio=decrease", "-c:v", "mjpeg", "-q:v", "4", "-f", "image2pipe")
		thumbnail, err = p.ffmpeg(ctx, thumbArgs, ".jpg", 4<<20)
		if err != nil {
			return empty, fmt.Errorf("生成视频封面失败: %w", err)
		}
	}
	if a := info.audio(); a != nil && a.CodecName != "aac" {
		return empty, errors.New("输出音频不是兼容AAC格式")
	}
	if stat.Size() > p.cfg.Media.MaxBytes {
		if p.cfg.Media.OversizeMode != "file" {
			return empty, errors.New("媒体大小超过发送限制")
		}
		kind = "file"
	}
	return model.Artifact{Kind: kind, Path: output, ThumbPath: thumbnail, Size: stat.Size(), Duration: duration, Width: width, Height: height}, nil
}
