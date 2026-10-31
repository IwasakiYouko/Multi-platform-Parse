// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("xhs", majorXHS) }
func majorXHS(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	// xsec_token and xsec_source are signed share parameters: never rebuild or strip the query.
	headers := majorHeaders(cookie, "https://www.xiaohongshu.com/", raw, "xiaohongshu.com")
	headers["User-Agent"] = majorMobileUA
	headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	page, final, err := Get(ctx, c, raw, headers)
	if err != nil {
		return nil, fmt.Errorf("小红书分享页请求失败: %w", err)
	}
	if err = majorXHSLocation(final); err != nil {
		return nil, err
	}
	var note map[string]any
	states := majorStates(page, "window.__INITIAL_STATE__", "__INITIAL_STATE__")
	for _, state := range states {
		if note = majorXHSNote(state); len(note) > 0 {
			break
		}
	}
	if len(note) == 0 {
		// Desktop state often requires the configured web_session; the mobile state does not always do so.
		desktop := majorHeaders(cookie, "https://www.xiaohongshu.com/", final, "xiaohongshu.com")
		page, final, err = Get(ctx, c, final, desktop)
		if err != nil {
			return nil, fmt.Errorf("小红书详情请求失败: %w", err)
		}
		if err = majorXHSLocation(final); err != nil {
			return nil, err
		}
		for _, state := range majorStates(page, "window.__INITIAL_STATE__", "__INITIAL_STATE__") {
			if note = majorXHSNote(state); len(note) > 0 {
				break
			}
		}
	}
	if len(note) == 0 {
		return nil, fmt.Errorf("小红书未返回笔记内容：需要有效登录 Cookie 或带 xsec_token 的分享链接；也可能已删除或触发验证码")
	}
	r, err := majorXHSParseNote(note)
	if err != nil {
		return nil, err
	}
	r.SourceURL = raw
	return r, nil
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
		return fmt.Errorf("小红书笔记不存在或不可见")
	}
	return nil
}
func majorXHSNote(state any) map[string]any {
	if note := Obj(Path(state, "noteData", "data", "noteData")); len(note) > 0 {
		return note
	}
	root := Obj(Path(state, "note"))
	details := Obj(root["noteDetailMap"])
	if note := Obj(Path(details, Text(root["firstNoteId"]), "note")); len(note) > 0 {
		return note
	}
	for _, entry := range details {
		if note := Obj(Path(entry, "note")); len(note) > 0 {
			return note
		}
	}
	var found map[string]any
	Walk(state, func(m map[string]any) {
		if found == nil && Text(m["noteId"]) != "" && (m["imageList"] != nil || m["video"] != nil) && m["type"] != nil {
			found = m
		}
	})
	return found
}
func majorXHSStream(v any) map[string]any {
	stream := Obj(v)
	for _, codec := range []string{"h264", "h265", "av1", "h266"} {
		var best map[string]any
		score := -1.0
		for _, v := range Array(stream[codec]) {
			s := Obj(v)
			if majorURL(s) == "" {
				continue
			}
			n := Number(s["width"]) * Number(s["height"])
			if n > score {
				best = s
				score = n
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}
func majorXHSImage(v any) string {
	image := Obj(v)
	if s := majorURL(image["urlDefault"]); s != "" {
		return s
	}
	if s := majorURL(image["url"]); s != "" {
		return s
	}
	for _, v := range Array(image["infoList"]) {
		info := Obj(v)
		if s := majorURL(info["url"]); s != "" {
			return s
		}
	}
	return ""
}
func majorXHSParseNote(note map[string]any) (*model.ParseResult, error) {
	r := &model.ParseResult{Platform: "xhs", Title: Text(note["title"]), Content: strings.ReplaceAll(Text(note["desc"]), "[话题]#", ""), Author: majorFirstText(Path(note, "user", "nickname"), Path(note, "user", "nickName")), Headers: majorMediaHeaders("https://www.xiaohongshu.com/")}
	images := Array(note["imageList"])
	if Text(note["type"]) == "video" || note["video"] != nil {
		stream := majorXHSStream(Path(note, "video", "media", "stream"))
		if stream == nil {
			return nil, fmt.Errorf("小红书视频没有可用播放流（可能需要登录或已被限制）")
		}
		m := model.Media{Kind: "video", URL: majorURL(stream), BackupURLs: majorURLs(stream["backupUrls"]), Width: int(Number(stream["width"])), Height: int(Number(stream["height"])), Duration: Number(stream["duration"]) / 1000, Headers: r.Headers}
		if len(images) > 0 {
			m.ThumbnailURL = majorXHSImage(images[0])
		}
		r.Media = append(r.Media, m)
	} else {
		for index, v := range images {
			image := Obj(v)
			uri := majorXHSImage(image)
			if uri == "" {
				return nil, fmt.Errorf("小红书第 %d 张图片缺少地址", index+1)
			}
			r.Media = append(r.Media, model.Media{Kind: "image", URL: uri, ThumbnailURL: majorURL(image["urlPre"]), Width: int(Number(image["width"])), Height: int(Number(image["height"])), Headers: r.Headers})
			if live, ok := image["livePhoto"].(bool); ok && live {
				stream := majorXHSStream(image["stream"])
				if stream == nil {
					return nil, fmt.Errorf("小红书第 %d 张实况照片缺少视频流", index+1)
				}
				r.Media = append(r.Media, model.Media{Kind: "video", URL: majorURL(stream), ThumbnailURL: uri, Width: int(Number(stream["width"])), Height: int(Number(stream["height"])), Duration: Number(stream["duration"]) / 1000, Headers: r.Headers})
			}
		}
	}
	if len(r.Media) == 0 {
		return nil, fmt.Errorf("小红书笔记没有可下载媒体，可能需要登录")
	}
	majorResultType(r)
	return r, nil
}
