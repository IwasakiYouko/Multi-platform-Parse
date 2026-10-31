// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("kuaishou", majorKuaishou) }
func majorKSID(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	for _, key := range []string{"photoId", "vid", "id", "modal_id", "v", "pid"} {
		if s := u.Query().Get(key); s != "" {
			return s
		}
	}
	return strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".html")
}
func majorKuaishou(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	source := raw
	page, final, err := Get(ctx, c, raw, majorHeaders(cookie, "https://www.kuaishou.com/", raw, "kuaishou.com"))
	if err == nil {
		raw = final
		u, e := url.Parse(final)
		if e != nil {
			return nil, e
		}
		host := u.Hostname()
		if host != "kuaishou.com" && !strings.HasSuffix(host, ".kuaishou.com") && host != "chenzhongtech.com" && !strings.HasSuffix(host, ".chenzhongtech.com") {
			return nil, fmt.Errorf("快手分享链接没有指向站内作品")
		}
	}
	id := majorKSID(raw)
	if id == "" || id == "." || id == "/" {
		return nil, fmt.Errorf("快手分享链接缺少作品 ID")
	}
	if err == nil {
		if r, e := majorKSPage(page, id); e == nil {
			r.SourceURL = source
			return r, nil
		} else {
			err = e
		}
	}
	mobile := "https://v.m.chenzhongtech.com/fw/photo/" + url.PathEscape(id)
	h := majorHeaders("", "https://www.kuaishou.com/", mobile)
	h["User-Agent"] = majorMobileUA
	if b, _, e := Get(ctx, c, mobile, h); e == nil {
		if r, e := majorKSPage(b, id); e == nil {
			r.SourceURL = source
			return r, nil
		} else {
			err = e
		}
	}
	// The public native GraphQL route serves videos when the share route is incomplete.
	if majorCookie(cookie, "did") == "" {
		cookie = majorSetCookie(cookie, "did", "web_"+model.NewID())
	}
	endpoint := "https://www.kuaishou.com/graphql"
	body := map[string]any{"operationName": "visionVideoDetail", "variables": map[string]any{"photoId": id, "page": "detail"}, "query": `query visionVideoDetail($photoId: String, $page: String) { visionVideoDetail(photoId: $photoId, page: $page) { status type author { name } photo { id caption duration coverUrl photoUrl videoResource manifest { adaptationSet { duration representation { url backupUrl width height codecs } } } } } }`}
	var response map[string]any
	if e := majorPost(ctx, c, endpoint, majorHeaders(cookie, "https://www.kuaishou.com/short-video/"+url.PathEscape(id), endpoint, "kuaishou.com"), body, &response); e != nil {
		return nil, fmt.Errorf("快手分享页/API 均未返回媒体，可能需要登录或验证码: %w", e)
	}
	if errors := Array(response["errors"]); len(errors) > 0 {
		return nil, fmt.Errorf("快手原生 API 拒绝访问，需要有效登录 Cookie 或完成验证码")
	}
	detail := Obj(Path(response, "data", "visionVideoDetail"))
	photo := Obj(detail["photo"])
	if len(photo) == 0 {
		if err != nil {
			return nil, fmt.Errorf("快手作品不可见、已删除或触发风控: %w", err)
		}
		return nil, fmt.Errorf("快手作品不可见、已删除或需要登录")
	}
	r, e := majorKSPhoto(photo, nil)
	if e != nil {
		return nil, e
	}
	r.Author = Text(Path(detail, "author", "name"))
	r.SourceURL = source
	return r, nil
}
func majorKSDeref(v any, client map[string]any, depth int) any {
	if depth > 16 {
		return nil
	}
	switch x := v.(type) {
	case map[string]any:
		ref := Text(x["__ref"])
		if ref == "" && Text(x["type"]) == "id" {
			ref = Text(x["id"])
		}
		if ref != "" {
			if target, ok := client[ref]; ok {
				return majorKSDeref(target, client, depth+1)
			}
			return nil
		}
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[k] = majorKSDeref(value, client, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = majorKSDeref(value, client, depth+1)
		}
		return out
	default:
		return v
	}
}
func majorKSPage(page []byte, id string) (*model.ParseResult, error) {
	for _, state := range majorStates(page, "window.__APOLLO_STATE__", "window.INIT_STATE", "window.__INITIAL_STATE__") {
		client := Obj(Path(state, "defaultClient"))
		if len(client) == 0 {
			client = Obj(state)
		}
		if selected, ok := client["VisionVideoDetailPhoto:"+id]; ok {
			photo := Obj(majorKSDeref(selected, client, 0))
			if selectedID := majorKSPhotoID(photo); selectedID != "" && selectedID != id {
				return nil, fmt.Errorf("快手作品 ID 与请求不匹配")
			}
			if r, err := majorKSPhoto(photo, nil); err == nil {
				return r, nil
			}
			continue
		}
		var photo, payload map[string]any
		Walk(state, func(m map[string]any) {
			if photo != nil {
				return
			}
			if p := Obj(m["photo"]); majorKSPhotoID(p) == id {
				photo = p
				payload = m
				return
			}
			if majorKSPhotoID(m) == id && (m["photoUrl"] != nil || m["mainMvUrls"] != nil || m["manifest"] != nil || m["videoResource"] != nil || m["ext_params"] != nil) {
				photo = m
			}
		})
		if photo == nil {
			continue
		}
		photo = Obj(majorKSDeref(photo, client, 0))
		payload = map[string]any{"atlas": majorKSDeref(payload["atlas"], client, 0)}
		if r, err := majorKSPhoto(photo, payload); err == nil {
			return r, nil
		}
	}
	return nil, fmt.Errorf("快手分享页未返回完整视频/图集，可能需要登录、验证码或作品不可见")
}
func majorKSPhotoID(photo map[string]any) string {
	return majorFirstText(photo["id"], photo["photoId"], photo["photo_id"])
}
func majorKSPhoto(photo, payload map[string]any) (*model.ParseResult, error) {
	r := &model.ParseResult{Platform: "kuaishou", Title: Text(photo["caption"]), Content: Text(photo["caption"]), Headers: majorMediaHeaders("https://www.kuaishou.com/")}
	atlas := Obj(Path(photo, "ext_params", "atlas"))
	if len(Array(atlas["list"])) == 0 {
		atlas = Obj(payload["atlas"])
	}
	if items := Array(atlas["list"]); len(items) > 0 {
		cdn := Text(atlas["cdn"])
		if cdn == "" {
			if values := Array(atlas["cdn"]); len(values) > 0 {
				cdn = Text(values[0])
			}
		}
		if cdn == "" {
			cdn = majorFirstText(Path(atlas, "cdnList", "0", "cdn"))
			if a := Array(atlas["cdnList"]); len(a) > 0 {
				cdn = Text(Path(a[0], "cdn"))
			}
		}
		if cdn != "" && !strings.HasPrefix(cdn, "http") {
			cdn = "https://" + strings.TrimPrefix(cdn, "//")
		}
		sizes := Array(atlas["size"])
		for i, item := range items {
			uri := majorURL(item)
			if uri == "" {
				uri = strings.TrimRight(cdn, "/") + "/" + strings.TrimLeft(Text(item), "/")
			}
			if majorURL(uri) == "" {
				return nil, fmt.Errorf("快手图集第 %d 张图片缺少 CDN 地址", i+1)
			}
			m := model.Media{Kind: "image", URL: uri, Headers: r.Headers}
			if i < len(sizes) {
				m.Width = int(Number(Path(sizes[i], "w")))
				m.Height = int(Number(Path(sizes[i], "h")))
			}
			r.Media = append(r.Media, m)
		}
		majorResultType(r)
		return r, nil
	}
	cover := majorFirstText(majorURL(photo["coverUrl"]), majorURL(photo["coverUrls"]), majorURL(photo["webpCoverUrls"]))
	m := model.Media{Kind: "video", URL: majorFirstText(majorURL(photo["photoUrl"]), majorURL(photo["mainMvUrls"]), majorURL(photo["photoUrls"])), ThumbnailURL: cover, Duration: Number(photo["duration"]) / 1000, Headers: r.Headers}
	manifests := []any{Path(photo, "videoResource", "h264"), photo["manifest"], photo["manifestH265"]}
	for _, value := range manifests {
		manifest := majorObject(value)
		for _, setValue := range Array(manifest["adaptationSet"]) {
			set := Obj(setValue)
			var best map[string]any
			score := -1.0
			for _, v := range Array(set["representation"]) {
				rep := Obj(v)
				if majorURL(rep) == "" {
					continue
				}
				n := Number(rep["width"]) * Number(rep["height"])
				if n > score {
					best = rep
					score = n
				}
			}
			if best != nil {
				m.URL = majorURL(best)
				m.BackupURLs = majorURLs(best["backupUrl"])
				m.Width = int(Number(best["width"]))
				m.Height = int(Number(best["height"]))
				if d := Number(set["duration"]); d > 0 {
					m.Duration = d
				}
				break
			}
		}
		if m.Width > 0 {
			break
		}
	}
	if m.URL == "" {
		return nil, fmt.Errorf("快手没有视频播放地址；封面不能作为视频返回")
	}
	r.Media = []model.Media{m}
	r.Type = "video"
	return r, nil
}
