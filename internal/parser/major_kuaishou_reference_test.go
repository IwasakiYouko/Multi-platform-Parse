package parser

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestKSReferenceRequestedApolloAndBackups(t *testing.T) {
	page := []byte(`window.__APOLLO_STATE__={"defaultClient":{
        "VisionVideoDetailPhoto:other":{"id":"other","photoUrl":"https://cdn.example/other.mp4"},
        "VisionVideoSetRepresentation:1":{"url":"https://cdn.example/unrelated.mp4"},
        "VisionVideoDetailPhoto:wanted":{"id":"wanted","caption":"Wanted","manifest":{"__ref":"Manifest:wanted"}},
        "Manifest:wanted":{"adaptationSet":[{"type":"id","id":"Adaptation:wanted"}]},
        "Adaptation:wanted":{"duration":12,"representation":[{"__ref":"Rep:wanted"}]},
        "Rep:wanted":{"backupUrl":[{"url":"//cdn.example/first.mp4"},"https://cdn.example/second.mp4"],"width":1280,"height":720}
    }};`)
	r, err := majorKSPage(page, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	m := r.Media[0]
	if r.Title != "Wanted" || m.URL != "https://cdn.example/first.mp4" || len(m.BackupURLs) != 1 || m.BackupURLs[0] != "https://cdn.example/second.mp4" || m.Duration != 12 || m.Width != 1280 {
		t.Fatalf("bad Apollo reference/backup selection: %#v", r)
	}
	if _, err = majorKSPage(page, "missing"); err == nil {
		t.Fatal("unbound global representation accepted")
	}
	if _, err = majorKSPage([]byte(`window.__APOLLO_STATE__={"defaultClient":{"VisionVideoDetailPhoto:wanted":{"id":"other","photoUrl":"https://cdn.example/other.mp4"}}}`), "wanted"); err == nil {
		t.Fatal("mismatched requested photo accepted")
	}
	cycle := map[string]any{"A": map[string]any{"__ref": "A"}}
	if majorKSDeref(map[string]any{"__ref": "A"}, cycle, 0) != nil {
		t.Fatal("reference cycle survived depth bound")
	}
}

func TestKSReferenceAtlasQualityAndClassification(t *testing.T) {
	page := []byte(`window.INIT_STATE={"detail":{"photo":{"id":"wanted","caption":"Atlas","ext_params":{"atlas":{"cdn":"low.example","list":["low.jpg"],"size":[{"w":1,"h":2}]}}},"atlas":{"cdnList":[{"cdn":"//high.example"}],"list":["one.webp","https://high.example/animated.gif"],"size":[{"w":100,"h":200},{"w":300,"h":400}]}}};`)
	r, err := majorKSPage(page, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "image" || len(r.Media) != 2 || r.Media[0].URL != "https://high.example/one.webp" || r.Media[1].Kind != "gif" || r.Media[1].Width != 300 {
		t.Fatalf("preferred atlas or media type lost: %#v", r)
	}
	r, err = majorKSPhoto(map[string]any{"ext_params": map[string]any{"atlas": map[string]any{"cdn": "cdn.example", "list": []any{"a.jpg", "b.jpg"}, "size": []any{map[string]any{"w": 1, "h": 2}}}}}, nil)
	if err != nil || r.Media[0].Width != 0 || r.Media[1].Height != 0 {
		t.Fatalf("partial sizes incorrectly assigned: %#v %v", r, err)
	}
	// A cover is a valid last-resort still only for a bound non-video page.
	r, err = majorKSPage([]byte(`window.INIT_STATE={"detail":{"photo":{"id":"wanted","coverUrls":[{"url":"//cdn.example/image.jpg"}]}}};`), "wanted")
	if err != nil || r.Type != "image" {
		t.Fatalf("bound image cover fallback lost: %#v %v", r, err)
	}
	if _, err = majorKSPageMode([]byte(`window.INIT_STATE={"detail":{"photo":{"id":"wanted","coverUrls":[{"url":"//cdn.example/image.jpg"}]}}};`), "wanted", true); err == nil {
		t.Fatal("short-video cover faked successful video")
	}
	if _, err = majorKSPhoto(map[string]any{"coverUrl": "https://cdn.example/poster.jpg"}, nil); err == nil {
		t.Fatal("unclassified cover accepted as video")
	}
}

func TestKSReferenceRepresentationsAndAPIH265(t *testing.T) {
	photo := map[string]any{"manifestH265": `{"adaptationSet":[{"duration":9,"representation":[{"url":"https://cdn.example/hevc.mp4","width":720,"height":1280}]}]}`, "videoResource": map[string]any{"h264": map[string]any{"adaptationSet": []any{map[string]any{"duration": 7, "representation": []any{map[string]any{"url": "https://cdn.example/avc.mp4", "width": 640, "height": 360}}}}}}}
	r, err := majorKSPhotoMode(photo, map[string]any{"api": true}, true, false)
	if err != nil || r.Media[0].URL != "https://cdn.example/hevc.mp4" || r.Media[0].Duration != 9 {
		t.Fatalf("API HEVC selection lost: %#v %v", r, err)
	}
	r, err = majorKSPhoto(photo, nil)
	if err != nil || r.Media[0].URL != "https://cdn.example/avc.mp4" {
		t.Fatalf("HTML h264 selection lost: %#v %v", r, err)
	}
	rep := map[string]any{"m3u8Slice": "#EXTM3U\n#EXTINF:2\nhttps://cdn.example/first.ts\nhttps://cdn.example/second.ts"}
	media, ok := majorKSRepresentation(rep, map[string]any{"duration": 2}, nil)
	if !ok || media.URL != "https://cdn.example/first.ts" {
		t.Fatalf("manifest slice fallback lost: %#v", media)
	}
	if _, ok = majorKSRepresentation(map[string]any{"url": 123, "backupUrl": []any{nil, 456}}, nil, nil); ok {
		t.Fatal("non-URL representation accepted")
	}
}

func TestKSReferenceRequestAndGraphQLFallback(t *testing.T) {
	var attempts []string
	client := &http.Client{Transport: majorTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") == "" {
			t.Error("configured cookie missing from owned route")
		}
		if req.URL.Path == "/graphql" {
			attempts = append(attempts, "API")
			raw, _ := io.ReadAll(req.Body)
			var body map[string]any
			if json.Unmarshal(raw, &body) != nil {
				t.Error("invalid GraphQL request")
			}
			if Text(Path(body, "variables", "photoId")) != "wanted" || Text(Path(body, "variables", "page")) != "search" || !strings.Contains(Text(body["query"]), "manifestH265") {
				t.Error("GraphQL identity or manifest query lost")
			}
			return majorTestResponse(req, `{"data":{"visionVideoDetail":{"author":{"name":"Creator"},"photo":{"id":"wanted","caption":"API","manifestH265":{"adaptationSet":[{"duration":3,"representation":[{"backupUrl":["https://cdn.example/backup.mp4"],"width":100,"height":200}]}]}}}}}`), nil
		}
		name := req.URL.Host + req.URL.Path
		if req.Header.Get("User-Agent") == "Mozilla/5.0" {
			name += " light"
		}
		attempts = append(attempts, name)
		return majorTestResponse(req, `{"result":2}`), nil
	})}
	r, err := majorKuaishou(context.Background(), client, "https://www.kuaishou.com/short-video/wanted", "did=local-device")
	if err != nil {
		t.Fatal(err)
	}
	want := "www.kuaishou.com/short-video/wanted|www.kuaishou.com/short-video/wanted light|v.m.chenzhongtech.com/fw/photo/wanted|v.m.chenzhongtech.com/fw/photo/wanted light|API"
	if strings.Join(attempts, "|") != want || len(r.FallbackErrors) != 4 || r.Author != "Creator" || r.Media[0].URL != "https://cdn.example/backup.mp4" {
		t.Fatalf("fallback order/results wrong: %#v %#v", attempts, r)
	}
}

func TestKSReferenceUnavailableAndWrongAPIID(t *testing.T) {
	for _, code := range []string{"223", "20113"} {
		page := []byte(`window.INIT_STATE={"detail":{"result":` + code + `,"error_msg":"removed"}};`)
		_, err := majorKSPage(page, "wanted")
		if !referenceUnavailable(err) {
			t.Fatalf("content-unavailable %s lost: %v", code, err)
		}
	}
	calls := 0
	client := &http.Client{Transport: majorTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/graphql" {
			calls++
			return majorTestResponse(req, `{"data":{"visionVideoDetail":{"photo":{"id":"other","photoUrl":"https://cdn.example/other.mp4"}}}}`), nil
		}
		return majorTestResponse(req, "empty"), nil
	})}
	if _, err := majorKuaishou(context.Background(), client, "https://www.kuaishou.com/short-video/wanted", ""); err == nil || calls != 1 {
		t.Fatalf("wrong GraphQL photo accepted: %v calls=%d", err, calls)
	}
	client.Transport = majorTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/graphql" {
			t.Error("unavailable content triggered API request")
		}
		return majorTestResponse(req, `window.INIT_STATE={"error":{"result":223,"error_msg":"removed"}};`), nil
	})
	if _, err := majorKuaishou(context.Background(), client, "https://www.kuaishou.com/short-video/wanted", ""); !referenceUnavailable(err) {
		t.Fatalf("unavailable route error lost: %v", err)
	}
}

func TestKSReferenceDurationUnits(t *testing.T) {
	direct, err := majorKSPhoto(map[string]any{
		"photoUrl": "https://cdn.example/video.mp4",
		"duration": 30000,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Media[0].Duration != 30 {
		t.Fatalf("photo milliseconds not normalized: %v", direct.Media[0].Duration)
	}
	representation, ok := majorKSRepresentation(
		map[string]any{"url": "https://cdn.example/video.mp4"},
		map[string]any{"duration": 30},
		nil,
	)
	if !ok || representation.Duration != 30 {
		t.Fatalf("adaptation duration seconds changed: %#v", representation)
	}
}
