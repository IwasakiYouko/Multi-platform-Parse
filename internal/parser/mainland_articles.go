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
)

func init() {
	Register("weixin", mainlandWeixin)
	Register("coolapk", mainlandCoolapk)
	Register("douban", mainlandDouban)
	Register("bangumi", mainlandBangumi)
	Register("toutiao", mainlandToutiao)
}

func mainlandWeixin(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	b, final, err := Get(ctx, c, raw, Headers(cookie, raw))
	if err != nil {
		return nil, err
	}
	page, err := mainlandDoc(string(b))
	if err != nil {
		return nil, err
	}
	r := mainlandResult("weixin", final)
	r.Type = "article"
	r.Title = strings.TrimSpace(page.Find("#activity-name").Text())
	r.Author = strings.TrimSpace(page.Find("#js_name").Text())
	source := ""
	state, stateErr := mainlandLiteralState(b, "cgiDataNew")
	if stateErr == nil {
		r.Title = mainlandFirst(Path(state, "title"), r.Title)
		source = Text(Path(state, "content_noencode"))
		switch kind := int(Number(Path(state, "item_show_type"))); kind {
		case 0:
		case 8:
			r.Content = mainlandText(source)
			r.Type = "image"
			for _, p := range Array(Path(state, "picture_page_info_list")) {
				mainlandAdd(r, "image", mainlandFirst(Path(p, "watermark_info", "cdn_url"), Path(p, "cdn_url")), Obj(p))
			}
			return mainlandFinish(r, false)
		case 10:
			r.Content = mainlandText(Text(Path(state, "text_page_info", "content_noencode")))
			return mainlandFinish(r, false)
		default:
			return nil, fmt.Errorf("微信公众号内容类型 %d 尚未提供可读取正文", kind)
		}
	}
	if source == "" {
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
		if e := JSON(ctx, c, endpoint, Headers(cookie, final), &playback); e != nil {
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
	return mainlandFinish(r, expected > 0)
}

func mainlandCoolapk(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	b, final, e := Get(ctx, c, raw, Headers(cookie, "https://www.coolapk.com/"))
	if e != nil {
		return nil, e
	}
	d, e := mainlandDoc(string(b))
	if e != nil {
		return nil, e
	}
	r := mainlandResult("coolapk", final)
	r.Title = strings.TrimSpace(d.Find(".message-title").First().Text())
	r.Author = strings.TrimSpace(d.Find(".user-name,.feed-user-name").First().Text())
	body := d.Find(".feed-article-message").First()
	if body.Length() > 0 {
		r.Type = "article"
		s, _ := body.Html()
		_ = mainlandBody(r, s)
	} else {
		body = d.Find(".feed-message").First()
		if body.Length() == 0 {
			return nil, fmt.Errorf("酷安未返回动态正文；分享链接必须保留 shareKey 或 s 参数，受限动态需要有效 Cookie")
		}
		s, _ := body.Html()
		_ = mainlandBody(r, s)
		d.Find(".message-image-group img").Each(func(_ int, s *goquery.Selection) { u, _ := s.Attr("src"); mainlandAdd(r, "image", u, nil) })
	}
	expected := d.Find(".feed-video,.video-player,video").Length() > 0
	d.Find(".feed-video video,.feed-video source,video").Each(func(_ int, s *goquery.Selection) { u, _ := s.Attr("src"); mainlandAdd(r, "video", u, nil) })
	return mainlandFinish(r, expected)
}

func mainlandDouban(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	r := mainlandResult("douban", raw)
	h := Headers(cookie, "https://m.douban.com/")
	h["User-Agent"] = mainlandMobileUA
	match := regexp.MustCompile(`douban\.com/(?:group/)?topic/(\d+)`).FindStringSubmatch(raw)
	if len(match) > 0 {
		var v any
		if e := JSON(ctx, c, "https://m.douban.com/rexxar/api/v2/group/topic/"+match[1], h, &v); e != nil {
			return nil, e
		}
		if Text(Path(v, "title")) == "" {
			return nil, fmt.Errorf("豆瓣话题不可访问: %s", mainlandFirst(Path(v, "localized_message"), Path(v, "msg")))
		}
		r.Title = Text(Path(v, "title"))
		r.Author = Text(Path(v, "author", "name"))
		_ = mainlandBody(r, Text(Path(v, "content")))
		for _, p := range Array(Path(v, "photos")) {
			image := Path(p, "image")
			anim := Text(Path(image, "is_animated")) == "true"
			video := Obj(Path(image, "video"))
			if anim && Text(video["url"]) != "" {
				mainlandAdd(r, "video", Text(video["url"]), video)
			} else {
				large := Obj(Path(image, "large"))
				if Text(large["url"]) == "" {
					large = Obj(Path(image, "normal"))
				}
				mainlandAdd(r, "image", Text(large["url"]), large)
			}
		}
		vi := Obj(Path(v, "video_info"))
		if len(vi) > 0 {
			duration := float64(0)
			for _, p := range strings.Split(Text(vi["duration"]), ":") {
				duration = duration*60 + Number(p)
			}
			mainlandAdd(r, "video", Text(vi["video_url"]), map[string]any{"thumbnail": vi["cover_url"], "duration": duration, "width": vi["video_width"], "height": vi["video_height"]})
		}
		for i := range r.Media {
			r.Media[i].Headers = Headers("", "https://www.douban.com/")
		}
		r.Type = "article"
		return mainlandFinish(r, len(vi) > 0)
	}
	if !regexp.MustCompile(`/(?:note|review)/(\d+)`).MatchString(raw) {
		return nil, fmt.Errorf("豆瓣支持小组话题、日记和评论链接")
	}
	b, final, e := Get(ctx, c, raw, h)
	if e != nil {
		return nil, e
	}
	r.SourceURL = final
	d, e := mainlandDoc(string(b))
	if e != nil {
		return nil, e
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
	s, _ := body.Html()
	_ = mainlandBody(r, s)
	r.Type = "article"
	for i := range r.Media {
		r.Media[i].Headers = Headers("", "https://www.douban.com/")
	}
	return mainlandFinish(r, body.Find("video").Length() > 0)
}

func mainlandBangumi(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	match := regexp.MustCompile(`/(?:(subject|group)/topic|rakuen/topic/(subject|group)|(blog))/(\d+)`).FindStringSubmatch(raw)
	if len(match) == 0 {
		return nil, fmt.Errorf("Bangumi 支持讨论主题和日志链接")
	}
	kind := mainlandFirst(match[1], match[2], match[3])
	id := match[4]
	endpoint := "https://next.bgm.tv/p1/"
	if kind == "blog" {
		endpoint += "blogs/" + id
	} else if kind == "subject" {
		endpoint += "subjects/-/topics/" + id
	} else {
		endpoint += "groups/-/topics/" + id
	}
	var v any
	if e := JSON(ctx, c, endpoint, Headers(cookie, "https://bgm.tv/"), &v); e != nil {
		return nil, e
	}
	r := mainlandResult("bangumi", raw)
	r.Title = Text(Path(v, "title"))
	source := Text(Path(v, "content"))
	if kind != "blog" {
		replies := Array(Path(v, "replies"))
		if len(replies) > 0 {
			source = Text(Path(replies[0], "content"))
			r.Author = Text(Path(replies[0], "user", "nickname"))
		}
	}
	images := regexp.MustCompile(`(?is)\[(img|photo)(?:=[^\]]*)?\](.*?)\[/(?:img|photo)\]`)
	source = images.ReplaceAllStringFunc(source, func(s string) string {
		m := images.FindStringSubmatch(s)
		u := strings.TrimSpace(m[2])
		if strings.EqualFold(m[1], "photo") {
			u = "https://lain.bgm.tv/pic/photo/l/" + strings.TrimLeft(u, "/")
		}
		mainlandAdd(r, "image", u, nil)
		return ""
	})
	source = regexp.MustCompile(`(?i)\[/?(?:url|b|i|s|u|mask|quote|code|user|size|color|align|left|right|center)(?:=[^\]]*)?\]`).ReplaceAllString(source, "")
	r.Content = strings.TrimSpace(source)
	r.Type = "article"
	if kind == "blog" {
		var photos any
		if e := JSON(ctx, c, "https://next.bgm.tv/p1/blogs/"+id+"/photos?limit=100", Headers(cookie, "https://bgm.tv/"), &photos); e != nil {
			return nil, fmt.Errorf("Bangumi 日志照片读取失败: %w", e)
		}
		for _, p := range Array(Path(photos, "data")) {
			if target := Text(Path(p, "target")); target != "" {
				mainlandAdd(r, "image", "https://lain.bgm.tv/pic/photo/l/"+strings.TrimLeft(target, "/"), nil)
			}
		}
	}
	return mainlandFinish(r, false)
}

func mainlandToutiao(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	re := regexp.MustCompile(`toutiao\.com/(?:(?:article|video|w|group|item)/|a|i)(\d+)`)
	m := re.FindStringSubmatch(raw)
	if len(m) == 0 {
		_, final, e := Get(ctx, c, raw, Headers(cookie, raw))
		if e != nil {
			return nil, e
		}
		raw = final
		m = re.FindStringSubmatch(raw)
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("头条链接缺少内容 ID")
	}
	var v any
	h := Headers(cookie, "https://m.toutiao.com/")
	h["User-Agent"] = mainlandMobileUA
	if e := JSON(ctx, c, "https://m.toutiao.com/i"+m[1]+"/info/v2/", h, &v); e != nil {
		return nil, e
	}
	data := Obj(Path(v, "data"))
	if len(data) == 0 {
		return nil, fmt.Errorf("头条内容不存在、已删除或访问受限")
	}
	r := mainlandResult("toutiao", raw)
	r.Title = Text(data["title"])
	r.Author = mainlandFirst(data["source"], data["media_name"])
	if thread := Path(data, "thread", "thread_base"); thread != nil {
		r.Content = Text(Path(thread, "content"))
		for _, image := range Array(Path(thread, "large_image_list")) {
			mainlandAdd(r, "image", Text(Path(image, "url")), Obj(image))
		}
		return mainlandFinish(r, false)
	}
	source := Text(data["content"])
	d, e := mainlandDoc(source)
	if e != nil {
		return nil, e
	}
	var tokens []string
	d.Find(".tt-video-box").Each(func(_ int, s *goquery.Selection) {
		token, _ := s.Attr("data-token")
		tokens = append(tokens, token)
		s.Remove()
	})
	if token := Text(data["play_auth_token_v2"]); token != "" {
		tokens = append(tokens, token)
	}
	htmlBody, _ := d.Html()
	_ = mainlandBody(r, htmlBody)
	expected := len(tokens) > 0 || strings.Contains(raw, "/video/") || Text(data["video_id"]) != ""
	for _, token := range tokens {
		if token == "" {
			return nil, fmt.Errorf("头条视频缺少播放授权 token")
		}
		video, e := mainlandToutiaoVideo(ctx, c, token, Headers("", raw))
		if e != nil {
			return nil, e
		}
		if video.Duration == 0 {
			video.Duration = Number(data["video_duration"])
		}
		if video.ThumbnailURL == "" {
			video.ThumbnailURL = Text(data["poster_url"])
		}
		video.Headers = Headers("", raw)
		r.Media = append(r.Media, video)
	}
	if !expected {
		r.Type = "article"
	}
	return mainlandFinish(r, expected)
}
func mainlandToutiaoVideo(ctx context.Context, c *http.Client, token string, h map[string]string) (model.Media, error) {
	out := model.Media{Kind: "video"}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(token, "="))
	if e != nil {
		return out, fmt.Errorf("头条播放 token 编码错误")
	}
	var t any
	if e = json.Unmarshal(b, &t); e != nil {
		return out, e
	}
	query := Text(Path(t, "GetPlayInfoToken"))
	if query == "" {
		return out, fmt.Errorf("头条播放 token 缺少 GetPlayInfoToken")
	}
	if _, e = url.ParseQuery(query); e != nil {
		return out, e
	}
	var v any
	if e = JSON(ctx, c, "https://vod.bytedanceapi.com/?"+query, h, &v); e != nil {
		return out, e
	}
	if err := Path(v, "ResponseMetadata", "Error"); err != nil {
		return out, fmt.Errorf("头条点播接口: %s", mainlandFirst(Path(err, "Message"), Path(err, "message"), err))
	}
	best := float64(-1)
	for _, p := range Array(Path(v, "Result", "Data", "PlayInfoList")) {
		score := Number(Path(p, "Height"))*1e12 + Number(Path(p, "Size"))
		if u := mainlandURL(Text(Path(p, "MainPlayUrl")), "https://vod.bytedanceapi.com/"); u != "" && score > best {
			best = score
			out.URL = u
			out.Width = int(Number(Path(p, "Width")))
			out.Height = int(Number(Path(p, "Height")))
			out.Duration = Number(Path(p, "Duration"))
			out.BackupURLs = nil
			if u := mainlandURL(Text(Path(p, "BackupPlayUrl")), out.URL); u != "" {
				out.BackupURLs = []string{u}
			}
		}
	}
	if out.URL == "" {
		return out, fmt.Errorf("头条点播接口未返回可播放的视频")
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
