package parser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"math/bits"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("zhihu", mainlandZhihu) }
func mainlandZhihu(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	ctx = netx.WithExplicitCookies(ctx)
	if strings.TrimSpace(cookie) == "" {
		return nil, fmt.Errorf("知乎需要配置已登录的 Cookie")
	}
	return mainlandZhihuParse(ctx, c, raw, cookie, make(map[string]bool))
}

func mainlandZhihuParse(ctx context.Context, c *http.Client, raw, cookie string, seen map[string]bool) (*model.ParseResult, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return nil, e
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return nil, fmt.Errorf("知乎链接格式无效")
	}
	switch u.Hostname() {
	case "www.zhihu.com", "zhuanlan.zhihu.com", "daily.zhihu.com":
	default:
		return nil, fmt.Errorf("知乎原文链接必须使用官方域名")
	}
	if seen[raw] || len(seen) >= 8 {
		return nil, fmt.Errorf("知乎日报原文链接循环或递归过深")
	}
	seen[raw] = true
	r := mainlandResult("zhihu", raw)
	r.Type = "article"
	if u.Hostname() == "daily.zhihu.com" || strings.HasPrefix(u.Path, "/story/") {
		m := regexp.MustCompile(`^/story/(\d+)(?:/|$)`).FindStringSubmatch(u.Path)
		if len(m) == 0 {
			return nil, fmt.Errorf("知乎日报链接缺少 story ID")
		}
		var v any
		if e = JSON(netx.WithoutCookies(ctx), c, "https://daily.zhihu.com/api/7/story/"+m[1], Headers("", ""), &v); e != nil {
			return nil, e
		}
		if err := mainlandZhihuAPIError(v); err != nil {
			return nil, err
		}
		doc, err := mainlandDoc(Text(Path(v, "body")))
		if err != nil {
			return nil, err
		}
		origin := ""
		doc.Find("a").EachWithBreak(func(_ int, s *goquery.Selection) bool {
			if strings.Contains(s.Text(), "查看知乎原文") {
				href, _ := s.Attr("href")
				origin = mainlandURL(href, raw)
				return false
			}
			return true
		})
		if origin == "" {
			return nil, fmt.Errorf("未在知乎日报中找到原文链接")
		}
		result, err := mainlandZhihuParse(ctx, c, origin, cookie, seen)
		if err == nil {
			result.SourceURL = raw
		}
		return result, err
	}
	dc := mainlandCookie(cookie, "d_c0")
	if dc == "" {
		return nil, fmt.Errorf("知乎需要配置包含 d_c0 的已登录 Cookie")
	}
	endpoint := ""
	pin, video := false, false
	if m := regexp.MustCompile(`^/(?:question/\d+/)?answer/(\d+)(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 0 {
		endpoint = "https://www.zhihu.com/api/v4/answers/" + m[1] + "?include=data%5B%2A%5D.content"
	} else if m := regexp.MustCompile(`^/question/(\d+)(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 0 {
		var listing any
		if e = mainlandZhihuJSON(ctx, c, "https://www.zhihu.com/api/v4/questions/"+m[1]+"/answers?offset=&limit=1&sort_by=default&include=data%5B%2A%5D.content", cookie, dc, &listing); e != nil {
			return nil, e
		}
		answers := Array(Path(listing, "data"))
		if len(answers) == 0 {
			var question any
			if e = mainlandZhihuJSON(ctx, c, "https://www.zhihu.com/api/v4/questions/"+m[1], cookie, dc, &question); e != nil {
				return nil, e
			}
			r.Title = Text(Path(question, "title"))
			r.Type = "multimedia"
			if r.Title == "" {
				return nil, fmt.Errorf("知乎问题没有标题")
			}
			return r, nil
		}
		id := Text(Path(answers[0], "id"))
		if !regexp.MustCompile(`^\d+$`).MatchString(id) {
			return nil, fmt.Errorf("知乎回答 ID 格式无效")
		}
		endpoint = "https://www.zhihu.com/api/v4/answers/" + id + "?include=data%5B%2A%5D.content"
	} else if m := regexp.MustCompile(`^/(?:appview/)?p/(\d+)(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 0 {
		endpoint = "https://zhuanlan.zhihu.com/api/articles/" + m[1]
	} else if m := regexp.MustCompile(`^/pin/(\d+)(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 0 {
		endpoint = "https://www.zhihu.com/api/v4/pins/" + m[1]
		pin = true
	} else if m := regexp.MustCompile(`^/zvideo/(\d+)(?:/|$)`).FindStringSubmatch(u.Path); len(m) > 0 {
		// Retained extension: standalone zvideo links are outside the reference adapter.
		endpoint = "https://www.zhihu.com/api/v4/zvideos/" + m[1]
		video = true
	} else {
		return nil, fmt.Errorf("知乎支持回答、问题、专栏、想法、视频和日报链接")
	}
	var v any
	if e = mainlandZhihuJSON(ctx, c, endpoint, cookie, dc, &v); e != nil {
		return nil, e
	}
	r.Title = mainlandFirst(Path(v, "title"), Path(v, "question", "title"))
	if strings.Contains(endpoint, "/answers/") {
		r.Title = Text(Path(v, "question", "title"))
	}
	r.Author = Text(Path(v, "author", "name"))
	if video {
		r.Type = "video"
		r.Content = mainlandText(mainlandFirst(Path(v, "description"), Path(v, "title")))
		if !mainlandZhihuPlaylist(r, Path(v, "video")) {
			if id := mainlandFirst(Path(v, "video", "id"), Path(v, "video", "video_id")); id != "" {
				if e = mainlandZhihuLens(ctx, c, r, id); e != nil {
					return nil, e
				}
			}
		}
		return mainlandFinish(r, true)
	}
	if pin {
		kind := "image"
		for _, part := range Array(Path(v, "content")) {
			switch Text(Path(part, "type")) {
			case "text":
				r.Title = Text(Path(part, "title"))
				r.Content, _ = referenceHTML(Text(Path(part, "content")), raw)
			case "image":
				kind = "image"
				mainlandAdd(r, "image", Text(Path(part, "original_url")), map[string]any{"thumbnail": Path(part, "url"), "width": Path(part, "width"), "height": Path(part, "height")})
			case "video":
				kind = "video"
				if !mainlandZhihuPlaylist(r, Path(part, "video_info")) {
					return nil, fmt.Errorf("知乎想法视频没有可播放清晰度")
				}
			}
		}
		// The reference accidentally selects an image as video for mixed pins.
		// Keep the final declared type, but only media of that actual type.
		media := r.Media[:0]
		for _, m := range r.Media {
			if m.Kind == kind {
				media = append(media, m)
			}
		}
		r.Media = media
		if kind == "video" && len(r.Media) > 1 {
			r.Media = r.Media[:1]
		}
		r.Type = kind
		if kind == "image" && r.Content == "" && len(r.Media) == 0 && r.Title != "" {
			return r, nil
		}
		return mainlandFinish(r, kind == "video")
	}
	source := Text(Path(v, "content"))
	doc, e := mainlandDoc(source)
	if e != nil {
		return nil, e
	}
	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		if strings.HasPrefix(src, "data:image/svg") {
			alt, _ := s.Attr("alt")
			s.ReplaceWithHtml(html.EscapeString(alt))
			return
		}
		if !strings.Contains(src, "/equation") {
			mainlandAdd(r, "image", src, nil)
		}
	})
	normalized, e := doc.Html()
	if e != nil {
		return nil, e
	}
	referenceSetHTML(r, normalized)
	// QA and articles expose rich text and images, not inferred audio/video.
	if r.MarkdownContent == "" && strings.Contains(endpoint, "/answers/") {
		r.Type = "multimedia"
	}
	if r.Title == "" && r.Content == "" && len(r.Media) == 0 {
		return nil, fmt.Errorf("知乎没有可读取的正文或标题")
	}
	return r, nil
}

func mainlandZhihuPlaylist(r *model.ParseResult, v any) bool {
	playlist := Obj(Path(v, "playlist"))
	best := ""
	score := float64(-1)
	var details map[string]any
	if entries, ok := Path(v, "playlist").([]any); ok {
		if len(entries) == 0 {
			return false
		}
		details = Obj(entries[0])
		best = mainlandFirst(details["url"], details["play_url"])
		if best == "" {
			return false
		}
	}
	for _, p := range playlist {
		u := mainlandFirst(Path(p, "play_url"), Path(p, "url"))
		s := Number(Path(p, "height"))*1e9 + Number(Path(p, "bitrate"))
		if u != "" && (s > score || s == score && u < best) {
			best, score, details = u, s, Obj(p)
		}
	}
	if best == "" {
		return false
	}
	mainlandAdd(r, "video", best, map[string]any{"width": details["width"], "height": details["height"], "duration": Path(v, "duration"), "thumbnail": mainlandFirst(Path(v, "thumbnail"), Path(v, "cover_url"), Path(v, "cover"))})
	return true
}

func mainlandZhihuLens(ctx context.Context, c *http.Client, r *model.ParseResult, id string) error {
	if !regexp.MustCompile(`^\d+$`).MatchString(id) {
		return fmt.Errorf("知乎视频 ID 格式无效")
	}
	var v any
	if e := JSON(ctx, c, "https://lens.zhihu.com/api/v4/videos/"+id, Headers("", r.SourceURL), &v); e != nil {
		return e
	}
	if !mainlandZhihuPlaylist(r, v) {
		return fmt.Errorf("知乎视频 %s 未返回可播放清晰度", id)
	}
	return nil
}

func mainlandZhihuAPIError(v any) error {
	if err := Path(v, "error"); err != nil {
		return fmt.Errorf("知乎接口 %s: %s", Text(Path(err, "code")), Text(Path(err, "message")))
	}
	return nil
}

func mainlandZhihuJSON(ctx context.Context, c *http.Client, endpoint, cookie, dc string, target *any) error {
	u, e := url.Parse(endpoint)
	if e != nil {
		return e
	}
	signPath, e := url.PathUnescape(u.RequestURI())
	if e != nil {
		return e
	}
	sig, e := mainlandZhihuSignature(signPath, dc)
	if e != nil {
		return e
	}
	h := Headers(cookie, "https://zhuanlan.zhihu.com/")
	h["Origin"] = "https://zhuanlan.zhihu.com"
	h["Accept"] = "*/*"
	h["Accept-Language"] = "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"
	h["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36 Edg/123.0.0.0"
	h["Sec-Ch-Ua"] = `"Microsoft Edge";v="123", "Not:A-Brand";v="8", "Chromium";v="123"`
	h["Sec-Ch-Ua-Mobile"] = "?0"
	h["Sec-Ch-Ua-Platform"] = `"Windows"`
	h["Sec-Fetch-Dest"] = "empty"
	h["Sec-Fetch-Mode"] = "cors"
	h["Sec-Fetch-Site"] = "same-site"
	h["X-Zse-93"] = "101_3_3.0"
	h["X-Zse-96"] = sig
	h["X-Requested-With"] = "fetch"
	var body json.RawMessage
	if e = JSON(ctx, c, endpoint, h, &body); e != nil {
		return fmt.Errorf("知乎接口访问失败；请检查登录 Cookie 或访问验证: %w", e)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if e = decoder.Decode(target); e != nil {
		return e
	}
	if strings.HasPrefix(u.Path, "/api/v4/pins/") {
		var wire struct {
			Content []struct {
				VideoInfo struct {
					Playlist json.RawMessage `json:"playlist"`
				} `json:"video_info"`
			} `json:"content"`
		}
		if e = json.Unmarshal(body, &wire); e != nil {
			return e
		}
		parts := Array(Path(*target, "content"))
		for i, part := range wire.Content {
			if len(part.VideoInfo.Playlist) == 0 || string(part.VideoInfo.Playlist) == "null" {
				continue
			}
			playlistDecoder := json.NewDecoder(bytes.NewReader(part.VideoInfo.Playlist))
			token, err := playlistDecoder.Token()
			if err != nil || token != json.Delim('{') {
				return fmt.Errorf("知乎想法视频清晰度格式无效")
			}
			var entries []any
			for playlistDecoder.More() {
				if _, e = playlistDecoder.Token(); e != nil {
					return e
				}
				var entry any
				if e = playlistDecoder.Decode(&entry); e != nil {
					return e
				}
				entries = append(entries, entry)
			}
			if videoInfo := Obj(Path(parts[i], "video_info")); videoInfo != nil {
				videoInfo["playlist"] = entries
			}
		}
	}
	return mainlandZhihuAPIError(*target)
}

var mainlandZhihuSBox = [256]byte{
	20, 223, 245, 7, 248, 2, 194, 209, 87, 6, 227, 253, 240, 128, 222, 91, 237, 9, 125, 157, 230, 93, 252, 205, 90, 79, 144, 199, 159, 197, 186, 167,
	39, 37, 156, 198, 38, 42, 43, 168, 217, 153, 15, 103, 80, 189, 71, 191, 97, 84, 247, 95, 36, 69, 14, 35, 12, 171, 28, 114, 178, 148, 86, 182,
	32, 83, 158, 109, 22, 255, 94, 238, 151, 85, 77, 124, 254, 18, 4, 26, 123, 176, 232, 193, 131, 172, 143, 142, 150, 30, 10, 146, 162, 62, 224, 218,
	196, 229, 1, 192, 213, 27, 110, 56, 231, 180, 138, 107, 242, 187, 54, 120, 19, 44, 117, 228, 215, 203, 53, 239, 251, 127, 81, 11, 133, 96, 204, 132,
	41, 115, 73, 55, 249, 147, 102, 48, 122, 145, 106, 118, 74, 190, 29, 16, 174, 5, 177, 129, 63, 113, 99, 31, 161, 76, 246, 34, 211, 13, 60, 68,
	207, 160, 65, 111, 82, 165, 67, 169, 225, 57, 112, 244, 155, 51, 236, 200, 233, 58, 61, 47, 100, 137, 185, 64, 17, 70, 234, 163, 219, 108, 170, 166,
	59, 149, 52, 105, 24, 212, 78, 173, 45, 0, 116, 226, 119, 136, 206, 135, 175, 195, 25, 92, 121, 208, 126, 139, 3, 75, 141, 21, 130, 98, 241, 40,
	154, 66, 184, 49, 181, 46, 243, 88, 101, 183, 8, 23, 72, 188, 104, 179, 210, 134, 250, 201, 164, 89, 216, 202, 220, 50, 221, 152, 140, 33, 235, 214,
}
var mainlandZhihuRoundKeys = [32]int64{1170614578, 1024848638, 1413669199, -343334464, -766094290, -1373058082, -143119608, -297228157, 1933479194, -971186181, -406453910, 460404854, -547427574, -1891326262, -1679095901, 2119585428, -2029270069, 2035090028, -1521520070, -5587175, -77751101, -2094365853, -1243052806, 1579901135, 1321810770, 456816404, -1391643889, -229302305, 330002838, -788960546, 363569021, -1947871109}

func mainlandZhihuBlock(dst, src []byte) {
	var x [36]uint32
	for i := range 4 {
		x[i] = binary.BigEndian.Uint32(src[i*4:])
	}
	for i := range 32 {
		v := x[i+1] ^ x[i+2] ^ x[i+3] ^ uint32(mainlandZhihuRoundKeys[i])
		t := uint32(mainlandZhihuSBox[byte(v>>24)])<<24 | uint32(mainlandZhihuSBox[byte(v>>16)])<<16 | uint32(mainlandZhihuSBox[byte(v>>8)])<<8 | uint32(mainlandZhihuSBox[byte(v)])
		x[i+4] = x[i] ^ t ^ bits.RotateLeft32(t, 2) ^ bits.RotateLeft32(t, 10) ^ bits.RotateLeft32(t, 18) ^ bits.RotateLeft32(t, 24)
	}
	for i := range 4 {
		binary.BigEndian.PutUint32(dst[i*4:], x[35-i])
	}
}
func mainlandZhihuSignature(path, dc string) (string, error) {
	var iv [16]byte
	if _, e := rand.Read(iv[:]); e != nil {
		return "", e
	}
	return mainlandZhihuSignatureIV(path, dc, iv), nil
}
func mainlandZhihuSignatureIV(path, dc string, iv [16]byte) string {
	digest := mainlandMD5("101_3_3.0+" + path + "+" + dc)
	var plain [32]byte
	copy(plain[:], digest[14:])
	for i := 18; i < len(plain); i++ {
		plain[i] = 14
	}
	var blob [48]byte
	copy(blob[:16], iv[:])
	for off := 0; off < 32; off += 16 {
		var block [16]byte
		for j := range block {
			block[j] = plain[off+j] ^ blob[off+j]
		}
		mainlandZhihuBlock(blob[off+16:off+32], block[:])
	}
	const alphabet = "6fpLRqJO8M/c3jnYxFkUVC4ZIG12SiH=5v0mXDazWBTsuw7QetbKdoPyAl+hN9rgE"
	masks := [16]byte{58, 0, 0, 0, 0, 40, 3, 0, 0, 0, 32, 14, 0, 0, 0, 0}
	var sextets [64]byte
	for i := range sextets {
		bit := i * 6
		for j := range 6 {
			sextets[i] = sextets[i]<<1 | (blob[(bit+j)/8]>>uint(7-(bit+j)%8))&1
		}
	}
	var out [64]byte
	for i := range out {
		out[i] = alphabet[sextets[63-i]^masks[i%16]]
	}
	return "2.0_" + string(out[:])
}
