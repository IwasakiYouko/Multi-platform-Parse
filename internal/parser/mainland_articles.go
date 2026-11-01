package parser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/atom"
	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() {
	Register("weixin", mainlandWeixin)
	Register("coolapk", mainlandCoolapk)
	Register("douban", mainlandDouban)
	Register("bangumi", mainlandBangumi)
	Register("toutiao", mainlandToutiao)
}

func mainlandWeixin(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithoutCookies(ctx)
	b, final, err := Get(ctx, c, raw, Headers("", raw))
	if err != nil {
		return nil, err
	}
	page, err := mainlandDoc(string(b))
	if err != nil {
		return nil, err
	}
	r := mainlandResult("weixin", articleWeixinCanonical(final))
	r.Type = "article"
	r.Title = strings.TrimSpace(page.Find("#activity-name").Text())
	r.Author = strings.TrimSpace(page.Find("#js_name").Text())
	source := ""
	state, stateErr := mainlandLiteralState(b, "cgiDataNew")
	if stateErr != nil && strings.Contains(string(b), "cgiDataNew") {
		return nil, stateErr
	}
	if stateErr == nil {
		r.Title = Text(Path(state, "title"))
		source = Text(Path(state, "content_noencode"))
		kind, kindErr := strconv.Atoi(Text(Path(state, "item_show_type")))
		if kindErr != nil {
			return nil, fmt.Errorf("不支持的微信公众号内容类型: %v", Path(state, "item_show_type"))
		}
		switch kind {
		case 0:
		case 8:
			r.Content = mainlandText(source)
			r.Type = "image"
			for _, p := range Array(Path(state, "picture_page_info_list")) {
				mainlandAdd(r, "image", mainlandFirst(Path(p, "watermark_info", "cdn_url"), Path(p, "cdn_url")), Obj(p))
			}
			return articleFinish(r, false)
		case 10:
			r.Title = ""
			r.Content = mainlandText(Text(Path(state, "text_page_info", "content_noencode")))
			r.Type = "image"
			return articleFinish(r, false)
		default:
			return nil, fmt.Errorf("不支持的微信公众号内容类型: %d", kind)
		}
	}
	if source == "" && stateErr != nil {
		source, _ = page.Find("#js_content").First().Html()
	}
	body, err := mainlandDoc(source)
	if err != nil {
		return nil, err
	}
	expected := 0
	var videoErr error
	body.Find("iframe[data-vid],iframe[src*='vid='],mpvideo,video").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		expected++
		if goquery.NodeName(s) == "video" && len(mainlandMediaSources(s, final)) > 0 {
			return true
		}
		vid, _ := s.Attr("data-vid")
		if vid == "" {
			vid, _ = s.Attr("vid")
		}
		if vid == "" {
			src, _ := s.Attr("src")
			if u, e := url.Parse(src); e == nil {
				vid = u.Query().Get("vid")
			}
		}
		if vid == "" {
			videoErr = fmt.Errorf("微信公众号第 %d 个内嵌视频缺少播放地址和 vid", expected)
			return false
		}
		var playback any
		endpoint := "https://mp.weixin.qq.com/mp/videoplayer?vid=" + url.QueryEscape(vid) + "&action=get_mp_video_play_url&preview=0"
		if e := JSON(ctx, c, endpoint, Headers("", final), &playback); e != nil {
			videoErr = fmt.Errorf("微信公众号内嵌视频 %s 读取失败: %w", vid, e)
			return false
		}
		best := ""
		size := float64(-1)
		for _, entry := range Array(Path(playback, "url_info")) {
			u := mainlandURL(Text(Path(entry, "url")), final)
			if u != "" && Number(Path(entry, "filesize")) > size {
				best = u
				size = Number(Path(entry, "filesize"))
			}
		}
		if best == "" {
			videoErr = fmt.Errorf("微信公众号内嵌视频 %s 未返回可播放地址，可能已删除或需要登录", vid)
			return false
		}
		node := s.Get(0)
		node.Data = "video"
		node.DataAtom = atom.Video
		s.Empty()
		s.SetAttr("src", best)
		return true
	})
	if videoErr != nil {
		return nil, videoErr
	}
	resolved, err := body.Html()
	if err != nil {
		return nil, err
	}
	if err = mainlandBody(r, resolved); err != nil {
		return nil, err
	}
	referenceSetHTML(r, resolved)
	return articleFinish(r, expected > 0)
}

func mainlandCoolapk(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithoutCookies(ctx)
	b, final, err := Get(ctx, c, raw, Headers("", "https://www.coolapk.com/"))
	if err != nil {
		return nil, err
	}
	d, err := mainlandDoc(string(b))
	if err != nil {
		return nil, err
	}
	r := mainlandResult("coolapk", articleCanonical(final))
	r.Title = strings.TrimSpace(d.Find(".message-title").First().Text())
	r.Author = strings.TrimSpace(d.Find(".user-name,.feed-user-name").First().Text())
	if r.Title != "" {
		body := d.Find(".feed-article-message").First()
		if body.Length() == 0 {
			u, _ := url.Parse(raw)
			if u != nil && (u.Query().Has("shareKey") || u.Query().Has("s")) {
				return nil, fmt.Errorf("酷安获取内容失败")
			}
			return nil, fmt.Errorf("酷安获取内容失败, 分享时请保留 shareKey 或 s 参数")
		}
		source, _ := body.Html()
		referenceSetHTML(r, source)
		r.Type = "article"
		body.Find("img.message-image").Each(func(_ int, image *goquery.Selection) {
			src, _ := image.Attr("src")
			articleAddCoolapkImage(r, src)
		})
	} else {
		body := d.Find(".feed-message").First()
		if r.Content = strings.TrimSpace(body.Text()); r.Content == "" {
			return nil, fmt.Errorf("酷安获取内容失败, 分享时请保留 shareKey 或 s 参数")
		}
		r.Content = regexp.MustCompile(` ?#[^#]+# ?`).ReplaceAllStringFunc(r.Content, func(tag string) string {
			return " " + strings.TrimSuffix(strings.TrimSpace(tag), "#") + " "
		})
		r.Type = "image"
		d.Find(".message-image-group").First().Find("img").Each(func(_ int, image *goquery.Selection) {
			src, _ := image.Attr("src")
			articleAddCoolapkImage(r, src)
		})
		for _, media := range r.Media {
			if media.Kind == "gif" {
				r.Type = "multimedia"
				break
			}
		}
	}
	return articleFinish(r, false)
}

func mainlandDouban(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Hostname() == "douc.cc" || strings.HasPrefix(u.Path, "/doubanapp/dispatch") {
		_, raw, err = Get(netx.WithoutCookies(ctx), c, raw, Headers("", "https://m.douban.com/"))
		if err != nil {
			return nil, err
		}
		u, err = url.Parse(raw)
		if err != nil {
			return nil, err
		}
	}
	if u.Hostname() != "douban.com" && u.Hostname() != "www.douban.com" && u.Hostname() != "m.douban.com" {
		return nil, fmt.Errorf("豆瓣分享链接未跳转到受支持的豆瓣页面")
	}
	r := mainlandResult("douban", articleCanonical(raw))
	h := Headers(cookie, "https://m.douban.com/")
	h["User-Agent"] = mainlandMobileUA
	match := regexp.MustCompile(`^/(?:group/)?topic/(\d+)(?:/|$)`).FindStringSubmatch(u.Path)
	if len(match) > 0 {
		var v any
		endpoint := "https://m.douban.com/rexxar/api/v2/group/topic/" + match[1]
		if err := articleDoubanJSON(ctx, c, endpoint, h, &v); err != nil {
			return nil, err
		}
		if _, ok := v.(map[string]any); !ok {
			return nil, fmt.Errorf("豆瓣话题接口未返回有效对象")
		}
		if message := mainlandFirst(Path(v, "localized_message"), Path(v, "msg")); message != "" && Path(v, "content") == nil {
			return nil, fmt.Errorf("豆瓣解析失败: %s", message)
		}
		r.Title = Text(Path(v, "title"))
		r.Author = Text(Path(v, "author", "name"))
		referenceSetHTML(r, Text(Path(v, "content")))
		r.Type = "article"
		gallery := mainlandFirst(Path(v, "image_layout"), "horizontal") == "horizontal"
		if gallery {
			r.Type = "image"
			r.MarkdownContent = ""
		} else if video := Obj(Path(v, "video_info")); Text(video["video_url"]) != "" {
			mainlandAdd(r, "video", Text(video["video_url"]), map[string]any{
				"thumbnail": video["cover_url"], "duration": articleDoubanDuration(video["duration"]),
				"width": video["video_width"], "height": video["video_height"],
			})
		}
		for _, photo := range Array(Path(v, "photos")) {
			image := Obj(Path(photo, "image"))
			normal := Obj(image["normal"])
			video := Obj(image["video"])
			if Text(image["is_animated"]) == "true" && Text(video["url"]) != "" {
				mainlandAdd(r, "gif", Text(video["url"]), map[string]any{
					"width": video["width"], "height": video["height"], "thumbnail": normal["url"],
				})
				continue
			}
			large := Obj(image["large"])
			if len(large) == 0 {
				large = normal
			}
			mainlandAdd(r, "image", Text(large["url"]), map[string]any{
				"width": large["width"], "height": large["height"], "thumbnail": normal["url"],
			})
		}
		articleDoubanHeaders(r)
		return articleFinish(r, false)
	}
	// Preserve the existing note/review extension; upstream only guarantees group topics.
	if !regexp.MustCompile(`^/(?:note|review)/\d+(?:/|$)`).MatchString(u.Path) {
		return nil, fmt.Errorf("豆瓣支持小组话题、日记和评论链接")
	}
	b, final, err := Get(ctx, c, raw, h)
	if err != nil {
		return nil, err
	}
	r.SourceURL = articleCanonical(final)
	d, err := mainlandDoc(string(b))
	if err != nil {
		return nil, err
	}
	body := d.Find("#link-report .note,.note-content,#link-report .review-content,.review-content").First()
	if body.Length() == 0 {
		body = d.Find("#link-report").First()
	}
	if body.Length() == 0 {
		return nil, fmt.Errorf("豆瓣正文不可访问；日记可能已删除、仅好友可见或需要登录")
	}
	r.Title = strings.TrimSpace(d.Find("h1").First().Text())
	r.Author = strings.TrimSpace(d.Find(".note-author,.main-hd .name").First().Text())
	source, _ := body.Html()
	if err := mainlandBody(r, source); err != nil {
		return nil, err
	}
	referenceSetHTML(r, source)
	r.Type = "article"
	articleDoubanHeaders(r)
	return articleFinish(r, body.Find("video").Length() > 0)
}

func mainlandBangumi(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithoutCookies(ctx)
	match := regexp.MustCompile(`/(?:(subject|group)/topic|rakuen/topic/(subject|group)|(blog))/(\d+)(?:[/?#]|$)`).FindStringSubmatch(raw)
	if len(match) == 0 {
		return nil, fmt.Errorf("Bangumi 支持讨论主题和日志链接")
	}
	kind, id := mainlandFirst(match[1], match[2], match[3]), match[4]
	endpoint := "https://next.bgm.tv/p1/"
	switch kind {
	case "blog":
		endpoint += "blogs/" + id
	case "subject":
		endpoint += "subjects/-/topics/" + id
	default:
		endpoint += "groups/-/topics/" + id
	}
	var v any
	if err := JSON(ctx, c, endpoint, Headers("", "https://bgm.tv/"), &v); err != nil {
		return nil, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("Bangumi 接口未返回有效对象")
	}
	r := mainlandResult("bangumi", articleCanonical(raw))
	r.Title = Text(Path(v, "title"))
	source := Text(Path(v, "content"))
	if kind != "blog" {
		source = ""
		if replies := Array(Path(v, "replies")); len(replies) > 0 {
			source = Text(Path(replies[0], "content"))
			r.Author = Text(Path(replies[0], "user", "nickname"))
		}
	}
	rendered, images := articleBBCodeHTML(source)
	if kind == "blog" {
		var photos any
		if err := JSON(ctx, c, endpoint+"/photos?limit=100", Headers("", "https://bgm.tv/"), &photos); err != nil {
			return nil, fmt.Errorf("Bangumi 日志照片读取失败: %w", err)
		}
		for _, photo := range Array(Path(photos, "data")) {
			if target := Text(Path(photo, "target")); target != "" {
				image := "https://lain.bgm.tv/pic/photo/l/" + strings.TrimLeft(target, "/")
				if !articleContains(images, image) {
					images = append(images, image)
					rendered += "<p>" + articleImageHTML(image) + "</p>"
				}
			}
		}
	}
	for _, image := range images {
		mainlandAdd(r, "image", image, nil)
	}
	referenceSetHTML(r, rendered)
	r.Type = "article"
	return articleFinish(r, false)
}

func mainlandToutiao(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithoutCookies(ctx)
	re := regexp.MustCompile(`toutiao\.com/(?:(?:article|video|w|group|item)/|a|i)(\d+)`)
	match := re.FindStringSubmatch(raw)
	if len(match) == 0 {
		_, final, err := Get(ctx, c, raw, Headers("", raw))
		if err != nil {
			return nil, err
		}
		raw = final
		match = re.FindStringSubmatch(raw)
	}
	if len(match) == 0 {
		return nil, fmt.Errorf("头条链接缺少内容 ID")
	}
	var v any
	if err := JSON(ctx, c, "https://m.toutiao.com/i"+match[1]+"/info/v2/", Headers("", "https://m.toutiao.com/"), &v); err != nil {
		return nil, err
	}
	data := Obj(Path(v, "data"))
	if Path(v, "data") == nil {
		return nil, ContentUnavailableError{Reason: "今日头条内容不存在或已删除"}
	}
	r := mainlandResult("toutiao", articleCanonical(raw))
	r.Title = Text(data["title"])
	r.Author = mainlandFirst(data["source"], data["media_name"])
	if _, exists := data["thread"]; exists {
		thread := Path(data, "thread", "thread_base")
		r.Title = ""
		r.Content = strings.TrimSpace(Text(Path(thread, "content")))
		r.Type = "image"
		for _, image := range Array(Path(thread, "large_image_list")) {
			mainlandAdd(r, "image", Text(Path(image, "url")), Obj(image))
		}
		return articleFinish(r, false)
	}
	if token := Text(data["play_auth_token_v2"]); Text(data["biz_tag"]) != "图文" && token != "" {
		video, err := mainlandToutiaoVideo(ctx, c, token, Headers("", r.SourceURL))
		if err != nil {
			return nil, err
		}
		if duration := Number(data["video_duration"]); duration != 0 {
			video.Duration = duration
		}
		video.ThumbnailURL = mainlandURL(Text(data["poster_url"]), raw)
		video.Headers = Headers("", r.SourceURL)
		r.Media = append(r.Media, video)
		r.Type = "video"
		return articleFinish(r, true)
	}
	d, err := mainlandDoc(Text(data["content"]))
	if err != nil {
		return nil, err
	}
	var embeddedErr error
	d.Find("div.tt-video-box").EachWithBreak(func(_ int, box *goquery.Selection) bool {
		token, _ := box.Attr("data-token")
		video, err := mainlandToutiaoVideo(ctx, c, token, Headers("", r.SourceURL))
		if err != nil {
			embeddedErr = err
			return false
		}
		video.Headers = Headers("", r.SourceURL)
		r.Media = append(r.Media, video)
		box.Remove()
		return true
	})
	if embeddedErr != nil {
		return nil, embeddedErr
	}
	d.Find("img").Each(func(_ int, image *goquery.Selection) {
		src, _ := image.Attr("src")
		mainlandAdd(r, "image", src, nil)
	})
	// Reference media order is images first, followed by resolved embedded videos.
	var images, videos []model.Media
	for _, media := range r.Media {
		if media.Kind == "image" {
			images = append(images, media)
		} else {
			videos = append(videos, media)
		}
	}
	r.Media = append(images, videos...)
	source, _ := d.Html()
	referenceSetHTML(r, source)
	r.Type = "article"
	return articleFinish(r, false)
}

func mainlandToutiaoVideo(ctx context.Context, c *http.Client, token string, h map[string]string) (model.Media, error) {
	out := model.Media{Kind: "video"}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
	if err != nil {
		return out, fmt.Errorf("头条播放 token 编码错误")
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return out, fmt.Errorf("头条播放 token 不是有效 JSON: %w", err)
	}
	query := Text(Path(decoded, "GetPlayInfoToken"))
	if query == "" {
		return out, fmt.Errorf("头条播放 token 缺少 GetPlayInfoToken")
	}
	if _, err := url.ParseQuery(query); err != nil {
		return out, fmt.Errorf("头条播放 token 查询参数无效: %w", err)
	}
	var payload any
	if err := JSON(ctx, c, "https://vod.bytedanceapi.com/?"+query, h, &payload); err != nil {
		return out, err
	}
	if failure := Path(payload, "ResponseMetadata", "Error"); failure != nil {
		return out, fmt.Errorf("头条点播接口: %s", mainlandFirst(Path(failure, "Message"), Path(failure, "message"), Path(failure, "ErrorMessage"), failure))
	}
	var best any
	for _, info := range Array(Path(payload, "Result", "Data", "PlayInfoList")) {
		if best == nil || Number(Path(info, "Height")) > Number(Path(best, "Height")) ||
			(Number(Path(info, "Height")) == Number(Path(best, "Height")) && Number(Path(info, "Size")) > Number(Path(best, "Size"))) {
			best = info
		}
	}
	out.URL = mainlandURL(Text(Path(best, "MainPlayUrl")), "")
	if out.URL == "" {
		return out, fmt.Errorf("头条点播接口未返回可播放的视频")
	}
	out.Width = int(Number(Path(best, "Width")))
	out.Height = int(Number(Path(best, "Height")))
	out.Duration = Number(Path(best, "Duration"))
	if backup := mainlandURL(Text(Path(best, "BackupPlayUrl")), out.URL); backup != "" {
		out.BackupURLs = []string{backup}
	}
	return out, nil
}

// Numeric string coercion appears in Weixin's otherwise JSON5-like literals.
func mainlandLiteralState(page []byte, marker string) (any, error) {
	s := string(page)
	at := strings.Index(s, marker)
	if at < 0 {
		return nil, fmt.Errorf("未找到 %s 数据", marker)
	}
	s = s[at+len(marker):]
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return nil, fmt.Errorf("%s 数据缺少对象", marker)
	}
	p := mainlandLiteral{source: s[start:]}
	return p.value(0)
}

type mainlandLiteral struct {
	source string
	at     int
}

func (p *mainlandLiteral) space() {
	for p.at < len(p.source) {
		if strings.ContainsRune(" \t\r\n", rune(p.source[p.at])) {
			p.at++
			continue
		}
		if strings.HasPrefix(p.source[p.at:], "//") {
			if end := strings.IndexByte(p.source[p.at:], '\n'); end >= 0 {
				p.at += end + 1
				continue
			}
		}
		if strings.HasPrefix(p.source[p.at:], "/*") {
			if end := strings.Index(p.source[p.at+2:], "*/"); end >= 0 {
				p.at += end + 4
				continue
			}
		}
		break
	}
}
func (p *mainlandLiteral) value(depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("页面对象嵌套过深")
	}
	p.space()
	if p.at >= len(p.source) {
		return nil, fmt.Errorf("页面对象意外结束")
	}
	ch := p.source[p.at]
	p.at++
	switch ch {
	case '{':
		m := map[string]any{}
		for {
			p.space()
			if p.at >= len(p.source) {
				break
			}
			if p.source[p.at] == '}' {
				p.at++
				return m, nil
			}
			var key string
			if p.source[p.at] == '\'' || p.source[p.at] == '"' {
				v, e := p.value(depth + 1)
				if e != nil {
					return nil, e
				}
				key = Text(v)
			} else {
				start := p.at
				for p.at < len(p.source) && (p.source[p.at] == '_' || p.source[p.at] == '$' || p.source[p.at] >= 'a' && p.source[p.at] <= 'z' || p.source[p.at] >= 'A' && p.source[p.at] <= 'Z' || p.source[p.at] >= '0' && p.source[p.at] <= '9') {
					p.at++
				}
				key = p.source[start:p.at]
			}
			p.space()
			if p.at >= len(p.source) || p.source[p.at] != ':' {
				return nil, fmt.Errorf("页面对象字段 %q 缺少冒号", key)
			}
			p.at++
			v, e := p.value(depth + 1)
			if e != nil {
				return nil, e
			}
			m[key] = v
			p.space()
			if p.at < len(p.source) && p.source[p.at] == ',' {
				p.at++
				continue
			}
			if p.at < len(p.source) && p.source[p.at] == '}' {
				p.at++
				return m, nil
			}
			return nil, fmt.Errorf("页面对象字段 %q 后存在不支持的表达式", key)
		}
	case '[':
		var a []any
		for {
			p.space()
			if p.at >= len(p.source) {
				break
			}
			if p.source[p.at] == ']' {
				p.at++
				return a, nil
			}
			v, e := p.value(depth + 1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
			p.space()
			if p.at < len(p.source) && p.source[p.at] == ',' {
				p.at++
				continue
			}
			if p.at < len(p.source) && p.source[p.at] == ']' {
				p.at++
				return a, nil
			}
			return nil, fmt.Errorf("页面数组格式错误")
		}
	case '\'', '"':
		var out strings.Builder
		for p.at < len(p.source) {
			c := p.source[p.at]
			p.at++
			if c == ch {
				p.space()
				if p.at < len(p.source) && p.source[p.at] == '*' {
					p.at++
					p.space()
					if p.at < len(p.source) && p.source[p.at] == '1' {
						p.at++
					} else {
						return nil, fmt.Errorf("不支持的页面数字表达式")
					}
				}
				return out.String(), nil
			}
			if c != '\\' {
				out.WriteByte(c)
				continue
			}
			if p.at >= len(p.source) {
				break
			}
			esc := p.source[p.at]
			p.at++
			switch esc {
			case '\'', '"', '\\', '/':
				out.WriteByte(esc)
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case '\n':
			case 'x', 'u':
				n := 2
				if esc == 'u' {
					n = 4
				}
				if p.at+n > len(p.source) {
					return nil, fmt.Errorf("页面转义截断")
				}
				value, e := strconv.ParseUint(p.source[p.at:p.at+n], 16, 32)
				if e != nil {
					return nil, e
				}
				p.at += n
				r := rune(value)
				if esc == 'u' && value >= 0xD800 && value <= 0xDBFF && p.at+6 <= len(p.source) && p.source[p.at:p.at+2] == `\u` {
					low, e := strconv.ParseUint(p.source[p.at+2:p.at+6], 16, 32)
					if e == nil && low >= 0xDC00 && low <= 0xDFFF {
						r = utf16.DecodeRune(r, rune(low))
						p.at += 6
					}
				}
				out.WriteRune(r)
			default:
				out.WriteByte(esc)
			}
		}
	default:
		start := p.at - 1
		for p.at < len(p.source) && !strings.ContainsRune(",]} \t\r\n", rune(p.source[p.at])) {
			p.at++
		}
		token := p.source[start:p.at]
		switch token {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null", "undefined", "NaN":
			return nil, nil
		}
		if _, e := strconv.ParseFloat(token, 64); e == nil {
			return json.Number(token), nil
		}
		return nil, fmt.Errorf("不支持的页面表达式 %q", token)
	}
	return nil, fmt.Errorf("页面对象未闭合")
}
