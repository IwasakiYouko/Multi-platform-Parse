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
	"multiparse/internal/netx"
)

func init() { Register("kuaishou", majorKuaishou) }
func majorKSID(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	for _, key := range []string{"photoId", "vid", "id", "modal_id", "v", "s", "pid"} {
		if s := u.Query().Get(key); s != "" {
			return s
		}
	}
	return strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".html")
}
func majorKSLocation(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	for _, domain := range []string{"kuaishou.com", "chenzhongtech.com", "gifshow.com"} {
		if u.Hostname() == domain || strings.HasSuffix(u.Hostname(), "."+domain) {
			return nil
		}
	}
	return fmt.Errorf("快手分享链接没有指向站内作品")
}
func majorKSHeaders(cookie, raw string, light bool) map[string]string {
	h := majorHeaders(cookie, "https://www.kuaishou.com/", raw, "kuaishou.com", "chenzhongtech.com", "gifshow.com")
	h["Content-Type"] = "application/json; charset=UTF-8"
	h["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36"
	if light {
		delete(h, "Content-Type")
		h["User-Agent"] = "Mozilla/5.0"
		h["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	}
	return h
}
func majorKuaishou(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithExplicitCookies(ctx)
	if err := majorKSLocation(raw); err != nil {
		return nil, err
	}
	source, id := raw, majorKSID(raw)
	if id == "" || id == "." || id == "/" {
		return nil, fmt.Errorf("快手分享链接缺少作品 ID")
	}
	forceVideo := strings.Contains(raw, "/short-video/")
	var unavailable error
	var stages []referenceStage
	for _, mobile := range []bool{false, true} {
		if mobile && strings.Contains(raw, "/fw/photo/") {
			continue
		}
		for _, light := range []bool{false, true} {
			name := "HTML"
			if mobile {
				name = "移动页"
			}
			if light {
				name += " 精简请求头"
			}
			stages = append(stages, referenceStage{Name: name, Run: func() (*model.ParseResult, error) {
				candidate := raw
				if mobile {
					candidate = "https://v.m.chenzhongtech.com/fw/photo/" + url.PathEscape(id)
				}
				page, final, err := Get(ctx, c, candidate, majorKSHeaders(cookie, candidate, light))
				if err != nil {
					return nil, err
				}
				if err = majorKSLocation(final); err != nil {
					return nil, err
				}
				// Only share redirects resolve IDs; a detail URL may not silently switch works.
				resolved := majorKSID(final)
				if resolved != "" && resolved != id {
					u, _ := url.Parse(raw)
					if strings.HasPrefix(u.Hostname(), "v.") && !strings.Contains(u.Path, "/fw/photo/") || strings.Contains(u.Path, "/f/") {
						id = resolved
					} else {
						return nil, fmt.Errorf("快手重定向作品 ID 与请求不匹配")
					}
				}
				r, err := majorKSPageMode(page, id, forceVideo)
				if referenceUnavailable(err) {
					unavailable = err
				}
				if err != nil {
					return nil, err
				}
				r.SourceURL = source
				return r, nil
			}})
		}
	}
	stages = append(stages, referenceStage{Name: "API", Run: func() (*model.ParseResult, error) {
		if unavailable != nil {
			return nil, unavailable
		}
		// This is an anonymous device ID, never an embedded upstream login cookie.
		apiCookie := cookie
		if majorCookie(apiCookie, "did") == "" {
			apiCookie = majorSetCookie(apiCookie, "did", "web_"+model.NewID())
		}
		endpoint := "https://www.kuaishou.com/graphql"
		body := map[string]any{"operationName": "visionVideoDetail", "variables": map[string]any{"photoId": id, "page": "search"}, "query": majorKSDetailQuery}
		var response map[string]any
		if err := majorPost(ctx, c, endpoint, majorKSHeaders(apiCookie, endpoint, false), body, &response); err != nil {
			return nil, err
		}
		if len(Array(response["errors"])) > 0 || Number(Path(response, "data", "result")) == 400002 {
			return nil, fmt.Errorf("快手原生 API 拒绝访问，需要有效登录 Cookie 或完成验证码")
		}
		detail := Obj(Path(response, "data", "visionVideoDetail"))
		photo := Obj(detail["photo"])
		if len(photo) == 0 {
			return nil, fmt.Errorf("快手原生 API 未返回作品")
		}
		if majorKSPhotoID(photo) != id {
			return nil, fmt.Errorf("快手 API 作品 ID 与请求不匹配")
		}
		r, err := majorKSPhotoMode(photo, map[string]any{"api": true}, true, false)
		if err != nil {
			return nil, err
		}
		r.Author = Text(Path(detail, "author", "name"))
		r.SourceURL = source
		return r, nil
	}})
	r, err := referenceFallback(stages, false)
	if err != nil && unavailable != nil {
		return nil, unavailable
	}
	return r, err
}

const majorKSDetailQuery = `query visionVideoDetail($photoId: String, $page: String) { visionVideoDetail(photoId: $photoId, page: $page) { status type author { id name } photo { id caption duration coverUrl photoUrl manifest { adaptationSet { duration representation { id url backupUrl width height codecs m3u8Slice qualityType qualityLabel } } } manifestH265 photoH265Url videoResource } } }`

func majorKSDeref(v any, client map[string]any, depth int) any {
	if depth > 32 {
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
	return majorKSPageMode(page, id, false)
}
func majorKSPageMode(page []byte, id string, forceVideo bool) (*model.ParseResult, error) {
	var last error
	var unavailable error
	for _, marker := range []string{"window.__APOLLO_STATE__", "window.INIT_STATE", "window.__INITIAL_STATE__"} {
		state, err := DecodeState(page, marker)
		if err != nil {
			continue
		}
		client := Obj(Path(state, "defaultClient"))
		if len(client) == 0 {
			client = Obj(state)
		}
		resolved := majorKSDeref(state, client, 0)
		Walk(resolved, func(m map[string]any) {
			code := Number(m["result"])
			if (code == 223 || code == 20113) && Text(m["error_msg"]) != "" {
				unavailable = &ContentUnavailableError{Reason: fmt.Sprintf("快手 %.0f %s", code, Text(m["error_msg"]))}
			}
		})
		if selected, ok := client["VisionVideoDetailPhoto:"+id]; ok {
			photo := Obj(majorKSDeref(selected, client, 0))
			if selectedID := majorKSPhotoID(photo); selectedID != "" && selectedID != id {
				return nil, fmt.Errorf("快手作品 ID 与请求不匹配")
			}
			// Only representations reachable from this photo are safe. A global
			// VisionVideoSetRepresentation:1 may belong to a recommendation.
			if r, e := majorKSPhotoMode(photo, nil, true, false); e == nil {
				return r, nil
			} else {
				last = e
			}
		}
		var candidates []struct{ photo, payload map[string]any }
		Walk(resolved, func(m map[string]any) {
			if p := Obj(m["photo"]); majorKSPhotoID(p) == id {
				candidates = append(candidates, struct{ photo, payload map[string]any }{p, m})
				return
			}
			if majorKSPhotoID(m) == id && (m["photoUrl"] != nil || m["mainMvUrls"] != nil || m["manifest"] != nil || m["manifestH265"] != nil || m["videoResource"] != nil || m["ext_params"] != nil) {
				candidates = append(candidates, struct{ photo, payload map[string]any }{m, nil})
			}
		})
		for _, candidate := range candidates {
			video := forceVideo || marker == "window.__APOLLO_STATE__"
			if r, e := majorKSPhotoMode(candidate.photo, candidate.payload, video, !video); e == nil {
				return r, nil
			} else {
				last = e
			}
		}
	}
	if unavailable != nil {
		return nil, unavailable
	}
	if last != nil {
		return nil, last
	}
	return nil, fmt.Errorf("快手分享页未返回请求作品的完整视频/图集，可能需要登录或验证码")
}
func majorKSPhotoID(photo map[string]any) string {
	return majorFirstText(photo["id"], photo["photoId"], photo["photo_id"])
}
func majorKSPhoto(photo, payload map[string]any) (*model.ParseResult, error) {
	return majorKSPhotoMode(photo, payload, false, false)
}

func majorKSAtlas(photo, payload map[string]any) map[string]any {
	variants := []map[string]any{majorObject(Path(photo, "ext_params", "atlas")), Obj(payload["atlas"])}
	var first map[string]any
	for _, atlas := range variants {
		if len(Array(atlas["list"])) == 0 {
			continue
		}
		if first == nil {
			first = atlas
		}
		for _, item := range Array(atlas["list"]) {
			if strings.HasSuffix(strings.ToLower(Text(item)), ".webp") {
				return atlas
			}
		}
	}
	return first
}
func majorKSRepresentation(rep, set map[string]any, headers map[string]string) (model.Media, bool) {
	urls := majorURLs(rep["backupUrl"])
	primary := majorURL(rep["url"])
	if primary == "" && len(urls) > 0 {
		primary = urls[0]
		urls = urls[1:]
	}
	if primary == "" {
		for _, line := range strings.Split(Text(rep["m3u8Slice"]), "\n") {
			if uri := majorURL(strings.TrimSpace(line)); uri != "" {
				primary = uri
				break
			}
		}
	}
	if primary == "" {
		return model.Media{}, false
	}
	return model.Media{Kind: "video", URL: primary, BackupURLs: urls, Width: int(Number(rep["width"])), Height: int(Number(rep["height"])), Duration: Number(set["duration"]), Headers: headers}, true
}
func majorKSPhotoMode(photo, payload map[string]any, forceVideo, allowImageCover bool) (*model.ParseResult, error) {
	r := &model.ParseResult{Platform: "kuaishou", Title: Text(photo["caption"]), Content: Text(photo["caption"]), Headers: majorMediaHeaders("https://www.kuaishou.com/")}
	cover := majorFirstText(majorURL(photo["coverUrl"]), majorURL(photo["coverUrls"]), majorURL(photo["webpCoverUrls"]))
	if !forceVideo {
		atlas := majorKSAtlas(photo, payload)
		if items := Array(atlas["list"]); len(items) > 0 {
			cdn := Text(atlas["cdn"])
			if cdn == "" {
				if values := Array(atlas["cdn"]); len(values) > 0 {
					cdn = Text(values[0])
				}
			}
			if cdn == "" {
				for _, v := range Array(atlas["cdnList"]) {
					if cdn = Text(Obj(v)["cdn"]); cdn != "" {
						break
					}
				}
			}
			if cdn != "" && !strings.HasPrefix(cdn, "http://") && !strings.HasPrefix(cdn, "https://") {
				cdn = "https://" + strings.TrimPrefix(cdn, "//")
			}
			sizes := Array(atlas["size"])
			for i, item := range items {
				uri := majorURL(item)
				if uri == "" && cdn != "" {
					if p, ok := item.(string); ok && p != "" {
						uri = strings.TrimRight(cdn, "/") + "/" + strings.TrimLeft(p, "/")
					}
				}
				if majorURL(uri) == "" {
					return nil, fmt.Errorf("快手图集第 %d 张图片缺少 CDN 地址", i+1)
				}
				kind := "image"
				if u, e := url.Parse(uri); e == nil && strings.HasSuffix(strings.ToLower(u.Path), ".gif") {
					kind = "gif"
				}
				m := model.Media{Kind: kind, URL: uri, Headers: r.Headers}
				if len(sizes) == len(items) {
					m.Width = int(Number(Path(sizes[i], "w")))
					m.Height = int(Number(Path(sizes[i], "h")))
				}
				r.Media = append(r.Media, m)
			}
			referenceResultType(r)
			return r, nil
		}
	}
	direct := majorFirstText(majorURL(photo["photoUrl"]), majorURL(photo["mainMvUrls"]), majorURL(photo["photoUrls"]))
	m := model.Media{Kind: "video", URL: direct, ThumbnailURL: cover, Duration: Number(photo["duration"]) / 1000, Headers: r.Headers}
	manifests := []any{Path(photo, "videoResource", "h264"), photo["manifest"], photo["manifestH265"]}
	if api, _ := payload["api"].(bool); api {
		manifests = []any{photo["manifestH265"], Path(photo, "videoResource", "h264"), photo["manifest"]}
	}
	selected := false
	for _, value := range manifests {
		for _, setValue := range Array(majorObject(value)["adaptationSet"]) {
			set := Obj(setValue)
			for _, rep := range Array(set["representation"]) {
				if candidate, ok := majorKSRepresentation(Obj(rep), set, r.Headers); ok {
					m = candidate
					m.ThumbnailURL = cover
					selected = true
					break
				}
			}
			if selected {
				break
			}
		}
		if selected {
			break
		}
	}
	if m.URL == "" {
		if allowImageCover && cover != "" {
			r.Media = []model.Media{{Kind: "image", URL: cover, Headers: r.Headers}}
			r.Type = "image"
			return r, nil
		}
		return nil, fmt.Errorf("快手没有视频播放地址；封面不能作为视频返回")
	}
	r.Media = []model.Media{m}
	r.Type = "video"
	return r, nil
}
