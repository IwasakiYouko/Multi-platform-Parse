package parser

// Provider protocols adapted from https://github.com/z-mio/ParseHub (MIT).
// Copyright (c) 2024 梓澪. License notice: mainland_common.go.

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() {
	Register("weibo", mainlandWeibo)
	Register("tieba", mainlandTieba)
	Register("pipix", mainlandPipix)
	Register("zuiyou", mainlandZuiyou)
}

// These APIs select dictionary values by insertion order, not by resolution or key.
// Keep the decoder scoped to the few response objects where that order is semantic.
type socialOrderedValues []json.RawMessage

func (values *socialOrderedValues) UnmarshalJSON(source []byte) error {
	if bytes.Equal(bytes.TrimSpace(source), []byte("null")) {
		*values = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(source))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return fmt.Errorf("媒体字段不是 JSON 对象")
	}
	*values = nil
	for dec.More() {
		if _, err = dec.Token(); err != nil {
			return err
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return err
		}
		*values = append(*values, value)
	}
	_, err = dec.Token()
	return err
}

type socialWeiboStatus struct {
	TextRaw string `json:"text_raw"`
	User    struct {
		ScreenName string `json:"screen_name"`
	} `json:"user"`
	PicInfos     socialOrderedValues `json:"pic_infos"`
	PageInfo     map[string]any      `json:"page_info"`
	MixMediaInfo struct {
		Items []struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		} `json:"items"`
	} `json:"mix_media_info"`
	RetweetedStatus *socialWeiboStatus `json:"retweeted_status"`
}

var socialWeiboHashtag = regexp.MustCompile(` ?#[^#]+# ?`)
var socialWeiboStatusID = regexp.MustCompile(`^(?:\d+|[0-9A-Za-z]{9})$`)

func socialWeiboText(text string) string {
	return strings.TrimSpace(socialWeiboHashtag.ReplaceAllStringFunc(text, func(tag string) string {
		return " " + strings.TrimSuffix(strings.TrimSpace(tag), "#") + " "
	}))
}

func mainlandWeibo(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("微博链接无效: %w", err)
	}
	if u.Hostname() == "mapp.api.weibo.cn" && strings.HasPrefix(u.Path, "/fx/") || u.Hostname() == "video.weibo.com" && strings.HasPrefix(u.Path, "/show") {
		// The reference resolves exactly the first Location, without fetching its page.
		client := *c
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if e != nil {
			return nil, e
		}
		for k, v := range Headers(cookie, "https://weibo.com") {
			req.Header.Set(k, v)
		}
		resp, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("微博跳转 HTTP %d", resp.StatusCode)
		}
		if location := resp.Header.Get("Location"); location != "" {
			next, e := url.Parse(location)
			if e != nil {
				return nil, e
			}
			u = u.ResolveReference(next)
			raw = u.String()
		}
	}
	id := u.Path[strings.LastIndex(u.Path, "/")+1:]
	statusPath := regexp.MustCompile(`^/status/([^/?#]+)`).FindStringSubmatch(u.Path)
	tv := strings.Contains(u.Path, "/tv/show")
	if len(statusPath) > 0 {
		id = statusPath[1]
	}
	if !(tv && len(id) == 21) && !socialWeiboStatusID.MatchString(id) {
		return nil, fmt.Errorf("微博链接缺少有效状态 ID")
	}
	r := mainlandResult("weibo", raw)
	if tv {
		var data struct {
			Data struct {
				Play struct {
					Text     string              `json:"text"`
					URLs     socialOrderedValues `json:"urls"`
					Duration float64             `json:"duration_time"`
					Cover    string              `json:"cover_image"`
				} `json:"Component_Play_Playinfo"`
			} `json:"data"`
		}
		h := Headers(cookie, "https://weibo.com/tv/home")
		h["Content-Type"] = "application/x-www-form-urlencoded"
		component, _ := json.Marshal(map[string]any{"Component_Play_Playinfo": map[string]string{"oid": id}})
		body := url.Values{"data": {string(component)}}
		if err = mainlandPost(ctx, c, "https://weibo.com/tv/api/component?page="+url.QueryEscape("/tv/show/"+id), h, []byte(body.Encode()), &data); err != nil {
			return nil, err
		}
		pi := data.Data.Play
		if len(pi.URLs) == 0 {
			return nil, fmt.Errorf("微博 TV 未返回视频地址")
		}
		var video string
		if err = json.Unmarshal(pi.URLs[0], &video); err != nil {
			return nil, fmt.Errorf("微博 TV 视频地址无效: %w", err)
		}
		referenceSetHTML(r, socialWeiboText(pi.Text))
		mainlandAdd(r, "video", video, map[string]any{"duration": int(pi.Duration), "thumbnail": pi.Cover})
		return mainlandFinish(r, true)
	}
	var status socialWeiboStatus
	if err = JSON(ctx, c, "https://weibo.com/ajax/statuses/show?id="+url.QueryEscape(id)+"&isGetLongText=true", Headers(cookie, "https://weibo.com"), &status); err != nil {
		return nil, err
	}
	for current := &status; current != nil; current = current.RetweetedStatus {
		if len(current.PageInfo) == 0 {
			continue
		}
		switch Text(current.PageInfo["object_type"]) {
		case "video", "pic", "livephoto", "gif", "article":
		default:
			return nil, fmt.Errorf("微博未知页面媒体类型")
		}
	}
	// isGetLongText returns the full text_raw. Do not replace it with truncated HTML,
	// fetch another mobile API, or append the retweet's text to the outer post.
	r.Content = socialWeiboText(strings.ReplaceAll(status.TextRaw, Text(status.PageInfo["short_url"]), ""))
	r.Author = status.User.ScreenName
	if len(status.PicInfos) == 0 && Text(status.PageInfo["object_type"]) == "video" {
		playback := Array(Path(status.PageInfo, "media_info", "playback_list"))
		if len(playback) > 0 {
			pi := Obj(Path(playback[0], "play_info"))
			mainlandAdd(r, "video", Text(pi["url"]), map[string]any{"width": pi["width"], "height": pi["height"], "duration": int(Number(pi["duration"])), "thumbnail": status.PageInfo["page_pic"]})
			return mainlandFinish(r, true)
		}
	}
	pictures := status.PicInfos
	if status.RetweetedStatus != nil && len(status.RetweetedStatus.PicInfos) > 0 {
		pictures = status.RetweetedStatus.PicInfos
	}
	if len(pictures) > 0 {
		for _, rawPicture := range pictures {
			var picture map[string]any
			if err = json.Unmarshal(rawPicture, &picture); err != nil {
				return nil, err
			}
			if err = socialWeiboPicture(r, picture, Text(picture["type"])); err != nil {
				return nil, err
			}
		}
	} else {
		for _, item := range status.MixMediaInfo.Items {
			switch item.Type {
			case "pic":
				if err = socialWeiboPicture(r, item.Data, "pic"); err != nil {
					return nil, err
				}
			case "video":
				page := item.Data
				mi := Obj(page["media_info"])
				video := mainlandFirst(mi["mp4_hd_url"], mi["mp4_sd_url"])
				width, height := any(0), any(0)
				if playback := Array(mi["playback_list"]); len(playback) > 0 {
					pi := Obj(Path(playback[0], "play_info"))
					video, width, height = Text(pi["url"]), pi["width"], pi["height"]
				}
				before := len(r.Media)
				mainlandAdd(r, "video", video, map[string]any{"width": width, "height": height, "duration": mi["duration"], "thumbnail": page["page_pic"]})
				if len(r.Media) == before {
					return nil, fmt.Errorf("微博混合媒体缺少视频地址")
				}
			case "livephoto", "gif", "article":
				// Reference MixMediaInfo only decodes pic and video entries.
			default:
				return nil, fmt.Errorf("微博未知混合媒体类型 %q", item.Type)
			}
		}
	}
	referenceResultType(r)
	return mainlandFinish(r, false)
}

func socialWeiboPicture(r *model.ParseResult, picture map[string]any, kind string) error {
	largest, thumbnail := Obj(picture["largest"]), Text(Path(picture, "thumbnail", "url"))
	mediaURL := Text(picture["video"])
	if Text(picture["type"]) == "pic" {
		mediaURL = Text(largest["url"])
	}
	details := map[string]any{"width": largest["width"], "height": largest["height"], "thumbnail": thumbnail}
	switch kind {
	case "pic", "article":
		mainlandAdd(r, "image", mediaURL, details)
	case "livephoto":
		photo, video := mainlandURL(thumbnail, r.SourceURL), mainlandURL(mediaURL, r.SourceURL)
		if photo == "" || video == "" {
			return fmt.Errorf("微博实况照片缺少配对图片或视频")
		}
		r.Media = append(r.Media, model.Media{Kind: "livephoto", URL: photo, VideoURL: video, Width: int(Number(largest["width"])), Height: int(Number(largest["height"])), Headers: Headers("", r.SourceURL)})
	case "gif":
		// AniRef deliberately has no dimensions in the Weibo reference adapter.
		mainlandAdd(r, "gif", mediaURL, map[string]any{"thumbnail": thumbnail})
	case "video":
		mainlandAdd(r, "video", mediaURL, details)
	default:
		return fmt.Errorf("微博未知图片类型 %q", kind)
	}
	return nil
}

func socialTiebaSign(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var source strings.Builder
	for _, key := range keys {
		source.WriteString(key)
		source.WriteByte('=')
		source.WriteString(values.Get(key))
	}
	source.WriteString("36770b1f34c9bbf2e7d1a99d2b82fa9e")
	sum := md5.Sum([]byte(source.String()))
	return hex.EncodeToString(sum[:])
}

func mainlandTieba(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	match := regexp.MustCompile(`/p/(\d+)`).FindStringSubmatch(raw)
	if len(match) == 0 {
		return nil, fmt.Errorf("贴吧链接缺少帖子 ID")
	}
	var tbs any
	if err := JSON(ctx, c, "http://tieba.baidu.com/dc/common/tbs", Headers(cookie, "https://tieba.baidu.com/"), &tbs); err != nil {
		return nil, err
	}
	token := Text(Path(tbs, "tbs"))
	if token == "" {
		return nil, fmt.Errorf("获取 tbs 失败")
	}
	values := url.Values{"pn": {"1"}, "lz": {"0"}, "r": {"2"}, "mark_type": {"0"}, "back": {"0"}, "fr": {"personalize_page"}, "kz": {match[1]}, "session_request_times": {"1"}, "tbs": {token}, "subapp_type": {"pc"}, "_client_type": {"20"}}
	values.Set("sign", socialTiebaSign(values))
	headers := Headers(cookie, "https://tieba.baidu.com/")
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	var data any
	if err := mainlandPost(ctx, c, "https://tieba.baidu.com/c/f/pb/page_pc", headers, []byte(values.Encode()), &data); err != nil {
		return nil, err
	}
	if _, ok := Obj(data)["error_code"]; !ok {
		return nil, fmt.Errorf("贴吧接口响应缺少状态码")
	}
	if Number(Path(data, "error_code")) != 0 {
		return nil, fmt.Errorf("贴吧接口: %s", mainlandFirst(Path(data, "error_msg"), "获取帖子内容失败"))
	}
	thread := Obj(Path(data, "thread"))
	origin := Obj(thread["origin_thread_info"])
	if len(origin) == 0 {
		return nil, fmt.Errorf("贴吧响应缺少首帖内容")
	}
	r := mainlandResult("tieba", raw)
	r.Title = Text(origin["title"])
	var content []string
	for _, part := range Array(origin["content"]) {
		if Number(Path(part, "type")) == 0 {
			content = append(content, Text(Path(part, "text")))
		}
	}
	r.Content = strings.Join(content, "\n")
	photos := Array(origin["media"])
	if len(photos) > 0 {
		for _, photo := range photos {
			p := Obj(photo)
			kind := "image"
			imageURL := mainlandURL(Text(p["big_pic"]), raw)
			if imageURL == "" {
				return nil, fmt.Errorf("贴吧图片地址无效")
			}
			req, err := http.NewRequestWithContext(netx.WithoutCookies(ctx), http.MethodHead, imageURL, nil)
			if err != nil {
				return nil, err
			}
			// Inspection is best-effort, through the controlled client, without login cookies.
			resp, err := c.Do(req)
			if err == nil {
				if resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "gif") {
					kind = "gif"
				}
				resp.Body.Close()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			mainlandAdd(r, kind, imageURL, map[string]any{"thumbnail": p["small_pic"], "width": p["width"], "height": p["height"]})
		}
	} else if video := Obj(thread["video_info"]); len(video) > 0 {
		mainlandAdd(r, "video", Text(video["video_url"]), map[string]any{"thumbnail": video["thumbnail_url"], "width": video["video_width"], "height": video["video_height"], "duration": video["video_duration"]})
		return mainlandFinish(r, true)
	}
	referenceResultType(r)
	return mainlandFinish(r, false)
}

func mainlandPipix(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	body, final, err := Get(ctx, c, raw, Headers(cookie, raw))
	if err != nil {
		return nil, err
	}
	doc, err := mainlandDoc(string(body))
	if err != nil {
		return nil, err
	}
	script := doc.Find("script#RENDER_DATA")
	if script.Length() == 0 {
		return nil, fmt.Errorf("皮皮虾解析失败: 缺少分享数据")
	}
	decoded, err := url.PathUnescape(script.Text())
	if err != nil {
		return nil, fmt.Errorf("皮皮虾解析失败: %w", err)
	}
	if !json.Valid([]byte(decoded)) {
		return nil, fmt.Errorf("皮皮虾解析失败: 分享数据不是有效 JSON")
	}
	var state any
	dec := json.NewDecoder(strings.NewReader(decoded))
	dec.UseNumber()
	if err = dec.Decode(&state); err != nil {
		return nil, fmt.Errorf("皮皮虾解析失败: %w", err)
	}
	item := Obj(Path(state, "ppxItemDetail", "item"))
	rawType, ok := item["item_type"].(json.Number)
	itemType := Number(rawType)
	if !ok || itemType != 1 && itemType != 2 {
		return nil, fmt.Errorf("皮皮虾解析失败: 不支持的内容类型")
	}
	content, ok := item["content"].(string)
	if !ok {
		return nil, fmt.Errorf("皮皮虾解析失败: 缺少正文")
	}
	r := mainlandResult("pipix", final)
	r.Title = content
	if itemType == 2 {
		video := Obj(Path(item, "video", "video_download"))
		urls, covers := Array(video["url_list"]), Array(Path(video, "cover_image", "download_list"))
		if len(urls) == 0 || len(covers) == 0 {
			return nil, fmt.Errorf("皮皮虾解析失败: 缺少视频或封面")
		}
		for _, key := range []string{"duration", "height", "width"} {
			if _, ok := video[key].(json.Number); !ok {
				return nil, fmt.Errorf("皮皮虾解析失败: 缺少视频参数 %s", key)
			}
		}
		mainlandAdd(r, "video", Text(Path(urls[0], "url")), map[string]any{"width": video["width"], "height": video["height"], "duration": math.Ceil(Number(video["duration"])), "thumbnail": Path(covers[0], "url")})
		return mainlandFinish(r, true)
	}
	for _, image := range Array(Path(item, "cover", "download_list")) {
		mainlandAdd(r, "image", Text(Path(image, "url")), nil)
	}
	referenceResultType(r)
	return mainlandFinish(r, false)
}

func mainlandZuiyou(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Hostname() == "s.izuiyou.com" && strings.HasPrefix(u.Path, "/s/") {
		_, final, e := Get(ctx, c, raw, Headers(cookie, raw))
		if e != nil {
			return nil, e
		}
		raw = final
		u, err = url.Parse(raw)
		if err != nil {
			return nil, err
		}
	}
	pid := ""
	if values := u.Query()["pid"]; len(values) > 0 {
		pid = values[len(values)-1]
	}
	if !regexp.MustCompile(`^\d+$`).MatchString(pid) {
		return nil, fmt.Errorf("最右链接缺少有效 pid")
	}
	pid = strings.TrimLeft(pid, "0")
	if pid == "" {
		pid = "0"
	}
	var state struct {
		Data struct {
			Post *struct {
				Content string `json:"content"`
				Member  struct {
					Name string `json:"name"`
				} `json:"member"`
				Imgs []struct {
					ID    json.Number         `json:"id"`
					Video any                 `json:"video"`
					URLs  socialOrderedValues `json:"urls"`
				} `json:"imgs"`
				Videos map[string]struct {
					URL string `json:"url"`
				} `json:"videos"`
			} `json:"post"`
		} `json:"data"`
	}
	if err = mainlandPostJSON(ctx, c, "https://share.xiaochuankeji.cn/planck/share/post/detail_h5", Headers(cookie, raw), map[string]any{"pid": json.Number(pid)}, &state); err != nil {
		return nil, err
	}
	post := state.Data.Post
	if post == nil {
		return nil, fmt.Errorf("最右帖子数据不可用")
	}
	r := mainlandResult("zuiyou", raw)
	r.Content, r.Author = post.Content, post.Member.Name
	for _, image := range post.Imgs {
		if image.ID == "" {
			return nil, fmt.Errorf("最右媒体缺少 ID")
		}
		if len(image.URLs) == 0 {
			return nil, fmt.Errorf("最右图片缺少地址")
		}
		var source struct {
			URLs []string `json:"urls"`
		}
		if err = json.Unmarshal(image.URLs[len(image.URLs)-1], &source); err != nil {
			return nil, err
		}
		if len(source.URLs) == 0 {
			return nil, fmt.Errorf("最右图片缺少地址")
		}
		thumbnail := source.URLs[len(source.URLs)-1]
		kind, mediaURL := "image", thumbnail
		if socialTruthy(image.Video) {
			kind, mediaURL = "video", post.Videos[image.ID.String()].URL
		}
		before := len(r.Media)
		mainlandAdd(r, kind, mediaURL, map[string]any{"thumbnail": thumbnail})
		if len(r.Media) == before {
			return nil, fmt.Errorf("最右媒体地址不可用")
		}
	}
	referenceResultType(r)
	return mainlandFinish(r, false)
}

func socialTruthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case json.Number:
		return Number(value) != 0
	case float64:
		return value != 0
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return true
	}
}
