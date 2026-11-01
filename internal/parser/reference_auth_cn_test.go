package parser

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type authCNTransport func(*http.Request) (*http.Response, error)

func (f authCNTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func authCNResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func TestReferenceAuthCNSignatureVectors(t *testing.T) {
	// Generated from the checked ParseHub 2.3.15 wheel, not from the Go implementation.
	if got := mainlandZhihuSignatureIV("/api/v4/answers/123", `"cookie"`, [16]byte{}); got != "2.0_N3O1=BdRlIM0h9H4apzutK+4rbrkmooHXQbJZW30IB7n6666+6666WL6665n6666" {
		t.Fatalf("Zhihu zero-IV vector: %s", got)
	}
	iv := [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	if got := mainlandZhihuSignatureIV("/api/v4/answers/2?include=data[*].content", `"session|value"`, iv); got != "2.0_4nqL=hHC5tSzmXtF2nIgkFoDoQ72v1ldbF+sZdNVLDSjnd6LtWxp8dmfqxxnpR66" {
		t.Fatalf("Zhihu query vector: %s", got)
	}
	for _, path := range []string{"/bbs/app/link/tree", "//bbs///app/link/tree/"} {
		if got := mainlandXHHSign(path, 1700000001, "ABCDEF0123456789ABCDEF0123456789"); got != "V2V1Z67" {
			t.Fatalf("XHH hkey %q: %s", path, got)
		}
	}
	if got := mainlandXHHSign("/bbs/app/link/tree", 1, "0"); got != "Y3UVD25" {
		t.Fatalf("XHH short-input vector: %s", got)
	}
	if got := mainlandXHHNonce(1700000000, "0.5"); got != "CDB3B12441BB0769ECB7C525498E8BA3" {
		t.Fatalf("XHH nonce vector: %s", got)
	}
	if got, err := mainlandXHHDES("heybox_website", "uy7mzc4h"); err != nil || got != "2jpTW+3W6P//jTYLqlf0KA==" {
		t.Fatalf("SecuritySm DES vector: %s, %v", got, err)
	}
}

func authCNCheckZhihuHeaders(t *testing.T, req *http.Request) {
	t.Helper()
	if req.Header.Get("Cookie") != `d_c0="session|value"; z_c0=user` || req.Header.Get("Origin") != "https://zhuanlan.zhihu.com" || req.Header.Get("Referer") != "https://zhuanlan.zhihu.com/" || req.Header.Get("X-Zse-93") != "101_3_3.0" || req.Header.Get("X-Requested-With") != "fetch" {
		t.Fatalf("Zhihu request headers: %+v", req.Header)
	}
	const alphabet = "6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE"
	masks := [16]byte{58, 0, 0, 0, 0, 40, 3, 0, 0, 0, 32, 14, 0, 0, 0, 0}
	sig := req.Header.Get("X-Zse-96")
	if !strings.HasPrefix(sig, "2.0_") || len(sig) != 68 {
		t.Fatalf("missing Zhihu signature: %q", sig)
	}
	var sextets [64]byte
	for i, ch := range []byte(sig[4:]) {
		n := strings.IndexByte(alphabet, ch)
		if n < 0 {
			t.Fatalf("invalid signature alphabet: %s", sig)
		}
		sextets[63-i] = byte(n) ^ masks[i%16]
	}
	var iv [16]byte
	for bit := range 128 {
		iv[bit/8] |= ((sextets[bit/6] >> uint(5-bit%6)) & 1) << uint(7-bit%8)
	}
	path, err := url.PathUnescape(req.URL.RequestURI())
	if err != nil {
		t.Fatal(err)
	}
	if want := mainlandZhihuSignatureIV(path, `"session|value"`, iv); sig != want {
		t.Fatalf("signature does not cover exact decoded request path: %s", req.URL)
	}
}

func TestReferenceZhihuQuestionTitleFallback(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		authCNCheckZhihuHeaders(t, req)
		switch calls {
		case 1:
			if req.URL.Path != "/api/v4/questions/12/answers" || req.URL.RawQuery != "offset=&limit=1&sort_by=default&include=data%5B%2A%5D.content" {
				t.Fatalf("question listing contract: %s", req.URL)
			}
			return authCNResponse(req, `{"data":[]}`), nil
		case 2:
			if req.URL.Path != "/api/v4/questions/12" || req.URL.RawQuery != "" {
				t.Fatalf("question title contract: %s", req.URL)
			}
			return authCNResponse(req, `{"title":"No answers yet"}`), nil
		default:
			return nil, fmt.Errorf("unexpected request")
		}
	})}
	r, err := mainlandZhihu(context.Background(), client, "https://www.zhihu.com/question/12", `d_c0="session|value"; z_c0=user`)
	if err != nil || r.Title != "No answers yet" || r.Type != "multimedia" || r.Content != "" || len(r.Media) != 0 || calls != 2 {
		t.Fatalf("title-only result: %+v, %v, calls=%d", r, err, calls)
	}
}

func TestReferenceZhihuDailyRecursesToRichAnswer(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "daily.zhihu.com" {
			if req.Header.Get("Cookie") != "" || req.URL.Path != "/api/7/story/1" {
				t.Fatalf("daily request boundary: %s, %+v", req.URL, req.Header)
			}
			return authCNResponse(req, `{"title":"Daily wrapper","body":"<a href='https://www.zhihu.com/question/12/answer/34'>查看知乎原文</a>"}`), nil
		}
		authCNCheckZhihuHeaders(t, req)
		if req.URL.Path != "/api/v4/answers/34" || req.URL.Query().Get("include") != "data[*].content" {
			t.Fatalf("answer request contract: %s", req.URL)
		}
		return authCNResponse(req, `{"question":{"title":"Original question"},"content":"<h2>Heading</h2><p><a href='https://example.com/a'>Link</a></p><img src='https://cdn.example/photo.jpg'><img src='https://www.zhihu.com/equation?tex=x'><img src='data:image/svg+xml,svg'><audio src='https://cdn.example/music.mp3'></audio><a class='video-box' data-video-id='3'>video</a>"}`), nil
	})}
	r, err := mainlandZhihu(context.Background(), client, "https://daily.zhihu.com/story/1", `d_c0="session|value"; z_c0=user`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Original question" || r.SourceURL != "https://daily.zhihu.com/story/1" || r.Type != "article" || !strings.Contains(r.MarkdownContent, "[Link](https://example.com/a)") || !strings.Contains(r.MarkdownContent, "## Heading") || strings.Contains(r.MarkdownContent, "data:image/svg") || len(r.Media) != 1 || r.Media[0].Kind != "image" || calls != 2 {
		t.Fatalf("daily richtext result: %+v, calls=%d", r, calls)
	}
}

func TestReferenceZhihuPinTypesAndPlaylistOrder(t *testing.T) {
	cases := []struct{ name, body, kind, mediaURL string }{
		{"text", `{"content":[{"type":"text","title":"Pin","content":"<p>Hello <strong>world</strong></p>"}]}`, "image", ""},
		{"image", `{"content":[{"type":"text","title":"Pin","content":"hello"},{"type":"image","original_url":"https://cdn.example/original.jpg","url":"https://cdn.example/thumb.jpg","width":100,"height":200}]}`, "image", "https://cdn.example/original.jpg"},
		{"video", `{"content":[{"type":"text","title":"Pin","content":"hello"},{"type":"video","video_info":{"thumbnail":"https://cdn.example/thumb.jpg","duration":9,"playlist":{"first":{"url":"https://cdn.example/first.mp4","height":100},"second":{"url":"https://cdn.example/second.mp4","height":200}}}}]}`, "video", "https://cdn.example/first.mp4"},
		{"mixed-reference-bug", `{"content":[{"type":"text","title":"Pin","content":"hello"},{"type":"image","original_url":"https://cdn.example/not-video.jpg"},{"type":"video","video_info":{"playlist":{"hd":{"url":"https://cdn.example/video.mp4"}}}}]}`, "video", "https://cdn.example/video.mp4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) { return authCNResponse(req, tc.body), nil })}
			r, err := mainlandZhihu(context.Background(), client, "https://www.zhihu.com/pin/123", "d_c0=x")
			if err != nil {
				t.Fatal(err)
			}
			if r.Type != tc.kind || r.MarkdownContent != "" || r.Title != "Pin" || (tc.mediaURL == "" && len(r.Media) != 0) || (tc.mediaURL != "" && (len(r.Media) != 1 || r.Media[0].URL != tc.mediaURL || r.Media[0].Kind != tc.kind)) {
				t.Fatalf("pin result: %+v", r)
			}
		})
	}
}

func TestReferenceZhihuErrorsAndDailyBoundary(t *testing.T) {
	for _, tc := range []struct{ name, raw, cookie, body, contains string }{
		{"cookie", "https://daily.zhihu.com/story/1", "", "", "Cookie"},
		{"dc", "https://www.zhihu.com/question/1", "z_c0=x", "", "d_c0"},
		{"host", "https://www.zhihu.com.evil.example/question/1", "d_c0=x", "", "官方域名"},
		{"api-error", "https://www.zhihu.com/question/1/answer/2", "d_c0=x", `{"error":{"code":403,"message":"blocked"}}`, "403: blocked"},
		{"daily-missing", "https://daily.zhihu.com/story/1", "d_c0=x", `{"body":"<p>Daily body</p>"}`, "未在知乎日报中找到原文链接"},
		{"daily-loop", "https://daily.zhihu.com/story/1", "d_c0=x", `{"body":"<a href='https://daily.zhihu.com/story/1'>查看知乎原文</a>"}`, "循环"},
		{"daily-host", "https://daily.zhihu.com/story/1", "d_c0=x", `{"body":"<a href='https://evil.example/question/1'>查看知乎原文</a>"}`, "官方域名"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
				if tc.body == "" {
					t.Fatal("invalid input caused an HTTP request")
				}
				return authCNResponse(req, tc.body), nil
			})}
			_, err := mainlandZhihu(context.Background(), client, tc.raw, tc.cookie)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error = %v, want %q", err, tc.contains)
			}
		})
	}
}

func authCNXHHClient(t *testing.T, body string) (*http.Client, *int) {
	t.Helper()
	calls := 0
	return &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if req.URL.String() != "https://fp-it.portal101.cn/deviceprofile/v4" || req.Method != http.MethodPost || req.Header.Get("Cookie") != "" {
				t.Fatalf("device request: %s, %+v", req.URL, req.Header)
			}
			var payload map[string]any
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["appId"] != "heybox_website" || payload["organization"] != "0yD85BjYvGFAvHaSQ1mc" || payload["os"] != "web" || Number(payload["compress"]) != 2 || Number(payload["encode"]) != 5 || len(payload) != 7 {
				t.Fatalf("SecuritySm envelope: %+v", payload)
			}
			data, err := hex.DecodeString(Text(payload["data"]))
			if err != nil || len(data) == 0 || len(data)%16 != 0 {
				t.Fatalf("invalid AES-CBC ciphertext: %v", err)
			}
			ep, err := base64.StdEncoding.DecodeString(Text(payload["ep"]))
			if err != nil || len(ep) != 128 {
				t.Fatalf("invalid encrypted device UUID: %v", err)
			}
			return authCNResponse(req, `{"code":1100,"detail":{"deviceId":"fixture-device"}}`), nil
		}
		if calls != 2 || req.URL.Host != "api.xiaoheihe.cn" || req.URL.Path != "/bbs/app/link/tree" || req.Method != http.MethodGet || req.Header.Get("Cookie") != "x_xhh_tokenid=Bfixture-device" {
			t.Fatalf("link request: %s, %+v", req.URL, req.Header)
		}
		q := req.URL.Query()
		want := map[string]string{"os_type": "web", "app": "heybox", "client_type": "web", "version": "999.0.4", "web_version": "2.5", "x_client_type": "web", "x_app": "heybox_website", "heybox_id": "", "x_os_type": "Windows", "device_info": "Chrome", "link_id": "123", "owner_only": "1"}
		for key, value := range want {
			if q.Get(key) != value {
				t.Fatalf("query %s = %q, want %q", key, q.Get(key), value)
			}
		}
		if len(q) != len(want)+3 || !regexp.MustCompile(`^[A-F0-9]{32}$`).MatchString(q.Get("nonce")) {
			t.Fatalf("signature parameters: %v", q)
		}
		timestamp, err := strconv.ParseInt(q.Get("_time"), 10, 64)
		if err != nil || q.Get("hkey") != mainlandXHHSign("/bbs/app/link/tree", timestamp+1, q.Get("nonce")) {
			t.Fatalf("hkey does not cover nonce, path and offset timestamp: %v", q)
		}
		return authCNResponse(req, body), nil
	})}, &calls
}

func TestReferenceXHHBranchOrderAndGIFs(t *testing.T) {
	parts := `[{"type":"html","text":"<h2>Heading</h2><p><a href='https://example.com'>Link</a><img data-original='https://cdn.example/inline/thumb.jpg?size=1' src='bad'></p>"},{"type":"html","text":"MUST NOT APPEND"},{"type":"img","url":"https://cdn.example/photo/thumb.jpg?size=1","width":"100.9","height":"200.1"},{"type":"img","url":"https://cdn.example/animation.gif?size=2"}]`
	for _, concept := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"status": "ok", "result": map[string]any{"link": map[string]any{"title": "Post", "has_video": false, "use_concept_type": concept, "text": parts}}})
		client, calls := authCNXHHClient(t, string(body))
		r, err := mainlandXiaoheihe(context.Background(), client, "https://www.xiaoheihe.cn/app/bbs/link/123", "x_xhh_tokenid=must-not-reuse; login=must-not-send")
		if err != nil {
			t.Fatal(err)
		}
		if *calls != 2 || len(r.Media) != 2 || r.Media[0].URL != "https://cdn.example/photo.jpg" || r.Media[0].ThumbnailURL != "https://cdn.example/photo/thumb.jpg?size=1" || r.Media[0].Width != 100 || r.Media[0].Height != 200 || r.Media[1].Kind != "gif" || r.Media[1].URL != "https://cdn.example/animation.gif" {
			t.Fatalf("XHH media: %+v", r)
		}
		markdown := r.MarkdownContent
		if concept {
			markdown = r.Content
			if r.Type != "multimedia" || r.MarkdownContent != "" {
				t.Fatalf("concept result: %+v", r)
			}
		} else if r.Type != "article" {
			t.Fatalf("article result: %+v", r)
		}
		if !strings.Contains(markdown, "## Heading") || !strings.Contains(markdown, "https://cdn.example/inline.jpg") || strings.Contains(markdown, "MUST NOT APPEND") {
			t.Fatalf("first-part Markdown: %q", markdown)
		}
	}
}

func TestReferenceXHHVideoAndFailureStates(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"video", `{"status":"ok","result":{"link":{"title":"Video","has_video":true,"text":"<b>literal video text</b>","video_url":"https://cdn.example/video.mp4","video_thumb":"https://cdn.example/thumb.jpg"}}}`, ""},
		{"missing-video", `{"status":"ok","result":{"link":{"title":"Video","has_video":true,"text":"text"}}}`, "视频播放地址不可用"},
		{"empty-parts", `{"status":"ok","result":{"link":{"title":"Post","has_video":false,"text":"[]"}}}`, "正文内容为空"},
		{"invalid-parts", `{"status":"ok","result":{"link":{"has_video":false,"text":"not-json"}}}`, "正文格式错误"},
		{"login", `{"status":"login","msg":"auth"}`, "需要登录"},
		{"captcha", `{"status":"show_captcha"}`, "需要过验证码"},
		{"relogin", `{"status":"relogin"}`, "需要重新登录"},
		{"failed", `{"status":"failed"}`, "请求失败"},
		{"token", `{"status":"lack_token"}`, "缺少token"},
		{"unknown", `{"status":"other"}`, "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := authCNXHHClient(t, tc.body)
			r, err := mainlandXiaoheihe(context.Background(), client, "https://www.xiaoheihe.cn/v3/bbs/app/api?link_id=123", "")
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error: %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil || r.Type != "video" || r.Content != "<b>literal video text</b>" || len(r.Media) != 1 || r.Media[0].Kind != "video" {
				t.Fatalf("video: %+v, %v", r, err)
			}
		})
	}
}

func TestReferenceXHHDeviceFailure(t *testing.T) {
	for _, body := range []string{`{"code":1,"message":"denied"}`, `{"code":1100,"detail":{}}`} {
		calls := 0
		client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) { calls++; return authCNResponse(req, body), nil })}
		_, err := mainlandXiaoheihe(context.Background(), client, "https://www.xiaoheihe.cn/app/bbs/link/123", "x_xhh_tokenid=ignored")
		if err == nil || !strings.Contains(err.Error(), "设备认证失败") || calls != 1 {
			t.Fatalf("device state: %v, calls=%d", err, calls)
		}
	}
}

func authCNChannelsClient(t *testing.T, first, feed string) (*http.Client, *int) {
	t.Helper()
	calls := 0
	return &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var payload any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Channels method/encoding: %s, %+v", req.Method, req.Header)
		}
		if calls == 1 {
			if req.URL.String() != "https://yuanbao.tencent.com/api/weixin/get_parse_result" || req.Header.Get("Cookie") != "yuanbao=session" || req.Header.Get("Origin") != "https://yuanbao.tencent.com" || Text(Path(payload, "type")) != "video_channel_url" || Text(Path(payload, "url")) != "https://weixin.qq.com/sph/Abc_123" || Number(Path(payload, "scene")) != 1 || len(Obj(payload)) != 3 {
				t.Fatalf("Yuanbao contract: %s, %+v, %+v", req.URL, req.Header, payload)
			}
			return authCNResponse(req, first), nil
		}
		if calls != 2 || req.URL.Host != "channels.weixin.qq.com" || req.URL.Path != "/finder-preview/api/feed/get_feed_info" || req.Header.Get("Cookie") != "" || req.Header.Get("Origin") != "https://channels.weixin.qq.com" || req.Header.Get("Referer") != "https://channels.weixin.qq.com/finder-preview/pages/feed?token=token&eid=export" || req.URL.Query().Get("_pageUrl") != "https://channels.weixin.qq.com/finder-preview/pages/feed" || !regexp.MustCompile(`^[a-f0-9]+-[a-f0-9]{8}$`).MatchString(req.URL.Query().Get("_rid")) || Text(Path(payload, "baseReq", "generalToken")) != "token" || Text(Path(payload, "exportId")) != "export" || len(Obj(payload)) != 2 {
			t.Fatalf("officialpreview contract: %s, %+v, %+v", req.URL, req.Header, payload)
		}
		return authCNResponse(req, feed), nil
	})}, &calls
}

const authCNChannelsFirst = `{"code":0,"data":{"wx_export_id":"export","playable_url":"https://channels.weixin.qq.com/finder-preview/pages/feed?token=token&eid=export","desc":"fallback title","author":"fallback author"}}`

func TestReferenceChannelsVideoPreferenceAndCookieBoundary(t *testing.T) {
	for _, tc := range []struct{ info, want string }{
		{`"h264VideoInfo":{"videoUrl":"https://cdn.example/h264.mp4"},"videoUrl":"https://cdn.example/top.mp4","h265VideoInfo":{"videoUrl":"https://cdn.example/h265.mp4"}`, "h264"},
		{`"videoUrl":"https://cdn.example/top.mp4","h265VideoInfo":{"videoUrl":"https://cdn.example/h265.mp4"}`, "top"},
		{`"h265VideoInfo":{"videoUrl":"https://cdn.example/h265.mp4"}`, "h265"},
	} {
		feed := `{"errCode":0,"data":{"authorInfo":{"nickname":"Author"},"feedInfo":{"description":"Title","coverUrl":"https://cdn.example/cover.jpg",` + tc.info + `}}}`
		client, calls := authCNChannelsClient(t, authCNChannelsFirst, feed)
		r, err := mainlandChannels(context.Background(), client, "https://channels.weixin.qq.com/finder-preview/pages/sph?other=1&id=Abc_123", "yuanbao=session")
		if err != nil || *calls != 2 || r.Title != "Title" || r.Author != "Author" || len(r.Media) != 1 || r.Media[0].URL != "https://cdn.example/"+tc.want+".mp4" || r.Media[0].ThumbnailURL != "https://cdn.example/cover.jpg" || r.Media[0].Headers["Cookie"] != "" {
			t.Fatalf("Channels %s: %+v, %v", tc.want, r, err)
		}
	}
}

func TestReferenceChannelsErrorStates(t *testing.T) {
	for _, tc := range []struct {
		name, first, feed, want string
		unavailable             bool
	}{
		{"expired", `{"error":{"code":20000}}`, "", "Cookie 无效或已过期", false},
		{"yuanbao-error", `{"code":1,"message":"denied"}`, "", "denied", false},
		{"unavailable", `{"code":0,"data":{}}`, "", "不存在或已删除", true},
		{"host", `{"code":0,"data":{"wx_export_id":"x","playable_url":"https://evil.example/feed?token=t&eid=e"}}`, "", "非官方预览", false},
		{"scheme", `{"code":0,"data":{"wx_export_id":"x","playable_url":"http://channels.weixin.qq.com/finder-preview/pages/feed?token=t&eid=e"}}`, "", "非官方预览", false},
		{"token", `{"code":0,"data":{"wx_export_id":"x","playable_url":"https://channels.weixin.qq.com/finder-preview/pages/feed?eid=e"}}`, "", "token 或 eid", false},
		{"preview-error", authCNChannelsFirst, `{"errCode":1,"errMsg":"preview denied"}`, "preview denied", false},
		{"preview-content", authCNChannelsFirst, `{"errCode":0,"data":{"errMsg":{"title":"<b>Denied</b>","content":"Try &amp; retry"}}}`, "Denied Try & retry", false},
		{"unsupported", authCNChannelsFirst, `{"errCode":0,"data":{"feedInfo":{}}}`, "未提供可播放视频", false},
		{"encrypted", authCNChannelsFirst, `{"errCode":0,"data":{"feedInfo":{"h264VideoInfo":{"videoUrl":"https://cdn.example/video.mp4","decodeKey":"opaque"}}}}`, "加密媒体", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := authCNChannelsClient(t, tc.first, tc.feed)
			_, err := mainlandChannels(context.Background(), client, "https://weixin.qq.com/sph/Abc_123", "yuanbao=session")
			if err == nil || !strings.Contains(err.Error(), tc.want) || referenceUnavailable(err) != tc.unavailable {
				t.Fatalf("error: %v, unavailable=%v", err, referenceUnavailable(err))
			}
		})
	}
}

func TestReferenceAuthCNShortSignatureAndInvalidInputs(t *testing.T) {
	if got := mainlandXHHSign("/a", 1, "0"); got != "VD77I79" {
		t.Fatalf("short-path interleave vector: %s", got)
	}
	client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("invalid input issued a request: %s", req.URL)
		return nil, fmt.Errorf("unexpected request")
	})}
	for _, raw := range []string{"https://evil.example/app/bbs/link/123", "https://www.xiaoheihe.cn.evil.example/app/bbs/link/123", "file:///app/bbs/link/123"} {
		if _, err := mainlandXiaoheihe(context.Background(), client, raw, ""); err == nil {
			t.Fatalf("accepted Xiaoheihe input %s", raw)
		}
	}
	for _, raw := range []string{"https://evil.example/sph/Abc_123", "https://weixin.qq.com.evil.example/sph/Abc_123", "https://channels.weixin.qq.com/other?id=Abc_123", "https://weixin.qq.com/sph/"} {
		if _, err := mainlandChannels(context.Background(), client, raw, "yuanbao=session"); err == nil {
			t.Fatalf("accepted Channels input %s", raw)
		}
	}
}

func TestReferenceZhihuArticleInputsAndEmptyAnswer(t *testing.T) {
	for _, raw := range []string{"https://zhuanlan.zhihu.com/p/7", "https://www.zhihu.com/appview/p/7", "https://www.zhihu.com/question/1/answer/2"} {
		client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(raw, "/answer/") {
				if req.URL.Path != "/api/v4/answers/2" || req.URL.Query().Get("include") != "data[*].content" {
					t.Fatalf("answer contract: %s", req.URL)
				}
				return authCNResponse(req, `{"question":{"title":"Question"},"title":"not the question","content":""}`), nil
			}
			if req.URL.Host != "zhuanlan.zhihu.com" || req.URL.Path != "/api/articles/7" || req.URL.RawQuery != "" {
				t.Fatalf("article contract: %s", req.URL)
			}
			return authCNResponse(req, `{"title":"Article","content":"<h2>Section</h2><p><strong>Bold</strong> <a href='/p/8'>next</a></p><img src='https://cdn.example/photo.jpg'>"}`), nil
		})}
		r, err := mainlandZhihu(context.Background(), client, raw, "d_c0=x")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "/answer/") {
			if r.Type != "multimedia" || r.Title != "Question" || r.MarkdownContent != "" || len(r.Media) != 0 {
				t.Fatalf("empty answer: %+v", r)
			}
		} else if r.Type != "article" || r.Title != "Article" || len(r.Media) != 1 || !strings.Contains(r.MarkdownContent, "**Bold**") || !strings.Contains(r.MarkdownContent, "https://"+strings.Split(strings.TrimPrefix(raw, "https://"), "/")[0]+"/p/8") {
			t.Fatalf("article: %+v", r)
		}
	}
}

func TestReferenceZhihuQuestionUsesFirstAnswer(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		switch req.URL.Path {
		case "/api/v4/questions/1/answers":
			return authCNResponse(req, `{"data":[{"id":2},{"id":3}]}`), nil
		case "/api/v4/answers/2":
			return authCNResponse(req, `{"question":{"title":"Question"},"content":"<p>First answer</p>"}`), nil
		default:
			return nil, fmt.Errorf("unexpected endpoint %s", req.URL)
		}
	})}
	r, err := mainlandZhihu(context.Background(), client, "https://www.zhihu.com/question/1", "d_c0=x")
	if err != nil || calls != 2 || r.Title != "Question" || r.Content != "First answer" {
		t.Fatalf("first answer: %+v, %v, calls=%d", r, err, calls)
	}
}

func TestReferenceChannelsHTTPUnauthorized(t *testing.T) {
	client := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		response := authCNResponse(req, `{"message":"expired"}`)
		response.StatusCode = http.StatusUnauthorized
		return response, nil
	})}
	_, err := mainlandChannels(context.Background(), client, "https://weixin.qq.com/sph/Abc_123", "yuanbao=session")
	if err == nil || !strings.Contains(err.Error(), "Cookie 无效或已过期") || referenceUnavailable(err) {
		t.Fatalf("unauthorized state: %v", err)
	}
}

func TestReferenceXHHProviderBooleanSemantics(t *testing.T) {
	for _, value := range []any{nil, false, "", json.Number("0"), json.Number("0.0"), []any{}, map[string]any{}} {
		if mainlandXHHTruthy(value) {
			t.Fatalf("expected false provider value: %#v", value)
		}
	}
	for _, value := range []any{true, "0", "false", json.Number("1"), []any{nil}, map[string]any{"key": nil}} {
		if !mainlandXHHTruthy(value) {
			t.Fatalf("expected true provider value: %#v", value)
		}
	}
}

func TestReferenceAuthCNTitleOnlyPosts(t *testing.T) {
	zhihuClient := &http.Client{Transport: authCNTransport(func(req *http.Request) (*http.Response, error) {
		return authCNResponse(req, `{"content":[{"type":"text","title":"Pin title","content":""}]}`), nil
	})}
	pin, err := mainlandZhihu(context.Background(), zhihuClient, "https://www.zhihu.com/pin/1", "d_c0=x")
	if err != nil || pin.Title != "Pin title" || pin.Type != "image" || pin.Content != "" || len(pin.Media) != 0 {
		t.Fatalf("title-only pin: %+v, %v", pin, err)
	}
	xhhClient, _ := authCNXHHClient(t, `{"status":"ok","result":{"link":{"title":"Article title","text":"[{\"type\":\"text\",\"text\":\"\"}]","has_video":false}}}`)
	article, err := mainlandXiaoheihe(context.Background(), xhhClient, "https://www.xiaoheihe.cn/app/bbs/link/123", "")
	if err != nil || article.Title != "Article title" || article.Type != "article" || article.Content != "" || len(article.Media) != 0 {
		t.Fatalf("title-only article: %+v, %v", article, err)
	}
}
