package parser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bogdanfinn/fhttp/http2"
	"github.com/bogdanfinn/tls-client/profiles"
	"multiparse/internal/model"
)

func TestTwitterReferenceRouteCookieAndMedia(t *testing.T) {
	body := `{"data":{"tweetResult":{"result":{"rest_id":"123","legacy":{"full_text":"mixed https://t.co/media","entities":{"media":[{"type":"animated_gif","media_url_https":"https://pbs.twimg.com/gif.jpg","video_info":{"variants":[{"url":"https://video.twimg.com/animation.mp4"}]}},{"type":"photo","media_url_https":"https://pbs.twimg.com/p.jpg","original_info":{"width":640,"height":480}},{"type":"video","media_url_https":"https://pbs.twimg.com/v.jpg","video_info":{"duration_millis":2500,"variants":[{"url":"https://video.twimg.com/low.mp4"},{"url":"https://video.twimg.com/high.mp4"}]}}]}},"quoted_status_result":{"result":{"legacy":{"entities":{"media":[{"type":"photo","media_url_https":"https://pbs.twimg.com/unrelated.jpg"}]}}}}}}}}`
	for _, cookie := range []string{"", "ct0=csrf", "auth_token=token", "ct0=csrf; auth_token=token"} {
		t.Run(cookie, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: overseasTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "api.twitter.com" || req.URL.Path != "/graphql/kPLTRmMnzbPTv70___D06w/TweetResultByRestId" {
					t.Fatalf("wrong reference endpoint: %s", req.URL)
				}
				if req.Header.Get("Authorization") != "Bearer "+overseasTwitterBearer {
					t.Fatal("missing bearer")
				}
				if req.URL.Query().Get("fieldToggles") != `{"withArticleRichContentState":true,"withArticlePlainText":false}` {
					t.Fatal("wrong article protocol")
				}
				var variables map[string]any
				if err := json.Unmarshal([]byte(req.URL.Query().Get("variables")), &variables); err != nil || variables["tweetId"] != "123" || variables["withVoice"] != false {
					t.Fatalf("wrong variables: %#v %v", variables, err)
				}
				valid := strings.Contains(cookie, "ct0=") && strings.Contains(cookie, "auth_token=")
				if valid {
					if req.Header.Get("Cookie") != cookie || req.Header.Get("x-csrf-token") != "csrf" {
						t.Fatal("valid session not passed")
					}
				} else if req.Header.Get("Cookie") != "" || req.Header.Get("x-csrf-token") != "" {
					t.Fatal("partial session leaked")
				}
				return overseasTestResponse(req, body), nil
			})}
			r, err := overseasTwitter(context.Background(), client, "https://fixupx.com/writer/status/123?tracking=yes", cookie)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || r.SourceURL != "https://x.com/writer/status/123" || r.Type != "multimedia" || r.Content != "mixed " || len(r.Media) != 3 {
				t.Fatalf("wrong result: %#v", r)
			}
			if r.Media[0].Kind != "gif" || r.Media[1].Kind != "image" || r.Media[2].Kind != "video" || r.Media[2].Duration != 2.5 || r.Media[2].URL != "https://video.twimg.com/high.mp4" || r.Media[1].URL != "https://pbs.twimg.com/p.jpg?name=orig" {
				t.Fatalf("wrong media order/types: %#v", r.Media)
			}
		})
	}
}

func TestTwitterArticleDraftEntities(t *testing.T) {
	body := `{"data":{"tweetResult":{"result":{"rest_id":"123","legacy":{"full_text":"preview"},"article":{"article_results":{"result":{"title":"Article","cover_media":{"media_info":{"original_img_url":"https://pbs.twimg.com/cover.jpg"}},"media_entities":[{"media_id":"p","media_info":{"__typename":"ApiImage","original_img_url":"https://pbs.twimg.com/photo.jpg","original_img_width":500}},{"media_id":"v","media_info":{"__typename":"ApiVideo","preview_image":{"original_img_url":"https://pbs.twimg.com/poster.jpg"},"duration_millis":1200,"variants":[{"content_type":"application/x-mpegURL","bit_rate":99999,"url":"https://video.twimg.com/playlist.m3u8"},{"content_type":"video/mp4","bit_rate":10,"url":"https://video.twimg.com/low.mp4"},{"content_type":"video/mp4","bit_rate":20,"url":"https://video.twimg.com/high.mp4"}]}}],"content_state":{"entityMap":[{"key":"0","value":{"type":"LINK","data":{"url":"https://example.com/story"}}},{"key":"1","value":{"type":"MEDIA","data":{"mediaItems":[{"mediaId":"p"}]}}},{"key":"2","value":{"type":"MEDIA","data":{"mediaItems":[{"mediaId":"v"}]}}},{"key":"3","value":{"type":"DIVIDER"}}],"blocks":[{"type":"header-two","text":"Hello link","inlineStyleRanges":[{"offset":0,"length":5,"style":"Bold"},{"offset":0,"length":5,"style":"Italic"}],"entityRanges":[{"offset":6,"length":4,"key":0}]},{"type":"atomic","text":" ","entityRanges":[{"offset":0,"length":1,"key":1}]},{"type":"atomic","text":" ","entityRanges":[{"offset":0,"length":1,"key":2}]},{"type":"atomic","text":" ","entityRanges":[{"offset":0,"length":1,"key":3}]},{"type":"blockquote","text":"line\nnext"},{"type":"unordered-list-item","text":"item"},{"type":"ordered-list-item","text":"number"}]}}}}}}}}`
	r, err := overseasTwitter(context.Background(), overseasTestClient(body), "https://twitter.com/a/status/123", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "article" || r.Title != "Article" || len(r.Media) != 2 || r.Media[0].Kind != "image" || r.Media[1].Kind != "video" {
		t.Fatalf("wrong article: %#v", r)
	}
	for _, part := range []string{"![Cover Image](https://pbs.twimg.com/cover.jpg)", "## ***Hello*** [link](https://example.com/story)", "![Image](https://pbs.twimg.com/photo.jpg)", "[![Video](https://pbs.twimg.com/poster.jpg)](https://video.twimg.com/high.mp4)", "---", "> line\n> next", "- item", "1. number"} {
		if !strings.Contains(r.MarkdownContent, part) {
			t.Errorf("missing %q in %q", part, r.MarkdownContent)
		}
	}
	if strings.Contains(r.Content, "**") || !strings.Contains(r.Content, "Hello link") || !strings.Contains(r.Content, "Video: https://video.twimg.com/high.mp4") {
		t.Fatalf("not plain text: %q", r.Content)
	}
}

func TestTwitterReferenceErrorsAndInputs(t *testing.T) {
	for _, raw := range []string{"https://example.com/a/status/123", "https://x.com.evil/a/status/123", "https://x.com/i/article/123", "https://x.com/a", "https://user@x.com/a/status/123", "https://x.com:444/a/status/123"} {
		if _, _, err := twitterInput(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"twitter.com/a/status/123", "https://mobile.twitter.com/a/status/123", "https://x.com/i/web/status/123"} {
		if _, id, err := twitterInput(raw); err != nil || id != "123" {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
	for _, body := range []string{`{"errors":[{"message":"Rate limit exceeded"}]}`, `{"data":{"tweetResult":{}}}`, `{"data":{"tweetResult":{"result":{"reason":"Protected"}}}}`, `{"data":{"tweetResult":{"result":{"reason":"Suspended"}}}}`, `{"data":{"tweetResult":{"result":{"legacy":{"full_text":"preview"},"article":{"article_results":{"result":{"title":"Preview"}}}}}}}`} {
		if r, err := overseasTwitter(context.Background(), overseasTestClient(body), "https://x.com/a/status/123", ""); err == nil {
			t.Fatalf("unsupported success: %#v", r)
		}
	}
	_, err := overseasTwitter(context.Background(), overseasTestClient(`{"data":{"tweetResult":{}}}`), "https://x.com/a/status/123", "")
	var unavailable ContentUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("missing meaningful unavailable error: %v", err)
	}
	client := &http.Client{Transport: overseasTestTransport(func(req *http.Request) (*http.Response, error) {
		resp := overseasTestResponse(req, "")
		resp.StatusCode = 302
		resp.Header.Set("Location", "https://evil.example")
		return resp, nil
	})}
	if _, err = overseasTwitter(context.Background(), client, "https://x.com/a/status/123", "ct0=csrf; auth_token=secret"); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect not rejected: %v", err)
	}
}

func TestMediumReferenceGraphQLFormatting(t *testing.T) {
	body := `[{"data":{"post":{"title":"Title","isLocked":true,"content":{"bodyModel":{"paragraphs":[{"type":"H2","text":"Title"},{"type":"P","text":"😀 bold link code ","markups":[{"type":"STRONG","start":2,"end":8},{"type":"EM","start":3,"end":7},{"type":"A","start":8,"end":12,"href":"https://example.com/link"},{"type":"CODE","start":13,"end":18}]},{"type":"H3","text":"Heading"},{"type":"BQ","text":"quote\nline"},{"type":"PRE","text":"fmt.Println(1)","codeBlockMetadata":{"lang":"go"}},{"type":"IMG","text":"caption","metadata":{"id":"1*photo.png","originalWidth":800,"originalHeight":600}},{"type":"IFRAME","iframe":{"mediaResource":{"iframeSrc":"https://www.youtube.com/embed/abc","title":"Watch"}}},{"type":"MIXTAPE_EMBED","text":"Related","mixtapeMetadata":{"href":"https://example.com/related"}},{"type":"OLI","text":"one"},{"type":"OLI","text":"two"},{"type":"ULI","text":"bullet"},{"type":"P","text":"break"},{"type":"OLI","text":"reset"}]}}}}}]`
	calls := 0
	r, err := mediumWithHTTP(context.Background(), "https://writer.medium.com/title-123456abcdef?source=share", func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "POST" || req.URL.String() != "https://medium.com/_/graphql" || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Cookie") != "" {
			t.Fatalf("wrong API request: %#v", req)
		}
		var payload []map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || len(payload) != 1 || payload[0]["operationName"] != "P" || Path(payload[0], "variables", "id") != "123456abcdef" || payload[0]["query"] != mediumGraphQLQuery {
			t.Fatalf("wrong payload: %#v %v", payload, err)
		}
		return overseasTestResponse(req, body), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.Type != "article" || r.Title != "Title" || len(r.Media) != 1 || r.Media[0].URL != "https://miro.medium.com/v2/1*photo.png" || r.Media[0].Width != 800 {
		t.Fatalf("wrong article: %#v", r)
	}
	for _, part := range []string{"😀 ***bold*** [link](https://example.com/link) `code` ", "### Heading", "> quote\n> line", "```go\nfmt.Println(1)\n```", "![](https://miro.medium.com/v2/1*photo.png)\n*caption*", "[Watch](https://www.youtube.com/embed/abc)", "[Related](https://example.com/related)", "1. one\n2. two\n- bullet", "1. reset"} {
		if !strings.Contains(r.MarkdownContent, part) {
			t.Errorf("missing %q in %q", part, r.MarkdownContent)
		}
	}
	if strings.Contains(r.Content, "***") || strings.Contains(r.MarkdownContent, "## Title") || !strings.Contains(r.Content, "😀 bold link code") {
		t.Fatalf("wrong plain/title rendering: %#v", r)
	}
}

func TestMediumReferenceErrors(t *testing.T) {
	for _, raw := range []string{"https://evil.medium.com.evil/title-123456abcdef", "https://medium.com/@name", "https://user@medium.com/title-123456abcdef", "https://medium.com/title-123456abcdefg"} {
		if _, _, err := mediumInput(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, body := range []string{`[{"errors":[{"message":"denied"}]}]`, `[{"data":{"post":null}}]`, `[{"data":{"post":{"title":"locked","isLocked":true}}}]`, `[]`, `[{}]`, `<article>legacy is unsupported</article>`} {
		if r, err := mediumWithHTTP(context.Background(), "https://medium.com/title-123456abcdef", func(req *http.Request) (*http.Response, error) { return overseasTestResponse(req, body), nil }); err == nil {
			t.Fatalf("unsupported success: %#v", r)
		}
	}
	for _, status := range []int{302, 403, 429} {
		_, err := mediumWithHTTP(context.Background(), "https://medium.com/title-123456abcdef", func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		if err == nil {
			t.Errorf("accepted HTTP %d", status)
		}
	}
}

func TestMediumChromeGatewayRefusesUncontrolledTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	called := false
	client := &http.Client{Transport: overseasTestTransport(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not bypass CONNECT policy")
	})}
	if result, err := overseasMedium(ctx, client, "https://medium.com/title-123456abcdef", "secret=must-not-leak"); err == nil || result != nil {
		t.Fatalf("uncontrolled gateway succeeded: %#v %v", result, err)
	}
	if called {
		t.Fatal("Chrome bypassed controlled CONNECT gateway")
	}
	profile := profiles.Chrome_150
	if !strings.Contains(profile.GetClientHelloStr(), "150") || profile.GetSettings()[http2.SettingHeaderTableSize] != 65536 || profile.GetSettings()[http2.SettingInitialWindowSize] != 6291456 || profile.GetConnectionFlow() != 15663105 {
		t.Fatal("missing Chrome TLS/HTTP2 fingerprint")
	}
	if _, err := profile.GetClientHelloSpec(); err != nil {
		t.Fatal(err)
	}
}

func TestMediumEmptyImageOnlyArticleHasPlainContent(t *testing.T) {
	r := &model.ParseResult{Platform: "medium", SourceURL: "https://medium.com/title-123456abcdef"}
	var paragraphs []any
	if err := json.Unmarshal([]byte(`[{"type":"IMG","metadata":{"id":"photo.jpg"}}]`), &paragraphs); err != nil {
		t.Fatal(err)
	}
	mediumRender(r, paragraphs)
	if r.Content == "" || r.MarkdownContent == "" || len(r.Media) != 1 {
		t.Fatalf("image article lost: %#v", r)
	}
}
