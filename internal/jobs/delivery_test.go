package jobs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"multiparse/internal/model"
	"multiparse/internal/onebot"
)

type deliveryAction struct {
	Action string `json:"action"`
	Params struct {
		Message  []model.Segment `json:"message"`
		Messages []model.Segment `json:"messages"`
		File     string          `json:"file"`
		Name     string          `json:"name"`
		GroupID  int64           `json:"group_id"`
	} `json:"params"`
	Echo string `json:"echo"`
}

type deliveryRead struct {
	action deliveryAction
	err    error
}

type deliveryHarness struct {
	s       *Service
	peer    *websocket.Conn
	actions chan deliveryRead
	target  model.Target
}

func newDeliveryHarness(t *testing.T, transport string) *deliveryHarness {
	t.Helper()
	s := cacheService(t)
	hub := onebot.New(s.st.Config, nil)
	s.sender = hub
	mux := http.NewServeMux()
	mux.Handle("/onebot/", hub.Handler())
	mux.Handle("/media/", s.MediaHandler())
	server := httptest.NewServer(mux)
	t.Cleanup(func() { hub.Close(); server.Close() })
	cfg := s.st.Config()
	cfg.Media.Transport = transport
	cfg.Server.ExternalURL = server.URL
	if err := s.st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	peer, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/onebot/v11/ws", http.Header{
		"Authorization": {"Bearer " + cfg.OneBot.Token},
		"X-Self-Id":     {"10001"},
		"X-Client-Role": {"Universal"},
	})
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for len(hub.Status()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("isolated OneBot peer did not connect")
		}
		time.Sleep(time.Millisecond)
	}
	x := &deliveryHarness{s: s, peer: peer, actions: make(chan deliveryRead, 16), target: model.Target{BotID: "10001", GroupID: 123, UserID: 456}}
	go func() {
		for {
			var action deliveryAction
			err := peer.ReadJSON(&action)
			x.actions <- deliveryRead{action: action, err: err}
			if err != nil {
				return
			}
		}
	}()
	return x
}

func (x *deliveryHarness) entry(t *testing.T, files ...model.Artifact) model.CacheEntry {
	t.Helper()
	entry := seedCache(t, x.s, strings.Repeat("d", 64), time.Now())
	entry.Result = model.ParseResult{Platform: "bilibili", Title: "title", Author: "author", Tags: []string{"科技", "数码"}, Content: "content", SourceURL: entry.URL}
	entry.Files = files
	for i := range entry.Files {
		f := &entry.Files[i]
		f.Path = filepath.Join(x.s.media.Root(), entry.Key, f.Path)
		if f.ThumbPath != "" {
			f.ThumbPath = filepath.Join(x.s.media.Root(), entry.Key, f.ThumbPath)
		}
		for _, path := range []string{f.Path, f.ThumbPath} {
			if path != "" {
				if err := os.WriteFile(path, []byte(filepath.Base(path)), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := x.s.st.PutCache(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func (x *deliveryHarness) start(t *testing.T, entry *model.CacheEntry) (<-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- x.s.deliver(ctx, x.target, entry) }()
	return result, cancel
}

func (x *deliveryHarness) next(t *testing.T) deliveryAction {
	t.Helper()
	select {
	case got := <-x.actions:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.action.Echo == "" || got.action.Params.GroupID != x.target.GroupID {
			t.Fatalf("invalid OneBot action: %+v", got.action)
		}
		return got.action
	case <-time.After(3 * time.Second):
		t.Fatal("delivery action did not arrive")
		return deliveryAction{}
	}
}

func (x *deliveryHarness) none(t *testing.T) {
	t.Helper()
	select {
	case got := <-x.actions:
		t.Fatalf("unexpected action without a successful preceding ACK: %+v, %v", got.action, got.err)
	case <-time.After(75 * time.Millisecond):
	}
}

func (x *deliveryHarness) ack(t *testing.T, action deliveryAction, retcode int) {
	t.Helper()
	if err := x.peer.WriteJSON(map[string]any{"echo": action.Echo, "status": "ok", "retcode": retcode, "wording": "isolated peer response"}); err != nil {
		t.Fatal(err)
	}
}

func deliveryResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("delivery did not complete")
		return nil
	}
}

func deliveryTypes(t *testing.T, action deliveryAction, want string) {
	t.Helper()
	if action.Action != "send_group_msg" {
		t.Fatalf("expected ordinary send_group_msg, got %q", action.Action)
	}
	var kinds []string
	for _, segment := range action.Params.Message {
		kinds = append(kinds, segment.Type)
	}
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("message types = %q, want %q", got, want)
	}
}

func (x *deliveryHarness) location(t *testing.T, value any, path string) {
	t.Helper()
	raw, ok := value.(string)
	if !ok {
		t.Fatalf("media location is not a string: %#v", value)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if x.s.st.Config().Media.Transport == "file" {
		if u.Scheme != "file" || u.Path != filepath.ToSlash(path) {
			t.Fatalf("file location = %q, want %q", raw, path)
		}
		return
	}
	if u.Scheme != "http" || u.Host == "" || u.Query().Get("signature") == "" || u.Query().Get("expires") == "" || u.Query().Get("group") != "123" || u.Query().Get("user") != "456" || u.Query().Get("bot") != "10001" {
		t.Fatalf("HTTP media reference is not an absolute target-bound signed URL: %q", raw)
	}
	response, err := http.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != filepath.Base(path) {
		t.Fatalf("signed media unavailable or wrong path: status=%d body=%q want=%q", response.StatusCode, body, filepath.Base(path))
	}
}

func forwardNodeSegment(t *testing.T, node model.Segment) model.Segment {
	t.Helper()
	if node.Type != "node" {
		t.Fatalf("expected node segment, got %q", node.Type)
	}
	rawContent, ok := node.Data["content"].([]any)
	if !ok || len(rawContent) != 1 {
		t.Fatalf("unexpected forward node content: %#v", node.Data["content"])
	}
	item, ok := rawContent[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected forward segment map: %#v", rawContent[0])
	}
	segType, _ := item["type"].(string)
	segData, _ := item["data"].(map[string]any)
	return model.Segment{Type: segType, Data: segData}
}

func TestDeliverCoverCardThenMergedForwardAndFallback(t *testing.T) {
	for _, transport := range []string{"file", "http"} {
		t.Run(transport, func(t *testing.T) {
			x := newDeliveryHarness(t, transport)
			entry := x.entry(t,
				model.Artifact{Kind: "video", Path: "first.mp4", ThumbPath: "image.jpg", Size: 10},
				model.Artifact{Kind: "image", Path: "image.jpg", Size: 10},
				model.Artifact{Kind: "image", Path: "second-img.jpg", Size: 10},
				model.Artifact{Kind: "video", Path: "second.mp4", ThumbPath: "cover.jpg", Size: 10},
			)
			result, _ := x.start(t, &entry)
			// Message 1: Single primary cover + Title/Tags/Content/URL
			first := x.next(t)
			deliveryTypes(t, first, "image,text")
			x.location(t, first.Params.Message[0].Data["file"], entry.Files[0].ThumbPath)
			if got := first.Params.Message[1].Data["text"]; got != "【title】\n#科技 #数码\ncontent\n链接："+entry.URL {
				t.Fatalf("formatted text changed: %q", got)
			}
			x.none(t)
			select {
			case err := <-result:
				t.Fatalf("delivery completed before initial ACK: %v", err)
			default:
			}
			x.ack(t, first, 0)

			// Message 2: Merged forward containing all 4 deduplicated media items in order.
			fwd := x.next(t)
			if fwd.Action != "send_group_forward_msg" || len(fwd.Params.Messages) != 4 {
				t.Fatalf("expected 4-node send_group_forward_msg, got action=%q len=%d", fwd.Action, len(fwd.Params.Messages))
			}
			s0 := forwardNodeSegment(t, fwd.Params.Messages[0])
			s1 := forwardNodeSegment(t, fwd.Params.Messages[1])
			s2 := forwardNodeSegment(t, fwd.Params.Messages[2])
			s3 := forwardNodeSegment(t, fwd.Params.Messages[3])
			if s0.Type != "video" || s1.Type != "image" || s2.Type != "image" || s3.Type != "video" {
				t.Fatalf("wrong forward media sequence: %s,%s,%s,%s", s0.Type, s1.Type, s2.Type, s3.Type)
			}
			x.location(t, s0.Data["file"], entry.Files[0].Path)
			x.location(t, s0.Data["thumb"], entry.Files[0].ThumbPath)
			x.location(t, s1.Data["file"], entry.Files[1].Path)
			x.location(t, s2.Data["file"], entry.Files[2].Path)
			x.location(t, s3.Data["file"], entry.Files[3].Path)
			x.ack(t, fwd, 0)

			if err := deliveryResult(t, result); err != nil {
				t.Fatal(err)
			}
			x.none(t)
		})
	}
}

func TestDeliverMixedForwardNegativeACKFallsBackToImagesForwardAndSeparateVideos(t *testing.T) {
	x := newDeliveryHarness(t, "file")
	entry := x.entry(t,
		model.Artifact{Kind: "image", Path: "img1.jpg", Size: 10},
		model.Artifact{Kind: "image", Path: "img2.jpg", Size: 10},
		model.Artifact{Kind: "video", Path: "vid1.mp4", ThumbPath: "thumb1.jpg", Size: 10},
	)
	result, _ := x.start(t, &entry)
	msg1 := x.next(t)
	deliveryTypes(t, msg1, "image,text")
	x.ack(t, msg1, 0)

	mixedFwd := x.next(t)
	if mixedFwd.Action != "send_group_forward_msg" || len(mixedFwd.Params.Messages) != 3 {
		t.Fatalf("expected initial 3-node mixed forward, got %+v", mixedFwd)
	}
	// Simulate QQNT rejecting video inside merged forward -> automatic fallback!
	x.ack(t, mixedFwd, 1200)

	imgFwd := x.next(t)
	if imgFwd.Action != "send_group_forward_msg" || len(imgFwd.Params.Messages) != 2 {
		t.Fatalf("expected 2-image fallback forward, got %+v", imgFwd)
	}
	img0 := forwardNodeSegment(t, imgFwd.Params.Messages[0])
	img1 := forwardNodeSegment(t, imgFwd.Params.Messages[1])
	if img0.Type != "image" || img1.Type != "image" {
		t.Fatalf("expected image-only forward nodes, got %s, %s", img0.Type, img1.Type)
	}
	x.location(t, img0.Data["file"], entry.Files[0].Path)
	x.location(t, img1.Data["file"], entry.Files[1].Path)
	x.ack(t, imgFwd, 0)

	vidMsg := x.next(t)
	deliveryTypes(t, vidMsg, "video")
	x.location(t, vidMsg.Params.Message[0].Data["file"], entry.Files[2].Path)
	x.ack(t, vidMsg, 0)

	if err := deliveryResult(t, result); err != nil {
		t.Fatalf("mixed forward fallback should succeed: %v", err)
	}
	x.none(t)
}

func TestDeliverNegativeACKStopsWithoutRepeatingInitialMessage(t *testing.T) {
	for _, failedStage := range []string{"initial", "forward"} {
		t.Run(failedStage, func(t *testing.T) {
			x := newDeliveryHarness(t, "file")
			entry := x.entry(t, model.Artifact{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg", Size: 10}, model.Artifact{Kind: "video", Path: "later.mp4", Size: 10})
			result, _ := x.start(t, &entry)
			failed := x.next(t)
			deliveryTypes(t, failed, "image,text")
			wantContext := "图文"
			if failedStage == "forward" {
				x.ack(t, failed, 0)
				failed = x.next(t)
				if failed.Action != "send_group_forward_msg" {
					t.Fatalf("expected send_group_forward_msg, got %q", failed.Action)
				}
				wantContext = "合并转发"
			}
			x.ack(t, failed, 100)
			err := deliveryResult(t, result)
			var apiError *onebot.APIError
			if !errors.As(err, &apiError) || apiError.RetCode != 100 || !strings.Contains(err.Error(), wantContext) {
				t.Fatalf("missing contextual OneBot rejection: %v", err)
			}
			x.none(t)
		})
	}
}

func TestDeliverDisplaySwitchesAndMissingMedia(t *testing.T) {
	tests := []struct {
		name       string
		info       bool
		cover      bool
		files      []model.Artifact
		firstTypes string
		video      bool
	}{
		{"video info cover", true, true, []model.Artifact{{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg"}}, "image,text", true},
		{"video info no cover", true, false, []model.Artifact{{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg"}}, "text", true},
		{"video cover no info", false, true, []model.Artifact{{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg"}}, "image", true},
		{"video neither", false, false, []model.Artifact{{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg"}}, "", true},
		{"video no thumbnail", false, true, []model.Artifact{{Kind: "video", Path: "video.mp4"}}, "", true},
		{"images info", true, false, []model.Artifact{{Kind: "image", Path: "image.jpg"}}, "image,text", false},
		{"images no info", false, false, []model.Artifact{{Kind: "image", Path: "image.jpg"}}, "image", false},
		{"no media info", true, true, nil, "text", false},
		{"no media source fallback", false, false, nil, "text", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			x := newDeliveryHarness(t, "file")
			cfg := x.s.st.Config()
			cfg.Media.ShowInfo, cfg.Media.ShowCover = test.info, test.cover
			if err := x.s.st.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			entry := x.entry(t, test.files...)
			result, _ := x.start(t, &entry)
			if test.firstTypes != "" {
				first := x.next(t)
				deliveryTypes(t, first, test.firstTypes)
				if !test.info && len(test.files) == 0 && first.Params.Message[0].Data["text"] != entry.URL {
					t.Fatalf("source fallback changed: %+v", first)
				}
				x.ack(t, first, 0)
			}
			if test.video {
				video := x.next(t)
				deliveryTypes(t, video, "video")
				if entry.Files[0].ThumbPath != "" {
					x.location(t, video.Params.Message[0].Data["thumb"], entry.Files[0].ThumbPath)
				} else if _, exists := video.Params.Message[0].Data["thumb"]; exists {
					t.Fatal("video without thumbnail gained a thumb")
				}
				x.ack(t, video, 0)
			}
			if err := deliveryResult(t, result); err != nil {
				t.Fatal(err)
			}
			x.none(t)
		})
	}
}

func TestDeliverRechecksPolicyAfterHeldACK(t *testing.T) {
	for _, change := range []string{"platform", "size", "duration", "image count", "cancel"} {
		t.Run(change, func(t *testing.T) {
			x := newDeliveryHarness(t, "http")
			entry := x.entry(t, model.Artifact{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg", Size: 2048, Duration: 60})
			entry.Result.Media = []model.Media{{Kind: "image"}, {Kind: "image"}}
			result, cancel := x.start(t, &entry)
			first := x.next(t)
			deliveryTypes(t, first, "image,text")
			x.none(t)
			cfg := x.s.st.Config()
			switch change {
			case "platform":
				p := cfg.Platforms[entry.Platform]
				p.Enabled = false
				cfg.Platforms[entry.Platform] = p
			case "size":
				cfg.Media.MaxBytes = 1024
			case "duration":
				cfg.Media.MaxDurationSeconds = 1
			case "image count":
				cfg.Media.MaxImages = 1
			case "cancel":
				cancel()
			}
			if err := x.s.st.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			x.ack(t, first, 0)
			err := deliveryResult(t, result)
			if err == nil || (change == "cancel" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("held ACK bypassed changed %s policy: %v", change, err)
			}
			x.none(t)
		})
	}
}

func TestDeliverHeldACKRetainsCacheLease(t *testing.T) {
	x := newDeliveryHarness(t, "http")
	entry := x.entry(t, model.Artifact{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg", Size: 10})
	leased, release, err := x.s.acquire(context.Background(), x.s.st.Config(), entry.URL, entry.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	result, _ := x.start(t, leased)
	first := x.next(t)
	if err := x.s.Cleanup(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry.Files[0].Path); err != nil {
		t.Fatalf("held ACK lost leased video: %v", err)
	}
	response, err := http.Get(first.Params.Message[0].Data["file"].(string))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("cleared cache still served signed cover: %d", response.StatusCode)
	}
	x.none(t)
	x.ack(t, first, 0)
	video := x.next(t)
	deliveryTypes(t, video, "video")
	x.ack(t, video, 0)
	if err := deliveryResult(t, result); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry.Files[0].Path); err != nil {
		t.Fatalf("delivery released caller's cache lease: %v", err)
	}
}

func TestDeliverPreservesAudioFileAndOversizeUploads(t *testing.T) {
	for _, transport := range []string{"file", "http"} {
		t.Run(transport, func(t *testing.T) {
			x := newDeliveryHarness(t, transport)
			cfg := x.s.st.Config()
			cfg.Media.MaxBytes = 1024
			cfg.Media.OversizeMode = "file"
			cfg.Media.ShowCover = false
			if err := x.s.st.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			entry := x.entry(t,
				model.Artifact{Kind: "audio", Path: "audio.mp3", Size: 5},
				model.Artifact{Kind: "file", Path: "document.bin", Size: 5},
				model.Artifact{Kind: "image", Path: "large.jpg", Size: 2048},
				model.Artifact{Kind: "video", Path: "large.mp4", Size: 2048},
			)
			result, _ := x.start(t, &entry)
			first := x.next(t)
			deliveryTypes(t, first, "text")
			x.none(t)
			x.ack(t, first, 0)
			for _, f := range entry.Files {
				action := x.next(t)
				if action.Action != "upload_group_file" || len(action.Params.Message) != 0 || action.Params.Name != filepath.Base(f.Path) {
					t.Fatalf("upload fallback changed: %+v", action)
				}
				if transport == "file" {
					if action.Params.File != f.Path {
						t.Fatalf("OneBot file upload = %q, want %q", action.Params.File, f.Path)
					}
				} else {
					x.location(t, action.Params.File, f.Path)
				}
				x.none(t)
				x.ack(t, action, 0)
			}
			if err := deliveryResult(t, result); err != nil {
				t.Fatal(err)
			}
			x.none(t)
		})
	}
}

func TestDeliverGroupReplyAndPlatformTemplateOverride(t *testing.T) {
	x := newDeliveryHarness(t, "file")
	x.target.MessageID = "987654"
	cfg := x.s.st.Config()
	pol := cfg.Platforms["bilibili"]
	pol.TemplateMode = "custom"
	pol.MessageTemplate = "{title}"
	pol.CoverMode = "off"
	cfg.Platforms["bilibili"] = pol
	if err := x.s.st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	entry := x.entry(t, model.Artifact{Kind: "video", Path: "video.mp4", ThumbPath: "cover.jpg", Size: 10})
	result, _ := x.start(t, &entry)
	first := x.next(t)
	deliveryTypes(t, first, "reply,text")
	if first.Params.Message[0].Data["id"] != "987654" {
		t.Fatalf("reply id = %v, want 987654", first.Params.Message[0].Data["id"])
	}
	if first.Params.Message[1].Data["text"] != "【title】" {
		t.Fatalf("custom template text = %v, want 【title】", first.Params.Message[1].Data["text"])
	}
	x.ack(t, first, 0)
	second := x.next(t)
	deliveryTypes(t, second, "video")
	x.ack(t, second, 0)
	if err := deliveryResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCacheEntryAndPreview(t *testing.T) {
	s := cacheService(t)
	key := strings.Repeat("a", 64)
	entry := seedCache(t, s, key, time.Now())
	if err := s.DeleteCacheEntry(key); err != nil {
		t.Fatal(err)
	}
	if got, err := s.st.GetCache(key); err != nil || got != nil {
		t.Fatalf("cache entry persisted after delete: %+v %v", got, err)
	}
	if _, err := os.Stat(entry.Files[0].Path); !os.IsNotExist(err) {
		t.Fatalf("cache file persisted after delete: %v", err)
	}
	s.resolve = func(ctx context.Context, cfg model.Config, raw string) (*model.ParseResult, error) {
		return &model.ParseResult{Platform: "bilibili", Title: "预览标题", Author: "UP", SourceURL: raw, Type: "video"}, nil
	}
	res, err := s.Preview(context.Background(), "BV1GJ411x7h7")
	if err != nil || res.Title != "预览标题" {
		t.Fatalf("Preview failed: %+v %v", res, err)
	}
}
