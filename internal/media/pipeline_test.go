package media

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"multiparse/internal/model"
)

func generateMedia(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), name)
	commandArgs := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-threads", "2", "-filter_threads", "1", "-filter_complex_threads", "1"}
	commandArgs = append(commandArgs, args...)
	commandArgs = append(commandArgs, "-threads", "2", "-y", path)
	command := exec.CommandContext(ctx, "ffmpeg", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("generate native fixture: %v: %s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestNativeSeparateAudioVideoPipeline(t *testing.T) {
	video := generateMedia(t, "video.mp4", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=0.6", "-an", "-c:v", "mpeg4")
	audio := generateMedia(t, "audio.m4a", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.6", "-vn", "-c:a", "aac")
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/video": video, "/audio": audio})
	artifact, err := p.prepareItem(context.Background(), model.Media{Kind: "video", URL: "https://cdn.example/video", AudioURL: "https://cdn.example/audio"}, map[string]string{"User-Agent": "native-test"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "video" || artifact.Duration <= 0 || artifact.Width != 64 || artifact.Height != 48 || artifact.ThumbPath == "" || artifact.Size <= 0 {
		t.Fatalf("incomplete native artifact: %+v", artifact)
	}
	info, err := p.probe(context.Background(), artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.video() == nil || info.video().CodecName != "h264" || info.video().PixelFormat != "yuv420p" || info.audio() == nil || info.audio().CodecName != "aac" {
		t.Fatalf("incompatible merged codecs: %+v", info.Streams)
	}
	if _, err = os.Stat(artifact.ThumbPath); err != nil {
		t.Fatal(err)
	}
}
func TestNativeLocalizedHLSPipeline(t *testing.T) {
	segment := generateMedia(t, "segment.ts", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=0.6", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.6", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-f", "mpegts")
	manifest := []byte("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:0.6,\nsegment.ts\n#EXT-X-ENDLIST\n")
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/index.m3u8": manifest, "/segment.ts": segment})
	artifact, err := p.prepareItem(context.Background(), model.Media{Kind: "video", URL: "https://cdn.example/index.m3u8"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "video" || artifact.ThumbPath == "" || artifact.Duration <= 0 {
		t.Fatalf("incomplete HLS artifact: %+v", artifact)
	}
	info, err := p.probe(context.Background(), artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.video() == nil || info.video().CodecName != "h264" || info.audio() == nil || info.audio().CodecName != "aac" {
		t.Fatalf("incompatible HLS codecs: %+v", info.Streams)
	}
}
func TestPrepareItemFallbackOnCorruptPrimaryWithoutQuotaLeak(t *testing.T) {
	valid := generateMedia(t, "valid.mp4", "-f", "lavfi", "-i", "color=c=blue:s=64x48:r=10:d=1.0", "-f", "lavfi", "-i", "sine=frequency=440:duration=1.0", "-c:v", "mpeg4", "-c:a", "aac", "-shortest", "-movflags", "+faststart")
	mdatIdx := bytes.Index(valid, []byte("mdat"))
	if mdatIdx <= 0 {
		t.Fatal("faststart fixture missing mdat box")
	}
	truncatedAfterMoov := append([]byte(nil), valid[:mdatIdx+8]...)
	h264Faststart := generateMedia(t, "h264.mp4", "-f", "lavfi", "-i", "color=c=red:s=64x48:r=10:d=1.0", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart")
	h264Mdat := bytes.Index(h264Faststart, []byte("mdat"))
	if h264Mdat <= 0 {
		t.Fatal("h264 faststart fixture missing mdat box")
	}
	// avcC in moov satisfies ffprobe (64x48 yuv420p), while empty mdat fails later during ffmpeg thumbnail extraction.
	truncatedH264 := append([]byte(nil), h264Faststart[:h264Mdat+8]...)
	corruptHeader := []byte("0000corrupt-mp4-payload")

	p := newPreparation(t)
	hits := map[string]int{}
	p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/corrupt.mp4":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(corruptHeader)), ContentLength: -1, Header: make(http.Header), Request: r}, nil
		case "/truncated.mp4":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(truncatedAfterMoov)), ContentLength: -1, Header: make(http.Header), Request: r}, nil
		case "/truncated-h264.mp4":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(truncatedH264)), ContentLength: -1, Header: make(http.Header), Request: r}, nil
		case "/backup.mp4":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(valid)), ContentLength: int64(len(valid)), Header: make(http.Header), Request: r}, nil
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader(nil)), ContentLength: 0, Header: make(http.Header), Request: r}, nil
		}
	})}
	artifact, err := p.prepareItem(context.Background(), model.Media{
		Kind:       "video",
		URL:        "https://cdn1.example/corrupt.mp4",
		BackupURLs: []string{"https://cdn2.example/truncated.mp4", "https://cdn3.example/truncated-h264.mp4", "https://cdn4.example/backup.mp4"},
	}, nil)
	if err != nil {
		t.Fatalf("prepareItem failed to fallback to valid backup URL: %v", err)
	}
	if hits["/corrupt.mp4"] != 1 || hits["/truncated.mp4"] != 1 || hits["/truncated-h264.mp4"] != 1 || hits["/backup.mp4"] != 1 {
		t.Fatalf("unexpected candidate hit counts: %+v", hits)
	}
	if artifact.Kind != "video" || artifact.ThumbPath == "" || artifact.Size <= 0 {
		t.Fatalf("invalid fallback artifact: %+v", artifact)
	}
	info, err := p.probe(context.Background(), artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.video() == nil || info.video().CodecName != "h264" || info.video().PixelFormat != "yuv420p" || info.audio() == nil || info.audio().CodecName != "aac" {
		t.Fatalf("incompatible fallback codecs: %+v", info.Streams)
	}
	videoStat, err := os.Stat(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	thumbStat, err := os.Stat(artifact.ThumbPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("failed attempt left temporary files in stage dir: %v", names)
	}
	wantBytes := videoStat.Size() + thumbStat.Size()
	used, err := p.manager.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if used != wantBytes || p.bytes != wantBytes {
		t.Fatalf("disk accounting mismatch: used=%d prep=%d want=%d", used, p.bytes, wantBytes)
	}
}
func TestPrepareItemDoesNotFallbackOnNonRetryableError(t *testing.T) {
	overDuration := generateMedia(t, "long.mp4", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=2.2", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	p := newPreparation(t)
	p.cfg.Media.MaxDurationSeconds = 1
	hits := map[string]int{}
	p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		hits[r.URL.Path]++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(overDuration)), ContentLength: int64(len(overDuration)), Header: make(http.Header), Request: r}, nil
	})}
	_, err := p.prepareItem(context.Background(), model.Media{
		Kind:       "video",
		URL:        "https://cdn1.example/long.mp4",
		BackupURLs: []string{"https://cdn2.example/backup.mp4"},
	}, nil)
	if err == nil {
		t.Fatal("expected duration limit error")
	}
	if IsRetryable(err) {
		t.Fatalf("duration limit error must not be retryable: %v", err)
	}
	if hits["/long.mp4"] != 1 || hits["/backup.mp4"] != 0 {
		t.Fatalf("non-retryable error must not request BackupURLs: %+v", hits)
	}
	if used, _ := p.manager.Usage(); used != 0 || p.bytes != 0 {
		t.Fatalf("non-retryable rollback leaked quota: used=%d prep=%d", used, p.bytes)
	}
	if entries, _ := os.ReadDir(p.dir); len(entries) != 0 {
		t.Fatalf("non-retryable rollback left files: %v", entries)
	}
}
func TestPrepareDownloadedAudioCopyFallbackToAAC(t *testing.T) {
	// Non-HLS MPEG-TS carries ADTS AAC: stream-copying audio without aac_adtstoasc into MP4 fails first, exercising the -c:a aac fallback.
	mpegts := generateMedia(t, "adts.ts", "-f", "lavfi", "-i", "color=c=green:s=64x48:r=5:d=0.6", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.6", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-f", "mpegts")
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/adts.ts": mpegts})
	artifact, err := p.prepareItem(context.Background(), model.Media{Kind: "video", URL: "https://cdn.example/adts.ts"}, nil)
	if err != nil {
		t.Fatalf("audio copy -> AAC fallback failed: %v", err)
	}
	videoStat, err := os.Stat(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	thumbStat, err := os.Stat(artifact.ThumbPath)
	if err != nil {
		t.Fatal(err)
	}
	if used, _ := p.manager.Usage(); used != videoStat.Size()+thumbStat.Size() {
		t.Fatalf("audio fallback leaked quota: used=%d want=%d", used, videoStat.Size()+thumbStat.Size())
	}
}

func TestNativeVideoUsesOfficialCoverWithoutQuotaLeak(t *testing.T) {
	video := generateMedia(t, "video.mp4", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=0.6", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart")
	cover := generateMedia(t, "cover.png", "-f", "lavfi", "-i", "color=c=blue:s=320x240:d=0.1", "-frames:v", "1")
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/video.mp4": video, "/cover.png": cover})
	artifact, err := p.prepareItem(context.Background(), model.Media{Kind: "video", URL: "https://cdn.example/video.mp4", ThumbnailURL: "https://cdn.example/cover.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(artifact.ThumbPath) != ".png" {
		t.Fatalf("expected official .png cover to replace .jpg frame grab, got %q", artifact.ThumbPath)
	}
	videoStat, _ := os.Stat(artifact.Path)
	thumbStat, _ := os.Stat(artifact.ThumbPath)
	if used, _ := p.manager.Usage(); used != videoStat.Size()+thumbStat.Size() {
		t.Fatalf("official cover replacement leaked quota: used=%d want=%d", used, videoStat.Size()+thumbStat.Size())
	}
}

func TestPureGoWebPTallImagePreservesFullResolution(t *testing.T) {
	tallWebP := generateMedia(t, "tall.webp", "-f", "lavfi", "-i", "color=c=navy:s=120x5000:d=0.1", "-frames:v", "1", "-c:v", "libwebp")
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/tall.webp": tallWebP})
	artifact, err := p.prepareItem(context.Background(), model.Media{Kind: "image", URL: "https://cdn.example/tall.webp"}, nil)
	if err != nil {
		t.Fatalf("pure-Go tall WebP decode failed: %v", err)
	}
	if artifact.Width != 120 || artifact.Height != 5000 {
		t.Fatalf("tall WebP resolution altered: got %dx%d, want 120x5000", artifact.Width, artifact.Height)
	}
	stat, err := os.Stat(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if used, _ := p.manager.Usage(); used != stat.Size() || p.bytes != stat.Size() {
		t.Fatalf("tall WebP disk accounting mismatch: used=%d prep=%d file=%d", used, p.bytes, stat.Size())
	}
}

func TestDASHVideoAudioBackupURLFallback(t *testing.T) {
	videoOnly := generateMedia(t, "dash-v.mp4", "-f", "lavfi", "-i", "color=c=teal:s=640x360:r=10:d=0.8", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	audioGood := generateMedia(t, "dash-a.m4a", "-f", "lavfi", "-i", "sine=frequency=520:duration=0.8", "-vn", "-c:a", "aac")
	p := newPreparation(t)
	hits := map[string]int{}
	p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/dash-v.mp4":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(videoOnly)), ContentLength: int64(len(videoOnly)), Header: make(http.Header), Request: r}, nil
		case "/bad-audio.m4s":
			return &http.Response{StatusCode: 502, Body: io.NopCloser(bytes.NewReader([]byte("bad gateway"))), ContentLength: -1, Header: make(http.Header), Request: r}, nil
		case "/backup-audio.m4s":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(audioGood)), ContentLength: int64(len(audioGood)), Header: make(http.Header), Request: r}, nil
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader(nil)), ContentLength: -1, Header: make(http.Header), Request: r}, nil
		}
	})}
	artifact, err := p.prepareItem(context.Background(), model.Media{
		Kind:            "video",
		URL:             "https://upos.bilivideo.com/dash-v.mp4",
		AudioURL:        "https://xy1.mcdn.bilivideo.cn/bad-audio.m4s",
		AudioBackupURLs: []string{"https://upos.bilivideo.com/backup-audio.m4s"},
	}, nil)
	if err != nil {
		t.Fatalf("AudioBackupURLs fallback failed: %v", err)
	}
	if hits["/bad-audio.m4s"] != 1 || hits["/backup-audio.m4s"] != 1 {
		t.Fatalf("unexpected audio URL hit counts: %+v", hits)
	}
	vStat, _ := os.Stat(artifact.Path)
	tStat, _ := os.Stat(artifact.ThumbPath)
	if used, _ := p.manager.Usage(); used != vStat.Size()+tStat.Size() {
		t.Fatalf("AudioBackupURLs fallback leaked quota: used=%d want=%d", used, vStat.Size()+tStat.Size())
	}
}
