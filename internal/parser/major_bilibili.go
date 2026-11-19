// SPDX-License-Identifier: MIT
// Adapted from ParseHub, Copyright (c) 2024 梓澪; full MIT notice in major_common.go.
package parser

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"multiparse/internal/model"
)

func init() { Register("bilibili", majorBilibili) }

var majorBiliBV = regexp.MustCompile(`(?i)BV[0-9a-z]{10}`)
var majorBiliAV = regexp.MustCompile(`(?i)(?:^|/)av([0-9]+)(?:/|$)`)
var majorBiliDynamicID = regexp.MustCompile(`/(?:opus/|dynamic/)?([0-9]{15,22})(?:[/?#]|$)`)
var majorBiliWBIMixinTab = [64]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}
var majorBiliWBIFilter = strings.NewReplacer("!", "", "'", "", "(", "", ")", "", "*", "")

func majorBiliAPI(ctx context.Context, c *http.Client, endpoint, cookie, referer string) (map[string]any, error) {
	var response map[string]any
	if err := JSON(ctx, c, endpoint, majorHeaders(cookie, referer, endpoint, "bilibili.com"), &response); err != nil {
		return nil, err
	}
	if code := Number(response["code"]); code != 0 {
		return nil, fmt.Errorf("哔哩哔哩: code %.0f %s", code, Text(response["message"]))
	}
	data := Obj(response["data"])
	if len(data) == 0 {
		return nil, fmt.Errorf("哔哩哔哩未返回内容，作品可能已删除、私密或需要登录")
	}
	return data, nil
}
func majorBilibili(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	source := raw
	if canonical, ok := model.BilibiliURL(raw); ok {
		raw = canonical
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Hostname() == "b23.tv" || u.Hostname() == "bili2233.cn" {
		_, final, e := Get(ctx, c, raw, majorHeaders("", "https://www.bilibili.com/", raw))
		if e != nil {
			return nil, e
		}
		next, e := url.Parse(final)
		if e != nil {
			return nil, e
		}
		if !(next.Hostname() == "bilibili.com" || strings.HasSuffix(next.Hostname(), ".bilibili.com")) {
			return nil, fmt.Errorf("哔哩哔哩短链没有指向站内作品")
		}
		q := next.Query()
		for _, k := range []string{"p", "cid"} {
			if q.Get(k) == "" && u.Query().Get(k) != "" {
				q.Set(k, u.Query().Get(k))
			}
		}
		next.RawQuery = q.Encode()
		raw = next.String()
		u = next
	}
	if strings.HasPrefix(u.Path, "/read/") {
		r, e := Page(ctx, c, raw, cookie)
		if e != nil {
			return nil, e
		}
		r.Platform = "bilibili"
		r.SourceURL = source
		r.CanonicalURL = raw
		r.Type = "article"
		return r, nil
	}
	if u.Hostname() == "t.bilibili.com" || strings.HasPrefix(u.Path, "/opus/") || strings.HasPrefix(u.Path, "/dynamic/") {
		match := majorBiliDynamicID.FindStringSubmatch(u.Path)
		if match == nil {
			return nil, fmt.Errorf("哔哩哔哩动态 ID 无效")
		}
		r, e := majorBiliDynamic(ctx, c, match[1], raw, cookie, 0)
		if e != nil {
			return nil, e
		}
		r.SourceURL = source
		r.CanonicalURL = "https://www.bilibili.com/opus/" + match[1]
		return r, nil
	}
	r, e := majorBiliVideo(ctx, c, raw, cookie)
	if e != nil {
		return nil, e
	}
	r.SourceURL = source
	return r, nil
}
func majorBiliVideo(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if bv := majorBiliBV.FindString(u.Path); bv != "" {
		query.Set("bvid", "BV"+bv[2:])
	} else if av := majorBiliAV.FindStringSubmatch(u.Path); av != nil {
		query.Set("aid", av[1])
	} else {
		return nil, fmt.Errorf("哔哩哔哩链接缺少有效 BV/av 视频 ID（不支持付费番剧）")
	}
	view, err := majorBiliAPI(ctx, c, "https://api.bilibili.com/x/web-interface/view?"+query.Encode(), cookie, raw)
	if err != nil {
		return nil, err
	}
	p := 1
	if s := u.Query().Get("p"); s != "" {
		p, err = strconv.Atoi(s)
		if err != nil || p < 1 {
			return nil, fmt.Errorf("哔哩哔哩分 P 参数必须为正整数")
		}
	}
	pages := Array(view["pages"])
	var selected map[string]any
	for _, item := range pages {
		page := Obj(item)
		if int(Number(page["page"])) == p {
			selected = page
			break
		}
	}
	if selected == nil {
		if len(pages) == 0 && p == 1 {
			selected = view
		} else {
			return nil, fmt.Errorf("哔哩哔哩分 P %d 不存在（共 %d P）", p, len(pages))
		}
	}
	if requested := u.Query().Get("cid"); requested != "" {
		found := false
		for _, item := range pages {
			page := Obj(item)
			if Text(page["cid"]) == requested {
				if u.Query().Get("p") != "" && int(Number(page["page"])) != p {
					return nil, fmt.Errorf("哔哩哔哩 cid 与分 P 不匹配")
				}
				selected = page
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("哔哩哔哩 cid 不属于此视频")
		}
	}
	if sp := int(Number(selected["page"])); sp >= 1 {
		p = sp
	}
	cid := Text(selected["cid"])
	if cid == "" {
		return nil, fmt.Errorf("哔哩哔哩视频缺少 cid")
	}
	// A fresh anonymous fingerprint avoids stale buvid values, without discarding the user's session.
	fingerprint, e := majorBiliAPI(ctx, c, "https://api.bilibili.com/x/frontend/finger/spi", cookie, raw)
	if e == nil {
		for _, pair := range [][2]string{{"buvid3", "b_3"}, {"buvid4", "b_4"}} {
			if Text(fingerprint[pair[1]]) != "" {
				cookie = majorSetCookie(cookie, pair[0], Text(fingerprint[pair[1]]))
			}
		}
	}
	playQuery := url.Values{}
	if bv := Text(view["bvid"]); majorBiliBV.FindString(bv) == bv && bv != "" {
		playQuery.Set("bvid", bv)
	} else if av := query.Get("aid"); av != "" {
		playQuery.Set("avid", av)
	} else {
		playQuery.Set("bvid", query.Get("bvid"))
	}
	playQuery.Set("cid", cid)
	playQuery.Set("qn", "80")
	playQuery.Set("fnval", "4048")
	playQuery.Set("fnver", "0")
	playQuery.Set("fourk", "1")
	hadSessData := majorCookie(cookie, "SESSDATA") != ""
	cookieExpired := false
	play, err := majorBiliAPI(ctx, c, "https://api.bilibili.com/x/player/playurl?"+playQuery.Encode(), cookie, raw)
	if (err != nil || !majorBiliHasPlayableStream(play)) && ctx.Err() == nil {
		wbiPlay, expired, wbiErr := majorBiliWBI(ctx, c, playQuery, cookie, raw)
		cookieExpired = expired && hadSessData
		if wbiErr == nil && (majorBiliHasPlayableStream(wbiPlay) || err != nil) {
			play, err = wbiPlay, nil
		} else if err != nil && wbiErr != nil && wbiErr.Error() != err.Error() {
			return nil, fmt.Errorf("%w（WBI回退失败: %v）", err, wbiErr)
		}
	}
	if err != nil {
		return nil, err
	}
	canonical := raw
	if bv := Text(view["bvid"]); majorBiliBV.FindString(bv) == bv && bv != "" {
		canonical = "https://www.bilibili.com/video/" + bv
	} else if reqBV := query.Get("bvid"); reqBV != "" {
		canonical = "https://www.bilibili.com/video/" + reqBV
	} else if aid := query.Get("aid"); aid != "" {
		canonical = "https://www.bilibili.com/video/av" + aid
	}
	if cu, parseErr := url.Parse(canonical); parseErr == nil {
		cq := url.Values{}
		if p > 1 {
			cq.Set("p", strconv.Itoa(p))
		}
		cu.RawQuery = cq.Encode()
		cu.Fragment = ""
		canonical = cu.String()
	}
	var tags []string
	for _, field := range []string{"tname_v2", "tname"} {
		if t := strings.TrimSpace(Text(view[field])); t != "" && !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	cover := majorURL(view["pic"])
	r := &model.ParseResult{Platform: "bilibili", SourceURL: raw, CanonicalURL: canonical, CoverURL: cover, Tags: tags, CookieExpired: cookieExpired, Type: "video", Title: Text(view["title"]), Content: Text(view["desc"]), Author: Text(Path(view, "owner", "name")), Headers: majorMediaHeaders("https://www.bilibili.com/")}
	if r.Content == "-" {
		r.Content = ""
	}
	if len(pages) > 1 {
		if part := strings.TrimSpace(Text(selected["part"])); part != "" && part != r.Title {
			r.Title = fmt.Sprintf("%s · P%d %s (%d/%d)", r.Title, p, part, p, len(pages))
		} else {
			r.Title = fmt.Sprintf("%s (P%d/%d)", r.Title, p, len(pages))
		}
	}
	base := model.Media{Kind: "video", ThumbnailURL: cover, Duration: Number(selected["duration"]), Width: int(Number(Path(selected, "dimension", "width"))), Height: int(Number(Path(selected, "dimension", "height"))), Headers: r.Headers}
	dash := Obj(play["dash"])
	if len(dash) > 0 {
		video := majorBiliTrack(Array(dash["video"]), true)
		audio := majorBiliTrack(Array(dash["audio"]), false)
		if video == nil {
			return nil, fmt.Errorf("哔哩哔哩 DASH 没有可用视频轨道，可能需要登录或大会员")
		}
		base.URL = majorURL(video)
		base.BackupURLs = majorURLs(video["backupUrl"])
		if len(base.BackupURLs) == 0 {
			base.BackupURLs = majorURLs(video["backup_url"])
		}
		base.AudioURL = majorURL(audio)
		base.Width = int(Number(video["width"]))
		base.Height = int(Number(video["height"]))
		base.Duration = Number(dash["duration"])
		r.Media = append(r.Media, base)
	} else {
		for _, item := range Array(play["durl"]) {
			part := Obj(item)
			m := base
			m.URL = majorURL(part["url"])
			m.BackupURLs = majorURLs(part["backup_url"])
			if n := Number(part["length"]); n > 0 {
				m.Duration = n / 1000
			}
			if m.URL != "" {
				r.Media = append(r.Media, m)
			}
		}
	}
	if len(r.Media) == 0 || r.Media[0].URL == "" {
		return nil, fmt.Errorf("哔哩哔哩未提供播放地址，作品可能受登录、付费或地区限制")
	}
	return r, nil
}
func majorBiliHasPlayableStream(play map[string]any) bool {
	if len(play) == 0 {
		return false
	}
	if dash := Obj(play["dash"]); len(dash) > 0 {
		return majorBiliTrack(Array(dash["video"]), true) != nil
	}
	for _, item := range Array(play["durl"]) {
		if majorURL(Obj(item)["url"]) != "" {
			return true
		}
	}
	return false
}
func majorBiliWBIStem(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		raw = u.Path
	}
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		raw = raw[i+1:]
	}
	if i := strings.IndexByte(raw, '.'); i >= 0 {
		raw = raw[:i]
	}
	return raw
}
func majorBiliWBIMixinKey(imgURL, subURL string) (string, error) {
	imgKey := majorBiliWBIStem(imgURL)
	subKey := majorBiliWBIStem(subURL)
	if imgKey == "" || subKey == "" {
		return "", fmt.Errorf("bilibili nav 缺少 wbi_img")
	}
	rawKey := imgKey + subKey
	if len(rawKey) < 64 {
		return "", fmt.Errorf("bilibili wbi 密钥长度不足")
	}
	var mixed [32]byte
	for i := range mixed {
		mixed[i] = rawKey[majorBiliWBIMixinTab[i]]
	}
	return string(mixed[:]), nil
}
func majorBiliSignWBI(params url.Values, mixinKey string, wts int64) string {
	copied := make(url.Values, len(params)+1)
	for k, v := range params {
		copied[k] = append([]string(nil), v...)
	}
	copied.Set("wts", strconv.FormatInt(wts, 10))
	keys := make([]string, 0, len(copied))
	for k := range copied {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		filtered := majorBiliWBIFilter.Replace(copied.Get(k))
		ek := strings.ReplaceAll(url.QueryEscape(k), "+", "%20")
		ev := strings.ReplaceAll(url.QueryEscape(filtered), "+", "%20")
		parts = append(parts, ek+"="+ev)
	}
	encodedQuery := strings.Join(parts, "&")
	sum := md5.Sum([]byte(encodedQuery + mixinKey))
	wRid := hex.EncodeToString(sum[:])
	return encodedQuery + "&w_rid=" + wRid
}
func majorBiliWBI(ctx context.Context, c *http.Client, playQuery url.Values, cookie, referer string) (map[string]any, bool, error) {
	const navEndpoint = "https://api.bilibili.com/x/web-interface/nav"
	var nav map[string]any
	if err := JSON(ctx, c, navEndpoint, majorHeaders(cookie, referer, navEndpoint, "bilibili.com"), &nav); err != nil {
		return nil, false, err
	}
	expired := Number(nav["code"]) == -101 || Path(nav, "data", "isLogin") == false
	wbiImg := Obj(Path(nav, "data", "wbi_img"))
	mixinKey, err := majorBiliWBIMixinKey(Text(wbiImg["img_url"]), Text(wbiImg["sub_url"]))
	if err != nil {
		if code := Number(nav["code"]); code != 0 && code != -101 {
			return nil, expired, fmt.Errorf("哔哩哔哩: code %.0f %s", code, Text(nav["message"]))
		}
		return nil, expired, err
	}
	signedQuery := majorBiliSignWBI(playQuery, mixinKey, time.Now().Unix())
	play, err := majorBiliAPI(ctx, c, "https://api.bilibili.com/x/player/wbi/playurl?"+signedQuery, cookie, referer)
	return play, expired, err
}
func majorBiliTrack(tracks []any, video bool) map[string]any {
	var best map[string]any
	bestScore := -1.0
	for _, item := range tracks {
		track := Obj(item)
		if majorURL(track) == "" {
			continue
		}
		score := Number(track["bandwidth"])
		codec := strings.ToLower(Text(track["codecs"]))
		if (video && strings.HasPrefix(codec, "avc")) || (!video && strings.HasPrefix(codec, "mp4a")) {
			score += 1e15
		}
		if score > bestScore {
			best = track
			bestScore = score
		}
	}
	return best
}
func majorBiliDynamic(ctx context.Context, c *http.Client, id, raw, cookie string, depth int) (*model.ParseResult, error) {
	if depth > 4 {
		return nil, fmt.Errorf("哔哩哔哩转发动态嵌套过深")
	}
	if depth == 0 {
		if fingerprint, e := majorBiliAPI(ctx, c, "https://api.bilibili.com/x/frontend/finger/spi", cookie, raw); e == nil {
			for _, pair := range [][2]string{{"buvid3", "b_3"}, {"buvid4", "b_4"}} {
				if value := Text(fingerprint[pair[1]]); value != "" {
					cookie = majorSetCookie(cookie, pair[0], value)
				}
			}
		}
	}
	endpoint := "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?id=" + url.QueryEscape(id) + "&features=itemOpusStyle"
	data, err := majorBiliAPI(ctx, c, endpoint, cookie, raw)
	if err != nil {
		return nil, err
	}
	item := Obj(data["item"])
	r := &model.ParseResult{Platform: "bilibili", SourceURL: raw, Author: Text(Path(item, "modules", "module_author", "name")), Content: Text(Path(item, "modules", "module_dynamic", "desc", "text")), Headers: majorMediaHeaders("https://www.bilibili.com/")}
	if topic := strings.TrimSpace(Text(Path(item, "modules", "module_dynamic", "topic", "name"))); topic != "" {
		r.Tags = append(r.Tags, topic)
	}
	major := Obj(Path(item, "modules", "module_dynamic", "major"))
	opus := Obj(major["opus"])
	r.Title = Text(opus["title"])
	if text := Text(Path(opus, "summary", "text")); text != "" {
		r.Content = text
	}
	pics := Array(opus["pics"])
	if len(pics) == 0 {
		pics = Array(Path(major, "draw", "items"))
	}
	for _, v := range pics {
		pic := Obj(v)
		image := majorFirstText(pic["url"], pic["src"])
		if majorURL(image) == "" {
			continue
		}
		r.Media = append(r.Media, model.Media{Kind: "image", URL: majorURL(image), Width: int(Number(pic["width"])), Height: int(Number(pic["height"])), Headers: r.Headers})
		if live := majorURL(pic["live_url"]); live != "" {
			r.Media = append(r.Media, model.Media{Kind: "video", URL: live, ThumbnailURL: majorURL(image), Headers: r.Headers})
		}
	}
	if archive := Obj(major["archive"]); len(archive) > 0 {
		bv := Text(archive["bvid"])
		if bv != "" {
			v, e := majorBiliVideo(ctx, c, "https://www.bilibili.com/video/"+bv, cookie)
			if e != nil {
				return nil, e
			}
			r.Title = v.Title
			r.Media = append(r.Media, v.Media...)
		}
	}
	if article := Obj(major["article"]); len(article) > 0 {
		r.Title = Text(article["title"])
		link := majorURL(article["jump_url"])
		if link != "" {
			p, e := Page(ctx, c, link, cookie)
			if e != nil {
				return nil, e
			}
			if r.Content != "" {
				r.Content += "\n\n"
			}
			r.Content += p.Content
			r.Media = append(r.Media, p.Media...)
		}
	}
	if orig := Obj(item["orig"]); len(orig) > 0 {
		if oid := Text(orig["id_str"]); oid != "" {
			forward, e := majorBiliDynamic(ctx, c, oid, "https://t.bilibili.com/"+oid, cookie, depth+1)
			if e != nil {
				return nil, e
			}
			r.Content += "\n\n" + forward.Content
			if r.Title == "" {
				r.Title = forward.Title
			}
			r.Media = append(r.Media, forward.Media...)
		}
	}
	if r.Title == "" {
		r.Title = r.Content
	}
	if r.Title == "" && len(r.Media) == 0 {
		return nil, fmt.Errorf("哔哩哔哩动态内容不可见、已删除或类型不受支持")
	}
	majorResultType(r)
	return r, nil
}
