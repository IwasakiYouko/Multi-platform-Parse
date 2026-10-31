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
					if req.URL.Query().Get("aid") != "170001" {
						return majorTestResponse(req, `{"code":-404,"message":"wrong work"}`), nil
					}
					return majorTestResponse(req, `{"code":0,"data":{"title":"古早作品","desc":"正文","owner":{"name":"UP"},"pages":[{"page":1,"cid":11,"part":"第一P","duration":10},{"page":2,"cid":22,"part":"第二P","duration":20}]}}`), nil
				case "/x/frontend/finger/spi":
					return majorTestResponse(req, `{"code":0,"data":{"b_3":"a","b_4":"b"}}`), nil
				case "/x/player/playurl":
					if req.URL.Query().Get("aid") != "170001" || req.URL.Query().Get("cid") != "22" {
						return majorTestResponse(req, `{"code":-404,"message":"wrong page"}`), nil
					}
					return majorTestResponse(req, `{"code":0,"data":{"durl":[{"url":"https://cdn.example/second.mp4","length":20000}]}}`), nil
				default:
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
