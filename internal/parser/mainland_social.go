package parser

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"multiparse/internal/model"
)

func init() {
	Register("weibo", mainlandWeibo)
	Register("tieba", mainlandTieba)
	Register("pipix", mainlandPipix)
	Register("zuiyou", mainlandZuiyou)
}

func mainlandWeibo(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	u, _ := url.Parse(raw)
	if u.Hostname() == "mapp.api.weibo.cn" || u.Hostname() == "video.weibo.com" || u.Hostname() == "t.cn" {
		_, resolved, e := Get(ctx, c, raw, Headers(cookie, "https://weibo.com/"))
		if e != nil {
			return nil, e
		}
		raw = resolved
		u, _ = url.Parse(raw)
	}
	r := mainlandResult("weibo", raw)
	if strings.Contains(u.Path, "/ttarticle/") {
		b, _, e := Get(ctx, c, raw, Headers(cookie, "https://weibo.com/"))
		if e != nil {
			return nil, e
		}
		d, e := mainlandDoc(string(b))
		if e != nil {
			return nil, e
		}
		body := d.Find(".WB_editor_iframe_new,.article_content,.articalContent").First()
		if body.Length() == 0 {
			return nil, fmt.Errorf("微博文章正文不可用；需要有效 Cookie 或文章已删除")
		}
		r.Title = strings.TrimSpace(d.Find("h1,.title").First().Text())
		s, _ := body.Html()
		_ = mainlandBody(r, s)
		r.Type = "article"
		return mainlandFinish(r, false)
	}
	id := strings.Trim(strings.TrimSpace(u.Path), "/")
	if p := strings.Split(id, "/"); len(p) > 0 {
		id = p[len(p)-1]
	}
	if id == "show" {
		id = u.Query().Get("fid")
	}
	if id == "" {
		return nil, fmt.Errorf("微博链接缺少状态 ID")
	}
	if strings.Contains(u.Path, "/tv/show/") || strings.Contains(id, ":") {
		var v any
		h := Headers(cookie, "https://weibo.com/tv/home")
		h["Content-Type"] = "application/x-www-form-urlencoded"
		data := url.Values{"data": {`{"Component_Play_Playinfo":{"oid":"` + id + `"}}`}}
		e := mainlandPost(ctx, c, "https://weibo.com/tv/api/component?page="+url.QueryEscape("/tv/show/"+id), h, []byte(data.Encode()), &v)
		if e != nil {
			return nil, e
		}
		pi := Obj(Path(v, "data", "Component_Play_Playinfo"))
		r.Content = mainlandText(Text(pi["text"]))
		best := ""
		for _, v := range Obj(pi["urls"]) {
			if s := Text(v); s != "" {
				best = s
				break
			}
		}
		mainlandAdd(r, "video", best, map[string]any{"duration": pi["duration_time"], "thumbnail": pi["cover_image"]})
		return mainlandFinish(r, true)
	}
	var data any
	e := JSON(ctx, c, "https://weibo.com/ajax/statuses/show?id="+url.QueryEscape(id)+"&isGetLongText=true", Headers(cookie, "https://weibo.com/"), &data)
	if e != nil || Text(Path(data, "id")) == "" {
		var mobile any
		err := JSON(ctx, c, "https://m.weibo.cn/statuses/show?id="+url.QueryEscape(id), Headers(cookie, "https://m.weibo.cn/"), &mobile)
		if err != nil {
			if e != nil {
				return nil, e
			}
			return nil, err
		}
		data = Path(mobile, "data")
	}
	if len(Obj(data)) == 0 || mainlandFirst(Path(data, "id"), Path(data, "idstr")) == "" {
		return nil, fmt.Errorf("微博状态不可用或需要有效 Cookie: %s", mainlandFirst(Path(data, "msg"), Path(data, "message")))
	}
	root := Obj(data)
	r.Content = mainlandText(mainlandFirst(root["text_raw"], root["text"]))
	r.Author = Text(Path(root, "user", "screen_name"))
	if long := Path(root, "longText", "longTextContent"); Text(long) != "" {
		r.Content = mainlandText(Text(long))
	}
	if Number(root["isLongText"]) == 1 || Text(root["isLongText"]) == "true" {
		var ext any
		if JSON(ctx, c, "https://m.weibo.cn/statuses/extend?id="+url.QueryEscape(mainlandFirst(root["idstr"], root["id"])), Headers(cookie, "https://m.weibo.cn/"), &ext) == nil {
			if t := Text(Path(ext, "data", "longTextContent")); t != "" {
				r.Content = mainlandText(t)
			}
		}
	}
	expected := false
	var parseStatus func(map[string]any)
	parseStatus = func(status map[string]any) {
		for _, p := range Array(status["pics"]) {
			mainlandAdd(r, "image", mainlandFirst(Path(p, "large", "url"), Path(p, "url")), Obj(Path(p, "large")))
		}
		picIDs := Array(status["pic_ids"])
		infos := Obj(status["pic_infos"])
		if len(picIDs) == 0 {
			keys := make([]string, 0, len(infos))
			for k := range infos {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				picIDs = append(picIDs, k)
			}
		}
		for _, id := range picIDs {
			p := Obj(infos[Text(id)])
			mainlandAdd(r, "image", mainlandFirst(Path(p, "largest", "url"), Path(p, "original", "url"), Path(p, "large", "url")), Obj(Path(p, "largest")))
			if v := Text(p["video"]); v != "" {
				mainlandAdd(r, "video", v, nil)
			}
		}
		addVideo := func(page any) {
			mi := Obj(Path(page, "media_info"))
			if len(mi) == 0 {
				mi = Obj(page)
			}
			kind := Text(Path(page, "object_type"))
			if kind == "video" || len(mi) > 0 && mainlandFirst(mi["mp4_hd_url"], mi["stream_url"], mi["stream_url_hd"]) != "" {
				expected = true
			}
			best := mainlandFirst(mi["mp4_1080p_mp4"], mi["mp4_720p_mp4"], mi["mp4_hd_url"], mi["stream_url_hd"], mi["mp4_sd_url"], mi["stream_url"])
			score := float64(-1)
			details := map[string]any{"duration": mi["duration"], "thumbnail": mainlandFirst(Path(page, "page_pic", "url"), Path(page, "page_pic"))}
			for _, p := range Array(mi["playback_list"]) {
				pi := Obj(Path(p, "play_info"))
				if Text(pi["url"]) != "" && Number(pi["width"])*Number(pi["height"]) > score {
					best = Text(pi["url"])
					score = Number(pi["width"]) * Number(pi["height"])
					details["width"] = pi["width"]
					details["height"] = pi["height"]
				}
			}
			if best == "" {
				for _, v := range Obj(Path(page, "urls")) {
					if Text(v) != "" {
						best = Text(v)
						break
					}
				}
			}
			if best != "" {
				mainlandAdd(r, "video", best, details)
			}
		}
		if page := status["page_info"]; page != nil {
			addVideo(page)
		}
		for _, item := range Array(Path(status, "mix_media_info", "items")) {
			p := Path(item, "data")
			if Text(Path(item, "type")) == "video" {
				expected = true
				addVideo(p)
			} else {
				mainlandAdd(r, "image", mainlandFirst(Path(p, "largest", "url"), Path(p, "large", "url")), Obj(Path(p, "largest")))
			}
		}
		if ret := Obj(status["retweeted_status"]); len(ret) > 0 {
			if t := mainlandText(mainlandFirst(ret["text_raw"], ret["text"])); t != "" {
				r.Content += "\n\n转发：" + t
			}
			parseStatus(ret)
		}
	}
	parseStatus(root)
	return mainlandFinish(r, expected)
}

func mainlandTieba(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	match := regexp.MustCompile(`/p/(\d+)`).FindStringSubmatch(raw)
	if len(match) == 0 {
		_, u, e := Get(ctx, c, raw, Headers(cookie, "https://tieba.baidu.com/"))
		if e != nil {
			return nil, e
		}
		raw = u
		match = regexp.MustCompile(`/p/(\d+)`).FindStringSubmatch(raw)
	}
	if len(match) == 0 {
		return nil, fmt.Errorf("贴吧链接缺少帖子 ID")
	}
	var tbs any
	if e := JSON(ctx, c, "https://tieba.baidu.com/dc/common/tbs", Headers(cookie, "https://tieba.baidu.com/"), &tbs); e != nil {
		return nil, e
	}
	token := Text(Path(tbs, "tbs"))
	if token == "" {
		return nil, fmt.Errorf("贴吧未返回 tbs，可能触发访问验证")
	}
	v := url.Values{"pn": {"1"}, "lz": {"0"}, "r": {"2"}, "mark_type": {"0"}, "back": {"0"}, "fr": {"personalize_page"}, "kz": {match[1]}, "session_request_times": {"1"}, "tbs": {token}, "subapp_type": {"pc"}, "_client_type": {"20"}}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var s strings.Builder
	for _, k := range keys {
		s.WriteString(k + "=" + v.Get(k))
	}
	sum := md5.Sum([]byte(s.String() + "36770b1f34c9bbf2e7d1a99d2b82fa9e"))
	v.Set("sign", hex.EncodeToString(sum[:]))
	h := Headers(cookie, "https://tieba.baidu.com/")
	h["Content-Type"] = "application/x-www-form-urlencoded"
	var data any
	if e := mainlandPost(ctx, c, "https://tieba.baidu.com/c/f/pb/page_pc", h, []byte(v.Encode()), &data); e != nil {
		return nil, e
	}
	if Number(Path(data, "error_code")) != 0 {
		return nil, fmt.Errorf("贴吧接口: %s", Text(Path(data, "error_msg")))
	}
	r := mainlandResult("tieba", raw)
	thread := Obj(Path(data, "thread"))
	origin := Obj(thread["origin_thread_info"])
	r.Title = mainlandFirst(origin["title"], thread["title"])
	for _, p := range Array(origin["content"]) {
		if Number(Path(p, "type")) == 0 {
			r.Content += Text(Path(p, "text")) + "\n"
		}
	}
	for _, p := range Array(origin["media"]) {
		mainlandAdd(r, "image", Text(Path(p, "big_pic")), Obj(p))
	}
	if len(origin) == 0 {
		posts := Array(Path(data, "post_list"))
		if len(posts) > 0 {
			p := posts[0]
			r.Author = Text(Path(p, "author", "name"))
			for _, part := range Array(Path(p, "content")) {
				switch int(Number(Path(part, "type"))) {
				case 0:
					r.Content += Text(Path(part, "text")) + "\n"
				case 3:
					mainlandAdd(r, "image", mainlandFirst(Path(part, "origin_src"), Path(part, "big_cdn_src"), Path(part, "src")), Obj(part))
				}
			}
		}
	}
	vi := Obj(thread["video_info"])
	if len(vi) > 0 {
		mainlandAdd(r, "video", Text(vi["video_url"]), map[string]any{"thumbnail": vi["thumbnail_url"], "width": vi["video_width"], "height": vi["video_height"], "duration": vi["video_duration"]})
	}
	return mainlandFinish(r, len(vi) > 0)
}

func mainlandPipix(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	b, final, e := Get(ctx, c, raw, Headers(cookie, raw))
	if e != nil {
		return nil, e
	}
	d, e := mainlandDoc(string(b))
	if e != nil {
		return nil, e
	}
	encoded := d.Find("script#RENDER_DATA").Text()
	decoded, e := url.PathUnescape(strings.TrimSpace(encoded))
	if e != nil {
		return nil, fmt.Errorf("皮皮虾分享数据编码错误: %w", e)
	}
	var state any
	if e = json.Unmarshal([]byte(decoded), &state); e != nil {
		return nil, fmt.Errorf("皮皮虾未返回分享数据，内容可能已删除或需要访问验证")
	}
	item := Obj(Path(state, "ppxItemDetail", "item"))
	if len(item) == 0 {
		return nil, fmt.Errorf("皮皮虾分享数据缺少 item")
	}
	r := mainlandResult("pipix", final)
	r.Content = Text(item["content"])
	r.Author = mainlandFirst(Path(item, "author", "name"), Path(item, "author", "nickname"))
	video := Obj(Path(item, "video", "video_download"))
	expected := Number(item["item_type"]) == 2
	if expected {
		urls := Array(video["url_list"])
		if len(urls) > 0 {
			mainlandAdd(r, "video", mainlandFirst(Path(urls[0], "url"), urls[0]), map[string]any{"width": video["width"], "height": video["height"], "duration": video["duration"], "thumbnail": Path(video, "cover_image", "download_list", "0", "url")})
			if len(r.Media) > 0 {
				covers := Array(Path(video, "cover_image", "download_list"))
				if len(covers) > 0 {
					r.Media[0].ThumbnailURL = Text(Path(covers[0], "url"))
				}
				for _, v := range urls[1:] {
					if u := mainlandURL(mainlandFirst(Path(v, "url"), v), final); u != "" {
						r.Media[0].BackupURLs = append(r.Media[0].BackupURLs, u)
					}
				}
			}
		}
	} else {
		for _, image := range Array(Path(item, "cover", "download_list")) {
			mainlandAdd(r, "image", Text(Path(image, "url")), Obj(image))
		}
		for _, image := range Array(item["images"]) {
			links := Array(Path(image, "download_list"))
			if len(links) > 0 {
				mainlandAdd(r, "image", Text(Path(links[0], "url")), Obj(image))
			}
		}
	}
	return mainlandFinish(r, expected)
}

func mainlandZuiyou(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	u, _ := url.Parse(raw)
	pid := u.Query().Get("pid")
	if pid == "" {
		_, final, e := Get(ctx, c, raw, Headers(cookie, raw))
		if e != nil {
			return nil, e
		}
		raw = final
		u, _ = url.Parse(raw)
		pid = u.Query().Get("pid")
	}
	if !regexp.MustCompile(`^\d+$`).MatchString(pid) {
		return nil, fmt.Errorf("最右链接缺少有效 pid")
	}
	var state any
	if e := mainlandPostJSON(ctx, c, "https://share.xiaochuankeji.cn/planck/share/post/detail_h5", Headers(cookie, raw), map[string]any{"pid": json.Number(pid)}, &state); e != nil {
		return nil, e
	}
	post := Obj(Path(state, "data", "post"))
	if len(post) == 0 {
		return nil, fmt.Errorf("最右帖子不可用: %s", mainlandFirst(Path(state, "msg"), Path(state, "message")))
	}
	r := mainlandResult("zuiyou", raw)
	r.Content = Text(post["content"])
	r.Author = Text(Path(post, "member", "name"))
	expected := false
	for _, image := range Array(post["imgs"]) {
		id := Text(Path(image, "id"))
		sources := Obj(Path(image, "urls"))
		best := ""
		size := float64(-1)
		for _, v := range sources {
			urls := Array(Path(v, "urls"))
			if len(urls) == 0 {
				continue
			}
			score := Number(Path(v, "w")) * Number(Path(v, "h"))
			if score >= size {
				best = Text(urls[len(urls)-1])
				size = score
			}
		}
		video := Obj(Path(post, "videos", id))
		if Text(Path(image, "video")) == "true" || Number(Path(image, "video")) != 0 || len(video) > 0 {
			expected = true
			mainlandAdd(r, "video", Text(video["url"]), map[string]any{"thumbnail": best, "duration": video["dur"], "width": video["w"], "height": video["h"]})
		} else {
			mainlandAdd(r, "image", best, nil)
		}
	}
	return mainlandFinish(r, expected)
}
