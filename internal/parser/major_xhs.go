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
	"sort"
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
		// Preserve signed xsec_token and xsec_source parameters unchanged during upstream request.
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
		r.CanonicalURL = final
		return r, nil
	}
	hasCookie := strings.TrimSpace(cookie) != ""
	var stages []referenceStage
	if hasCookie {
		stages = append(stages, referenceStage{Name: "cookie", Run: func() (*model.ParseResult, error) { return extract(cookie, false) }})
	}
	stages = append(stages, referenceStage{Name: "游客 session", Run: func() (*model.ParseResult, error) {
		session, err := majorXHSNewSession(ctx, c)
		if err != nil {
			return nil, err
		}
		return extract(session, false)
	}}, referenceStage{Name: "移动端", Run: func() (*model.ParseResult, error) { return extract("", true) }})
	r, err := referenceFallback(stages, false)
	if err == nil && r != nil && hasCookie && len(r.FallbackErrors) > 0 && r.FallbackErrors[0].Name == "cookie" && isCookieAuthError(r.FallbackErrors[0].Error) {
		r.CookieExpired = true
	}
	return r, err
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

type xhsStreamCandidate struct {
	stream    map[string]any
	url       string
	backups   []string
	pixels    int64
	bitrate   float64
	codecRank int
}

// majorXHSBestStream selects the highest-resolution / highest-bitrate stream across codecs
// and collects backup URLs in descending quality order.
func majorXHSBestStream(v any) (map[string]any, []string) {
	container := Obj(v)
	var candidates []xhsStreamCandidate
	for codecIdx, codec := range []string{"h264", "h265", "av1", "h266"} {
		for _, raw := range Array(container[codec]) {
			s := Obj(raw)
			u := majorURL(s["masterUrl"])
			if u == "" {
				continue
			}
			w, h := int64(Number(s["width"])), int64(Number(s["height"]))
			br := Number(s["videoBitrate"])
			if br == 0 {
				br = Number(s["size"])
			}
			candidates = append(candidates, xhsStreamCandidate{
				stream:    s,
				url:       u,
				backups:   majorURLs(s["backupUrls"]),
				pixels:    w * h,
				bitrate:   br,
				codecRank: len(candidates) + codecIdx*100,
			})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].pixels != candidates[j].pixels {
			return candidates[i].pixels > candidates[j].pixels
		}
		if candidates[i].bitrate != candidates[j].bitrate {
			return candidates[i].bitrate > candidates[j].bitrate
		}
		return candidates[i].codecRank < candidates[j].codecRank
	})
	best := candidates[0]
	var backups []string
	seen := map[string]bool{best.url: true}
	addBackup := func(u string) {
		if u != "" && !seen[u] && len(backups) < 8 {
			seen[u] = true
			backups = append(backups, u)
		}
	}
	for _, b := range best.backups {
		addBackup(b)
	}
	for _, c := range candidates[1:] {
		addBackup(c.url)
		for _, b := range c.backups {
			addBackup(b)
		}
	}
	return best.stream, backups
}

func majorXHSOriginVideoURLs(originKey string, fallbackURL string, fallbackBackups []string) (string, []string) {
	key := strings.TrimLeft(strings.TrimSpace(originKey), "/")
	if key == "" || strings.Contains(key, "..") || strings.ContainsAny(key, " \t\r\n?#") {
		return fallbackURL, fallbackBackups
	}
	primary := "https://sns-video-bd.xhscdn.com/" + key
	seen := map[string]bool{primary: true}
	var backups []string
	add := func(u string) {
		if u != "" && !seen[u] && len(backups) < 8 {
			seen[u] = true
			backups = append(backups, u)
		}
	}
	add("https://sns-video-hw.xhscdn.com/" + key)
	add("https://sns-video-al.xhscdn.com/" + key)
	add(fallbackURL)
	for _, b := range fallbackBackups {
		add(b)
	}
	return primary, backups
}

func majorXHSImage(v any) string {
	image := Obj(v)
	if u := majorFirstText(majorURL(image["urlDefault"]), majorURL(image["url"])); u != "" {
		return u
	}
	for _, item := range Array(image["infoList"]) {
		info := Obj(item)
		if Text(info["imageScene"]) == "WB_DFT" {
			if u := majorURL(info["url"]); u != "" {
				return u
			}
		}
	}
	return ""
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

var (
	majorXHSObjectKey = regexp.MustCompile(`(?:^|/)((?:notes_pre_post/|spectrum/)?1040g[^!/]+)$`)
	majorXHSTrace     = regexp.MustCompile(`/[a-f0-9]{32}/(.+)`)
	majorXHSTimestamp = regexp.MustCompile(`^/[0-9]{8,14}/(.+)`)
	majorXHSTopic     = regexp.MustCompile(` ?#[^#\[\]]+\[话题\]# ?`)
)

func majorXHSExtractImageKey(image map[string]any, raw string) string {
	traceID := strings.Trim(strings.TrimSpace(Text(image["traceId"])), "/")
	if raw != "" && majorURL(raw) != "" {
		if u, err := url.Parse(raw); err == nil {
			cleanPath, _, _ := strings.Cut(u.EscapedPath(), "!")
			if m := majorXHSObjectKey.FindStringSubmatch(cleanPath); len(m) > 1 {
				return m[1]
			}
			if traceID != "" {
				if strings.Contains(cleanPath, "/notes_pre_post/") && !strings.HasPrefix(traceID, "notes_pre_post/") {
					return "notes_pre_post/" + traceID
				}
				if strings.Contains(cleanPath, "/spectrum/") && !strings.HasPrefix(traceID, "spectrum/") {
					return "spectrum/" + traceID
				}
				return traceID
			}
			if m := majorXHSTrace.FindStringSubmatch(cleanPath); len(m) > 1 {
				return m[1]
			}
			if m := majorXHSTimestamp.FindStringSubmatch(cleanPath); len(m) > 1 {
				return strings.TrimLeft(m[1], "/")
			}
			return strings.TrimLeft(cleanPath, "/")
		}
	}
	return traceID
}

func majorXHSRawImage(raw string) string {
	primary, _ := majorXHSRawImageURLs(nil, raw)
	return primary
}

func majorXHSRawImageURLs(image map[string]any, raw string) (string, []string) {
	if raw == "" && image != nil {
		raw = majorXHSImage(image)
	}
	key := majorXHSExtractImageKey(image, raw)
	if key == "" || strings.Contains(key, "..") {
		return "", nil
	}
	primary := "https://ci.xiaohongshu.com/" + key + "?imageView2/2/w/format/png"
	backups := []string{
		"https://sns-img-bd.xhscdn.com/" + key + "?imageView2/2/w/format/png",
		"https://sns-img-hw.xhscdn.com/" + key + "?imageView2/2/w/format/png",
		"https://sns-img-al.xhscdn.com/" + key + "?imageView2/2/w/format/jpg",
	}
	if validRaw := majorURL(raw); validRaw != "" && validRaw != primary {
		backups = append(backups, validRaw)
	}
	return primary, backups
}

func majorXHSParseNote(note map[string]any) (*model.ParseResult, error) {
	var tags []string
	seenTags := map[string]bool{}
	addTag := func(raw string) {
		t := strings.TrimSpace(strings.Trim(strings.TrimSuffix(strings.TrimSpace(raw), "[话题]"), "#"))
		if t == "" {
			return
		}
		k := strings.ToLower(t)
		if !seenTags[k] {
			seenTags[k] = true
			tags = append(tags, t)
		}
	}
	for _, item := range Array(note["tagList"]) {
		addTag(Text(Obj(item)["name"]))
	}
	rawDesc := Text(note["desc"])
	for _, m := range majorXHSTopic.FindAllString(rawDesc, -1) {
		addTag(strings.ReplaceAll(strings.TrimSpace(m), "[话题]#", ""))
	}
	desc := majorXHSTopic.ReplaceAllStringFunc(rawDesc, func(s string) string {
		return strings.ReplaceAll(strings.TrimSpace(s), "[话题]#", "") + " "
	})

	r := &model.ParseResult{
		Platform: "xhs",
		Title:    Text(note["title"]),
		Author:   majorFirstText(Path(note, "user", "nickname"), Path(note, "user", "nickName")),
		Tags:     tags,
		Content:  strings.TrimSpace(desc),
		Headers:  majorMediaHeaders("https://www.xiaohongshu.com/"),
	}
	images := Array(note["imageList"])
	switch Text(note["type"]) {
	case "video":
		stream, streamBackups := majorXHSBestStream(Path(note, "video", "media", "stream"))
		originKey := majorFirstText(Path(note, "video", "consumer", "originVideoKey"), Path(note, "video", "originVideoKey"))
		streamURL := ""
		var width, height int
		var duration float64
		if stream != nil {
			streamURL = majorURL(stream["masterUrl"])
			width = int(Number(stream["width"]))
			height = int(Number(stream["height"]))
			duration = Number(stream["duration"]) / 1000
		}
		if duration == 0 {
			duration = Number(Path(note, "video", "capa", "duration"))
		}
		videoURL, backups := majorXHSOriginVideoURLs(originKey, streamURL, streamBackups)
		if videoURL == "" {
			return nil, fmt.Errorf("小红书视频没有可用播放流")
		}
		if len(images) == 0 || majorXHSImage(images[0]) == "" {
			return nil, fmt.Errorf("小红书视频缺少缩略图")
		}
		firstImg := Obj(images[0])
		coverURL, _ := majorXHSRawImageURLs(firstImg, majorXHSImage(firstImg))
		if coverURL == "" {
			coverURL = majorXHSImage(firstImg)
		}
		r.CoverURL = coverURL
		r.Media = []model.Media{{
			Kind:         "video",
			URL:          videoURL,
			BackupURLs:   backups,
			ThumbnailURL: coverURL,
			Width:        width,
			Height:       height,
			Duration:     duration,
			Headers:      r.Headers,
		}}
	case "normal":
		for index, v := range images {
			image := Obj(v)
			uri, backups := majorXHSRawImageURLs(image, majorXHSImage(image))
			if uri == "" {
				return nil, fmt.Errorf("小红书第 %d 张图片缺少地址", index+1)
			}
			if r.CoverURL == "" {
				r.CoverURL = uri
			}
			m := model.Media{
				Kind:         "image",
				URL:          uri,
				BackupURLs:   backups,
				ThumbnailURL: majorXHSImageThumb(image),
				Width:        int(Number(image["width"])),
				Height:       int(Number(image["height"])),
				Headers:      r.Headers,
			}
			if live, _ := image["livePhoto"].(bool); live {
				stream, liveBackups := majorXHSBestStream(image["stream"])
				liveOrigin := majorFirstText(Path(image, "originVideoKey"), Path(image, "video", "consumer", "originVideoKey"))
				liveStreamURL := ""
				var liveDuration float64
				if stream != nil {
					liveStreamURL = majorURL(stream["masterUrl"])
					liveDuration = Number(stream["duration"]) / 1000
				}
				liveURL, liveVideoBackups := majorXHSOriginVideoURLs(liveOrigin, liveStreamURL, liveBackups)
				if liveURL == "" {
					continue
				}
				m.Kind = "livephoto"
				m.VideoURL = liveURL
				m.VideoBackupURLs = liveVideoBackups
				m.Duration = liveDuration
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
