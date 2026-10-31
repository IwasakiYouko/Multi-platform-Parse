package parser

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestBilibiliAVResolvesRequestedPage(t *testing.T) {
	for _, input := range []string{"AV170001?p=2", "https://www.bilibili.com/video/av170001?p=2&tracking=BV1xx411c7mD", "https://b23.tv/old-video?p=2"} {
		t.Run(input, func(t *testing.T) {
			client := &http.Client{Transport: majorTestTransport(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/old-video":
					response := majorTestResponse(req, "")
					response.StatusCode = http.StatusFound
					response.Header.Set("Location", "https://www.bilibili.com/video/av170001")
					return response, nil
				case "/video/av170001":
					return majorTestResponse(req, "<html></html>"), nil
				case "/x/web-interface/view":
					if req.URL.Query().Get("aid") != "170001" || req.URL.Query().Has("bvid") || req.URL.Query().Has("avid") {
						return majorTestResponse(req, `{"code":-404,"message":"wrong work"}`), nil
					}
					return majorTestResponse(req, `{"code":0,"data":{"title":"古早作品","desc":"正文","owner":{"name":"UP"},"pages":[{"page":1,"cid":11,"part":"第一P","duration":10},{"page":2,"cid":22,"part":"第二P","duration":20}]}}`), nil
				case "/x/frontend/finger/spi":
					return majorTestResponse(req, `{"code":0,"data":{"b_3":"a","b_4":"b"}}`), nil
				case "/x/player/playurl":
					if req.URL.Query().Has("aid") {
						t.Error("playurl must not receive aid")
						return majorTestResponse(req, `{"code":-400,"message":"请求错误"}`), nil
					}
					if req.URL.Query().Get("avid") != "170001" || req.URL.Query().Has("bvid") || req.URL.Query().Get("cid") != "22" {
						return majorTestResponse(req, `{"code":-404,"message":"wrong page"}`), nil
					}
					return majorTestResponse(req, `{"code":0,"data":{"durl":[{"url":"https://cdn.example/second.mp4","length":20000}]}}`), nil
				default:
					t.Errorf("unexpected request: %s", req.URL)
					return majorTestResponse(req, `{"code":-404}`), nil
				}
			})}
			result, err := majorBilibili(context.Background(), client, input, "")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result.Title, "第二P") || len(result.Media) != 1 || result.Media[0].URL != "https://cdn.example/second.mp4" {
				t.Fatalf("wrong AV content/page: %+v", result)
			}
		})
	}
}

func TestBilibiliMobileLongAVDASH(t *testing.T) {
	viewRequested, playRequested := false, false
	client := &http.Client{Transport: majorTestTransport(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		switch req.URL.Path {
		case "/x/web-interface/view":
			viewRequested = true
			if query.Get("aid") != "117489350541328" || query.Has("bvid") || query.Has("avid") {
				t.Errorf("wrong view identity: %s", req.URL.RawQuery)
				return majorTestResponse(req, `{"code":-400,"message":"请求错误"}`), nil
			}
			return majorTestResponse(req, `{"code":0,"data":{"bvid":"BV1rtnb6uEfk","title":"Mobile AV","desc":"DASH regression","owner":{"name":"Uploader"},"cid":42980608201,"pages":[{"page":1,"cid":42980608201,"duration":20}]}}`), nil
		case "/x/frontend/finger/spi":
			return majorTestResponse(req, `{"code":0,"data":{"b_3":"a","b_4":"b"}}`), nil
		case "/x/player/playurl":
			playRequested = true
			if query.Has("aid") || query.Has("avid") || query.Get("bvid") != "BV1rtnb6uEfk" || query.Get("cid") != "42980608201" {
				t.Errorf("wrong playback identity/page: %s", req.URL.RawQuery)
				return majorTestResponse(req, `{"code":-400,"message":"请求错误"}`), nil
			}
			return majorTestResponse(req, `{"code":0,"data":{"dash":{"duration":20,"video":[{"baseUrl":"https://cdn.bilivideo.com/mobile-video.m4s","codecs":"avc1.640028","bandwidth":100,"width":1920,"height":1080}],"audio":[{"baseUrl":"https://cdn.bilivideo.com/mobile-audio.m4s","codecs":"mp4a.40.2","bandwidth":128000}]}}}`), nil
		default:
			t.Errorf("unexpected request: %s", req.URL)
			return majorTestResponse(req, `{"code":-404}`), nil
		}
	})}
	result, err := majorBilibili(context.Background(), client, "https://m.bilibili.com/video/av117489350541328", "")
	if err != nil {
		t.Fatal(err)
	}
	if !viewRequested || !playRequested {
		t.Fatal("view and playback requests are required")
	}
	if result.Platform != "bilibili" || result.Type != "video" || result.Title != "Mobile AV" || len(result.Media) != 1 {
		t.Fatalf("wrong mobile AV result: %+v", result)
	}
	media := result.Media[0]
	if media.Kind != "video" || media.URL != "https://cdn.bilivideo.com/mobile-video.m4s" || media.AudioURL != "https://cdn.bilivideo.com/mobile-audio.m4s" || media.Width != 1920 || media.Height != 1080 || media.Duration != 20 {
		t.Fatalf("DASH tracks lost: %+v", media)
	}
}
