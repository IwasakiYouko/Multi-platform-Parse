package parser

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mainlandFixtureTransport func(*http.Request) (*http.Response, error)

func (f mainlandFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMainlandNativeResolvers(t *testing.T) {
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"GetPlayInfoToken":"Action=GetPlayInfo&Version=2018-01-01"}`))
	cases := []struct {
		id, raw, cookie, kind string
		routes                map[string]string
	}{
		{"weibo", "https://weibo.com/123/Abcd12345", "", "video", map[string]string{"/ajax/statuses/show": `{"id":"1","text_raw":"正文","page_info":{"object_type":"video","media_info":{"playback_list":[{"play_info":{"url":"https://cdn.example/video.mp4","width":1280,"height":720}}]}}}`}},
		{"tieba", "https://tieba.baidu.com/p/123", "", "image", map[string]string{"/dc/common/tbs": `{"tbs":"token"}`, "/c/f/pb/page_pc": `{"error_code":0,"thread":{"origin_thread_info":{"title":"帖子","content":[{"type":0,"text":"正文"}],"media":[{"big_pic":"https://cdn.example/photo.jpg"}]}}}`}},
		{"weixin", "https://mp.weixin.qq.com/s/article", "", "image", map[string]string{"/s/article": `<script>window.cgiDataNew={item_show_type:'0' * 1,title:'文章',content_noencode:'<p>正文</p><img data-src="https://cdn.example/photo.jpg">',};</script><div id="js_content"></div>`}},
		{"weixin_channels", "https://weixin.qq.com/sph/Abcd123", "yuanbao_session=secret", "video", map[string]string{"/api/weixin/get_parse_result": `{"code":0,"data":{"wx_export_id":"id","playable_url":"https://channels.weixin.qq.com/finder-preview/pages/feed?token=token&eid=eid"}}`, "/finder-preview/api/feed/get_feed_info": `{"errCode":0,"data":{"feedInfo":{"description":"视频正文","h264VideoInfo":{"videoUrl":"https://cdn.example/video.mp4"}}}}`}},
		{"coolapk", "https://www.coolapk.com/feed/123?s=key", "", "image", map[string]string{"/feed/123": `<div class="feed-message">正文</div><div class="message-image-group"><img src="https://cdn.example/photo.jpg"></div><aside><img src="https://cdn.example/avatar.jpg"></aside>`}},
		{"pipix", "https://h5.pipix.com/item/123", "", "video", map[string]string{"/item/123": `<script id="RENDER_DATA">{"ppxItemDetail":{"item":{"item_type":2,"content":"正文","video":{"video_download":{"url_list":[{"url":"https://cdn.example/video.mp4"}],"cover_image":{"download_list":[{"url":"https://cdn.example/cover.jpg"}]},"width":1280,"height":720}}}}}</script>`}},
		{"zuiyou", "https://share.xiaochuankeji.cn/hybrid/share/post?pid=123", "", "video", map[string]string{"/planck/share/post/detail_h5": `{"data":{"post":{"content":"正文","imgs":[{"id":42,"video":true,"urls":{"original":{"urls":["https://cdn.example/cover.jpg"],"w":1280,"h":720}}}],"videos":{"42":{"url":"https://cdn.example/video.mp4"}}}}}`}},
		{"xiaoheihe", "https://www.xiaoheihe.cn/app/bbs/link/123", "x_xhh_tokenid=device", "image", map[string]string{"/bbs/app/link/tree": `{"status":"ok","result":{"link":{"title":"帖子","has_video":false,"text":"[{\"type\":\"text\",\"text\":\"正文\"},{\"type\":\"img\",\"url\":\"https://cdn.example/photo/thumb.jpg?size=1\"}]"}}}`}},
		{"zhihu", "https://www.zhihu.com/question/1/answer/2", `d_c0="session|value"; z_c0=secret`, "video", map[string]string{"/api/v4/answers/2": `{"question":{"title":"问题"},"content":"<p>正文</p><a class=\"video-box\" data-video-id=\"3\">视频</a>"}`, "/api/v4/videos/3": `{"playlist":{"hd":{"url":"https://cdn.example/video.mp4","width":1280,"height":720}},"duration":20}`}},
		{"douban", "https://www.douban.com/group/topic/123", "", "image", map[string]string{"/rexxar/api/v2/group/topic/123": `{"title":"话题","content":"<p>正文</p>","photos":[{"image":{"large":{"url":"https://cdn.example/photo.jpg"}}}]}`}},
		{"bangumi", "https://bgm.tv/group/topic/123", "", "image", map[string]string{"/p1/groups/-/topics/123": `{"title":"讨论","replies":[{"content":"正文[img]https://cdn.example/photo.jpg[/img]"}]}`}},
		{"toutiao", "https://www.toutiao.com/video/123", "session=secret", "video", map[string]string{"/i123/info/v2/": fmt.Sprintf(`{"data":{"title":"视频","play_auth_token_v2":%q}}`, token), "/": `{"Result":{"Data":{"PlayInfoList":[{"MainPlayUrl":"https://cdn.example/video.mp4","Width":1280,"Height":720}]}}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				source, ok := tc.routes[req.URL.Path]
				if !ok {
					return nil, fmt.Errorf("unexpected endpoint %s", req.URL)
				}
				if req.URL.Host == "vod.bytedanceapi.com" || req.URL.Host == "channels.weixin.qq.com" || req.URL.Host == "lens.zhihu.com" {
					if req.Header.Get("Cookie") != "" {
						t.Errorf("cookie leaked to auxiliary endpoint %s", req.URL.Host)
					}
				}
				if tc.id == "zhihu" && req.URL.Host == "www.zhihu.com" {
					if !strings.HasPrefix(req.Header.Get("X-Zse-96"), "2.0_") {
						t.Error("missing native signature")
					}
				}
				if tc.id == "xiaoheihe" {
					if req.URL.Query().Get("hkey") == "" || req.URL.Query().Get("nonce") == "" {
						t.Error("missing native request signature")
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(source)), Request: req}, nil
			})}
			resolver := registry[tc.id]
			if resolver == nil {
				t.Fatalf("resolver not registered")
			}
			r, err := resolver(context.Background(), client, tc.raw, tc.cookie)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Media) != 1 || r.Media[0].Kind != tc.kind {
				t.Fatalf("unexpected media: %+v", r.Media)
			}
			if r.Content == "" && r.Title == "" {
				t.Fatal("missing extracted body/title")
			}
			if calls == 0 {
				t.Fatal("did not use native provider")
			}
		})
	}
}

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

func TestMainlandSignatureEncoding(t *testing.T) {
	var iv [16]byte
	a := mainlandZhihuSignatureIV("/api/v4/answers/123", `"cookie"`, iv)
	b := mainlandZhihuSignatureIV("/api/v4/answers/123", `"cookie"`, iv)
	if a != b || len(a) != 68 || !strings.HasPrefix(a, "2.0_") {
		t.Fatalf("invalid deterministic signature %q", a)
	}
	if a == mainlandZhihuSignatureIV("/api/v4/answers/124", `"cookie"`, iv) {
		t.Fatal("signature does not cover URL")
	}
	if h := mainlandXHHSign("/bbs/app/link/tree", 1700000001, "ABCDEF0123456789ABCDEF0123456789"); len(h) != 7 {
		t.Fatalf("invalid hkey %q", h)
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
