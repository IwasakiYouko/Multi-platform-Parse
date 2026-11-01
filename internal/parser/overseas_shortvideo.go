package parser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
)

func init() { Register("tiktok", overseasTikTok) }

func tikTokItemID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Only video/photo routes identify posts; profiles and embed pages do not.
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range parts {
		if (part == "video" || part == "photo") && i+1 < len(parts) {
			id := parts[i+1]
			if id == "" {
				return ""
			}
			for _, ch := range id {
				if ch < '0' || ch > '9' {
					return ""
				}
			}
			return id
		}
	}
	return ""
}

func tikTokUniversal(body []byte) map[string]any {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var state map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(html.UnescapeString(doc.Find("script#__UNIVERSAL_DATA_FOR_REHYDRATION__").First().Text()))), &state) != nil {
		return nil
	}
	return Obj(state["__DEFAULT_SCOPE__"])
}

func tikTokBase64(value string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "="))
}

func tikTokChallenge(ctx context.Context, s *overseasSession, body []byte) bool {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return false
	}
	class := func(id string) string { value, _ := doc.Find("#" + id).First().Attr("class"); return value }
	encoded, name := class("cs"), class("wci")
	if encoded == "" || name == "" {
		return false
	}
	data, err := tikTokBase64(encoded)
	if err != nil {
		return false
	}
	var challenge map[string]any
	if json.Unmarshal(data, &challenge) != nil {
		return false
	}
	prefix, err := tikTokBase64(Text(Path(challenge, "v", "a")))
	if err != nil || len(prefix) > 4096 {
		return false
	}
	expected, err := tikTokBase64(Text(Path(challenge, "v", "c")))
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	buffer := make([]byte, len(prefix), len(prefix)+7)
	copy(buffer, prefix)
	found := false
	for i := 0; i <= 1000000; i++ {
		if i%1024 == 0 && ctx.Err() != nil {
			return false
		}
		candidate := strconv.AppendInt(buffer[:len(prefix)], int64(i), 10)
		digest := sha256.Sum256(candidate)
		if bytes.Equal(digest[:], expected) {
			challenge["d"] = base64.StdEncoding.EncodeToString(candidate[len(prefix):])
			found = true
			break
		}
	}
	if !found {
		return false
	}
	data, err = json.Marshal(challenge)
	if err != nil {
		return false
	}
	cookies := []*http.Cookie{{Name: name, Value: base64.StdEncoding.EncodeToString(data), Domain: ".tiktok.com", Path: "/", Secure: true}}
	if rci, rs := class("rci"), class("rs"); rci != "" && rs != "" {
		cookies = append(cookies, &http.Cookie{Name: rci, Value: rs, Domain: ".tiktok.com", Path: "/", Secure: true})
	}
	for _, ck := range cookies {
		if ck.Valid() != nil {
			return false
		}
	}
	u, _ := url.Parse("https://www.tiktok.com/")
	s.client.Jar.SetCookies(u, cookies)
	return true
}

func tikTokURLs(value any) []string {
	switch node := value.(type) {
	case string:
		if address := overseasURL(node); address != "" {
			return []string{address}
		}
	case []any:
		var urls []string
		for _, child := range node {
			urls = append(urls, tikTokURLs(child)...)
		}
		return urls
	case map[string]any:
		for _, key := range []string{"url_list", "UrlList", "urlList"} {
			if child, ok := node[key]; ok {
				return tikTokURLs(child)
			}
		}
	}
	return nil
}

func tikTokFirstURL(value any, preferred bool) string {
	urls := tikTokURLs(value)
	if preferred {
		for _, address := range urls {
			if strings.Contains(address, "aweme") {
				return address
			}
		}
	}
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}

func tikTokValue(node map[string]any, keys ...string) any {
	for _, key := range keys {
		value := node[key]
		switch x := value.(type) {
		case nil:
			continue
		case string:
			if x == "" {
				continue
			}
		case float64:
			if x == 0 {
				continue
			}
		case map[string]any:
			if len(x) == 0 {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		}
		return value
	}
	return nil
}

func tikTokVideo(video map[string]any) (model.Media, error) {
	var cover string
	for _, key := range []string{"origin_cover", "cover", "dynamic_cover", "originCover", "dynamicCover"} {
		if cover = tikTokFirstURL(video[key], false); cover != "" {
			break
		}
	}
	best := model.Media{Kind: "video", ThumbnailURL: cover, Ext: "mp4"}
	quality := [3]float64{-1, -1, -1}
	for _, entry := range Array(tikTokValue(video, "bit_rate", "bitrateInfo")) {
		rate := Obj(entry)
		address := tikTokValue(rate, "play_addr", "PlayAddr")
		stream := tikTokFirstURL(address, true)
		if stream == "" {
			continue
		}
		addr := Obj(address)
		width := Number(tikTokValue(addr, "width", "Width"))
		if width == 0 {
			width = Number(video["width"])
		}
		height := Number(tikTokValue(addr, "height", "Height"))
		if height == 0 {
			height = Number(video["height"])
		}
		bitrate := Number(tikTokValue(rate, "bit_rate", "Bitrate", "bitrate"))
		size := Number(tikTokValue(addr, "data_size", "DataSize"))
		if size == 0 {
			size = Number(rate["data_size"])
		}
		duration := Number(tikTokValue(addr, "duration", "Duration"))
		if duration == 0 {
			duration = Number(video["duration"])
		}
		q := [3]float64{width * height, bitrate, size}
		if q[0] > quality[0] || q[0] == quality[0] && (q[1] > quality[1] || q[1] == quality[1] && q[2] > quality[2]) {
			quality = q
			best.URL, best.Width, best.Height, best.Duration = stream, int(width), int(height), duration
		}
	}
	if best.URL == "" {
		address := tikTokValue(video, "play_addr", "playAddr")
		best.URL = tikTokFirstURL(address, true)
		addr := Obj(address)
		best.Width = int(Number(tikTokValue(addr, "width")))
		if best.Width == 0 {
			best.Width = int(Number(video["width"]))
		}
		best.Height = int(Number(tikTokValue(addr, "height")))
		if best.Height == 0 {
			best.Height = int(Number(video["height"]))
		}
		best.Duration = Number(tikTokValue(addr, "duration"))
		if best.Duration == 0 {
			best.Duration = Number(video["duration"])
		}
	}
	if best.URL == "" {
		best.URL = tikTokFirstURL(tikTokValue(video, "download_addr", "downloadAddr"), true)
		best.Width, best.Height = int(Number(video["width"])), int(Number(video["height"]))
		best.Duration = Number(video["duration"])
	}
	if best.URL == "" {
		return best, fmt.Errorf("tiktok: no accessible video URL")
	}
	return best, nil
}

func overseasTikTok(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	s := newOverseasSession(c, "tiktok.com", cookie)
	if s.client.Timeout == 0 || s.client.Timeout > 15*time.Second {
		s.client.Timeout = 15 * time.Second
	}
	s.headers["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	s.headers["Accept-Language"] = "en-US,en;q=0.9"
	u, err := url.Parse(raw)
	if err != nil || !s.allowed(u) {
		return nil, fmt.Errorf("tiktok: invalid post URL")
	}
	final := raw
	id := tikTokItemID(raw)
	if id == "" {
		_, final, err = s.request(ctx, http.MethodGet, raw, nil, true)
		if err != nil {
			return nil, err
		}
		id = tikTokItemID(final)
	}
	if id == "" || strings.Contains(strings.ToLower(final), "notfound") {
		return nil, fmt.Errorf("tiktok: cannot identify video or photo post")
	}
	endpoint := "https://www.tiktok.com/@_/video/" + id
	var scope map[string]any
	for attempt := range 3 {
		body, resolved, err := s.request(ctx, http.MethodGet, endpoint, nil, true)
		if err != nil {
			return nil, err
		}
		resolvedURL, _ := url.Parse(resolved)
		if resolvedURL.Path == "/login" {
			return nil, fmt.Errorf("tiktok: login required")
		}
		scope = tikTokUniversal(body)
		if len(scope) != 0 {
			break
		}
		if tikTokChallenge(ctx, s, body) {
			continue
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	detail := Obj(scope["webapp.video-detail"])
	item := Obj(Path(detail, "itemInfo", "itemStruct"))
	if len(item) == 0 {
		status := int(Number(detail["statusCode"]))
		if status == 10204 {
			return nil, &ContentUnavailableError{Reason: "tiktok: post does not exist or is unavailable"}
		}
		if status == 10216 || status == 10222 {
			return nil, fmt.Errorf("tiktok: login or permission required")
		}
		return nil, fmt.Errorf("tiktok: missing post details (status %d)", status)
	}
	if got := overseasFirst(item["aweme_id"], item["id"]); got != "" && got != id {
		return nil, fmt.Errorf("tiktok: returned post ID does not match")
	}
	r := &model.ParseResult{Platform: "tiktok", SourceURL: final, Title: Text(item["desc"]), Content: Text(item["desc"]), Author: overseasFirst(Path(item, "author", "nickname"), Path(item, "author", "uniqueId"))}
	photos := Obj(tikTokValue(item, "image_post_info", "imagePost"))
	if len(photos) > 0 {
		for _, entry := range Array(photos["images"]) {
			photo := Obj(entry)
			image := tikTokValue(photo, "display_image", "displayImage", "imageURL", "image")
			address := tikTokFirstURL(image, false)
			if address == "" {
				continue
			}
			width := Number(Path(image, "width"))
			if width == 0 {
				width = Number(photo["imageWidth"])
			}
			height := Number(Path(image, "height"))
			if height == 0 {
				height = Number(photo["imageHeight"])
			}
			overseasAdd(r, model.Media{Kind: "image", URL: address, Width: int(width), Height: int(height)})
		}
		if len(r.Media) == 0 {
			return nil, fmt.Errorf("tiktok: no accessible photo URLs")
		}
	} else {
		media, err := tikTokVideo(Obj(item["video"]))
		if err != nil {
			return nil, err
		}
		media.Headers = Headers("", "https://www.tiktok.com/")
		overseasAdd(r, media)
	}
	return overseasFinish(r)
}
