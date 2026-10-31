package media

import (
	"context"
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
