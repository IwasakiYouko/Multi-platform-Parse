package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"multiparse/internal/model"
)

const localFormats = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,mpegts,mpeg,avi,flv,ogg,mp3,aac,wav,flac,gif,image2,jpeg_pipe,png_pipe,webp_pipe,avif,heif"

type permanentMediaError struct {
	err error
}

func (e permanentMediaError) Error() string { return e.err.Error() }
func (e permanentMediaError) Unwrap() error { return e.err }

func permanentError(err error) error {
	if err == nil {
		return nil
	}
	return permanentMediaError{err: err}
}

var nonRetryableMediaPhrases = []string{
	"未安装ffmpeg",
	"未安装ffprobe",
	"超过限制",
	"超出限制",
	"超过安全限制",
	"超出安全限制",
	"超过大小限制",
	"超过发送限制",
	"超过下载上限",
	"超过2MiB限制",
	"清单过大",
	"变体过多",
	"提取媒体",
	"媒体提取",
	"提取器",
	"无效缓存键",
	"无效媒体资源限制",
	"无效下载上限",
	"无效磁盘预留",
	"磁盘上限",
	"磁盘计数溢出",
	"并发限制",
	"工作集",
	"符号链接",
	"非普通文件",
	"必须是实际目录",
	"不是实际目录",
	"缓存键已存在",
	"清理媒体临时目录失败",
	"缺少下载地址",
	"备用地址数量",
	"实况照片必须包含",
	"仅允许HTTP(S)源资源",
	"拒绝HTTPS降级资源",
	"禁止认证URL",
	"不支持的加密或DRM",
	"不支持低延迟或会话加密HLS",
	"直播流不受支持",
	"拒绝无法限制的媒体",
}

// IsRetryable reports whether err comes from a remote stream, edge node, or codec failure rather than local limits or environment.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errInvalidReservation) || errors.Is(err, errMediaDiskLimit) || errors.Is(err, errMediaAccountingOverflow) || errors.Is(err, io.ErrShortWrite) || errors.Is(err, exec.ErrNotFound) {
		return false
	}
	var permErr permanentMediaError
	var pathErr *os.PathError
	var linkErr *os.LinkError
	var syscallErr *os.SyscallError
	var execErr *exec.Error
	if errors.As(err, &permErr) || errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.As(err, &syscallErr) || errors.As(err, &execErr) {
		return false
	}
	msg := err.Error()
	for _, phrase := range nonRetryableMediaPhrases {
		if strings.Contains(msg, phrase) {
			return false
		}
	}
	return true
}

func hasASCIIPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func (p *preparation) sanitizeStderr(raw string) string {
	text := raw
	if p != nil && p.dir != "" {
		text = strings.ReplaceAll(text, p.dir, "[stage]")
		if slash := filepath.ToSlash(p.dir); slash != p.dir {
			text = strings.ReplaceAll(text, slash, "[stage]")
		}
	}
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		matched := ""
		for _, prefix := range []string{"socks5h://", "socks5://", "https://", "http://"} {
			if hasASCIIPrefixFold(text[i:], prefix) {
				matched = prefix
				break
			}
		}
		if matched != "" {
			j := i + len(matched)
			for j < len(text) {
				r, width := utf8.DecodeRuneInString(text[j:])
				if unicode.IsSpace(r) || strings.ContainsRune("\"'<>）)", r) {
					break
				}
				j += width
			}
			b.WriteString("[url]")
			i = j
			continue
		}
		_, width := utf8.DecodeRuneInString(text[i:])
		b.WriteString(text[i : i+width])
		i += width
	}
	text = strings.TrimSpace(b.String())
	if runes := []rune(text); len(runes) > 600 {
		text = string(runes[len(runes)-600:])
	}
	return text
}

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
			return result, permanentError(errors.New("未安装ffprobe"))
		}
		slog.Warn("ffprobe媒体探测失败", "ext", filepath.Ext(path), "err", err, "stderr", p.sanitizeStderr(stderr.String()))
		return result, errors.New("ffprobe无法识别媒体或媒体文件已损坏")
	}
	if stdout.exceeded {
		return result, permanentError(errors.New("ffprobe输出超出限制"))
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
		return result, permanentError(errors.New("媒体实际时长超过限制"))
	}
	if video := result.video(); video != nil {
		if video.Width < 1 || video.Height < 1 {
			return result, errors.New("ffprobe返回无效媒体信息")
		}
		if video.Width > 16384 || video.Height > 16384 || int64(video.Width)*int64(video.Height) > 40_000_000 {
			return result, permanentError(errors.New("媒体分辨率超出安全限制"))
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
		w.failure = permanentError(errors.New("处理后的媒体超过大小限制"))
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
	stderr := &cappedBuffer{limit: 8 << 10}
	command.Stderr = stderr
	if err = command.Run(); err != nil {
		if writer.failure != nil {
			return "", writer.failure
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", permanentError(errors.New("未安装ffmpeg"))
		}
		slog.Warn("ffmpeg媒体处理失败", "ext", extension, "err", err, "stderr", p.sanitizeStderr(stderr.String()))
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

func (p *preparation) rollbackAttempt(before, beforeHLS int, beforeBytes int64, savedHLSCache map[string]hlsCached) error {
	p.hlsResources = beforeHLS
	p.hlsCache = savedHLSCache
	entries, cleanupErr := os.ReadDir(p.dir)
	for _, entry := range entries {
		stem, _, _ := strings.Cut(entry.Name(), ".")
		sequence, parseErr := strconv.Atoi(stem)
		if parseErr == nil && sequence > before {
			cleanupErr = errors.Join(cleanupErr, p.discard(filepath.Join(p.dir, entry.Name())))
		}
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	if p.bytes != beforeBytes {
		return errMediaAccountingOverflow
	}
	return nil
}

func (p *preparation) prepareItem(ctx context.Context, item model.Media, headers map[string]string) (model.Artifact, error) {
	var empty model.Artifact
	if item.Kind == "livephoto" {
		return p.prepareLivePhoto(ctx, item, headers)
	}
	if item.URL == "" {
		return empty, permanentError(errors.New("媒体缺少下载地址"))
	}
	urls := append([]string{item.URL}, item.BackupURLs...)
	if len(urls) > 9 {
		return empty, permanentError(errors.New("媒体备用地址数量超过限制"))
	}
	var lastErr error
	for _, candidate := range urls {
		before, beforeHLS, beforeBytes := p.sequence, p.hlsResources, p.bytes
		var savedHLSCache map[string]hlsCached
		if len(p.hlsCache) > 0 {
			savedHLSCache = make(map[string]hlsCached, len(p.hlsCache))
			for k, v := range p.hlsCache {
				savedHLSCache[k] = v
			}
		}
		input, finalURL, err := p.download(ctx, candidate, headers, p.hardLimit(), ".bin")
		var artifact model.Artifact
		if err == nil {
			artifact, err = p.prepareDownloaded(ctx, item, input, finalURL, headers, true)
		}
		if err == nil && artifact.Kind == "video" && item.ThumbnailURL != "" && artifact.ThumbPath != "" {
			cover, coverErr := p.optionalExtractionCover(ctx, item.ThumbnailURL, headers)
			if coverErr != nil {
				err = coverErr
			} else if cover.Path != "" {
				if discardErr := p.discard(artifact.ThumbPath); discardErr != nil {
					err = discardErr
				} else {
					artifact.ThumbPath = cover.Path
				}
			}
		}
		if err == nil {
			return artifact, nil
		}
		if cleanupErr := p.rollbackAttempt(before, beforeHLS, beforeBytes, savedHLSCache); cleanupErr != nil {
			return empty, errors.Join(err, cleanupErr)
		}
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		if !IsRetryable(err) {
			return empty, err
		}
		lastErr = err
	}
	return empty, lastErr
}

// prepareLocal accepts only staged media, never a source URL or a remote companion.
func (p *preparation) prepareLocal(ctx context.Context, item model.Media, input string, headers map[string]string) (model.Artifact, error) {
	if err := p.validateLocal(input); err != nil {
		return model.Artifact{}, err
	}
	if item.AudioURL != "" || item.VideoURL != "" || item.Kind == "livephoto" {
		return model.Artifact{}, permanentError(errors.New("本地提取媒体不能引用远程伴随媒体"))
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
			return empty, permanentError(errors.New("提取器必须返回已下载媒体，不能返回播放列表"))
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
		return empty, permanentError(errors.New("无法确定媒体时长，拒绝无法限制的媒体"))
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
		buildArgs := func(copyAudio bool) []string {
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
				if copyAudio {
					args = append(args, "-c:a", "copy")
					if hls || strings.HasSuffix(audioInput, ".m3u8") {
						// Fragmented MP4 needs explicit ADTS-to-ASC conversion for HLS AAC.
						args = append(args, "-bsf:a", "aac_adtstoasc")
					}
				} else {
					args = append(args, "-c:a", "aac", "-b:a", "128k")
				}
			}
			args = append(args, "-sn", "-dn", "-map_metadata", "-1", "-max_muxing_queue_size", "1024", "-t", strconv.FormatFloat(float64(p.cfg.Media.MaxDurationSeconds)+0.25, 'f', 2, 64), "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-f", "mp4")
			return args
		}
		extension := ".mp4"
		if video == nil {
			extension = ".m4a"
		}
		copyAudio := audio != nil && compatibleAudio
		output, err = p.ffmpeg(ctx, buildArgs(copyAudio), extension, p.hardLimit())
		if err != nil && copyAudio && ctx.Err() == nil && IsRetryable(err) {
			output, err = p.ffmpeg(ctx, buildArgs(false), extension, p.hardLimit())
		}
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
		return empty, permanentError(errors.New("输出媒体大小超过安全限制"))
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
			return empty, permanentError(errors.New("媒体大小超过发送限制"))
		}
		kind = "file"
	}
	return model.Artifact{Kind: kind, Path: output, ThumbPath: thumbnail, Size: stat.Size(), Duration: duration, Width: width, Height: height}, nil
}
