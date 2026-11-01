package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multiparse/internal/model"
)

func extractionFixture() model.ParseResult {
	return model.ParseResult{Platform: "youtube", SourceURL: "https://www.youtube.com/watch?v=video", Extraction: &model.Extraction{Engine: "yt-dlp", InfoJSON: json.RawMessage(`{"id":"video"}`)}, Media: []model.Media{{Kind: "video", URL: "https://www.youtube.com/watch?v=video", ThumbnailURL: "https://cdn.example/cover"}}}
}

func TestExtractionReservesAggregateAndReconcilesFiles(t *testing.T) {
	p := newPreparation(t)
	p.cfg.Media.MaxBytes = 1024
	p.cfg.Media.MaxDiskBytes = 4096
	p.cfg.Media.OversizeMode = "link"
	policy := p.cfg.Platforms["youtube"]
	policy.Cookie = "SID=private"
	p.cfg.Platforms["youtube"] = policy
	paths, err := p.stageExtraction(context.Background(), extractionFixture(), func(ctx context.Context, client *http.Client, extraction model.Extraction, platform, cookie, dir string, limit int64) ([]string, error) {
		used, _ := p.manager.Usage()
		if used != 3072 || limit != 3072 || cookie != "SID=private" || platform != "youtube" || extraction.Engine != "yt-dlp" {
			t.Fatalf("backend started without aggregate reservation or policy: used=%d limit=%d cookie=%q platform=%s", used, limit, cookie, platform)
		}
		path := filepath.Join(dir, "media.mp4")
		if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "fragment.part"), []byte("unused"), 0600); err != nil {
			return nil, err
		}
		return []string{path}, nil
	})
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	used, _ := p.manager.Usage()
	actual, err := treeSize(p.dir)
	entries, readErr := os.ReadDir(p.dir)
	if used != 3 || p.bytes != 3 || actual != 3 || err != nil || readErr != nil || len(entries) != 1 {
		t.Fatalf("reservation/fragment not reconciled: used=%d bytes=%d actual=%d entries=%v: %v %v", used, p.bytes, actual, entries, err, readErr)
	}
}

func TestExtractionRejectsUnsafeAndUnboundedOutputs(t *testing.T) {
	for _, scenario := range []string{"outside", "relative", "symlink", "duplicate", "empty", "oversize", "aggregate", "failure", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			p := newPreparation(t)
			p.cfg.Media.MaxBytes = 1024
			p.cfg.Media.MaxDiskBytes = 4096
			p.cfg.Media.OversizeMode = "link"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			outside := filepath.Join(t.TempDir(), "outside.mp4")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := p.stageExtraction(ctx, extractionFixture(), func(ctx context.Context, client *http.Client, extraction model.Extraction, platform, cookie, dir string, limit int64) ([]string, error) {
				path := filepath.Join(dir, "media.mp4")
				data := []byte("abc")
				if scenario == "oversize" {
					data = bytes.Repeat([]byte{1}, 1025)
				}
				if scenario == "empty" {
					data = nil
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					return nil, err
				}
				switch scenario {
				case "outside":
					return []string{outside}, nil
				case "relative":
					return []string{"media.mp4"}, nil
				case "symlink":
					link := filepath.Join(dir, "linked.mp4")
					if err := os.Symlink(outside, link); err != nil {
						return nil, err
					}
					return []string{link}, nil
				case "duplicate":
					return []string{path, path}, nil
				case "aggregate":
					if err := os.WriteFile(filepath.Join(dir, "fragments.part"), bytes.Repeat([]byte{1}, int(limit)), 0600); err != nil {
						return nil, err
					}
				case "failure":
					return nil, errors.New("backend failed after writing fragments")
				case "cancelled":
					cancel()
					return nil, ctx.Err()
				}
				return []string{path}, nil
			})
			if err == nil || (scenario == "cancelled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("unsafe extraction accepted: %v", err)
			}
			used, _ := p.manager.Usage()
			entries, readErr := os.ReadDir(p.dir)
			if used != 0 || p.bytes != 0 || len(entries) != 0 || readErr != nil {
				t.Fatalf("failed extraction leaked: used=%d bytes=%d files=%v: %v", used, p.bytes, entries, readErr)
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "untouched" {
				t.Fatal("unsafe output cleanup modified outside file", err)
			}
		})
	}
}

func TestExtractionRejectsEnginePlatformAndUnavailableReservation(t *testing.T) {
	for _, scenario := range []string{"engine", "platform", "space"} {
		t.Run(scenario, func(t *testing.T) {
			p := newPreparation(t)
			p.cfg.Media.MaxBytes = 1024
			p.cfg.Media.MaxDiskBytes = 4096
			p.cfg.Media.OversizeMode = "link"
			result := extractionFixture()
			switch scenario {
			case "engine":
				result.Extraction.Engine = "user-command"
			case "platform":
				result.Platform = "bilibili"
			case "space":
				if err := p.claim(3073); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			_, err := p.stageExtraction(context.Background(), result, func(context.Context, *http.Client, model.Extraction, string, string, string, int64) ([]string, error) {
				called = true
				return nil, nil
			})
			if err == nil || called {
				t.Fatalf("invalid extraction spawned backend: called=%v err=%v", called, err)
			}
		})
	}
}

func TestExtractionProcessesLocalMediaAndControlledCoverOnly(t *testing.T) {
	video := generateMedia(t, "extracted.webm", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=0.6", "-an", "-c:v", "libvpx")
	cover := photoFixture(t)
	p := newPreparation(t)
	requests := 0
	p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.String() != "https://cdn.example/cover" || r.Header.Get("Referer") != "https://www.youtube.com/watch?v=video" {
			t.Fatalf("metadata page or media was redownloaded: %s", r.URL)
		}
		return fixtureClient(map[string][]byte{"/cover": cover}).Transport.RoundTrip(r)
	})}
	result := extractionFixture()
	paths, err := p.stageExtraction(context.Background(), result, func(ctx context.Context, client *http.Client, extraction model.Extraction, platform, cookie, dir string, limit int64) ([]string, error) {
		path := filepath.Join(dir, "video.webm")
		return []string{path}, os.WriteFile(path, video, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := p.prepareExtracted(context.Background(), result, paths)
	if err != nil || len(artifacts) != 1 {
		t.Fatal(artifacts, err)
	}
	a := artifacts[0]
	if a.Kind != "video" || a.Duration <= 0 || a.Size <= 0 || !strings.HasSuffix(a.Path, ".mp4") || a.ThumbPath == "" || requests != 1 {
		t.Fatalf("extracted output not processed: %+v requests=%d", a, requests)
	}
	got, err := os.ReadFile(a.ThumbPath)
	if err != nil || !bytes.Equal(got, cover) {
		t.Fatal("controlled metadata cover not retained", err)
	}
	actual, err := treeSize(p.dir)
	used, _ := p.manager.Usage()
	if err != nil || used != actual || p.bytes != actual {
		t.Fatalf("local conversion accounting mismatch: used=%d bytes=%d actual=%d: %v", used, p.bytes, actual, err)
	}
}

func TestLocalExtractionRejectsPlaylistsAndRemoteCompanions(t *testing.T) {
	p := newPreparation(t)
	path := filepath.Join(p.dir, "playlist.bin")
	if err := os.WriteFile(path, []byte("#EXTM3U\n#EXTINF:1,\nhttps://cdn.example/segment.ts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("local extraction attempted network request: %s", r.URL)
		return nil, errors.New("unexpected request")
	})}
	for _, item := range []model.Media{{Kind: "video"}, {Kind: "video", AudioURL: "https://cdn.example/audio"}, {Kind: "livephoto", VideoURL: "https://cdn.example/video"}} {
		if _, err := p.prepareLocal(context.Background(), item, path, nil); err == nil {
			t.Fatal("local output accepted remote resource")
		}
	}
	if _, err := p.prepareLocal(context.Background(), model.Media{Kind: "video"}, "https://cdn.example/video", nil); err == nil {
		t.Fatal("URL accepted as local filename")
	}
}

func TestExtractionReservationUsesAvailableDiskAndCookieScope(t *testing.T) {
	for _, platform := range []string{"youtube", "facebook", "snapchat"} {
		t.Run(platform, func(t *testing.T) {
			p := newPreparation(t)
			p.cfg.Media.MaxBytes = 1024
			p.cfg.Media.MaxDiskBytes = 2048
			p.cfg.Media.OversizeMode = "link"
			if err := p.claim(100); err != nil {
				t.Fatal(err)
			}
			policy := p.cfg.Platforms[platform]
			policy.Cookie = "SID=private"
			p.cfg.Platforms[platform] = policy
			result := extractionFixture()
			result.Platform = platform
			_, err := p.stageExtraction(context.Background(), result, func(ctx context.Context, client *http.Client, extraction model.Extraction, gotPlatform, cookie, dir string, limit int64) ([]string, error) {
				used, _ := p.manager.Usage()
				wantCookie := ""
				if platform == "youtube" {
					wantCookie = "SID=private"
				}
				if used != 2048 || limit != 1948 || cookie != wantCookie || gotPlatform != platform {
					t.Fatalf("available disk/cookie scope: used=%d limit=%d cookie=%q platform=%q", used, limit, cookie, gotPlatform)
				}
				return nil, errors.New("stop backend")
			})
			used, _ := p.manager.Usage()
			if err == nil || used != 100 || p.bytes != 100 {
				t.Fatalf("backend failure released another claim: used=%d bytes=%d: %v", used, p.bytes, err)
			}
		})
	}
}

func TestExtractionFileModeUsesPartiallyOccupiedCache(t *testing.T) {
	p := newPreparation(t)
	p.cfg.Media.MaxBytes = 1024
	p.cfg.Media.MaxDiskBytes = 4096
	p.cfg.Media.OversizeMode = "file"
	if err := p.claim(1024); err != nil {
		t.Fatal(err)
	}
	paths, err := p.stageExtraction(context.Background(), extractionFixture(), func(ctx context.Context, client *http.Client, extraction model.Extraction, platform, cookie, dir string, limit int64) ([]string, error) {
		used, _ := p.manager.Usage()
		if limit != 3072 || used != 4096 {
			t.Fatalf("occupied file-mode cache reservation: limit=%d used=%d", limit, used)
		}
		path := filepath.Join(dir, "media.mp4")
		return []string{path}, os.WriteFile(path, []byte("abc"), 0600)
	})
	if err != nil || len(paths) != 1 {
		t.Fatalf("occupied file-mode cache rejected extraction: %v %v", paths, err)
	}
	used, _ := p.manager.Usage()
	if used != 1027 || p.bytes != 1027 {
		t.Fatalf("occupied cache accounting changed: used=%d bytes=%d", used, p.bytes)
	}
}

func TestExtractionKeepsGeneratedThumbnailForUnavailableCover(t *testing.T) {
	video := generateMedia(t, "cover-fallback.mp4", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=5:d=0.6", "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	for _, scenario := range []string{"expired", "invalid", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			p := newPreparation(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://cdn.example/cover" {
					t.Fatalf("unexpected remote media request: %s", r.URL)
				}
				if scenario == "cancelled" {
					cancel()
					return nil, ctx.Err()
				}
				files := map[string][]byte{}
				if scenario == "invalid" {
					files["/cover"] = []byte("\x89PNG\r\n\x1a\ncorrupt image data")
				}
				return fixtureClient(files).Transport.RoundTrip(r)
			})}
			result := extractionFixture()
			paths, err := p.stageExtraction(ctx, result, func(ctx context.Context, client *http.Client, extraction model.Extraction, platform, cookie, dir string, limit int64) ([]string, error) {
				path := filepath.Join(dir, "media.mp4")
				return []string{path}, os.WriteFile(path, video, 0600)
			})
			if err != nil {
				t.Fatal(err)
			}
			artifacts, err := p.prepareExtracted(ctx, result, paths)
			if scenario == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cover cancellation suppressed: %v", err)
				}
			} else {
				if err != nil || len(artifacts) != 1 {
					t.Fatalf("optional cover discarded valid video: %v %v", artifacts, err)
				}
				a := artifacts[0]
				if a.Kind != "video" || a.ThumbPath == "" || filepath.Ext(a.ThumbPath) != ".jpg" {
					t.Fatalf("generated thumbnail not retained: %+v", a)
				}
				if info, statErr := os.Stat(a.ThumbPath); statErr != nil || info.Size() == 0 {
					t.Fatalf("generated thumbnail missing: %v", statErr)
				}
			}
			entries, readErr := os.ReadDir(p.dir)
			actual, sizeErr := treeSize(p.dir)
			used, _ := p.manager.Usage()
			if readErr != nil || sizeErr != nil || len(entries) != 2 || used != actual || p.bytes != actual {
				t.Fatalf("optional cover leaked files/accounting: files=%v used=%d bytes=%d actual=%d: %v %v", entries, used, p.bytes, actual, readErr, sizeErr)
			}
		})
	}
}

func TestOptionalExtractionCoverPropagatesResourceAndCleanupFailures(t *testing.T) {
	t.Run("disk-limit", func(t *testing.T) {
		p := newPreparation(t)
		p.cfg.Media.MaxDiskBytes = 0
		p.client = fixtureClient(map[string][]byte{"/cover": photoFixture(t)})
		_, err := p.optionalExtractionCover(context.Background(), "https://cdn.example/cover", nil)
		if !errors.Is(err, errMediaDiskLimit) {
			t.Fatalf("cover disk limit suppressed: %v", err)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		p := newPreparation(t)
		p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
			// An unexpected nonempty staged directory cannot be silently discarded.
			dir := p.next(".bin")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "unexpected"), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
			return fixtureClient(nil).Transport.RoundTrip(r)
		})}
		_, err := p.optionalExtractionCover(context.Background(), "https://cdn.example/cover", nil)
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatalf("cover cleanup failure suppressed: %v", err)
		}
	})
}
