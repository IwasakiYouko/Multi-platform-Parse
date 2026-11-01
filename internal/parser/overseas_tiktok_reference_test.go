package parser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type referenceSocialTransport func(*http.Request) (*http.Response, error)

func (f referenceSocialTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func referenceSocialResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}
func referenceTikTokItem(item string) string {
	return `<script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"__DEFAULT_SCOPE__":{"webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":` + item + `}}}}</script>`
}

func TestReferenceTikTokCanonicalPhotosAndFiltering(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.String() != "https://www.tiktok.com/@_/video/123" {
			t.Fatalf("wrong photo endpoint: %s %s", req.Method, req.URL)
		}
		return referenceSocialResponse(req, referenceTikTokItem(`{"id":"123","desc":"photos","image_post_info":{"images":[{"display_image":{"url_list":["invalid"]}},{"displayImage":{"urlList":["//cdn.example/first.jpg"],"width":120,"height":90}},{"image":{"UrlList":["https://cdn.example/second.jpg"]},"imageWidth":50,"imageHeight":40}]}}`)), nil
	})}
	result, err := overseasTikTok(context.Background(), client, "https://www.tiktok.com/@author/photo/123", "")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(result.Media) != 2 || result.Media[0].URL != "https://cdn.example/first.jpg" || result.Media[1].URL != "https://cdn.example/second.jpg" || result.Media[0].Width != 120 || result.Media[1].Height != 40 {
		t.Fatalf("photo order/filter mismatch: %#v", result)
	}
}

func TestReferenceTikTokVariantOrderingAndSourceCompatibility(t *testing.T) {
	video := map[string]any{}
	if err := json.Unmarshal([]byte(`{"width":99,"height":88,"duration":12000,"origin_cover":{"url_list":["https://cdn.example/original.jpg"]},"cover":"https://cdn.example/other.jpg","bit_rate":[{"bit_rate":999999,"play_addr":{"url_list":["https://cdn.example/low.mp4"],"width":640,"height":360}},{"bit_rate":5,"play_addr":{"url_list":["https://cdn.example/ordinary.mp4","https://cdn.example/aweme-h265.mp4"],"width":1920,"height":1080,"data_size":1}},{"Bitrate":5,"PlayAddr":{"UrlList":["https://cdn.example/winner.mp4"],"Width":1920,"Height":1080,"DataSize":2,"Duration":15}},{"bitrate":5,"PlayAddr":{"urlList":["https://cdn.example/tie.mp4"],"Width":1920,"Height":1080,"DataSize":2}}]}`), &video); err != nil {
		t.Fatal(err)
	}
	media, err := tikTokVideo(video)
	if err != nil {
		t.Fatal(err)
	}
	if media.URL != "https://cdn.example/winner.mp4" || media.Width != 1920 || media.Duration != 15 || media.ThumbnailURL != "https://cdn.example/original.jpg" {
		t.Fatalf("wrong lexicographic variant: %#v", media)
	}
	delete(video, "bit_rate")
	video["bitrateInfo"] = []any{map[string]any{"Bitrate": float64(3), "PlayAddr": map[string]any{"UrlList": []any{"https://cdn.example/plain.mp4", "https://cdn.example/aweme.mp4"}, "UrlKey": "bytevc2"}}}
	media, err = tikTokVideo(video)
	if err != nil || media.URL != "https://cdn.example/aweme.mp4" {
		t.Fatalf("reference does not discard codec variants: %#v %v", media, err)
	}
	delete(video, "bitrateInfo")
	video["playAddr"] = "https://cdn.example/play.mp4"
	media, err = tikTokVideo(video)
	if err != nil || media.URL != "https://cdn.example/play.mp4" || media.Duration != 12000 {
		t.Fatalf("camelCase fallback mismatch: %#v %v", media, err)
	}
	delete(video, "playAddr")
	video["download_addr"] = map[string]any{"url_list": []any{"https://cdn.example/download.mp4"}}
	media, err = tikTokVideo(video)
	if err != nil || media.URL != "https://cdn.example/download.mp4" {
		t.Fatalf("download fallback mismatch: %#v %v", media, err)
	}
}

func referenceTikTokChallenge() string {
	prefix := "waf-prefix"
	digest := sha256.Sum256([]byte(prefix + "42"))
	challenge, _ := json.Marshal(map[string]any{"v": map[string]string{"a": base64.StdEncoding.EncodeToString([]byte(prefix)), "c": base64.StdEncoding.EncodeToString(digest[:])}})
	return `<div id="cs" class="` + base64.StdEncoding.EncodeToString(challenge) + `"></div><div id="wci" class="waf_result"></div><div id="rci" class="waf_session"></div><div id="rs" class="session-value"></div>`
}

func TestReferenceTikTokShareAndWAFResultCookie(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if req.URL.String() != "https://vt.tiktok.com/share" {
				t.Fatalf("unexpected share request: %s", req.URL)
			}
			resp := referenceSocialResponse(req, "")
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://www.tiktok.com/@author/photo/123")
			return resp, nil
		case 2:
			if req.URL.Path != "/@author/photo/123" {
				t.Fatalf("share route: %s", req.URL)
			}
			return referenceSocialResponse(req, "resolved"), nil
		case 3:
			if req.URL.String() != "https://www.tiktok.com/@_/video/123" {
				t.Fatalf("noncanonical challenge endpoint: %s", req.URL)
			}
			return referenceSocialResponse(req, referenceTikTokChallenge()), nil
		case 4:
			if req.URL.String() != "https://www.tiktok.com/@_/video/123" {
				t.Fatalf("noncanonical retry: %s", req.URL)
			}
			ck, err := req.Cookie("waf_result")
			if err != nil {
				t.Fatal(err)
			}
			data, err := base64.StdEncoding.DecodeString(ck.Value)
			if err != nil {
				t.Fatal(err)
			}
			var solved map[string]any
			if json.Unmarshal(data, &solved) != nil || solved["d"] != "NDI=" {
				t.Fatalf("wrong SHA256 challenge proof: %s", data)
			}
			for name, want := range map[string]string{"waf_session": "session-value", "configured": "secret"} {
				cookie, err := req.Cookie(name)
				if err != nil || cookie.Value != want {
					t.Fatalf("missing scoped %s cookie", name)
				}
			}
			return referenceSocialResponse(req, referenceTikTokItem(`{"id":"123","video":{"playAddr":"https://cdn.example/video.mp4"}}`)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
	})}
	result, err := overseasTikTok(context.Background(), client, "https://vt.tiktok.com/share", "configured=secret")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || len(result.Media) != 1 || result.Media[0].Headers["Cookie"] != "" {
		t.Fatalf("bad result or leaked cookies: %#v", result)
	}
	if client.Jar != nil {
		t.Fatal("parse mutated shared client jar")
	}
}

func TestReferenceTikTokBoundedWAFAndMissingMedia(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://www.tiktok.com/@_/video/123" {
			t.Fatalf("wrong endpoint: %s", req.URL)
		}
		return referenceSocialResponse(req, referenceTikTokChallenge()), nil
	})}
	if _, err := overseasTikTok(context.Background(), client, "https://www.tiktok.com/@a/video/123", ""); err == nil || calls != 3 {
		t.Fatalf("WAF retry bound lost: calls=%d error=%v", calls, err)
	}
	for _, item := range []string{`{"id":"123","video":{}}`, `{"id":"123","imagePost":{"images":[{"imageURL":{"urlList":["bad"]}}]}}`, `{"id":"999","video":{"playAddr":"https://cdn.example/v.mp4"}}`} {
		client.Transport = referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
			return referenceSocialResponse(req, referenceTikTokItem(item)), nil
		})
		if _, err := overseasTikTok(context.Background(), client, "https://www.tiktok.com/@a/video/123", ""); err == nil {
			t.Fatalf("accepted missing or mismatched media: %s", item)
		}
	}
}
