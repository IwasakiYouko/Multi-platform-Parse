package policy

import (
	"testing"

	"multiparse/internal/model"
)

func TestParseHubMediaCheckCountsGIFAndLivePhotoAsImages(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Media.MaxImages = 2
	result := model.ParseResult{Media: []model.Media{{Kind: "gif"}, {Kind: "livephoto"}, {Kind: "image"}}}
	if err := MediaCheck(cfg, result, nil); err == nil {
		t.Fatal("GIF and LivePhoto did not count toward image limit")
	}
	result.Media = result.Media[:2]
	if err := MediaCheck(cfg, result, nil); err != nil {
		t.Fatalf("LivePhoto counted twice: %v", err)
	}
}

func TestParseHubMediaCheckValidatesEachLivePhotoComponent(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Media.MaxBytes = 100
	cfg.Media.MaxDurationSeconds = 10
	for _, test := range []struct {
		name string
		file model.Artifact
	}{
		{"image size", model.Artifact{Kind: "livephoto", Size: 101, VideoSize: 10}},
		{"video size", model.Artifact{Kind: "livephoto", Size: 10, VideoSize: 101}},
		{"video duration", model.Artifact{Kind: "livephoto", Size: 10, VideoSize: 10, Duration: 11}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := MediaCheck(cfg, model.ParseResult{}, []model.Artifact{test.file}); err == nil {
				t.Fatal("invalid LivePhoto component accepted")
			}
		})
	}
	if err := MediaCheck(cfg, model.ParseResult{Media: []model.Media{{Kind: "livephoto", Duration: 11}}}, nil); err == nil {
		t.Fatal("source LivePhoto duration bypassed limit")
	}
	cfg.Media.OversizeMode = "file"
	if err := MediaCheck(cfg, model.ParseResult{}, []model.Artifact{{Kind: "livephoto", Size: 101, VideoSize: 102, Duration: 10}}); err != nil {
		t.Fatalf("independent file fallback disallowed: %v", err)
	}
	if err := MediaCheck(cfg, model.ParseResult{}, []model.Artifact{{Kind: "livephoto", Size: 10, VideoSize: 10, Duration: 11}}); err == nil {
		t.Fatal("file fallback bypassed duration limit")
	}
}
