package parser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func articleFixtureClient(t *testing.T, routes map[string]string) *http.Client {
	t.Helper()
	return &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
		key := req.URL.Host + req.URL.RequestURI()
		body, ok := routes[key]
		if !ok {
			return nil, fmt.Errorf("unexpected article request %s %s", req.Method, key)
		}
		if req.Method != http.MethodGet {
			t.Errorf("unexpected method %s", req.Method)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
}

func TestArticleWeixinReferenceContentBranches(t *testing.T) {
	cases := []struct {
		name, state, kind, title, content string
		media                             int
	}{
		{"article", `{item_show_type:'0' * 1,title:'文章',content_noencode:'<h2>标题</h2><p><a href="https://example.org/read">链接</a></p><img data-src="https://cdn.example/photo?wx_fmt=png">',}`, "article", "文章", "标题", 1},
		{"gallery", `{item_show_type:8,title:'图集',content_noencode:'说明<br>第二行',picture_page_info_list:[{watermark_info:{cdn_url:'https://cdn.example/original'},cdn_url:'https://cdn.example/watermarked'},{cdn_url:'https://cdn.example/fallback'}]}`, "image", "图集", "说明\n第二行", 2},
		{"text", `{item_show_type:10,title:'不应使用',text_page_info:{content_noencode:'&lt;b&gt;文字&lt;/b&gt;<br>下一行'}}`, "image", "", "文字\n下一行", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := articleFixtureClient(t, map[string]string{"mp.weixin.qq.com/s?__biz=abc&mid=1&share=keep": "<script>window.cgiDataNew=" + tc.state + ";</script><div id=js_content>不应回退</div>"})
			result, err := mainlandWeixin(context.Background(), client, "https://mp.weixin.qq.com/s?__biz=abc&mid=1&share=keep", "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Type != tc.kind || result.Title != tc.title || !strings.Contains(result.Content, tc.content) || len(result.Media) != tc.media {
				t.Fatalf("reference branch mismatch: %+v", result)
			}
			if strings.Contains(result.SourceURL, "share=") || !strings.Contains(result.SourceURL, "__biz=abc") {
				t.Fatalf("bad canonical URL: %s", result.SourceURL)
			}
			if tc.name == "article" && (!strings.Contains(result.MarkdownContent, "https://example.org/read") || !strings.Contains(result.MarkdownContent, "https://cdn.example/photo?wx_fmt=png")) {
				t.Fatalf("lost rich content: %s", result.MarkdownContent)
			}
			if tc.name == "gallery" && result.Media[0].URL != "https://cdn.example/original" {
				t.Fatal("lost original photo precedence")
			}
		})
	}
	for _, state := range []string{`{item_show_type:99,content_noencode:'正文'}`, `{content_noencode:'正文'}`, `{item_show_type:0,content_noencode:unsupported()}`} {
		client := articleFixtureClient(t, map[string]string{"mp.weixin.qq.com/s/bad": "<script>window.cgiDataNew=" + state + ";</script><div id=js_content>不得掩盖错误</div>"})
		if result, err := mainlandWeixin(context.Background(), client, "https://mp.weixin.qq.com/s/bad", ""); err == nil || result != nil {
			t.Fatalf("accepted invalid state %s: %+v %v", state, result, err)
		}
	}
}

func TestArticleCoolapkTitlePrecedenceAndMediaFiltering(t *testing.T) {
	body := `<div class="message-title">标题</div><div class="feed-article-message"><p>之前 <a href="https://example.org/link">链接</a></p><img class="message-image" src="//cdn.example/a.gif"><img src="//cdn.example/decorative.jpg"><p>之后</p></div><div class="feed-message">错误分支</div><div class="message-image-group"><img src="//cdn.example/other.jpg"></div>`
	client := articleFixtureClient(t, map[string]string{"www.coolapk.com/feed/123?shareKey=required&s=required": body})
	result, err := mainlandCoolapk(context.Background(), client, "https://www.coolapk.com/feed/123?shareKey=required&s=required", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "article" || len(result.Media) != 1 || result.Media[0].Kind != "gif" || strings.Contains(result.Content, "错误分支") || !strings.Contains(result.MarkdownContent, "https://example.org/link") || strings.Contains(result.SourceURL, "?") {
		t.Fatalf("bad article branch: %+v", result)
	}
	client = articleFixtureClient(t, map[string]string{"www.coolapk.com/picture/1?s=key": `<div class="feed-message">正文 #话题# 末尾</div><div class="message-image-group"><img src="//cdn.example/a.gif"><img src="//cdn.example/b.jpg"></div><aside><img src="//cdn.example/avatar.jpg"></aside>`})
	result, err = mainlandCoolapk(context.Background(), client, "https://www.coolapk.com/picture/1?s=key", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != "multimedia" || result.Title != "" || len(result.Media) != 2 || strings.Contains(result.Content, "#话题#") {
		t.Fatalf("bad image branch: %+v", result)
	}
	client = articleFixtureClient(t, map[string]string{"www.coolapk.com/feed/2": `<div class="message-title">受限文章</div><div class="feed-message">不能回退</div>`})
	if result, err = mainlandCoolapk(context.Background(), client, "https://www.coolapk.com/feed/2", ""); err == nil || result != nil || !strings.Contains(err.Error(), "shareKey") {
		t.Fatalf("masked missing article: %+v %v", result, err)
	}
}

func TestArticleDoubanRedirectBeforeDispatch(t *testing.T) {
	for _, start := range []string{"https://douc.cc/short?share=keep", "https://www.douban.com/doubanapp/dispatch?uri=topic%2F123"} {
		t.Run(start, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				body, status, header := "", 200, http.Header{}
				switch req.URL.String() {
				case start:
					if req.Header.Get("Cookie") != "" {
						t.Error("cookie sent to redirect endpoint")
					}
					status, header = 302, http.Header{"Location": []string{"https://www.douban.com/group/topic/123/?share=tracking"}}
				case "https://www.douban.com/group/topic/123/?share=tracking":
					body = "redirect landing page"
				case "https://m.douban.com/rexxar/api/v2/group/topic/123":
					if req.Header.Get("Cookie") != "dbcl2=session" || req.Header.Get("User-Agent") != mainlandMobileUA {
						t.Error("missing topic credentials/mobile UA")
					}
					body = `{"title":"话题","content":"<p>正文</p>","photos":[{"image":{"large":{"url":"https://cdn.example/a.jpg"}}}]}`
				default:
					return nil, fmt.Errorf("unexpected redirect request %s", req.URL)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			result, err := mainlandDouban(context.Background(), client, start, "dbcl2=session")
			if err != nil {
				t.Fatal(err)
			}
			if calls != 3 || result.Type != "image" || result.SourceURL != "https://www.douban.com/group/topic/123/" {
				t.Fatalf("dispatch failed: calls=%d result=%+v", calls, result)
			}
		})
	}
	client := articleFixtureClient(t, nil)
	if _, err := mainlandDouban(context.Background(), client, "https://www.douban.com/gallery/topic/123", ""); err == nil {
		t.Fatal("gallery topic incorrectly routed to group topic API")
	}
}

func TestArticleDoubanGalleryAnimationAndPositionedRichText(t *testing.T) {
	for _, layout := range []string{"horizontal", "vertical"} {
		t.Run(layout, func(t *testing.T) {
			payload := `{"title":"话题","image_layout":` + fmt.Sprintf("%q", layout) + `,"content":"<p>前</p><img src=\"https://cdn.example/a.jpg\"><p>后 <a href=\"https://example.org/read\">链接</a></p>","video_info":{"video_url":"https://cdn.example/video.mp4","cover_url":"https://cdn.example/cover.jpg","duration":"01:02","video_width":1280},"photos":[{"image":{"is_animated":true,"large":{"url":"https://cdn.example/huge.gif"},"normal":{"url":"https://cdn.example/thumb.jpg"},"video":{"url":"https://cdn.example/animation.mp4","width":640}}},{"image":{"large":{},"normal":{"url":"https://cdn.example/a.jpg","width":800}}},{"image":{}},{"image":{"large":{"width":200},"normal":{"url":"https://cdn.example/not-fallback.jpg"}}}]} `
			client := articleFixtureClient(t, map[string]string{"m.douban.com/rexxar/api/v2/group/topic/123": payload})
			result, err := mainlandDouban(context.Background(), client, "https://www.douban.com/topic/123", "")
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if layout == "vertical" {
				want = 3
			}
			if len(result.Media) != want {
				t.Fatalf("bad media filtering: %+v", result.Media)
			}
			animation := result.Media[len(result.Media)-2]
			if animation.Kind != "gif" || animation.URL != "https://cdn.example/animation.mp4" || animation.ThumbnailURL != "https://cdn.example/thumb.jpg" || animation.Width != 640 {
				t.Fatalf("lost animation semantics: %+v", animation)
			}
			for _, media := range result.Media {
				if media.Headers["Referer"] != "https://www.douban.com/" {
					t.Fatal("missing image anti-hotlink referer")
				}
			}
			if layout == "horizontal" && (result.Type != "image" || result.MarkdownContent != "") {
				t.Fatalf("gallery became article: %+v", result)
			}
			if layout == "vertical" && (result.Type != "article" || result.Media[0].Duration != 62 || !strings.Contains(result.MarkdownContent, "https://example.org/read") || strings.Index(result.MarkdownContent, "前") >= strings.Index(result.MarkdownContent, "https://cdn.example/a.jpg") || strings.Index(result.MarkdownContent, "后") <= strings.Index(result.MarkdownContent, "https://cdn.example/a.jpg")) {
				t.Fatalf("lost positioned content: %+v", result)
			}
		})
	}
}

func TestArticleDoubanLocalizedErrors(t *testing.T) {
	for _, body := range []string{`{"localized_message":"仅成员可见"}`, `{"msg":"need_permission"}`, `<html>denied</html>`} {
		client := &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		})}
		result, err := mainlandDouban(context.Background(), client, "https://www.douban.com/group/topic/123", "")
		if err == nil || result != nil || (strings.Contains(body, "仅成员可见") && !strings.Contains(err.Error(), "仅成员可见")) || (strings.Contains(body, "need_permission") && !strings.Contains(err.Error(), "need_permission")) {
			t.Fatalf("bad error: %+v %v", result, err)
		}
	}
}

func TestArticleBangumiBBCodeRichSemantics(t *testing.T) {
	source := "开始[url=https://example.org/a?x=1&y=2][b]链接[/b][/url]\n[quote]引用\n[i]强调[/i][/quote]\n[code]if a < b:\n[img]https://cdn.example/not-media.jpg[/img]\n```[/code]\n[img]https://cdn.example/one.jpg[/img]中间[photo]/2026/two.jpg[/photo]结尾[s]删除[/s][user]alice[/user][center]居中[/center]"
	rendered, images := articleBBCodeHTML(source)
	plain, markdown := referenceHTML(rendered, "https://bgm.tv/blog/1")
	if len(images) != 2 || images[0] != "https://cdn.example/one.jpg" || images[1] != "https://lain.bgm.tv/pic/photo/l/2026/two.jpg" {
		t.Fatalf("bad BBCode media: %+v", images)
	}
	for _, fragment := range []string{"https://example.org/a?x=1&y=2", "**链接**", "*强调*", "~~删除~~", "[img]https://cdn.example/not-media.jpg[/img]", "if a < b:", "@alice", "居中"} {
		if !strings.Contains(markdown, fragment) {
			t.Errorf("missing %q from markdown %s", fragment, markdown)
		}
	}
	if !strings.Contains(markdown, "> ") || !strings.Contains(markdown, "```") || !strings.Contains(plain, "if a < b:") {
		t.Fatalf("lost quote/code: plain=%q markdown=%s", plain, markdown)
	}
	if strings.Index(markdown, images[0]) >= strings.Index(markdown, "中间") || strings.Index(markdown, images[1]) <= strings.Index(markdown, "中间") {
		t.Fatalf("lost positioned images: %s", markdown)
	}
}

func TestArticleBangumiBranchesAndBlogPhotos(t *testing.T) {
	for _, path := range []string{"subject/topic", "group/topic", "rakuen/topic/subject", "rakuen/topic/group"} {
		apiType := "groups"
		if strings.Contains(path, "subject") {
			apiType = "subjects"
		}
		client := articleFixtureClient(t, map[string]string{"next.bgm.tv/p1/" + apiType + "/-/topics/123": `{"title":"主题","content":"不应作为无回复正文","replies":[{"content":"[b]首帖[/b]"},{"content":"不是首帖"}]}`})
		result, err := mainlandBangumi(context.Background(), client, "https://chii.in/"+path+"/123", "")
		if err != nil || result == nil || result.Type != "article" || result.Content != "首帖" {
			t.Fatalf("bad %s branch: %+v %v", path, result, err)
		}
	}
	client := articleFixtureClient(t, map[string]string{
		"next.bgm.tv/p1/blogs/123":                  `{"title":"日志","content":"前[photo]one.jpg[/photo]后"}`,
		"next.bgm.tv/p1/blogs/123/photos?limit=100": `{"data":[{"target":"one.jpg"},{"target":"/two.jpg"},{"target":"two.jpg"}]}`,
	})
	result, err := mainlandBangumi(context.Background(), client, "https://bangumi.tv/blog/123?tracking=1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Media) != 2 || strings.Count(result.MarkdownContent, "two.jpg") != 1 || strings.Index(result.MarkdownContent, "后") >= strings.Index(result.MarkdownContent, "two.jpg") {
		t.Fatalf("bad blog photos: %+v", result)
	}
	client = articleFixtureClient(t, map[string]string{"next.bgm.tv/p1/groups/-/topics/123": `{"title":"主题","content":"不得伪造首帖","replies":[]}`})
	if result, err = mainlandBangumi(context.Background(), client, "https://bgm.tv/group/topic/123", ""); err == nil || result != nil {
		t.Fatalf("used unrelated top-level content: %+v %v", result, err)
	}
}

func articleTestToken(query string) string {
	data, _ := json.Marshal(map[string]string{"GetPlayInfoToken": query})
	return base64.RawURLEncoding.EncodeToString(data)
}

func TestArticleToutiaoBranchPrecedenceAndVOD(t *testing.T) {
	top, embedded := articleTestToken("Action=Top"), articleTestToken("Action=Embedded")
	for _, article := range []bool{false, true} {
		t.Run(fmt.Sprint(article), func(t *testing.T) {
			biz := "视频"
			query := "Top"
			if article {
				biz, query = "图文", "Embedded"
			}
			payload := fmt.Sprintf(`{"data":{"title":"标题","biz_tag":%q,"play_auth_token_v2":%q,"video_duration":99,"poster_url":"https://cdn.example/poster.jpg","content":%q}}`, biz, top, `<p>正文</p><img src="https://cdn.example/photo.jpg"><div class="tt-video-box" data-token="`+embedded+`"></div>`)
			client := articleFixtureClient(t, map[string]string{
				"m.toutiao.com/i123/info/v2/":           payload,
				"vod.bytedanceapi.com/?Action=" + query: `{"Result":{"Data":{"PlayInfoList":[{"MainPlayUrl":"https://cdn.example/small.mp4","Height":720,"Size":999999999999999},{"MainPlayUrl":"https://cdn.example/hd-small.mp4","Height":1080,"Size":100},{"MainPlayUrl":"https://cdn.example/selected.mp4","BackupPlayUrl":"https://cdn.example/backup.mp4","Height":1080,"Width":1920,"Size":200,"Duration":7}]}}}`,
			})
			result, err := mainlandToutiao(context.Background(), client, "https://www.toutiao.com/video/123?share=1", "do-not-forward=secret")
			if err != nil {
				t.Fatal(err)
			}
			video := result.Media[len(result.Media)-1]
			if video.URL != "https://cdn.example/selected.mp4" || video.Width != 1920 || video.Height != 1080 || len(video.BackupURLs) != 1 {
				t.Fatalf("wrong VOD selection: %+v", video)
			}
			if article && (result.Type != "article" || len(result.Media) != 2 || video.Duration != 7 || video.ThumbnailURL != "" || !strings.Contains(result.MarkdownContent, "正文") || strings.Contains(result.MarkdownContent, "tt-video-box")) {
				t.Fatalf("wrong article/token precedence: %+v", result)
			}
			if !article && (result.Type != "video" || len(result.Media) != 1 || video.Duration != 99 || video.ThumbnailURL != "https://cdn.example/poster.jpg") {
				t.Fatalf("wrong video branch: %+v", result)
			}
		})
	}
}

func TestArticleToutiaoShortlinkMicroAndErrors(t *testing.T) {
	client := &http.Client{Transport: mainlandFixtureTransport(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Cookie") != "" {
			t.Error("cookie leaked to Toutiao")
		}
		status, header, body := 200, http.Header{}, ""
		switch req.URL.String() {
		case "https://m.toutiao.com/is/short/":
			status, header = 302, http.Header{"Location": []string{"https://www.toutiao.com/w/123/?share=1"}}
		case "https://www.toutiao.com/w/123/?share=1":
			body = "landing"
		case "https://m.toutiao.com/i123/info/v2/":
			body = `{"data":{"title":"不应使用","biz_tag":"图文","play_auth_token_v2":"invalid","thread":{"thread_base":{"content":" 微头条 ","large_image_list":[{"url":"https://cdn.example/p.jpg","width":640},{}]}}}}`
		default:
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	result, err := mainlandToutiao(context.Background(), client, "https://m.toutiao.com/is/short/", "secret=value")
	if err != nil || result == nil || result.Type != "image" || result.Title != "" || result.Content != "微头条" || len(result.Media) != 1 || result.Media[0].Width != 640 || strings.Contains(result.SourceURL, "?") {
		t.Fatalf("bad micro/shortlink: %+v %v", result, err)
	}
	client = articleFixtureClient(t, map[string]string{"m.toutiao.com/i123/info/v2/": `{"data":null}`})
	if _, err := mainlandToutiao(context.Background(), client, "https://www.toutiao.com/item/123", ""); !referenceUnavailable(err) {
		t.Fatalf("missing typed unavailable: %v", err)
	}
	for _, token := range []string{"", "%%%", base64.RawURLEncoding.EncodeToString([]byte(`{}`))} {
		if _, err := mainlandToutiaoVideo(context.Background(), articleFixtureClient(t, nil), token, nil); err == nil {
			t.Fatalf("accepted malformed token %q", token)
		}
	}
	for _, vod := range []string{`{"ResponseMetadata":{"Error":{"ErrorMessage":"授权已失效"}}}`, `{"Result":{"Data":{"PlayInfoList":[]}}}`, `{"Result":{"Data":{"PlayInfoList":[{"MainPlayUrl":"javascript:bad","Height":1080}]}}}`} {
		client = articleFixtureClient(t, map[string]string{"vod.bytedanceapi.com/?Action=Bad": vod})
		if _, err := mainlandToutiaoVideo(context.Background(), client, articleTestToken("Action=Bad"), nil); err == nil {
			t.Fatalf("accepted malformed/error VOD %s", vod)
		}
	}
}
