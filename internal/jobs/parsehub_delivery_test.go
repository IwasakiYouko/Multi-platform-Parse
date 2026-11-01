package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"multiparse/internal/model"
	"multiparse/internal/onebot"
)

func parseHubDeliveryEntry(t *testing.T, x *deliveryHarness, files ...model.Artifact) model.CacheEntry {
	t.Helper()
	entry := x.entry(t, files...)
	entry.Platform, entry.Result.Platform = "xhs", "xhs"
	entry.URL = "https://www.xiaohongshu.com/explore/isolated"
	entry.Result.SourceURL = entry.URL
	for i := range entry.Files {
		f := &entry.Files[i]
		if f.VideoPath != "" {
			f.VideoPath = filepath.Join(x.s.media.Root(), entry.Key, f.VideoPath)
			if err := os.WriteFile(f.VideoPath, []byte(filepath.Base(f.VideoPath)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		entry.Result.Media = append(entry.Result.Media, model.Media{Kind: f.Kind, Duration: f.Duration})
	}
	if err := x.s.st.PutCache(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestParseHubLivePhotoRealACKPairAndSignedLocations(t *testing.T) {
	for _, transport := range []string{"file", "http"} {
		t.Run(transport, func(t *testing.T) {
			x := newDeliveryHarness(t, transport)
			pair := model.Artifact{Kind: "livephoto", Path: "photo.jpg", VideoPath: "motion.mp4", ThumbPath: "motion-thumb.jpg", Size: 10, VideoSize: 20}
			entry := parseHubDeliveryEntry(t, x, pair, pair,
				model.Artifact{Kind: "gif", Path: "animation.gif", Size: 10},
				model.Artifact{Kind: "video", Path: "native.mp4", ThumbPath: "native-thumb.jpg", Size: 10})
			entry.Result.MarkdownContent = "## Heading\n\n[linked text](https://example.org/article)"
			result, _ := x.start(t, &entry)
			initial := x.next(t)
			deliveryTypes(t, initial, "image,image,image,text")
			x.location(t, initial.Params.Message[0].Data["file"], entry.Files[0].Path)
			x.location(t, initial.Params.Message[1].Data["file"], entry.Files[2].Path)
			x.location(t, initial.Params.Message[2].Data["file"], entry.Files[3].ThumbPath)
			text := initial.Params.Message[3].Data["text"].(string)
			if !strings.Contains(text, entry.Result.MarkdownContent) || strings.Contains(text, "\ncontent\n") {
				t.Fatalf("semantic Markdown lost: %q", text)
			}
			x.none(t)
			x.ack(t, initial, 0)
			for _, f := range []model.Artifact{entry.Files[0], entry.Files[3]} {
				video := x.next(t)
				deliveryTypes(t, video, "video")
				path := f.Path
				if f.Kind == "livephoto" {
					path = f.VideoPath
				}
				x.location(t, video.Params.Message[0].Data["file"], path)
				x.location(t, video.Params.Message[0].Data["thumb"], f.ThumbPath)
				x.none(t)
				x.ack(t, video, 0)
			}
			if err := deliveryResult(t, result); err != nil {
				t.Fatal(err)
			}
			x.none(t)
		})
	}
}

func TestParseHubLivePhotoFailureNeverReplaysOrSendsNext(t *testing.T) {
	for _, stage := range []string{"initial", "video"} {
		t.Run(stage, func(t *testing.T) {
			x := newDeliveryHarness(t, "http")
			entry := parseHubDeliveryEntry(t, x,
				model.Artifact{Kind: "livephoto", Path: "photo.jpg", VideoPath: "motion.mp4", Size: 10, VideoSize: 20},
				model.Artifact{Kind: "video", Path: "later.mp4", Size: 10})
			result, _ := x.start(t, &entry)
			action := x.next(t)
			deliveryTypes(t, action, "image,text")
			if stage == "video" {
				x.ack(t, action, 0)
				action = x.next(t)
				deliveryTypes(t, action, "video")
			}
			x.ack(t, action, 100)
			var apiError *onebot.APIError
			if err := deliveryResult(t, result); !errors.As(err, &apiError) || apiError.RetCode != 100 {
				t.Fatalf("missing OneBot failure: %v", err)
			}
			x.none(t)
		})
	}
}

func TestParseHubLivePhotoDisplaySwitches(t *testing.T) {
	for _, showInfo := range []bool{false, true} {
		for _, showCover := range []bool{false, true} {
			t.Run(fmt.Sprintf("info=%t/cover=%t", showInfo, showCover), func(t *testing.T) {
				x := newDeliveryHarness(t, "file")
				cfg := x.s.st.Config()
				cfg.Media.ShowInfo, cfg.Media.ShowCover = showInfo, showCover
				if err := x.s.st.SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				entry := parseHubDeliveryEntry(t, x, model.Artifact{Kind: "livephoto", Path: "photo.jpg", VideoPath: "motion.mp4", ThumbPath: "motion-thumb.jpg", Size: 10, VideoSize: 20})
				result, _ := x.start(t, &entry)
				initial := x.next(t)
				want := "image"
				if showInfo {
					want += ",text"
				}
				deliveryTypes(t, initial, want)
				x.location(t, initial.Params.Message[0].Data["file"], entry.Files[0].Path)
				x.none(t)
				x.ack(t, initial, 0)
				video := x.next(t)
				deliveryTypes(t, video, "video")
				x.location(t, video.Params.Message[0].Data["thumb"], entry.Files[0].ThumbPath)
				x.ack(t, video, 0)
				if err := deliveryResult(t, result); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestParseHubLivePhotoIndependentFileFallbackSizes(t *testing.T) {
	for _, transport := range []string{"file", "http"} {
		for _, largePhoto := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/photo=%t", transport, largePhoto), func(t *testing.T) {
				x := newDeliveryHarness(t, transport)
				cfg := x.s.st.Config()
				cfg.Media.MaxBytes, cfg.Media.OversizeMode = 1024, "file"
				if err := x.s.st.SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				pair := model.Artifact{Kind: "livephoto", Path: "photo.jpg", VideoPath: "motion.mp4", Size: 10, VideoSize: 2048}
				if largePhoto {
					pair.Size, pair.VideoSize = 2048, 10
				}
				entry := parseHubDeliveryEntry(t, x, pair, pair)
				result, _ := x.start(t, &entry)
				initial := x.next(t)
				want := "image,text"
				if largePhoto {
					want = "text"
				}
				deliveryTypes(t, initial, want)
				x.none(t)
				x.ack(t, initial, 0)
				upload := x.next(t)
				path := entry.Files[0].VideoPath
				if largePhoto {
					path = entry.Files[0].Path
				}
				if upload.Action != "upload_group_file" || upload.Params.Name != filepath.Base(path) {
					t.Fatalf("wrong component fallback: %+v", upload)
				}
				if transport == "file" {
					if upload.Params.File != path {
						t.Fatalf("file upload path = %q, want %q", upload.Params.File, path)
					}
				} else {
					x.location(t, upload.Params.File, path)
				}
				x.none(t)
				x.ack(t, upload, 0)
				if largePhoto {
					video := x.next(t)
					deliveryTypes(t, video, "video")
					x.location(t, video.Params.Message[0].Data["file"], entry.Files[0].VideoPath)
					x.ack(t, video, 0)
				}
				if err := deliveryResult(t, result); err != nil {
					t.Fatal(err)
				}
				x.none(t)
			})
		}
	}
}

func TestParseHubLivePhotoCompanionPolicyRecheckedAfterACK(t *testing.T) {
	for _, change := range []string{"size", "duration", "images", "platform", "cancel"} {
		t.Run(change, func(t *testing.T) {
			x := newDeliveryHarness(t, "http")
			entry := parseHubDeliveryEntry(t, x, model.Artifact{Kind: "livephoto", Path: "photo.jpg", VideoPath: "motion.mp4", Size: 10, VideoSize: 2048, Duration: 60}, model.Artifact{Kind: "gif", Path: "animation.gif", Size: 10})
			result, cancel := x.start(t, &entry)
			initial := x.next(t)
			cfg := x.s.st.Config()
			switch change {
			case "size":
				cfg.Media.MaxBytes = 1024
			case "duration":
				cfg.Media.MaxDurationSeconds = 1
			case "images":
				cfg.Media.MaxImages = 1
			case "platform":
				p := cfg.Platforms[entry.Platform]
				p.Enabled = false
				cfg.Platforms[entry.Platform] = p
			case "cancel":
				cancel()
			}
			if err := x.s.st.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			x.ack(t, initial, 0)
			err := deliveryResult(t, result)
			if err == nil || (change == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("companion bypassed %s change: %v", change, err)
			}
			x.none(t)
		})
	}
}

func TestParseHubLivePhotoLeaseCleanupWhitelistAndMissingCompanion(t *testing.T) {
	x := newDeliveryHarness(t, "http")
	entry := parseHubDeliveryEntry(t, x, model.Artifact{Kind: "livephoto", Path: "photo.jpg", VideoPath: "motion.mp4", ThumbPath: "thumb.jpg", Size: 10, VideoSize: 20})
	if !x.s.filesExist(&entry) {
		t.Fatal("complete pair not reusable")
	}
	unlisted := filepath.Join(x.s.media.Root(), entry.Key, "unlisted.mp4")
	if err := os.WriteFile(unlisted, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, x.s.mediaLocation(&entry, unlisted, x.target), nil)
	recorder := httptest.NewRecorder()
	x.s.MediaHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("signed unlisted file served: %d", recorder.Code)
	}
	if err := os.Remove(entry.Files[0].VideoPath); err != nil {
		t.Fatal(err)
	}
	if x.s.filesExist(&entry) {
		t.Fatal("cache reused with missing companion")
	}
	if err := os.WriteFile(entry.Files[0].VideoPath, []byte(filepath.Base(entry.Files[0].VideoPath)), 0600); err != nil {
		t.Fatal(err)
	}
	leased, release, err := x.s.acquire(context.Background(), x.s.st.Config(), entry.URL, entry.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	result, _ := x.start(t, leased)
	initial := x.next(t)
	if err := x.s.Cleanup(true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{entry.Files[0].Path, entry.Files[0].VideoPath, entry.Files[0].ThumbPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("leased pair component removed: %v", err)
		}
		recorder = httptest.NewRecorder()
		x.s.MediaHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, x.s.mediaLocation(&entry, path, x.target), nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("cleared pair component still served: %d", recorder.Code)
		}
	}
	x.ack(t, initial, 0)
	video := x.next(t)
	deliveryTypes(t, video, "video")
	x.ack(t, video, 0)
	if err := deliveryResult(t, result); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{entry.Files[0].Path, entry.Files[0].VideoPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("delivery released caller lease: %v", err)
		}
	}
	release()
	release = func() {}
	for _, path := range []string{entry.Files[0].Path, entry.Files[0].VideoPath, entry.Files[0].ThumbPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("released pair component retained: %v", err)
		}
	}
}

func TestParseHubParserCacheNamespacePreservesProtectedPlatforms(t *testing.T) {
	cfg := model.DefaultConfig()
	for _, platform := range model.Platforms {
		raw := "https://example.org/isolated"
		input := fmt.Sprintf("%s|%s|quality=%d|bytes=%d|download=%t", platform.ID, raw, cfg.Media.Quality, cfg.Media.MaxBytes, cfg.Media.AutoDownload)
		oldHash := sha256.Sum256([]byte(input))
		oldKey := hex.EncodeToString(oldHash[:])
		key := parserCacheKey(platform.ID, raw, cfg)
		if platform.ID == "bilibili" || platform.ID == "douyin" {
			if key != oldKey {
				t.Fatalf("protected %s cache key changed", platform.ID)
			}
			continue
		}
		newHash := sha256.Sum256([]byte(input + "|parser=parsehub-2.3.15"))
		if key == oldKey || key != hex.EncodeToString(newHash[:]) {
			t.Fatalf("%s cache namespace missing or unstable", platform.ID)
		}
	}
}
