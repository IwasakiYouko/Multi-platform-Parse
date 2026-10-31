package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"multiparse/internal/model"
)

type overseasTestTransport func(*http.Request) (*http.Response, error)

func (f overseasTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func overseasTestResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func overseasTestClient(body string) *http.Client {
	return &http.Client{Transport: overseasTestTransport(func(r *http.Request) (*http.Response, error) { return overseasTestResponse(r, body), nil })}
}
func overseasTestState(v string) string {
	return `<html><script type="application/json">` + v + `</script></html>`
}

func TestOverseasNativePostStates(t *testing.T) {
	cases := []struct {
		name, url, state, kind, want string
		resolve                      Resolver
		count                        int
	}{
		{"instagram carousel", "https://www.instagram.com/p/AbC/", `{"data":{"shortcode_media":{"shortcode":"AbC","edge_media_to_caption":{"edges":[{"node":{"text":"carousel body"}}]},"edge_sidecar_to_children":{"edges":[{"node":{"__typename":"GraphImage","display_url":"https://cdn.example/photo.jpg"}},{"node":{"__typename":"GraphVideo","video_url":"https://cdn.example/video.mp4","display_url":"https://cdn.example/poster.jpg"}}]}}}}`, "multimedia", "carousel body", overseasInstagram, 2},
		{"threads native node", "https://www.threads.com/@author/post/AbC", `{"data":{"thread_items":[{"post":{"code":"AbC","caption":{"text":"thread body"},"user":{"username":"author"},"text_post_app_info":{},"image_versions2":{"candidates":[{"url":"https://cdn.example/p.jpg","width":100,"height":100}]}}}]}}`, "image", "thread body", overseasThreads, 1},
		{"facebook playable fields", "https://www.facebook.com/reel/123/", `{"data":{"video":{"id":"123","name":"Native video","playable_url_quality_hd":"https://cdn.example/hd.mp4","playable_url":"https://cdn.example/sd.mp4","savable_description":{"text":"facebook body"}}}}`, "video", "facebook body", overseasFacebook, 1},
		{"tiktok universal video", "https://www.tiktok.com/@author/video/123", `{"__DEFAULT_SCOPE__":{"webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":{"id":"123","desc":"tiktok body","author":{"nickname":"author"},"video":{"playAddr":"https://cdn.example/v.mp4","cover":"https://cdn.example/cover.jpg","duration":12}}}}}}`, "video", "tiktok body", overseasTikTok, 1},
		{"tiktok photo state", "https://www.tiktok.com/@author/photo/123", `{"ItemModule":{"123":{"id":"123","desc":"photo body","imagePost":{"images":[{"imageURL":{"urlList":["https://cdn.example/1.jpg"]}},{"imageURL":{"urlList":["https://cdn.example/2.jpg"]}}]}}}}`, "image", "photo body", overseasTikTok, 2},
		{"snapchat spotlight", "https://www.snapchat.com/spotlight/AbC", `{"props":{"pageProps":{"spotlightFeed":{"spotlightStories":[{"story":{"storyId":{"value":"other"}},"metadata":{"videoMetadata":{"contentUrl":"https://cdn.example/wrong.mp4"}}},{"story":{"storyId":{"value":"AbC"}},"metadata":{"description":"snap body","videoMetadata":{"name":"Spotlight","contentUrl":"https://cdn.example/snap.mp4","durationMs":1500,"creator":{"personCreator":{"username":"author"}}}}}]}}}}`, "video", "snap body", overseasSnapchat, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.resolve(context.Background(), overseasTestClient(overseasTestState(test.state)), test.url, "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Type != test.kind || result.Content != test.want || len(result.Media) != test.count {
				t.Fatalf("unexpected native result: %#v", result)
			}
			for _, media := range result.Media {
				if media.Headers["Referer"] == "" {
					t.Fatal("missing media referer")
				}
				if strings.Contains(media.URL, "poster") {
					t.Fatal("poster mislabeled as media")
				}
			}
		})
	}
}
func TestOverseasTwitterSyndication(t *testing.T) {
	body := `{"text":"tweet body","user":{"name":"author"},"mediaDetails":[{"type":"photo","media_url_https":"https://pbs.twimg.com/photo.jpg"},{"type":"video","media_url_https":"https://pbs.twimg.com/poster.jpg","video_info":{"duration_millis":1000,"variants":[{"content_type":"video/mp4","bitrate":256000,"url":"https://video.twimg.com/low.mp4"},{"content_type":"video/mp4","bitrate":832000,"url":"https://video.twimg.com/high.mp4"}]}}]}`
	result, err := overseasTwitter(context.Background(), overseasTestClient(body), "https://x.com/author/status/123", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "multimedia" || result.Content != "tweet body" || len(result.Media) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	found := false
	for _, media := range result.Media {
		found = found || media.URL == "https://video.twimg.com/high.mp4"
	}
	if !found {
		t.Fatal("highest-bitrate direct stream was not selected")
	}
}
func TestOverseasTwitterFullArticle(t *testing.T) {
	body := `{"data":{"instructions":[{"result":{"rest_id":"123","legacy":{"full_text":"teaser"},"article":{"article_results":{"result":{"rest_id":"456","title":"Full article","content_state":{"blocks":[{"text":"First paragraph"},{"text":"Second paragraph"}]}}}}}}]}}`
	result, err := overseasTwitter(context.Background(), overseasTestClient(body), "https://x.com/author/status/123", "auth_token=value; ct0=csrf")
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "article" || result.Title != "Full article" || !strings.Contains(result.Content, "Second paragraph") {
		t.Fatalf("article body missing: %#v", result)
	}
}
func TestOverseasMediumBodyAndEmbeddedMedia(t *testing.T) {
	body := `<html><meta name="author" content="Writer"><article><h1>Actual title</h1><p>First paragraph.</p><p>Second paragraph.</p><figure><img src="https://miro.medium.com/photo.jpg"></figure><video src="https://cdn.example/embedded.mp4"></video><iframe src="https://www.youtube.com/embed/abc"></iframe></article></html>`
	result, err := overseasMedium(context.Background(), overseasTestClient(body), "https://medium.com/@writer/article-123456abcdef", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "article" || result.Author != "Writer" || result.Title != "Actual title" || len(result.Media) != 2 || !strings.Contains(result.Content, "Second paragraph.") {
		t.Fatalf("unexpected article: %#v", result)
	}
}
func TestOverseasNativeFailures(t *testing.T) {
	cases := []struct {
		name, url, body string
		resolve         Resolver
	}{
		{"instagram video withheld", "https://www.instagram.com/p/AbC/", overseasTestState(`{"shortcode":"AbC","is_video":true,"display_url":"https://cdn.example/poster.jpg"}`), overseasInstagram},
		{"tiktok private", "https://www.tiktok.com/@a/video/123", overseasTestState(`{"__DEFAULT_SCOPE__":{"webapp.video-detail":{"statusCode":10216}}}`), overseasTikTok},
		{"snapchat missing story", "https://www.snapchat.com/spotlight/AbC", overseasTestState(`{"props":{"pageProps":{}}}`), overseasSnapchat},
		{"facebook og is not a stream", "https://www.facebook.com/reel/123", `<meta property="og:image" content="https://cdn.example/poster.jpg">`, overseasFacebook},
		{"medium locked preview", "https://medium.com/@a/article-123456abcdef", `<article><h1>Title</h1><p>Preview only</p><div data-testid="paywallContent">Subscribe</div></article>`, overseasMedium},
		{"threads og is not native state", "https://www.threads.com/@a/post/AbC", `<meta property="og:video" content="https://cdn.example/unknown.mp4">`, overseasThreads},
		{"twitter article requires session", "https://x.com/i/article/123", `{}`, overseasTwitter},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if result, err := test.resolve(context.Background(), overseasTestClient(test.body), test.url, ""); err == nil {
				t.Fatalf("expected truthful native extraction error; got %#v", result)
			}
		})
	}
}
func TestOverseasFacebookDASHPairAndDRM(t *testing.T) {
	body := `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation width="1920" height="1080"><BaseURL>https://cdn.example/video.mp4</BaseURL></Representation></AdaptationSet><AdaptationSet mimeType="audio/mp4"><Representation bandwidth="128000"><BaseURL>https://cdn.example/audio.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`
	media, err := overseasFacebookDASH([]byte(body), "https://www.facebook.com/")
	if err != nil {
		t.Fatal(err)
	}
	if media.AudioURL != "https://cdn.example/audio.mp4" || media.Height != 1080 {
		t.Fatalf("missing separate audio: %#v", media)
	}
	protected := strings.Replace(body, `<AdaptationSet mimeType="video/mp4">`, `<AdaptationSet mimeType="video/mp4"><ContentProtection schemeIdUri="urn:uuid:test"/>`, 1)
	if _, err = overseasFacebookDASH([]byte(protected), "https://www.facebook.com/"); err == nil {
		t.Fatal("DRM was not rejected")
	}
}
func TestOverseasYouTubeNativeAdaptiveFormats(t *testing.T) {
	player := map[string]any{
		"playabilityStatus": map[string]any{"status": "OK", "playableInEmbed": true},
		"videoDetails":      map[string]any{"title": "Native YouTube", "shortDescription": "description", "author": "author", "lengthSeconds": "10"},
		"streamingData":     map[string]any{"formats": []any{map[string]any{"itag": 18, "url": "https://media.googlevideo.com/muxed.mp4", "mimeType": "video/mp4", "height": 360, "width": 640, "audioChannels": 2}}, "adaptiveFormats": []any{map[string]any{"itag": 137, "url": "https://media.googlevideo.com/video.mp4", "mimeType": "video/mp4", "height": 1080, "width": 1920}, map[string]any{"itag": 140, "url": "https://media.googlevideo.com/audio.m4a", "mimeType": "audio/mp4", "audioChannels": 2, "bitrate": 128000}}},
	}
	encoded, _ := json.Marshal(player)
	client := &http.Client{Transport: overseasTestTransport(func(req *http.Request) (*http.Response, error) {
		if !strings.Contains(req.Header.Get("Cookie"), "SID=session") {
			return nil, fmt.Errorf("native library lost scoped cookie")
		}
		switch {
		case strings.Contains(req.URL.Path, "/youtubei/v1/player"):
			return overseasTestResponse(req, string(encoded)), nil
		case req.URL.Path == "/watch":
			return overseasTestResponse(req, "var ytInitialPlayerResponse = "+string(encoded)+";"), nil
		default:
			return overseasTestResponse(req, "\nytcfg.set({\"INNERTUBE_CONTEXT\":{\"client\":{\"visitorData\":\"test\"}}});"), nil
		}
	})}
	result, err := overseasYouTube(context.Background(), client, "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "SID=session")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Media) != 1 || result.Media[0].Height != 1080 || result.Media[0].AudioURL == "" {
		t.Fatalf("native adaptive video/audio pair missing: %#v", result)
	}
}
func TestOverseasYouTubeCookieScoping(t *testing.T) {
	transport := overseasYouTubeTransport{ctx: context.Background(), cookie: "SID=secret", base: overseasTestTransport(func(req *http.Request) (*http.Response, error) {
		cookie := req.Header.Get("Cookie")
		if req.URL.Hostname() == "www.youtube.com" && cookie != "SID=secret" {
			t.Fatal("cookie missing from YouTube origin")
		}
		if req.URL.Hostname() == "media.googlevideo.com" && cookie != "" {
			t.Fatal("YouTube cookie leaked to CDN")
		}
		return overseasTestResponse(req, "ok"), nil
	})}
	for _, host := range []string{"www.youtube.com", "media.googlevideo.com"} {
		req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/", nil)
		req.Header.Set("Cookie", "SID=old")
		response, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
}
func TestOverseasMediaNeverAcceptsPosterAsVideo(t *testing.T) {
	result := &model.ParseResult{Platform: "instagram", SourceURL: "https://www.instagram.com/p/AbC/"}
	err := overseasInstagramMedia(result, map[string]any{"is_video": true, "display_url": "https://cdn.example/poster.jpg"})
	if err == nil || len(result.Media) != 0 {
		t.Fatal("video poster substituted for inaccessible stream")
	}
}
