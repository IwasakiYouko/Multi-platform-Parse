package parser

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type majorTestTransport func(*http.Request) (*http.Response, error)

func (f majorTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func majorTestResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func TestMajorSM3(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"abc", "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"}, {strings.Repeat("abcd", 16), "debe9ff92275b8a138604889c18e5a4d6fdb70e5387e5765293dcba39c0c5732"}} {
		if got := hex.EncodeToString(majorSM3([]byte(tc.input))); got != tc.want {
			t.Fatalf("SM3(%q)=%s", tc.input, got)
		}
	}
}
func TestMajorBilibiliPageSelectionAndDASH(t *testing.T) {
	requestedCID := ""
	client := &http.Client{Transport: majorTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{}}`
		switch r.URL.Path {
		case "/x/web-interface/view":
			body = `{"code":0,"data":{"title":"Multipart","desc":"Description","pic":"https://i0.hdslb.com/cover.jpg","owner":{"name":"Uploader"},"pages":[{"page":1,"cid":11,"part":"First","duration":10},{"page":2,"cid":22,"part":"Second","duration":20}]}}`
		case "/x/frontend/finger/spi":
			body = `{"code":0,"data":{"b_3":"anonymous3","b_4":"anonymous4"}}`
		case "/x/player/playurl":
			requestedCID = r.URL.Query().Get("cid")
			if !strings.Contains(r.Header.Get("Cookie"), "SESSDATA=test") {
				t.Error("session cookie lost")
			}
			body = `{"code":0,"data":{"dash":{"duration":20,"video":[{"baseUrl":"https://cdn.bilivideo.com/video.m4s","codecs":"avc1.640028","bandwidth":100,"width":1920,"height":1080}],"audio":[{"baseUrl":"https://cdn.bilivideo.com/audio.m4s","codecs":"mp4a.40.2","bandwidth":128000}]}}}`
		}
		return majorTestResponse(r, body), nil
	})}
	r, err := majorBilibili(context.Background(), client, "https://www.bilibili.com/video/BV1xx411c7mD?p=2", "SESSDATA=test")
	if err != nil {
		t.Fatal(err)
	}
	if requestedCID != "22" || len(r.Media) != 1 || r.Media[0].AudioURL != "https://cdn.bilivideo.com/audio.m4s" || r.Title != "Multipart - Second" {
		t.Fatalf("wrong multipart DASH result: %#v cid=%s", r, requestedCID)
	}
	if r.Media[0].Headers["Cookie"] != "" {
		t.Fatal("session cookie leaked into CDN headers")
	}
	if _, err = majorBilibili(context.Background(), client, "https://www.bilibili.com/video/BV1xx411c7mD?p=3", ""); err == nil {
		t.Fatal("invalid P silently selected P1")
	}
	if _, err = majorBilibili(context.Background(), client, "https://www.bilibili.com/video/BV1xx411c7mD?p=2&cid=11", ""); err == nil {
		t.Fatal("mismatched cid accepted")
	}
}
func TestMajorDouyinEmbeddedImages(t *testing.T) {
	state := `{"aweme_id":"123","desc":"Album","images":[{"url_list":["https://cdn.example/1.jpg"],"width":100,"height":200},{"url_list":["https://cdn.example/2.jpg"],"video":{"play_addr":{"url_list":["https://cdn.example/playwm/2.mp4"]},"duration":3000}}]}`
	page := []byte(`<script id="RENDER_DATA" type="application/json">` + url.PathEscape(state) + `</script>`)
	detail := majorDouyinState(page, "123")
	if detail == nil {
		t.Fatal("URL-encoded RENDER_DATA not found")
	}
	r, err := majorDouyinDetail(detail)
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "multimedia" || len(r.Media) != 3 || r.Media[0].URL != "https://cdn.example/1.jpg" || r.Media[2].URL != "https://cdn.example/play/2.mp4" || r.Media[2].Duration != 3 {
		t.Fatalf("wrong ordered live album: %#v", r)
	}
	if _, err = majorDouyinDetail(map[string]any{"video": map[string]any{"cover": map[string]any{"url_list": []any{"https://cdn.example/poster.jpg"}}}}); err == nil {
		t.Fatal("poster accepted as playable video")
	}
}
func TestMajorXHSLivePhoto(t *testing.T) {
	note := map[string]any{"type": "normal", "title": "Live", "imageList": []any{map[string]any{"urlDefault": "https://sns-img.example/image.jpg", "livePhoto": true, "stream": map[string]any{"h264": []any{map[string]any{"masterUrl": "https://sns-video.example/live.mp4", "duration": 3000, "width": 100, "height": 200}}}}}}
	r, err := majorXHSParseNote(note)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Media) != 2 || r.Media[0].Kind != "image" || r.Media[1].Kind != "video" || r.Type != "multimedia" {
		t.Fatalf("live image/video pair lost: %#v", r)
	}
	if err = majorXHSLocation("https://www.xiaohongshu.com/login?redirect=x"); err == nil {
		t.Fatal("login redirect accepted")
	}
}
func TestMajorKuaishouAtlasAndPoster(t *testing.T) {
	page := []byte(`window.INIT_STATE={"detail":{"photo":{"id":"abc","caption":"Album"},"atlas":{"cdn":["cdn.example"],"list":["one.webp","two.webp"],"size":[{"w":100,"h":200},{"w":300,"h":400}]}}};`)
	r, err := majorKSPage(page, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "image" || len(r.Media) != 2 || r.Media[1].URL != "https://cdn.example/two.webp" || r.Media[1].Width != 300 {
		t.Fatalf("incorrect atlas: %#v", r)
	}
	if _, err = majorKSPhoto(map[string]any{"caption": "Poster", "coverUrl": "https://cdn.example/cover.jpg"}, nil); err == nil {
		t.Fatal("poster accepted as video")
	}
}
func TestMajorCookieScope(t *testing.T) {
	for _, raw := range []string{"https://evil.example/", "https://xiaohongshu.com.evil.example/", "https://xhslink.com/a/short"} {
		if h := majorHeaders("secret=1", "https://www.xiaohongshu.com/", raw, "xiaohongshu.com"); h["Cookie"] != "" {
			t.Fatalf("cookie leaked to %s", raw)
		}
	}
	if h := majorHeaders("secret=1", "https://www.xiaohongshu.com/", "https://www.xiaohongshu.com/explore/id", "xiaohongshu.com"); h["Cookie"] != "secret=1" {
		t.Fatal("platform cookie not forwarded")
	}
}
func TestMajorSignedRequestErrorRedaction(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://www.douyin.com/api?uifid=private&msToken=private", Err: errors.New("connection refused")}
	if got := majorSafeRequestError(err); got != "connection refused" {
		t.Fatalf("signed request URL leaked: %s", got)
	}
	if got := majorSetCookie("SESSDATA=preserve; buvid3=stale; buvid4=old", "buvid3", "fresh"); got != "SESSDATA=preserve; buvid4=old; buvid3=fresh" {
		t.Fatalf("cookie update lost session or kept stale fingerprint: %s", got)
	}
}
func TestMajorStatesPreservesSignedURLs(t *testing.T) {
	signed := "https://cdn.example/path%2Fpart.mp4?token=a%26b%3Dc%22d&signature=keep%252F"
	state := `{"media":{"url":"` + signed + `"}}`
	for _, scriptID := range []string{"__NEXT_DATA__", "RENDER_DATA", "SIGI_STATE", "__UNIVERSAL_DATA_FOR_REHYDRATION__"} {
		states := majorStates([]byte(`<script id="` + scriptID + `">` + state + `</script>`))
		if len(states) != 1 || Text(Path(states[0], "media", "url")) != signed {
			t.Fatalf("%s modified valid JSON signed URL: %#v", scriptID, states)
		}
	}
	states := majorStates([]byte(`<script id="RENDER_DATA">` + url.PathEscape(state) + `</script>`))
	if len(states) != 1 || Text(Path(states[0], "media", "url")) != signed {
		t.Fatalf("encoded RENDER_DATA not decoded exactly once: %#v", states)
	}
}
func TestMajorKuaishouSelectsRequestedApolloPhoto(t *testing.T) {
	page := []byte(`window.__APOLLO_STATE__={"defaultClient":{
  "VisionVideoDetailPhoto:other":{"id":"other","caption":"Other title","photoUrl":"https://cdn.example/other.mp4"},
  "VisionVideoSetRepresentation:1":{"url":"https://cdn.example/unrelated.mp4","width":640,"height":360,"codecs":"avc1"},
  "VisionVideoDetailPhoto:wanted":{"id":"wanted","caption":"Requested title","manifest":{"__ref":"Manifest:wanted"}},
  "Manifest:wanted":{"adaptationSet":[{"type":"id","id":"Adaptation:wanted"}]},
  "Adaptation:wanted":{"duration":12,"representation":[{"__ref":"Representation:wanted"}]},
  "Representation:wanted":{"url":"https://cdn.example/wanted.mp4","width":1920,"height":1080,"codecs":"avc1"}
 }};`)
	r, err := majorKSPage(page, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Requested title" || len(r.Media) != 1 || r.Media[0].URL != "https://cdn.example/wanted.mp4" || r.Media[0].Duration != 12 {
		t.Fatalf("selected another work or representation: %#v", r)
	}
	if _, err = majorKSPage(page, "missing"); err == nil {
		t.Fatal("unrequested Apollo photo returned for missing ID")
	}
}
func TestMajorKuaishouNeverBorrowsUnrelatedStream(t *testing.T) {
	page := []byte(`window.__APOLLO_STATE__={"defaultClient":{
  "VisionVideoDetailPhoto:wanted":{"id":"wanted","caption":"Requested title","coverUrl":"https://cdn.example/wanted.jpg"},
  "VisionVideoDetailPhoto:other":{"id":"other","caption":"Other title","photoUrl":"https://cdn.example/other.mp4"},
  "VisionVideoSetRepresentation:1":{"url":"https://cdn.example/other.mp4","width":640,"height":360,"codecs":"avc1"}
 }};`)
	if _, err := majorKSPage(page, "wanted"); err == nil {
		t.Fatal("requested photo borrowed an unrelated stream")
	}
}
func TestMajorKuaishouMatchesMobilePhotoID(t *testing.T) {
	page := []byte(`window.INIT_STATE={"recommendations":[{"photo":{"id":"other","caption":"Other","photoUrl":"https://cdn.example/other.mp4"}}],"detail":{"photo":{"id":"wanted","caption":"Wanted","mainMvUrls":[{"url":"https://cdn.example/wanted.mp4"}]}}};`)
	r, err := majorKSPage(page, "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Wanted" || len(r.Media) != 1 || r.Media[0].URL != "https://cdn.example/wanted.mp4" {
		t.Fatalf("mobile page selected another work: %#v", r)
	}
	if _, err = majorKSPage(page, "missing"); err == nil {
		t.Fatal("unrequested mobile photo returned for missing ID")
	}
}
func TestMajorURLRejectsMissingAndScalarValues(t *testing.T) {
	for _, value := range []any{nil, map[string]any(nil), []any(nil), map[string]any{}, map[string]any{"width": 1920}, map[string]any{"url": nil}, map[string]any{"url": 123}, []any{nil, false, 456}, 123, 12.5, false, struct{}{}, "not a URL"} {
		if got := majorURL(value); got != "" {
			t.Fatalf("majorURL(%#v) = %q, want empty", value, got)
		}
		if got := majorURLs(value); len(got) != 0 {
			t.Fatalf("majorURLs(%#v) = %#v, want no URLs", value, got)
		}
	}
	value := map[string]any{"url": nil, "url_list": []any{nil, 123, map[string]any{"url": "https://cdn.example/video.mp4"}}}
	if got := majorURL(value); got != "https://cdn.example/video.mp4" {
		t.Fatalf("valid URL after missing/scalar alternatives lost: %q", got)
	}
	if _, err := majorDouyinVideo(map[string]any{"cover": map[string]any{"url_list": []any{"https://cdn.example/poster.jpg"}}}, nil); err == nil {
		t.Fatal("Douyin missing play address accepted")
	}
	if _, err := majorKSPhoto(map[string]any{"caption": "No playback", "photoUrl": 123}, nil); err == nil {
		t.Fatal("Kuaishou numeric play address accepted")
	}
	if track := majorBiliTrack([]any{map[string]any{"bandwidth": 100, "baseUrl": nil}}, true); track != nil {
		t.Fatal("Bilibili missing track address accepted")
	}
}
func TestMajorURLBoundsNesting(t *testing.T) {
	cycle := map[string]any{}
	cycle["url"] = cycle
	if got := majorURL(cycle); got != "" {
		t.Fatalf("cyclic state produced URL: %q", got)
	}
	var nested any = "https://cdn.example/video.mp4"
	for range 40 {
		nested = []any{nested}
	}
	if got := majorURL(nested); got != "" {
		t.Fatalf("over-deep state produced URL: %q", got)
	}
}
