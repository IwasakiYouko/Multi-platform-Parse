package parser

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/bits"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
)

func init() { Register("zhihu", mainlandZhihu) }
func mainlandZhihu(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	r := mainlandResult("zhihu", raw)
	r.Type = "article"
	u, e := url.Parse(raw)
	if e != nil {
		return nil, e
	}
	if u.Hostname() == "daily.zhihu.com" || strings.Contains(u.Path, "/story/") {
		m := regexp.MustCompile(`/story/(\d+)`).FindStringSubmatch(u.Path)
		if len(m) == 0 {
			return nil, fmt.Errorf("知乎日报链接缺少 story ID")
		}
		var v any
		if e = JSON(ctx, c, "https://daily.zhihu.com/api/7/story/"+m[1], Headers("", raw), &v); e != nil {
			return nil, e
		}
		r.Title = Text(Path(v, "title"))
		_ = mainlandBody(r, Text(Path(v, "body")))
		return mainlandFinish(r, false)
	}
	dc := mainlandCookie(cookie, "d_c0")
	if dc == "" {
		return nil, fmt.Errorf("知乎需要配置包含 d_c0 的已登录 Cookie")
	}
	endpoint := ""
	pin, video := false, false
	if m := regexp.MustCompile(`/answer/(\d+)`).FindStringSubmatch(u.Path); len(m) > 0 {
		endpoint = "https://www.zhihu.com/api/v4/answers/" + m[1] + "?include=content"
	} else if m := regexp.MustCompile(`/question/(\d+)`).FindStringSubmatch(u.Path); len(m) > 0 {
		var listing any
		if e = mainlandZhihuJSON(ctx, c, "https://www.zhihu.com/api/v4/questions/"+m[1]+"/answers?offset=&limit=1&sort_by=default&include=data%5B%2A%5D.content", cookie, dc, &listing); e != nil {
			return nil, e
		}
		answers := Array(Path(listing, "data"))
		if len(answers) == 0 {
			return nil, fmt.Errorf("知乎问题没有可读取回答，可能需要登录或权限")
		}
		endpoint = "https://www.zhihu.com/api/v4/answers/" + Text(Path(answers[0], "id")) + "?include=content"
	} else if m := regexp.MustCompile(`/(?:appview/)?p/(\d+)`).FindStringSubmatch(u.Path); len(m) > 0 {
		endpoint = "https://zhuanlan.zhihu.com/api/articles/" + m[1]
	} else if m := regexp.MustCompile(`/pin/(\d+)`).FindStringSubmatch(u.Path); len(m) > 0 {
		endpoint = "https://www.zhihu.com/api/v4/pins/" + m[1]
		pin = true
	} else if m := regexp.MustCompile(`/zvideo/(\d+)`).FindStringSubmatch(u.Path); len(m) > 0 {
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
	r.Author = Text(Path(v, "author", "name"))
	expected := video
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
	} else if pin {
		r.Type = ""
		for _, part := range Array(Path(v, "content")) {
			switch Text(Path(part, "type")) {
			case "text":
				r.Title = mainlandFirst(Path(part, "title"), r.Title)
				old := r.Content
				_ = mainlandBody(r, Text(Path(part, "content")))
				if old != "" {
					r.Content = old + "\n" + r.Content
				}
			case "image":
				mainlandAdd(r, "image", mainlandFirst(Path(part, "original_url"), Path(part, "url")), Obj(part))
			case "video":
				expected = true
				if !mainlandZhihuPlaylist(r, Path(part, "video_info")) {
					id := mainlandFirst(Path(part, "video_id"), Path(part, "video_info", "video_id"), Path(part, "video_info", "id"))
					if id != "" {
						if e = mainlandZhihuLens(ctx, c, r, id); e != nil {
							return nil, e
						}
					}
				}
			}
		}
	} else {
		source := Text(Path(v, "content"))
		_ = mainlandBody(r, source)
		doc, e := mainlandDoc(source)
		if e != nil {
			return nil, e
		}
		var videoIDs []string
		doc.Find("[data-video-id],a.video-box,iframe[src*='video'],[data-lens-id]").Each(func(_ int, s *goquery.Selection) {
			expected = true
			id, _ := s.Attr("data-video-id")
			if id == "" {
				id, _ = s.Attr("data-lens-id")
			}
			if id == "" {
				for _, key := range []string{"href", "src"} {
					v, _ := s.Attr(key)
					if match := regexp.MustCompile(`(?:video/|videos/|video_id=)(\d+)`).FindStringSubmatch(v); len(match) > 0 {
						id = match[1]
						break
					}
				}
			}
			if id != "" {
				videoIDs = append(videoIDs, id)
			}
		})
		seen := map[string]bool{}
		for _, id := range videoIDs {
			if !seen[id] {
				seen[id] = true
				if e = mainlandZhihuLens(ctx, c, r, id); e != nil {
					return nil, e
				}
			}
		}
		for _, entry := range Array(Path(v, "attachment")) {
			if Text(Path(entry, "type")) == "video" {
				expected = true
				if !mainlandZhihuPlaylist(r, entry) {
					if id := mainlandFirst(Path(entry, "video_id"), Path(entry, "id")); id != "" {
						if e = mainlandZhihuLens(ctx, c, r, id); e != nil {
							return nil, e
						}
					}
				}
			}
		}
	}
	return mainlandFinish(r, expected)
}
func mainlandZhihuPlaylist(r *model.ParseResult, v any) bool {
	playlist := Obj(Path(v, "playlist"))
	best := ""
	score := float64(-1)
	var details map[string]any
	for _, p := range playlist {
		u := mainlandFirst(Path(p, "play_url"), Path(p, "url"))
		s := Number(Path(p, "height"))*1e9 + Number(Path(p, "bitrate"))
		if u != "" && s > score {
			best = u
			score = s
			details = Obj(p)
		}
	}
	if best == "" {
		return false
	}
	copy := map[string]any{"width": details["width"], "height": details["height"], "duration": Path(v, "duration"), "thumbnail": mainlandFirst(Path(v, "thumbnail"), Path(v, "cover_url"), Path(v, "cover"))}
	mainlandAdd(r, "video", best, copy)
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
		return fmt.Errorf("知乎视频 %s 未返回可播放清晰度，可能已删除或权限不足", id)
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
	h["X-Zse-93"] = "101_3_3.0"
	h["X-Zse-96"] = sig
	h["X-Requested-With"] = "fetch"
	if e = JSON(ctx, c, endpoint, h, target); e != nil {
		return fmt.Errorf("知乎接口访问失败；请检查登录 Cookie 或访问验证: %w", e)
	}
	if v := Path(*target, "error"); v != nil {
		return fmt.Errorf("知乎接口 %s: %s", Text(Path(v, "code")), Text(Path(v, "message")))
	}
	return nil
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
