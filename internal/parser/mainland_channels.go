package parser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("weixin_channels", mainlandChannels) }

func mainlandChannels(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithExplicitCookies(ctx)
	if cookie == "" {
		return nil, fmt.Errorf("微信视频号需要配置 yuanbao.tencent.com 已登录 Cookie（不是微信公众平台 Cookie）")
	}
	u, e := url.Parse(raw)
	if e != nil {
		return nil, e
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return nil, fmt.Errorf("微信视频号链接格式无效")
	}
	id := ""
	if u.Hostname() == "weixin.qq.com" {
		if m := regexp.MustCompile(`^/sph/(\w+)(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 0 {
			id = m[1]
		}
	} else if u.Hostname() == "channels.weixin.qq.com" && u.Path == "/finder-preview/pages/sph" {
		id = u.Query().Get("id")
	}
	if !regexp.MustCompile(`^\w+$`).MatchString(id) {
		return nil, fmt.Errorf("微信视频号仅支持 weixin.qq.com/sph/ 分享链接或 finder-preview/pages/sph?id= 链接")
	}
	var first any
	h := Headers(cookie, "https://yuanbao.tencent.com/")
	h["Origin"] = "https://yuanbao.tencent.com"
	e = mainlandChannelsYuanbao(ctx, c, h, map[string]any{"type": "video_channel_url", "url": "https://weixin.qq.com/sph/" + id, "scene": 1}, &first)
	if e != nil {
		return nil, fmt.Errorf("微信视频号元宝授权: %w", e)
	}
	if err := Path(first, "error"); err != nil {
		if Text(Path(err, "code")) == "20000" {
			return nil, fmt.Errorf("微信视频号元宝 Cookie 无效或已过期")
		}
		return nil, fmt.Errorf("微信视频号元宝接口: %s", mainlandFirst(Path(err, "message"), Path(err, "code")))
	}
	if Text(Path(first, "code")) != "0" {
		return nil, fmt.Errorf("微信视频号元宝接口: %s", mainlandFirst(Path(first, "message"), Path(first, "code")))
	}
	data := Obj(Path(first, "data"))
	playable := Text(data["playable_url"])
	if Text(data["wx_export_id"]) == "" || playable == "" {
		return nil, ContentUnavailableError{Reason: "微信视频号视频不存在或已删除"}
	}
	playURL, e := url.Parse(playable)
	if e != nil {
		return nil, e
	}
	if playURL.Scheme != "https" || playURL.Hostname() != "channels.weixin.qq.com" || playURL.User != nil || playURL.Port() != "" || playURL.Path != "/finder-preview/pages/feed" {
		return nil, fmt.Errorf("微信视频号返回非官方预览地址")
	}
	token := playURL.Query().Get("token")
	eid := playURL.Query().Get("eid")
	if token == "" || eid == "" {
		return nil, fmt.Errorf("微信视频号预览地址缺少 token 或 eid")
	}
	var random [4]byte
	if _, e = rand.Read(random[:]); e != nil {
		return nil, e
	}
	q := url.Values{"_rid": {strconv.FormatInt(time.Now().Unix(), 16) + "-" + hex.EncodeToString(random[:])}, "_pageUrl": {"https://channels.weixin.qq.com/finder-preview/pages/feed"}}
	var feed any
	h = Headers("", playable)
	h["Origin"] = "https://channels.weixin.qq.com"
	if e = mainlandPostJSON(netx.WithoutCookies(ctx), c, "https://channels.weixin.qq.com/finder-preview/api/feed/get_feed_info?"+q.Encode(), h, map[string]any{"baseReq": map[string]any{"generalToken": token}, "exportId": eid}, &feed); e != nil {
		return nil, e
	}
	if Text(Path(feed, "errCode")) != "0" {
		return nil, fmt.Errorf("微信视频号预览接口: %s", mainlandFirst(Path(feed, "errMsg"), Path(feed, "errCode")))
	}
	if msg := mainlandText(mainlandFirst(Path(feed, "data", "errMsg", "title")) + " " + mainlandFirst(Path(feed, "data", "errMsg", "content"))); msg != "" {
		return nil, fmt.Errorf("微信视频号: %s", msg)
	}
	info := Obj(Path(feed, "data", "feedInfo"))
	video := mainlandFirst(Path(info, "h264VideoInfo", "videoUrl"), info["videoUrl"], Path(info, "h265VideoInfo", "videoUrl"))
	if video == "" {
		return nil, fmt.Errorf("微信视频号未提供可播放视频；该内容类型可能不支持预览")
	}
	// Official preview's h264VideoInfo is already playable; finder raw encrypted media are not interchangeable.
	if mainlandChannelsEncrypted(info) || mainlandChannelsEncrypted(Obj(Path(info, "h264VideoInfo"))) || mainlandChannelsEncrypted(Obj(Path(info, "h265VideoInfo"))) {
		return nil, fmt.Errorf("微信视频号官方预览返回加密媒体，而非可直接播放版本")
	}
	r := mainlandResult("weixin_channels", raw)
	r.Title = mainlandFirst(info["description"], data["desc"])
	r.Author = mainlandFirst(Path(feed, "data", "authorInfo", "nickname"), data["author"])
	mainlandAdd(r, "video", video, map[string]any{"thumbnail": mainlandFirst(info["coverUrl"], data["cover_url"]), "duration": info["duration"], "width": info["width"], "height": info["height"]})
	return mainlandFinish(r, true)
}

func mainlandChannelsEncrypted(info map[string]any) bool {
	key := Text(info["decodeKey"])
	return key != "" && key != "0" || Text(info["decryptKey"]) != "" || Text(info["decrypt_key"]) != ""
}

func mainlandChannelsYuanbao(ctx context.Context, c *http.Client, headers map[string]string, payload, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://yuanbao.tencent.com/api/weixin/get_parse_result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("Cookie 无效或已过期")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("元宝接口 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("元宝接口响应超过 16 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}
