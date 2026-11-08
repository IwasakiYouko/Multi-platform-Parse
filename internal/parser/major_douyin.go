// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("douyin", majorDouyin) }

var majorDouyinID = regexp.MustCompile(`/(?:share/)?(?:video|note|slides)/([0-9]+)`)

const majorDouyinAppUA = "com.ss.android.ugc.aweme/390500 (Linux; U; Android 13; zh_CN; Pixel 6; Build/TQ3A.230805.001; Cronet/TTNetVersion:6b6f6e6e 2024-04-10 QuicVersion:47946d2a 2024-03-28)"

type majorDouyinDevice struct {
	deviceID string
	iid      string
	cdid     string
	openudid string
}

var (
	majorDouyinPoolMu  sync.Mutex
	majorDouyinPool    []majorDouyinDevice
	majorDouyinPoolIdx int
)

func majorDouyinFindID(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	if m := majorDouyinID.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	for _, k := range []string{"modal_id", "vid", "aweme_id", "item_ids"} {
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
	var webErr error
	if strings.TrimSpace(cookie) != "" {
		if r, err := majorDouyinWeb(ctx, c, id, cookie); err == nil {
			r.SourceURL = source
			return r, nil
		} else {
			webErr = err
		}
	}
	if r, err := majorDouyinMobile(ctx, c, id); err == nil {
		r.SourceURL = source
		return r, nil
	} else if webErr == nil {
		if r, wErr := majorDouyinWeb(ctx, c, id, cookie); wErr == nil {
			r.SourceURL = source
			return r, nil
		} else {
			webErr = wErr
		}
		if strings.Contains(err.Error(), "作品不可见") {
			return nil, err
		}
	}
	return nil, webErr
}

func majorDouyinWeb(ctx context.Context, c *http.Client, id, cookie string) (*model.ParseResult, error) {
	params := url.Values{
		"device_platform": {"webapp"}, "aid": {"6383"}, "channel": {"channel_pc_web"}, "pc_client_type": {"1"}, "version_code": {"290100"}, "version_name": {"29.1.0"}, "cookie_enabled": {"true"}, "screen_width": {"1920"}, "screen_height": {"1080"}, "browser_language": {"zh-CN"}, "browser_platform": {"Win32"}, "browser_name": {"Chrome"}, "browser_version": {"130.0.0.0"}, "browser_online": {"true"}, "engine_name": {"Blink"}, "engine_version": {"130.0.0.0"}, "os_name": {"Windows"}, "os_version": {"10"}, "cpu_core_num": {"12"}, "device_memory": {"8"}, "platform": {"PC"}, "downlink": {"10"}, "effective_type": {"4g"}, "aweme_id": {id},
	}
	if token := majorCookie(cookie, "msToken"); token != "" {
		params.Set("msToken", token)
	}
	query, signHeaders, err := majorDouyinSignedQuery(params, cookie)
	if err != nil {
		return nil, err
	}
	endpoint := "https://www.douyin.com/aweme/v1/web/aweme/detail/?" + query
	h := majorHeaders(cookie, "https://www.douyin.com/", endpoint, "douyin.com")
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
	return majorDouyinDetail(detail)
}

func majorDouyinRandDigits19() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	v := (binary.LittleEndian.Uint64(b[:]) % 8999999999999999999) + 1000000000000000000
	return strconv.FormatUint(v, 10)
}
func majorDouyinRandHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
func majorDouyinEnvDevice() (majorDouyinDevice, bool) {
	devID := strings.TrimSpace(os.Getenv("PARSEHUB_DOUYIN_DEVICE_ID"))
	if devID == "" {
		devID = strings.TrimSpace(os.Getenv("DOUYIN_DEVICE_ID"))
	}
	iid := strings.TrimSpace(os.Getenv("PARSEHUB_DOUYIN_IID"))
	if iid == "" {
		iid = strings.TrimSpace(os.Getenv("PARSEHUB_DOUYIN_INSTALL_ID"))
	}
	if iid == "" {
		iid = strings.TrimSpace(os.Getenv("DOUYIN_IID"))
	}
	if devID == "" || iid == "" {
		return majorDouyinDevice{}, false
	}
	cdid := strings.TrimSpace(os.Getenv("PARSEHUB_DOUYIN_CDID"))
	if cdid == "" {
		cdid = strings.TrimSpace(os.Getenv("DOUYIN_CDID"))
	}
	openudid := strings.TrimSpace(os.Getenv("PARSEHUB_DOUYIN_OPENUDID"))
	if openudid == "" {
		openudid = strings.TrimSpace(os.Getenv("DOUYIN_OPENUDID"))
	}
	return majorDouyinDevice{deviceID: devID, iid: iid, cdid: cdid, openudid: openudid}, true
}
func majorDouyinRegisterDevice(ctx context.Context, c *http.Client) (majorDouyinDevice, error) {
	reqCtx := netx.WithoutCookies(ctx)
	hosts := []string{"log.snssdk.com", "api.amemv.com"}
	var lastErr error
	for _, host := range hosts {
		if err := reqCtx.Err(); err != nil {
			return majorDouyinDevice{}, err
		}
		cdid, err := mainlandUUID()
		if err != nil {
			return majorDouyinDevice{}, err
		}
		clientUDID, err := mainlandUUID()
		if err != nil {
			return majorDouyinDevice{}, err
		}
		reqID, err := mainlandUUID()
		if err != nil {
			return majorDouyinDevice{}, err
		}
		openudid := majorDouyinRandHex(8)
		now := time.Now()
		v := url.Values{
			"aid": {"1128"}, "app_name": {"aweme"}, "version_code": {"390500"}, "version_name": {"39.5.0"},
			"device_platform": {"android"}, "os": {"android"}, "os_version": {"13"}, "ssmix": {"a"},
			"language": {"zh"}, "channel": {"wandoujia_aweme"}, "device_type": {"Pixel 6"},
			"device_brand": {"google"}, "resolution": {"1080*2400"}, "dpi": {"420"},
			"host_abi": {"arm64-v8a"}, "manifest_version_code": {"390500"}, "update_version_code": {"390500"},
			"ac": {"wifi"}, "app_type": {"normal"}, "cpu_support64": {"true"},
			"_rticket": {strconv.FormatInt(now.UnixMilli(), 10)}, "cdid": {cdid},
			"ts": {strconv.FormatInt(now.Unix(), 10)}, "iid": {majorDouyinRandDigits19()},
			"device_id": {majorDouyinRandDigits19()}, "openudid": {openudid},
		}
		query := v.Encode()
		headers := majorDouyinMobileSign(query, 8404)
		headers["User-Agent"] = majorDouyinAppUA
		headers["x-tt-trace-id"] = majorDouyinTraceID(v.Get("device_id"))
		headers["sdk-version"] = "2"
		headers["passport-sdk-version"] = "203226"
		headers["Content-Type"] = "application/json; charset=utf-8"
		payload := map[string]any{
			"magic_tag": "ss_app_log",
			"header": map[string]any{
				"display_name": "抖音", "aid": 1128, "channel": "wandoujia_aweme",
				"package": "com.ss.android.ugc.aweme", "app_version": "39.5.0",
				"version_code": 390500, "manifest_version_code": 390500, "update_version_code": 390500,
				"sdk_version": "3.9.5", "sdk_target_version": 29, "os": "Android",
				"os_version": "13", "os_api": 33, "device_model": "Pixel 6",
				"device_brand": "google", "device_manufacturer": "Google", "cpu_abi": "arm64-v8a",
				"release_build": "TQ3A.230805.001", "density_dpi": 420, "display_density": "xhdpi",
				"resolution": "1080x2400", "language": "zh", "timezone": 8,
				"region": "CN", "tz_name": "Asia/Shanghai", "cdid": cdid,
				"openudid": openudid, "clientudid": clientUDID, "google_aid": "", "req_id": reqID,
			},
			"_gen_time": now.Unix(),
		}
		body, err := json.Marshal(payload)
		if err != nil {
			lastErr = err
			continue
		}
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "https://"+host+"/service/2/device_register/?"+query, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		for hk, hv := range headers {
			req.Header.Set(hk, hv)
		}
		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("device_register HTTP %d", resp.StatusCode)
			continue
		}
		var res map[string]any
		if err := json.Unmarshal(rawBody, &res); err != nil {
			lastErr = err
			continue
		}
		devID := majorFirstText(res["device_id_str"], res["device_id"])
		iid := majorFirstText(res["install_id_str"], res["install_id"], res["iid"])
		if devID != "" && devID != "0" && iid != "" && iid != "0" {
			return majorDouyinDevice{deviceID: devID, iid: iid, cdid: cdid, openudid: openudid}, nil
		}
		lastErr = fmt.Errorf("invalid device_register response")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("device registration failed")
	}
	return majorDouyinDevice{}, lastErr
}
func majorDouyinSelectDevice(ctx context.Context, c *http.Client) (majorDouyinDevice, bool, error) {
	if dev, ok := majorDouyinEnvDevice(); ok {
		return dev, true, nil
	}
	majorDouyinPoolMu.Lock()
	if len(majorDouyinPool) > 0 {
		dev := majorDouyinPool[majorDouyinPoolIdx%len(majorDouyinPool)]
		majorDouyinPoolIdx = (majorDouyinPoolIdx + 1) % len(majorDouyinPool)
		majorDouyinPoolMu.Unlock()
		return dev, false, nil
	}
	majorDouyinPoolMu.Unlock()
	var registered []majorDouyinDevice
	var lastErr error
	for range 4 {
		if err := ctx.Err(); err != nil {
			return majorDouyinDevice{}, false, err
		}
		dev, err := majorDouyinRegisterDevice(ctx, c)
		if err != nil {
			lastErr = err
			continue
		}
		registered = append(registered, dev)
		if len(registered) >= 2 {
			break
		}
	}
	if len(registered) == 0 {
		return majorDouyinDevice{}, false, lastErr
	}
	majorDouyinPoolMu.Lock()
	for _, d := range registered {
		exists := false
		for _, existing := range majorDouyinPool {
			if existing.deviceID == d.deviceID && existing.iid == d.iid {
				exists = true
				break
			}
		}
		if !exists && len(majorDouyinPool) < 3 {
			majorDouyinPool = append(majorDouyinPool, d)
		}
	}
	dev := majorDouyinPool[majorDouyinPoolIdx%len(majorDouyinPool)]
	majorDouyinPoolIdx = (majorDouyinPoolIdx + 1) % len(majorDouyinPool)
	majorDouyinPoolMu.Unlock()
	return dev, false, nil
}
func majorDouyinEvictDevice(target majorDouyinDevice) {
	majorDouyinPoolMu.Lock()
	defer majorDouyinPoolMu.Unlock()
	filtered := majorDouyinPool[:0]
	for _, d := range majorDouyinPool {
		if d.deviceID != target.deviceID || d.iid != target.iid {
			filtered = append(filtered, d)
		}
	}
	majorDouyinPool = filtered
	if len(majorDouyinPool) > 0 {
		majorDouyinPoolIdx %= len(majorDouyinPool)
	} else {
		majorDouyinPoolIdx = 0
	}
}
func majorDouyinAttachStoryPlay(ctx context.Context, c *http.Client, detail map[string]any) {
	isStory := Number(detail["is_story"]) == 1 || detail["is_story"] == true || Text(detail["is_story"]) == "1" ||
		Number(detail["is_24_story"]) == 1 || detail["is_24_story"] == true || Text(detail["is_24_story"]) == "1"
	if !isStory {
		return
	}
	video := Obj(detail["video"])
	if len(video) == 0 {
		return
	}
	videoURI := Text(Path(video, "play_addr", "uri"))
	if videoURI == "" {
		bestRate := -1.0
		for _, item := range Array(video["bit_rate"]) {
			u := Text(Path(item, "play_addr", "uri"))
			br := Number(Path(item, "bit_rate"))
			if u != "" && br >= bestRate {
				videoURI = u
				bestRate = br
			}
		}
	}
	if videoURI == "" {
		return
	}
	bestURL := ""
	var bestLen int64 = -1
	reqCtx := netx.WithoutCookies(ctx)
	for _, ratio := range []string{"default", "1080p", "720p", "540p", "480p"} {
		api := "https://aweme.snssdk.com/aweme/v1/play/?video_id=" + url.QueryEscape(videoURI) + "&ratio=" + ratio + "&line=0"
		req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, api, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", majorUA)
		req.Header.Set("Referer", "https://www.douyin.com/")
		resp, err := c.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.Request == nil || resp.Request.URL == nil {
			continue
		}
		cl, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if bestURL == "" || cl > bestLen {
			bestURL = resp.Request.URL.String()
			bestLen = cl
		}
	}
	if bestURL == "" {
		return
	}
	synthetic := map[string]any{
		"bit_rate":      0,
		"is_story_play": true,
		"duration":      video["duration"],
		"play_addr": map[string]any{
			"uri":       videoURI,
			"url_list":  []any{bestURL},
			"data_size": bestLen,
			"width":     video["width"],
			"height":    video["height"],
		},
	}
	video["bit_rate"] = append([]any{synthetic}, Array(video["bit_rate"])...)
}
func majorDouyinMobile(ctx context.Context, c *http.Client, id string) (*model.ParseResult, error) {
	reqCtx := netx.WithoutCookies(ctx)
	hosts := []string{"api.amemv.com", "api3-core-c.amemv.com", "aweme.snssdk.com", "api5-normal-lf.amemv.com", "api3-normal-c.amemv.com"}
	versions := []int{8404, 4404}
	var lastErr error
	for range 3 {
		if err := reqCtx.Err(); err != nil {
			return nil, err
		}
		dev, fixed, err := majorDouyinSelectDevice(reqCtx, c)
		if err != nil {
			lastErr = err
			break
		}
		now := time.Now()
		cdid := dev.cdid
		if cdid == "" {
			cdid, err = mainlandUUID()
			if err != nil {
				lastErr = err
				break
			}
		}
		openudid := dev.openudid
		if openudid == "" {
			openudid = majorDouyinRandHex(8)
		}
		v := url.Values{
			"aweme_id": {id}, "aid": {"1128"}, "app_name": {"aweme"}, "version_code": {"390500"},
			"version_name": {"39.5.0"}, "device_platform": {"android"}, "os": {"android"}, "os_version": {"13"},
			"ssmix": {"a"}, "language": {"zh"}, "channel": {"wandoujia_aweme"}, "device_type": {"Pixel 6"},
			"device_brand": {"google"}, "resolution": {"1080*2400"}, "dpi": {"420"}, "host_abi": {"arm64-v8a"},
			"manifest_version_code": {"390500"}, "update_version_code": {"390500"}, "ac": {"wifi"},
			"is_guest_mode": {"0"}, "minor_status": {"0"}, "app_type": {"normal"},
			"_rticket": {strconv.FormatInt(now.UnixMilli(), 10)}, "cdid": {cdid},
			"ts": {strconv.FormatInt(now.Unix(), 10)}, "iid": {dev.iid},
			"device_id": {dev.deviceID}, "openudid": {openudid},
		}
		query := v.Encode()
		replaceDevice := false
		for _, ver := range versions {
			headers := majorDouyinMobileSign(query, ver)
			headers["User-Agent"] = majorDouyinAppUA
			headers["x-tt-trace-id"] = majorDouyinTraceID(dev.deviceID)
			headers["sdk-version"] = "2"
			headers["passport-sdk-version"] = "203226"
			for _, host := range hosts {
				req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "https://"+host+"/aweme/v1/aweme/detail/?"+query, nil)
				if err != nil {
					lastErr = err
					continue
				}
				for hk, hv := range headers {
					req.Header.Set(hk, hv)
				}
				resp, err := c.Do(req)
				if err != nil {
					lastErr = err
					continue
				}
				body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
				resp.Body.Close()
				if err != nil {
					lastErr = err
					continue
				}
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					lastErr = fmt.Errorf("%s returned HTTP %d", host, resp.StatusCode)
					continue
				}
				if len(body) == 0 {
					lastErr = fmt.Errorf("%s returned empty body", host)
					if !fixed {
						majorDouyinEvictDevice(dev)
						replaceDevice = true
						break
					}
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					lastErr = err
					continue
				}
				if detail := Obj(payload["aweme_detail"]); len(detail) > 0 {
					majorDouyinAttachStoryPlay(reqCtx, c, detail)
					return majorDouyinDetail(detail)
				}
				if filter := Obj(payload["filter_detail"]); len(filter) > 0 {
					return nil, fmt.Errorf("抖音作品不可见: %s %s", Text(filter["filter_reason"]), majorFirstText(filter["detail_msg"], filter["notice"]))
				}
				lastErr = fmt.Errorf("missing aweme_detail")
			}
			if replaceDevice {
				break
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("mobile detail request failed")
	}
	return nil, lastErr
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
		if rate["is_story_play"] == true {
			n += 1e15
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
