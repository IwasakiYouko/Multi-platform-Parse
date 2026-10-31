// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"multiparse/internal/model"
)

func init() { Register("douyin", majorDouyin) }

var majorDouyinID = regexp.MustCompile(`/(?:share/)?(?:video|note|slides)/([0-9]+)`)

func majorDouyinFindID(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	if m := majorDouyinID.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	for _, k := range []string{"modal_id", "vid", "aweme_id"} {
		if s := u.Query().Get(k); s != "" {
			return s
		}
	}
	return ""
}
func majorDouyin(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	source := raw
	headers := majorHeaders(cookie, "https://www.douyin.com/", raw, "douyin.com", "iesdouyin.com")
	headers["User-Agent"] = majorMobileUA
	page, final, requestErr := Get(ctx, c, raw, headers)
	if requestErr == nil {
		u, e := url.Parse(final)
		if e != nil {
			return nil, e
		}
		host := u.Hostname()
		if host != "douyin.com" && !strings.HasSuffix(host, ".douyin.com") && host != "iesdouyin.com" && !strings.HasSuffix(host, ".iesdouyin.com") {
			return nil, fmt.Errorf("抖音短链没有指向站内作品")
		}
		raw = final
	}
	id := majorDouyinFindID(raw)
	if id == "" {
		return nil, fmt.Errorf("抖音链接没有有效作品 ID")
	}
	if requestErr == nil {
		if detail := majorDouyinState(page, id); detail != nil {
			r, e := majorDouyinDetail(detail)
			if e != nil {
				return nil, e
			}
			r.SourceURL = source
			return r, nil
		}
	}
	// The mobile share endpoint exposes videoInfoRes even when the desktop route is a JS shell.
	share := "https://www.iesdouyin.com/share/video/" + url.PathEscape(id) + "/"
	h := majorHeaders(cookie, "https://www.douyin.com/", share, "douyin.com", "iesdouyin.com")
	h["User-Agent"] = majorMobileUA
	if b, _, e := Get(ctx, c, share, h); e == nil {
		if detail := majorDouyinState(b, id); detail != nil {
			r, e := majorDouyinDetail(detail)
			if e != nil {
				return nil, e
			}
			r.SourceURL = source
			return r, nil
		}
	}
	params := url.Values{
		"device_platform": {"webapp"}, "aid": {"6383"}, "channel": {"channel_pc_web"}, "pc_client_type": {"1"}, "version_code": {"290100"}, "version_name": {"29.1.0"}, "cookie_enabled": {"true"}, "screen_width": {"1920"}, "screen_height": {"1080"}, "browser_language": {"zh-CN"}, "browser_platform": {"Win32"}, "browser_name": {"Chrome"}, "browser_version": {"122.0.0.0"}, "browser_online": {"true"}, "engine_name": {"Blink"}, "engine_version": {"122.0.0.0"}, "os_name": {"Windows"}, "os_version": {"10"}, "cpu_core_num": {"12"}, "device_memory": {"8"}, "platform": {"PC"}, "downlink": {"10"}, "effective_type": {"4g"}, "aweme_id": {id},
	}
	if token := majorCookie(cookie, "msToken"); token != "" {
		params.Set("msToken", token)
	}
	query, signHeaders, err := majorDouyinSignedQuery(params, cookie)
	if err != nil {
		return nil, err
	}
	endpoint := "https://www.douyin.com/aweme/v1/web/aweme/detail/?" + query
	h = majorHeaders(cookie, "https://www.douyin.com/", endpoint, "douyin.com")
	h["User-Agent"] = majorDouyinUA
	for k, v := range signHeaders {
		h[k] = v
	}
	var response map[string]any
	if err = JSON(ctx, c, endpoint, h, &response); err != nil {
		return nil, fmt.Errorf("抖音分享页未返回详情，签名 API 请求失败；请提供有效登录 Cookie（含 UIFID_TEMP）: %s", majorSafeRequestError(err))
	}
	if code := Number(response["status_code"]); code != 0 {
		return nil, fmt.Errorf("抖音 API 拒绝访问: code %.0f %s", code, Text(response["status_msg"]))
	}
	detail := Obj(response["aweme_detail"])
	if len(detail) == 0 {
		if filter := Obj(response["filter_detail"]); len(filter) > 0 {
			return nil, fmt.Errorf("抖音作品不可见: %s %s", Text(filter["filter_reason"]), majorFirstText(filter["detail_msg"], filter["notice"]))
		}
		return nil, fmt.Errorf("抖音未返回作品详情：作品可能已删除、私密，或需要有效登录 Cookie（含 UIFID_TEMP）")
	}
	r, err := majorDouyinDetail(detail)
	if err != nil {
		return nil, err
	}
	r.SourceURL = source
	return r, nil
}
func majorDouyinState(page []byte, id string) map[string]any {
	for _, state := range majorStates(page, "window._ROUTER_DATA", "_ROUTER_DATA", "window.__ROUTER_DATA__", "window.__INITIAL_STATE__", "_SSR_HYDRATED_DATA") {
		var found map[string]any
		Walk(state, func(m map[string]any) {
			if found != nil {
				return
			}
			mid := majorFirstText(m["aweme_id"], m["awemeId"])
			if mid != "" && mid != id {
				return
			}
			if (mid == id || m["desc"] != nil) && (m["video"] != nil || m["images"] != nil || m["image_post_info"] != nil || m["imagePostInfo"] != nil) {
				found = m
			}
		})
		if found != nil {
			return found
		}
	}
	return nil
}
func majorDouyinVideo(video map[string]any, headers map[string]string) (model.Media, error) {
	rates := Array(video["bit_rate"])
	if len(rates) == 0 {
		rates = Array(video["bitRateList"])
	}
	best := Obj(video["play_addr"])
	if len(best) == 0 {
		best = Obj(video["playAddr"])
	}
	score := -1.0
	for _, v := range rates {
		rate := Obj(v)
		addr := Obj(rate["play_addr"])
		if len(addr) == 0 {
			addr = Obj(rate["playAddr"])
		}
		if majorURL(addr) == "" {
			continue
		}
		n := Number(addr["width"]) * Number(addr["height"])
		if Number(rate["is_bytevc1"]) == 0 && Number(rate["is_h265"]) == 0 {
			n += 1e12
		}
		if n > score {
			score = n
			best = addr
		}
	}
	urls := majorURLs(best["url_list"])
	if len(urls) == 0 {
		urls = majorURLs(best["urlList"])
	}
	if len(urls) == 0 {
		if direct := majorURL(best); direct != "" {
			urls = []string{direct}
		}
	}
	if len(urls) == 0 {
		return model.Media{}, fmt.Errorf("抖音视频没有播放地址；封面不是视频")
	}
	for i, raw := range urls {
		u, err := url.Parse(raw)
		if err == nil {
			u.Path = strings.ReplaceAll(u.Path, "/playwm/", "/play/")
			urls[i] = u.String()
		}
	}
	cover := majorURL(video["origin_cover"])
	if cover == "" {
		cover = majorURL(video["cover"])
	}
	m := model.Media{Kind: "video", URL: urls[0], BackupURLs: urls[1:], ThumbnailURL: cover, Width: int(Number(best["width"])), Height: int(Number(best["height"])), Duration: Number(video["duration"]) / 1000, Headers: headers}
	if m.Width == 0 {
		m.Width = int(Number(video["width"]))
	}
	if m.Height == 0 {
		m.Height = int(Number(video["height"]))
	}
	return m, nil
}
func majorDouyinDetail(detail map[string]any) (*model.ParseResult, error) {
	r := &model.ParseResult{Platform: "douyin", Title: Text(detail["desc"]), Content: Text(detail["desc"]), Author: Text(Path(detail, "author", "nickname")), Headers: majorMediaHeaders("https://www.douyin.com/")}
	images := Array(detail["images"])
	if len(images) == 0 {
		images = Array(Path(detail, "image_post_info", "images"))
	}
	if len(images) == 0 {
		images = Array(Path(detail, "imagePostInfo", "images"))
	}
	if len(images) > 0 {
		for index, v := range images {
			image := Obj(v)
			display := Obj(image["display_image"])
			if len(display) == 0 {
				display = Obj(image["displayImage"])
			}
			if len(display) == 0 {
				display = image
			}
			uri := majorURL(display)
			video := Obj(image["video"])
			if uri != "" {
				r.Media = append(r.Media, model.Media{Kind: "image", URL: uri, Width: int(Number(display["width"])), Height: int(Number(display["height"])), Headers: r.Headers})
			}
			if len(video) > 0 {
				m, e := majorDouyinVideo(video, r.Headers)
				if e != nil {
					return nil, fmt.Errorf("抖音第 %d 张实况照片: %w", index+1, e)
				}
				if m.ThumbnailURL == "" {
					m.ThumbnailURL = uri
				}
				r.Media = append(r.Media, m)
			} else if uri == "" {
				return nil, fmt.Errorf("抖音第 %d 张图片缺少地址", index+1)
			}
		}
	} else {
		video := Obj(detail["video"])
		if len(video) == 0 {
			return nil, fmt.Errorf("抖音作品没有视频或图片，可能已删除或需要登录")
		}
		m, e := majorDouyinVideo(video, r.Headers)
		if e != nil {
			return nil, e
		}
		r.Media = append(r.Media, m)
	}
	if len(r.Media) == 0 {
		return nil, fmt.Errorf("抖音作品没有可下载媒体")
	}
	majorResultType(r)
	return r, nil
}
