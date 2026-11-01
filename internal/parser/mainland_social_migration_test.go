package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type socialMigrationTransport func(*http.Request) (*http.Response, error)

func (f socialMigrationTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func socialMigrationResponse(req *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func socialMigrationClient(t *testing.T, body string) *http.Client {
	t.Helper()
	return &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
		return socialMigrationResponse(req, body), nil
	})}
}

func TestSocialMigrationWeiboOrderedKindsAndLongText(t *testing.T) {
	body := `{"id":"42","text_raw":"完整长正文 #话题# https://t.cn/page 尾","text":"截断正文","isLongText":true,"longText":{"longTextContent":"不要用此 HTML"},"pic_ids":["a","z","m"],"pics":[{"url":"https://cdn.example/duplicate.jpg"}],"pic_infos":{"z":{"pic_id":"z","type":"gif","thumbnail":{"url":"https://cdn.example/animation.jpg"},"largest":{"url":"https://cdn.example/wrong.gif","width":999,"height":999},"video":"https://cdn.example/animation.mp4"},"m":{"pic_id":"m","type":"livephoto","thumbnail":{"url":"https://cdn.example/live.jpg"},"largest":{"url":"https://cdn.example/wrong-live.jpg","width":1000,"height":800},"video":"https://cdn.example/live.mov"},"a":{"pic_id":"a","type":"pic","thumbnail":{"url":"https://cdn.example/small.jpg"},"largest":{"url":"https://cdn.example/original.jpg","width":1200,"height":900}}},"page_info":{"object_type":"video","short_url":"https://t.cn/page","media_info":{"playback_list":[{"play_info":{"url":"https://cdn.example/ignored.mp4"}}]}}}`
	calls := 0
	client := &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "weibo.com" || req.URL.Path != "/ajax/statuses/show" || req.URL.Query().Get("id") != "Abcd12345" || req.URL.Query().Get("isGetLongText") != "true" {
			t.Fatalf("wrong status endpoint: %s", req.URL)
		}
		if req.Header.Get("Cookie") != "SUB=configured" {
			t.Fatal("configured cookie replaced")
		}
		return socialMigrationResponse(req, body), nil
	})}
	r, err := mainlandWeibo(context.Background(), client, "https://m.weibo.cn/status/Abcd12345?irrelevant=1", "SUB=configured")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(r.Content, "完整长正文") || strings.Contains(r.Content, "话题#") || strings.Contains(r.Content, "https://t.cn/page") || strings.Contains(r.Content, "截断") {
		t.Fatalf("long text/link conversion: calls=%d content=%q", calls, r.Content)
	}
	if r.Type != "image" || len(r.Media) != 3 {
		t.Fatalf("wrong media selection: %+v", r)
	}
	gif, live, photo := r.Media[0], r.Media[1], r.Media[2]
	if gif.Kind != "gif" || gif.URL != "https://cdn.example/animation.mp4" || gif.ThumbnailURL != "https://cdn.example/animation.jpg" {
		t.Fatalf("AniRef conversion: %+v", gif)
	}
	if live.Kind != "livephoto" || live.URL != "https://cdn.example/live.jpg" || live.VideoURL != "https://cdn.example/live.mov" || live.Width != 1000 || live.Height != 800 {
		t.Fatalf("LivePhotoRef flattened or reordered: %+v", live)
	}
	if photo.Kind != "image" || photo.URL != "https://cdn.example/original.jpg" || photo.ThumbnailURL != "https://cdn.example/small.jpg" {
		t.Fatalf("photo selection: %+v", photo)
	}
}

func TestSocialMigrationWeiboRetweetMediaPrecedence(t *testing.T) {
	body := `{"id":42,"text_raw":"外层正文","pic_infos":{"own":{"type":"pic","largest":{"url":"https://cdn.example/own.jpg"}}},"retweeted_status":{"text_raw":"不可追加的转发正文","pic_infos":{"z":{"type":"pic","largest":{"url":"https://cdn.example/first.jpg"}},"a":{"type":"pic","largest":{"url":"https://cdn.example/second.jpg"}}}},"mix_media_info":{"items":[{"type":"video","data":{"media_info":{"mp4_hd_url":"https://cdn.example/ignored.mp4"}}}]}}`
	r, err := mainlandWeibo(context.Background(), socialMigrationClient(t, body), "https://weibo.com/1/Abcd12345", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Content != "外层正文" || len(r.Media) != 2 || r.Media[0].URL != "https://cdn.example/first.jpg" || r.Media[1].URL != "https://cdn.example/second.jpg" {
		t.Fatalf("retweet media precedence/order: %+v", r)
	}
}

func TestSocialMigrationWeiboFirstPlaybackAndMixedOrder(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			page := `{"object_type":"video","page_pic":"https://cdn.example/poster.jpg","media_info":{"duration":19,"mp4_hd_url":"https://cdn.example/ignored.mp4","playback_list":[{"play_info":{"url":"https://cdn.example/first.mp4","width":640,"height":360,"duration":3.9}},{"play_info":{"url":"https://cdn.example/second.mp4","width":1920,"height":1080}}]}}`
			body := `{"id":"1","text_raw":"正文","page_info":` + page + `}`
			if mixed {
				body = `{"id":"1","text_raw":"正文","mix_media_info":{"items":[{"type":"pic","data":{"type":"pic","largest":{"url":"https://cdn.example/first.jpg"}}},{"type":"video","data":` + page + `},{"type":"pic","data":{"type":"pic","largest":{"url":"https://cdn.example/last.jpg"}}}]}}`
			}
			r, err := mainlandWeibo(context.Background(), socialMigrationClient(t, body), "https://weibo.com/1/123", "")
			if err != nil {
				t.Fatal(err)
			}
			index := 0
			if mixed {
				index = 1
				if r.Type != "multimedia" || len(r.Media) != 3 || r.Media[0].URL != "https://cdn.example/first.jpg" || r.Media[2].URL != "https://cdn.example/last.jpg" {
					t.Fatalf("mixed media order: %+v", r)
				}
			} else if r.Type != "video" || len(r.Media) != 1 {
				t.Fatalf("standalone video: %+v", r)
			}
			video := r.Media[index]
			if video.Kind != "video" || video.URL != "https://cdn.example/first.mp4" || video.ThumbnailURL != "https://cdn.example/poster.jpg" || video.Width != 640 {
				t.Fatalf("reference first playback not selected: %+v", video)
			}
			if !mixed && video.Duration != 3 {
				t.Fatalf("duration not truncated: %+v", video)
			}
		})
	}
}

func TestSocialMigrationWeiboTVRedirectAndURLOrder(t *testing.T) {
	calls := 0
	const oid = "1034:1234567890123456"
	client := &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "video.weibo.com" {
			resp := socialMigrationResponse(req, "")
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://weibo.com/tv/show/"+oid)
			return resp, nil
		}
		if req.Method != http.MethodPost || req.URL.Path != "/tv/api/component" || req.URL.Query().Get("page") != "/tv/show/"+oid {
			t.Fatalf("unexpected landing/API request: %s %s", req.Method, req.URL)
		}
		if req.Header.Get("Cookie") != "SUB=configured" {
			t.Fatal("configured cookie replaced")
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		var component map[string]map[string]string
		if err := json.Unmarshal([]byte(req.Form.Get("data")), &component); err != nil || component["Component_Play_Playinfo"]["oid"] != oid {
			t.Fatalf("wrong TV component: %s", req.Form.Get("data"))
		}
		return socialMigrationResponse(req, `{"data":{"Component_Play_Playinfo":{"text":"#电视#","cover_image":"//cdn.example/cover.jpg","duration_time":7.9,"urls":{"z":"//cdn.example/first.mp4","a":"//cdn.example/second.mp4"}}}}`), nil
	})}
	r, err := mainlandWeibo(context.Background(), client, "https://video.weibo.com/show?fid="+url.QueryEscape(oid), "SUB=configured")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(r.Media) != 1 || r.Media[0].URL != "https://cdn.example/first.mp4" || r.Media[0].ThumbnailURL != "https://cdn.example/cover.jpg" || r.Media[0].Duration != 7 {
		t.Fatalf("TV selection/order: calls=%d result=%+v", calls, r)
	}
}

func TestSocialMigrationWeiboReferenceFailures(t *testing.T) {
	for _, body := range []string{
		`{"ok":0,"msg":"需要登录"}`,
		`{"id":"1","text_raw":"正文","page_info":{"object_type":"unknown"}}`,
		`{"id":"1","text_raw":"正文","pic_infos":{"a":{"type":"unknown"}}}`,
		`{"id":"1","text_raw":"正文","pic_infos":{"a":{"type":"livephoto","thumbnail":{"url":"https://cdn.example/poster.jpg"}}}}`,
		`{"id":"1","text_raw":"正文","page_info":{"object_type":"video","page_pic":"https://cdn.example/poster.jpg","media_info":{"playback_list":[{"play_info":{}}]}}}`,
	} {
		r, err := mainlandWeibo(context.Background(), socialMigrationClient(t, body), "https://weibo.com/1/123", "")
		if err == nil || r != nil || referenceUnavailable(err) {
			t.Fatalf("bad payload accepted or misclassified: result=%+v err=%v", r, err)
		}
	}
	client := &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("invalid input reached HTTP: %s", req.URL)
		return nil, nil
	})}
	if _, err := mainlandWeibo(context.Background(), client, "https://weibo.com/u/not-an-id", ""); err == nil {
		t.Fatal("accepted invalid status ID")
	}
}

func TestSocialMigrationTiebaFirstPostSignatureAndPhotoPrecedence(t *testing.T) {
	client := &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/dc/common/tbs":
			if req.URL.Scheme != "http" {
				t.Fatalf("reference tbs endpoint changed: %s", req.URL)
			}
			return socialMigrationResponse(req, `{"tbs":"token"}`), nil
		case "/c/f/pb/page_pc":
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.Method != http.MethodPost || req.Form.Get("kz") != "123" || req.Form.Get("sign") != "afa6a4c8e4c7ec0eae0c8da43079ead7" {
				t.Fatalf("wrong signed request: %v", req.Form)
			}
			return socialMigrationResponse(req, `{"error_code":0,"thread":{"origin_thread_info":{"title":"首帖","content":[{"type":0,"text":"第一段"},{"type":1,"text":"不可出现"},{"type":0,"text":"第二段"}],"media":[{"big_pic":"https://cdn.example/first","small_pic":"https://cdn.example/thumb1.jpg","width":80,"height":60},{"big_pic":"https://cdn.example/second","small_pic":"https://cdn.example/thumb2.jpg","width":90,"height":70}]},"video_info":{"video_url":"https://cdn.example/ignored.mp4"}},"post_list":[{"content":[{"type":0,"text":"错误楼层"}]}]}`), nil
		case "/first", "/second":
			if req.Method != http.MethodHead || req.Header.Get("Cookie") != "" {
				t.Fatalf("unsafe/non-HEAD image inspection: %s %v", req.Method, req.Header)
			}
			resp := socialMigrationResponse(req, "")
			resp.Header.Set("Content-Type", "image/gif")
			if req.URL.Path == "/second" {
				resp.StatusCode = 403
			}
			return resp, nil
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
	})}
	r, err := mainlandTieba(context.Background(), client, "https://tieba.baidu.com/p/123?pn=9", "BDUSS=configured")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "首帖" || r.Content != "第一段\n第二段" || r.Type != "image" || len(r.Media) != 2 {
		t.Fatalf("first-post/photo precedence: %+v", r)
	}
	if r.Media[0].Kind != "gif" || r.Media[0].URL != "https://cdn.example/first" || r.Media[0].ThumbnailURL != "https://cdn.example/thumb1.jpg" || r.Media[0].Width != 80 {
		t.Fatalf("GIF inspection/metadata: %+v", r.Media[0])
	}
	if r.Media[1].Kind != "image" || r.Media[1].URL != "https://cdn.example/second" {
		t.Fatalf("failed HEAD must retain image: %+v", r.Media[1])
	}
}

func TestSocialMigrationTiebaVideoAndReferenceFailures(t *testing.T) {
	for _, test := range []struct {
		body string
		fail bool
	}{
		{`{"error_code":0,"thread":{"origin_thread_info":{"title":"标题","content":[{"type":0,"text":"首帖"}]},"video_info":{"video_url":"https://cdn.example/video.mp4","thumbnail_url":"https://cdn.example/cover.jpg","video_width":1920,"video_height":1080,"video_duration":8}}}`, false},
		{`{"error_code":0,"thread":{"origin_thread_info":{"title":"标题","content":[{"type":0,"text":"首帖"}]},"video_info":{"thumbnail_url":"https://cdn.example/cover.jpg"}}}`, true},
		{`{"error_code":4,"error_msg":"已删除"}`, true},
		{`{"error_code":0,"thread":{},"post_list":[{"content":[{"type":0,"text":"不可回退到回复"}]}]}`, true},
	} {
		client := &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
			body := test.body
			if req.URL.Path == "/dc/common/tbs" {
				body = `{"tbs":"token"}`
			}
			return socialMigrationResponse(req, body), nil
		})}
		r, err := mainlandTieba(context.Background(), client, "https://tieba.baidu.com/p/123", "")
		if test.fail {
			if err == nil || r != nil || referenceUnavailable(err) {
				t.Fatalf("reference error lost: result=%+v err=%v", r, err)
			}
		} else if err != nil || len(r.Media) != 1 || r.Media[0].Kind != "video" || r.Media[0].Duration != 8 || r.Media[0].ThumbnailURL != "https://cdn.example/cover.jpg" {
			t.Fatalf("video result=%+v err=%v", r, err)
		}
	}
}

func TestSocialMigrationPipixReferenceFieldsAndTypes(t *testing.T) {
	for _, video := range []bool{false, true} {
		t.Run(fmt.Sprint(video), func(t *testing.T) {
			item := `{"item_type":1,"content":"图文标题","cover":{"download_list":[{"url":"https://cdn.example/first.jpg"},{"url":"https://cdn.example/last.jpg"}]},"images":[{"download_list":[{"url":"https://cdn.example/ignored.jpg"}]}]}`
			if video {
				item = `{"item_type":2,"content":"视频标题","video":{"video_download":{"duration":3.1,"width":1280,"height":720,"url_list":[{"url":"https://cdn.example/first.mp4"},{"url":"https://cdn.example/ignored.mp4"}],"cover_image":{"download_list":[{"url":"https://cdn.example/cover.jpg"},{"url":"https://cdn.example/ignored-cover.jpg"}]}}}}`
			}
			body := `<script id="RENDER_DATA">` + url.PathEscape(`{"ppxItemDetail":{"item":`+item+`}}`) + `</script>`
			r, err := mainlandPipix(context.Background(), socialMigrationClient(t, body), "https://h5.pipix.com/ppx/item/123", "")
			if err != nil {
				t.Fatal(err)
			}
			if video {
				if r.Title != "视频标题" || len(r.Media) != 1 || r.Media[0].Kind != "video" || r.Media[0].URL != "https://cdn.example/first.mp4" || r.Media[0].Duration != 4 || r.Media[0].ThumbnailURL != "https://cdn.example/cover.jpg" || r.Media[0].Width != 1280 || len(r.Media[0].BackupURLs) != 0 {
					t.Fatalf("reference video fields: %+v", r)
				}
			} else if r.Title != "图文标题" || len(r.Media) != 2 || r.Media[0].URL != "https://cdn.example/first.jpg" || r.Media[1].URL != "https://cdn.example/last.jpg" {
				t.Fatalf("reference image selection/order: %+v", r)
			}
		})
	}
}

func TestSocialMigrationPipixRejectsMalformedReferenceData(t *testing.T) {
	for _, body := range []string{
		`<p>no render data</p>`,
		`<script id="RENDER_DATA">%ZZ</script>`,
		`<script id="RENDER_DATA">{"ppxItemDetail":{"item":{"item_type":3,"content":"正文","cover":{"download_list":[{"url":"https://cdn.example/image.jpg"}]}}}}</script>`,
		`<script id="RENDER_DATA">{"ppxItemDetail":{"item":{"item_type":"1","content":"正文"}}}</script>`,
		`<script id="RENDER_DATA">{"ppxItemDetail":{"item":{"item_type":1.5,"content":"正文"}}}</script>`,
		`<script id="RENDER_DATA">{"ppxItemDetail":{"item":{"item_type":2,"content":"正文","video":{"video_download":{"cover_image":{"download_list":[{"url":"https://cdn.example/poster.jpg"}]}}}}}}</script>`,
		`<script id="RENDER_DATA">{"ppxItemDetail":{"item":{"item_type":2,"content":"正文","video":{"video_download":{"url_list":[{"url":"https://cdn.example/video.mp4"}],"cover_image":{"download_list":[{"url":"https://cdn.example/poster.jpg"}]}}}}}}</script>`,
	} {
		r, err := mainlandPipix(context.Background(), socialMigrationClient(t, body), "https://h5.pipix.com/ppx/item/123", "")
		if err == nil || r != nil || referenceUnavailable(err) {
			t.Fatalf("bad RENDER_DATA accepted/misclassified: result=%+v err=%v", r, err)
		}
	}
}

func TestSocialMigrationZuiyouLastMemberAndImageOrder(t *testing.T) {
	body := `{"data":{"post":{"content":"正文","imgs":[{"id":1,"urls":{"large":{"w":4000,"h":4000,"urls":["https://cdn.example/wrong.jpg"]},"small":{"w":10,"h":10,"urls":["https://cdn.example/wrong-first.jpg","https://cdn.example/photo.jpg"]}}},{"id":2,"video":true,"urls":{"z":{"urls":["https://cdn.example/ignored-cover.jpg"]},"a":{"urls":["https://cdn.example/cover.jpg"]}}},{"id":3,"video":false,"urls":{"only":{"urls":["https://cdn.example/last.jpg"]}}}],"videos":{"1":{"url":"https://cdn.example/ignored1.mp4"},"2":{"url":"https://cdn.example/video.mp4"},"3":{"url":"https://cdn.example/ignored3.mp4"}}}}}`
	client := &http.Client{Transport: socialMigrationTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != "https://share.xiaochuankeji.cn/planck/share/post/detail_h5" {
			t.Fatalf("wrong endpoint: %s %s", req.Method, req.URL)
		}
		var request map[string]any
		if err := json.NewDecoder(req.Body).Decode(&request); err != nil || request["pid"] != float64(42) {
			t.Fatalf("pid normalization: %#v err=%v", request, err)
		}
		return socialMigrationResponse(req, body), nil
	})}
	r, err := mainlandZuiyou(context.Background(), client, "https://share.xiaochuankeji.cn/hybrid/share/post?pid=wrong&pid=00042&other=x", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "multimedia" || len(r.Media) != 3 || r.Media[0].Kind != "image" || r.Media[0].URL != "https://cdn.example/photo.jpg" || r.Media[0].ThumbnailURL != r.Media[0].URL || r.Media[1].Kind != "video" || r.Media[1].URL != "https://cdn.example/video.mp4" || r.Media[1].ThumbnailURL != "https://cdn.example/cover.jpg" || r.Media[2].Kind != "image" || r.Media[2].URL != "https://cdn.example/last.jpg" {
		t.Fatalf("ordered reference selection: %+v", r)
	}
}

func TestSocialMigrationZuiyouReferenceErrors(t *testing.T) {
	for _, body := range []string{
		`{"data":{}}`,
		`{"data":{"post":{"content":"正文","imgs":[{"id":1,"video":true,"urls":{"x":{"urls":["https://cdn.example/poster.jpg"]}}}]}}}`,
		`{"data":{"post":{"content":"正文","imgs":[{"id":1,"urls":{"first":{"urls":["https://cdn.example/valid-but-not-selected.jpg"]},"last":{"urls":[]}}}]}}}`,
	} {
		r, err := mainlandZuiyou(context.Background(), socialMigrationClient(t, body), "https://share.xiaochuankeji.cn/hybrid/share/post?pid=42", "")
		if err == nil || r != nil || referenceUnavailable(err) {
			t.Fatalf("reference error accepted/misclassified: result=%+v err=%v", r, err)
		}
	}
}
