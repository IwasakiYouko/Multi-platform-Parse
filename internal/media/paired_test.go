package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multiparse/internal/model"
)

func photoFixture(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestLivePhotoPreservesPairAndAccounting(t *testing.T) {
	photo := photoFixture(t)
	video := generateMedia(t, "paired.mp4", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=0.6", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/photo": photo, "/video": video})
	a, err := p.prepareItem(context.Background(), model.Media{Kind: "livephoto", URL: "https://cdn.example/photo", VideoURL: "https://cdn.example/video"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "livephoto" || a.Path == a.VideoPath || a.Size != int64(len(photo)) || a.VideoSize != int64(len(video)) || a.Duration <= 0 || a.ThumbPath == "" || a.Width != 3 || a.Height != 2 {
		t.Fatalf("incomplete paired artifact: %+v", a)
	}
	for path, size := range map[string]int64{a.Path: a.Size, a.VideoPath: a.VideoSize} {
		info, err := os.Stat(path)
		if err != nil || info.Size() != size {
			t.Fatalf("paired path %q: %v, info=%v", path, err, info)
		}
	}
	got, err := os.ReadFile(a.Path)
	if err != nil || !bytes.Equal(got, photo) {
		t.Fatal("paired photo bytes changed", err)
	}
	info, err := p.probe(context.Background(), a.VideoPath)
	if err != nil || info.video() == nil || info.video().CodecName != "h264" {
		t.Fatal("paired video was not processed locally", err)
	}
	actual, err := treeSize(p.dir)
	used, _ := p.manager.Usage()
	if err != nil || used != actual || p.bytes != actual || used <= a.Size+a.VideoSize {
		t.Fatalf("pair/thumbnail accounting: used=%d preparation=%d actual=%d: %v", used, p.bytes, actual, err)
	}
}

func TestLivePhotoCompanionFailureIsAtomic(t *testing.T) {
	for _, scenario := range []string{"missing", "cancelled", "oversize", "not-video"} {
		t.Run(scenario, func(t *testing.T) {
			p := newPreparation(t)
			p.cfg.Media.MaxBytes = 1024
			p.cfg.Media.OversizeMode = "link"
			photo := photoFixture(t)
			files := map[string][]byte{"/photo": photo}
			if scenario == "oversize" {
				files["/video"] = bytes.Repeat([]byte{1}, 1025)
			}
			if scenario == "not-video" {
				files["/video"] = photo
			}
			p.client = fixtureClient(files)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				base := p.client.Transport
				p.client.Transport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/video" {
						cancel()
						return nil, ctx.Err()
					}
					return base.RoundTrip(r)
				})
			}
			prior := filepath.Join(p.dir, "prior")
			if err := os.WriteFile(prior, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := p.claim(3); err != nil {
				t.Fatal(err)
			}
			_, err := p.prepareItem(ctx, model.Media{Kind: "livephoto", URL: "https://cdn.example/photo", VideoURL: "https://cdn.example/video"}, nil)
			if err == nil || (scenario == "cancelled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("companion failure accepted: %v", err)
			}
			entries, err := os.ReadDir(p.dir)
			used, _ := p.manager.Usage()
			if err != nil || len(entries) != 1 || entries[0].Name() != "prior" || used != 3 || p.bytes != 3 {
				t.Fatalf("failed pair leaked or removed old files: %v used=%d bytes=%d: %v", entries, used, p.bytes, err)
			}
		})
	}
}

func TestLivePhotoCountsAgainstImagePolicy(t *testing.T) {
	p := newPreparation(t)
	p.cfg.Media.MaxImages = 1
	result := model.ParseResult{Platform: "twitter", Media: []model.Media{{Kind: "livephoto"}, {Kind: "livephoto"}}}
	if _, err := p.manager.Prepare(context.Background(), p.cfg, result, strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "图片数量") {
		t.Fatalf("paired photo bypassed image count: %v", err)
	}
	result.Media = result.Media[:1]
	result.Media[0].Duration = float64(p.cfg.Media.MaxDurationSeconds + 1)
	if _, err := p.manager.Prepare(context.Background(), p.cfg, result, strings.Repeat("a", 64)); err == nil || !strings.Contains(err.Error(), "时长") {
		t.Fatalf("paired photo bypassed duration bound: %v", err)
	}
}

func TestGIFBytesAndMP4AnimationRemainAnimated(t *testing.T) {
	t.Run("gif", func(t *testing.T) {
		palette := color.Palette{color.Black, color.White}
		frame := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
		var data bytes.Buffer
		if err := gif.EncodeAll(&data, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{20, 30}}); err != nil {
			t.Fatal(err)
		}
		p := newPreparation(t)
		p.client = fixtureClient(map[string][]byte{"/animation": data.Bytes()})
		a, err := p.prepareItem(context.Background(), model.Media{Kind: "gif", URL: "https://cdn.example/animation"}, nil)
		if err != nil || filepath.Ext(a.Path) != ".gif" || a.Duration != 0.5 {
			t.Fatalf("GIF flattened: %+v %v", a, err)
		}
		got, err := os.ReadFile(a.Path)
		if err != nil || !bytes.Equal(got, data.Bytes()) {
			t.Fatal("GIF frames changed", err)
		}
	})
	t.Run("mp4-animation", func(t *testing.T) {
		video := generateMedia(t, "animation.mp4", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=1.4", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p")
		p := newPreparation(t)
		p.client = fixtureClient(map[string][]byte{"/animation": video})
		a, err := p.prepareItem(context.Background(), model.Media{Kind: "gif", URL: "https://cdn.example/animation"}, nil)
		if err != nil || a.Kind != "video" || a.Duration <= 1 || filepath.Ext(a.Path) != ".mp4" || a.ThumbPath == "" {
			t.Fatalf("MP4 animation rejected or flattened: %+v %v", a, err)
		}
	})
}
