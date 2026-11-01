package parser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mainlandFixtureTransport func(*http.Request) (*http.Response, error)

func (f mainlandFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMainlandLiteralAndCookie(t *testing.T) {
	state, err := mainlandLiteralState([]byte(`window.cgiDataNew={item_show_type:'8' * 1, title:'don\'t', picture_page_info_list:[{cdn_url:'https://cdn.example/photo.jpg',},],};`), "cgiDataNew")
	if err != nil {
		t.Fatal(err)
	}
	if Number(Path(state, "item_show_type")) != 8 || Text(Path(state, "title")) != "don't" {
		t.Fatalf("unexpected state: %#v", state)
	}
	if got := mainlandCookie(`a=x; d_c0="value|time"; z_c0=z`, "d_c0"); got != `"value|time"` {
		t.Fatalf("signature cookie quotes changed: %q", got)
	}
	if _, err := mainlandLiteralState([]byte(`window.cgiDataNew={x:evil()}`), "cgiDataNew"); err == nil {
		t.Fatal("accepted executable JavaScript")
	}
}

func TestMainlandMissingVideoFails(t *testing.T) {
	r := mainlandResult("weibo", "https://weibo.com/1/2")
	r.Content = "正文"
	mainlandAdd(r, "image", "https://cdn.example/cover.jpg", nil)
	if _, err := mainlandFinish(r, true); err == nil {
		t.Fatal("poster-only result accepted as video")
	}
}

func TestMainlandBodyPreservesMixedMediaOrder(t *testing.T) {
	r := mainlandResult("weixin", "https://mp.weixin.qq.com/s/article")
	source := `<video src="https://cdn.example/a.mp4"><source src="https://cdn.example/a-backup.mp4" type="video/mp4"><img src="https://cdn.example/fallback.jpg"></video><img data-src="https://cdn.example/b.jpg"><audio><source src="https://cdn.example/unsupported.bin" type="application/octet-stream"><source src="https://cdn.example/c.mp3" type="audio/mpeg"><source src="https://cdn.example/c-backup.mp3" type="audio/mpeg"></audio><video><source src="https://cdn.example/d.mp4" type="video/mp4"><source src="https://cdn.example/d.webm" type="video/webm"></video>`
	if err := mainlandBody(r, source); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kind, url string
		backups   []string
	}{
		{"video", "https://cdn.example/a.mp4", []string{"https://cdn.example/a-backup.mp4"}},
		{"image", "https://cdn.example/b.jpg", nil},
		{"audio", "https://cdn.example/c.mp3", []string{"https://cdn.example/c-backup.mp3"}},
		{"video", "https://cdn.example/d.mp4", []string{"https://cdn.example/d.webm"}},
	}
	if len(r.Media) != len(want) {
		t.Fatalf("source alternatives or fallback images became extra media: %+v", r.Media)
	}
	for i, expected := range want {
		got := r.Media[i]
		if got.Kind != expected.kind || got.URL != expected.url {
			t.Fatalf("media %d reordered: %+v", i, got)
		}
		if len(got.BackupURLs) != len(expected.backups) {
			t.Fatalf("media %d alternatives: %+v", i, got.BackupURLs)
		}
		for j, backup := range expected.backups {
			if got.BackupURLs[j] != backup {
				t.Fatalf("media %d backup %d = %q", i, j, got.BackupURLs[j])
			}
		}
	}
}

func TestWeixinEmbeddedVideoFailuresAreNotPartialSuccess(t *testing.T) {
	for _, failure := range []string{"request", "missing-playback", "missing-id"} {
		t.Run(failure, func(t *testing.T) {
			seen := []string{}
			client := &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
				status := 200
				var body string
				if req.URL.Path == "/s/article" {
					second := `<mpvideo vid="second"></mpvideo>`
					if failure == "missing-id" {
						second = `<mpvideo></mpvideo>`
					}
					body = `<h1 id="activity-name">文章</h1><div id="js_content"><p>正文</p><mpvideo vid="first"></mpvideo><img src="https://cdn.example/photo.jpg">` + second + `</div>`
				} else if req.URL.Path == "/mp/videoplayer" {
					vid := req.URL.Query().Get("vid")
					seen = append(seen, vid)
					if vid == "second" {
						if failure == "request" {
							status = 403
						}
						body = `{"url_info":[]}`
					} else {
						body = `{"url_info":[{"url":"https://cdn.example/first.mp4","filesize":10}]}`
					}
				} else {
					return nil, fmt.Errorf("unexpected endpoint %s", req.URL)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			result, err := mainlandWeixin(context.Background(), client, "https://mp.weixin.qq.com/s/article", "")
			if err == nil || result != nil {
				t.Fatalf("returned partial article despite %s failure: result=%+v err=%v", failure, result, err)
			}
			if len(seen) == 0 || seen[0] != "first" {
				t.Fatalf("first embedded video was not resolved: %+v", seen)
			}
			if failure != "missing-id" && (len(seen) != 2 || seen[1] != "second") {
				t.Fatalf("second embedded video failure was not checked: %+v", seen)
			}
		})
	}
}

func TestWeixinStateVideosPreserveArticleOrder(t *testing.T) {
	client := &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		switch req.URL.Path {
		case "/s/article":
			body = `<script>window.cgiDataNew={item_show_type:0,title:'文章',content_noencode:'<p>正文</p><mpvideo vid="first"></mpvideo><img data-src="https://cdn.example/photo.jpg"><video><source src="https://cdn.example/direct.mp4" type="video/mp4"><source src="https://cdn.example/direct.webm" type="video/webm"></video>',};</script>`
		case "/mp/videoplayer":
			if req.URL.Query().Get("vid") != "first" {
				return nil, fmt.Errorf("unexpected video ID")
			}
			body = `{"url_info":[{"url":"https://cdn.example/first.mp4","filesize":10}]}`
		default:
			return nil, fmt.Errorf("unexpected endpoint %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	result, err := mainlandWeixin(context.Background(), client, "https://mp.weixin.qq.com/s/article", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://cdn.example/first.mp4", "https://cdn.example/photo.jpg", "https://cdn.example/direct.mp4"}
	if len(result.Media) != len(want) {
		t.Fatalf("incomplete or duplicated media: %+v", result.Media)
	}
	for i, u := range want {
		if result.Media[i].URL != u {
			t.Fatalf("media %d = %q; want %q", i, result.Media[i].URL, u)
		}
	}
	if len(result.Media[2].BackupURLs) != 1 || result.Media[2].BackupURLs[0] != "https://cdn.example/direct.webm" {
		t.Fatalf("direct video sources not grouped: %+v", result.Media[2])
	}
}
