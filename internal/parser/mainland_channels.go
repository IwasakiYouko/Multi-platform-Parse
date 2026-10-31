package parser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"multiparse/internal/model"
)

func init() { Register("weixin_channels", mainlandChannels) }

func mainlandChannels(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	if cookie == "" {
		return nil, fmt.Errorf("微信视频号需要配置 yuanbao.tencent.com 已登录 Cookie（不是微信公众平台 Cookie）")
	}
	u, e := url.Parse(raw)
	if e != nil {
		return nil, e
	}
	id := ""
	if m := regexp.MustCompile(`/sph/(\w+)`).FindStringSubmatch(u.Path); len(m) > 0 {
		id = m[1]
	} else {
		id = u.Query().Get("id")
	}
	if !regexp.MustCompile(`^\w+$`).MatchString(id) {
		return nil, fmt.Errorf("微信视频号仅支持 weixin.qq.com/sph/ 分享链接或 finder-preview/pages/sph?id= 链接")
	}
	var first any
	h := Headers(cookie, "https://yuanbao.tencent.com/")
	h["Origin"] = "https://yuanbao.tencent.com"
	e = mainlandPostJSON(ctx, c, "https://yuanbao.tencent.com/api/weixin/get_parse_result", h, map[string]any{"type": "video_channel_url", "url": "https://weixin.qq.com/sph/" + id, "scene": 1}, &first)
	if e != nil {
		return nil, fmt.Errorf("微信视频号元宝授权: %w", e)
	}
	if err := Path(first, "error"); err != nil {
		if Text(Path(err, "code")) == "20000" {
			return nil, fmt.Errorf("微信视频号元宝 Cookie 无效或已过期")
		}
		return nil, fmt.Errorf("微信视频号元宝接口: %s", mainlandFirst(Path(err, "message"), Path(err, "code")))
	}
	if Number(Path(first, "code")) != 0 {
		return nil, fmt.Errorf("微信视频号元宝接口: %s", mainlandFirst(Path(first, "message"), Path(first, "code")))
	}
	data := Obj(Path(first, "data"))
	playable := Text(data["playable_url"])
	if Text(data["wx_export_id"]) == "" || playable == "" {
		return nil, fmt.Errorf("微信视频号不存在、已删除或没有分享权限")
	}
	playURL, e := url.Parse(playable)
	if e != nil {
		return nil, e
	}
	if playURL.Hostname() != "channels.weixin.qq.com" {
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
	if e = mainlandPostJSON(ctx, c, "https://channels.weixin.qq.com/finder-preview/api/feed/get_feed_info?"+q.Encode(), h, map[string]any{"baseReq": map[string]any{"generalToken": token}, "exportId": eid}, &feed); e != nil {
		return nil, e
	}
	if Number(Path(feed, "errCode")) != 0 {
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
	if Number(info["decodeKey"]) != 0 || Text(info["decryptKey"]) != "" || Number(Path(info, "h264VideoInfo", "decodeKey")) != 0 {
		return nil, fmt.Errorf("微信视频号官方预览返回加密媒体，而非可直接播放版本")
	}
	r := mainlandResult("weixin_channels", raw)
	r.Content = mainlandFirst(info["description"], data["desc"])
	r.Author = mainlandFirst(Path(feed, "data", "authorInfo", "nickname"), data["author"])
	mainlandAdd(r, "video", video, map[string]any{"thumbnail": mainlandFirst(info["coverUrl"], data["cover_url"]), "duration": info["duration"], "width": info["width"], "height": info["height"]})
	return mainlandFinish(r, true)
}
