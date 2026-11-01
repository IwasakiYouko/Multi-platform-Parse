// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("xhs", majorXHS) }

func majorXHS(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithExplicitCookies(ctx)
	source := raw
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Hostname() == "xhslink.com" || u.Hostname() == "xhslink.cn" || strings.HasSuffix(u.Hostname(), ".xhslink.com") || strings.HasSuffix(u.Hostname(), ".xhslink.cn") {
		_, final, e := Get(netx.WithoutCookies(ctx), c, raw, majorXHSHeaders("", raw, true))
		if e != nil {
			return nil, e
		}
		raw = final
	}
	if err := majorXHSLocation(raw); err != nil {
		return nil, err
	}
	id := majorXHSID(raw)
	extract := func(cookie string, mobile bool) (*model.ParseResult, error) {
		// Preserve signed xsec_token and xsec_source parameters unchanged.
		requestCtx := ctx
		if mobile {
			requestCtx = netx.WithoutCookies(ctx)
		}
		page, final, err := Get(requestCtx, c, raw, majorXHSHeaders(cookie, raw, mobile))
		if err != nil {
			return nil, err
		}
		if err = majorXHSLocation(final); err != nil {
			return nil, err
		}
		if finalID := majorXHSID(final); id != "" && finalID != "" && id != finalID {
			return nil, fmt.Errorf("小红书重定向笔记 ID 与请求不匹配")
		}
		state, err := DecodeState(page, "window.__INITIAL_STATE__")
		if err != nil {
			return nil, err
		}
		note, err := majorXHSRequestedNote(state, id)
		if err != nil {
			return nil, err
		}
		r, err := majorXHSParseNote(note)
		if err != nil {
			return nil, err
		}
		r.SourceURL = source
		return r, nil
	}
	var stages []referenceStage
	if strings.TrimSpace(cookie) != "" {
		stages = append(stages, referenceStage{Name: "cookie", Run: func() (*model.ParseResult, error) { return extract(cookie, false) }})
	}
	stages = append(stages, referenceStage{Name: "游客 session", Run: func() (*model.ParseResult, error) {
		session, err := majorXHSNewSession(ctx, c)
		if err != nil {
			return nil, err
		}
		return extract(session, false)
	}}, referenceStage{Name: "移动端", Run: func() (*model.ParseResult, error) { return extract("", true) }})
	return referenceFallback(stages, false)
}

func majorXHSHeaders(cookie, raw string, mobile bool) map[string]string {
	h := majorHeaders(cookie, "https://www.xiaohongshu.com/", raw, "xiaohongshu.com")
	h["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"
	if mobile {
		h["User-Agent"] = majorMobileUA
		h["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	}
	return h
}

func majorXHSLocation(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Hostname() != "xiaohongshu.com" && !strings.HasSuffix(u.Hostname(), ".xiaohongshu.com") {
		return fmt.Errorf("小红书分享链接没有指向站内笔记")
	}
	switch {
	case strings.Contains(u.Path, "/login"):
		return fmt.Errorf("小红书笔记需要登录，请设置有效 Cookie")
	case strings.Contains(u.Path, "/404/sec"):
		return fmt.Errorf("小红书风控拦截，需要有效登录或完成验证码")
	case strings.Contains(u.Path, "/404"):
		return &ContentUnavailableError{Reason: "小红书笔记不存在或不可见"}
	}
	return nil
}

func majorXHSID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 1 && (parts[0] == "explore" || parts[0] == "discovery" && len(parts) > 2 && parts[1] == "item") {
		return path.Base(u.Path)
	}
	return ""
}

func majorXHSRequestedNote(state any, id string) (map[string]any, error) {
	var note map[string]any
	if h5 := Obj(Path(state, "noteData", "data", "noteData")); len(h5) > 0 {
		note = h5
	} else {
		root := Obj(Path(state, "note"))
		if id == "" {
			id = Text(root["firstNoteId"])
		}
		note = Obj(Path(root, "noteDetailMap", id, "note"))
	}
	if len(note) == 0 {
		return nil, fmt.Errorf("小红书未返回请求笔记，需要有效登录 Cookie 或分享链接")
	}
	selected := majorFirstText(note["noteId"], note["id"])
	if id != "" && selected != "" && selected != id {
		return nil, fmt.Errorf("小红书笔记 ID 与请求不匹配")
	}
	// H5 has no map key binding: an explicit requested ID must appear in the note.
	if Obj(Path(state, "noteData", "data", "noteData")) != nil && id != "" && selected == "" {
		return nil, fmt.Errorf("小红书移动页缺少笔记 ID")
	}
	return note, nil
}

func majorXHSStream(v any) map[string]any {
	for _, codec := range []string{"h264", "av1", "h265", "h266"} {
		streams := Array(Obj(v)[codec])
		if len(streams) > 0 {
			stream := Obj(streams[0])
			if majorURL(stream["masterUrl"]) != "" {
				return stream
			}
			return nil
		}
	}
	return nil
}
func majorXHSImage(v any) string {
	image := Obj(v)
	return majorFirstText(majorURL(image["urlDefault"]), majorURL(image["url"]))
}
func majorXHSImageThumb(v any) string {
	image := Obj(v)
	if uri := majorURL(image["urlPre"]); uri != "" {
		return uri
	}
	for _, v := range Array(image["infoList"]) {
		info := Obj(v)
		if Text(info["imageScene"]) == "H5_PRV" {
			if uri := majorURL(info["url"]); uri != "" {
				return uri
			}
		}
	}
	return majorXHSImage(v)
}

var majorXHSTrace = regexp.MustCompile(`/[a-f0-9]{32}/(.+)`)
var majorXHSTopic = regexp.MustCompile(` ?#[^#\[\]]+\[话题\]# ?`)

func majorXHSRawImage(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || majorURL(raw) == "" {
		return ""
	}
	trace, _, _ := strings.Cut(u.EscapedPath(), "!")
	if m := majorXHSTrace.FindStringSubmatch(trace); len(m) > 1 {
		trace = m[1]
	} else {
		trace = strings.TrimLeft(trace, "/")
	}
	if trace == "" {
		return ""
	}
	return "https://sns-img-hw.xhscdn.com/" + trace + "?imageView2/format/jpg"
}
func majorXHSParseNote(note map[string]any) (*model.ParseResult, error) {
	desc := majorXHSTopic.ReplaceAllStringFunc(Text(note["desc"]), func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "[话题]#", "") + " " })
	r := &model.ParseResult{Platform: "xhs", Title: Text(note["title"]), Content: strings.TrimSpace(desc), Author: majorFirstText(Path(note, "user", "nickname"), Path(note, "user", "nickName")), Headers: majorMediaHeaders("https://www.xiaohongshu.com/")}
	images := Array(note["imageList"])
	switch Text(note["type"]) {
	case "video":
		stream := majorXHSStream(Path(note, "video", "media", "stream"))
		if stream == nil {
			return nil, fmt.Errorf("小红书视频没有可用播放流")
		}
		if len(images) == 0 || majorXHSImage(images[0]) == "" {
			return nil, fmt.Errorf("小红书视频缺少缩略图")
		}
		r.Media = []model.Media{{Kind: "video", URL: majorURL(stream["masterUrl"]), BackupURLs: majorURLs(stream["backupUrls"]), ThumbnailURL: majorXHSImage(images[0]), Width: int(Number(stream["width"])), Height: int(Number(stream["height"])), Duration: Number(stream["duration"]) / 1000, Headers: r.Headers}}
	case "normal":
		for index, v := range images {
			image := Obj(v)
			uri := majorXHSRawImage(majorXHSImage(image))
			if uri == "" {
				return nil, fmt.Errorf("小红书第 %d 张图片缺少地址", index+1)
			}
			m := model.Media{Kind: "image", URL: uri, ThumbnailURL: majorXHSImageThumb(image), Width: int(Number(image["width"])), Height: int(Number(image["height"])), Headers: r.Headers}
			if live, _ := image["livePhoto"].(bool); live {
				stream := majorXHSStream(image["stream"])
				if stream == nil {
					continue
				}
				m.Kind = "livephoto"
				m.VideoURL = majorURL(stream["masterUrl"])
				m.Duration = Number(stream["duration"]) / 1000
				m.ThumbnailURL = uri
			}
			r.Media = append(r.Media, m)
		}
	default:
		return nil, fmt.Errorf("小红书不支持的笔记类型")
	}
	if len(r.Media) == 0 {
		return nil, fmt.Errorf("小红书笔记没有可下载媒体")
	}
	referenceResultType(r)
	return r, nil
}
