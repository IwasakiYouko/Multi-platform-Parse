package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
	"multiparse/internal/netx"
)

const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

type Resolver func(context.Context, *http.Client, string, string) (*model.ParseResult, error)

var registryMu sync.RWMutex
var registry = map[string]Resolver{}

func Register(id string, fn Resolver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[id]; exists {
		panic("duplicate resolver: " + id)
	}
	registry[id] = fn
}
func Available(id string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[id] != nil
}
func Resolve(ctx context.Context, cfg model.Config, rawURL string) (*model.ParseResult, error) {
	rawURL = netx.NormalizeURL(strings.TrimSpace(rawURL))
	p, ok := model.Identify(rawURL)
	if !ok {
		return nil, errors.New("不支持的平台链接")
	}
	policy := cfg.Platforms[p.ID]
	if !cfg.Policy.Enabled || !policy.Enabled || policy.NSFWBlocked {
		return nil, errors.New("平台策略拒绝解析")
	}
	registryMu.RLock()
	fn := registry[p.ID]
	registryMu.RUnlock()
	if fn == nil {
		return nil, fmt.Errorf("%s解析器不可用", p.Name)
	}
	client, err := netx.Client(cfg, p.ID)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	r, err := fn(ctx, client, rawURL, policy.Cookie)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("平台未返回解析结果")
	}
	r.Platform = p.ID
	if r.SourceURL == "" {
		r.SourceURL = rawURL
	}
	if final, known := model.Identify(r.SourceURL); known {
		fp := cfg.Platforms[final.ID]
		if !fp.Enabled || fp.NSFWBlocked {
			return nil, errors.New("跳转目标平台已被拒绝")
		}
	}
	if r.Headers == nil {
		r.Headers = Headers("", r.SourceURL)
	}
	seen := make(map[string]bool)
	media := make([]model.Media, 0, len(r.Media))
	for _, m := range r.Media {
		if m.URL == "" {
			continue
		}
		u, e := url.Parse(m.URL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		k := m.Kind + ":" + m.URL
		if seen[k] {
			continue
		}
		seen[k] = true
		if m.Headers == nil {
			m.Headers = Headers("", r.SourceURL)
		}
		media = append(media, m)
	}
	r.Media = media
	if strings.TrimSpace(r.Content) == "" && len(r.Media) == 0 {
		return nil, errors.New("未找到公开正文或媒体，可能需要Cookie或已被删除")
	}
	if (r.Type == "video" || r.Type == "image" || r.Type == "multimedia") && len(r.Media) == 0 {
		return nil, errors.New("未取得媒体，可能需要登录或受到风控限制")
	}
	return r, nil
}
func Headers(cookie, referer string) map[string]string {
	h := map[string]string{"User-Agent": UserAgent, "Accept": "*/*"}
	if referer != "" {
		h["Referer"] = referer
	}
	if cookie != "" {
		h["Cookie"] = cookie
	}
	return h
}
func Get(ctx context.Context, client *http.Client, raw string, headers map[string]string) ([]byte, string, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return nil, "", e
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := client.Do(req)
	if e != nil {
		var ue *url.Error
		if errors.As(e, &ue) {
			e = ue.Err
		}
		return nil, "", fmt.Errorf("平台请求失败：%w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Request.URL.String(), fmt.Errorf("平台返回HTTP %d", resp.StatusCode)
	}
	const max = 16 << 20
	b, e := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if e != nil {
		return nil, "", e
	}
	if len(b) > max {
		return nil, "", errors.New("平台页面超过16MiB限制")
	}
	return b, resp.Request.URL.String(), nil
}
func JSON(ctx context.Context, client *http.Client, raw string, headers map[string]string, target any) error {
	b, _, e := Get(ctx, client, raw, headers)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(target); e != nil {
		return fmt.Errorf("平台返回的JSON无效：%w", e)
	}
	return nil
}
func Text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
func Number(v any) float64     { n, _ := strconv.ParseFloat(Text(v), 64); return n }
func Obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func Array(v any) []any        { a, _ := v.([]any); return a }
func Path(v any, keys ...string) any {
	for _, k := range keys {
		if m, ok := v.(map[string]any); ok {
			v = m[k]
		} else if a, ok := v.([]any); ok {
			i, e := strconv.Atoi(k)
			if e != nil || i < 0 || i >= len(a) {
				return nil
			}
			v = a[i]
		} else {
			return nil
		}
	}
	return v
}
func Walk(v any, visit func(map[string]any)) { walk(v, visit, 0) }
func walk(v any, visit func(map[string]any), depth int) {
	if depth > 80 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		visit(x)
		for _, c := range x {
			walk(c, visit, depth+1)
		}
	case []any:
		for _, c := range x {
			walk(c, visit, depth+1)
		}
	}
}

// DecodeState extracts balanced JSON without evaluating page-provided JavaScript.
func DecodeState(page []byte, marker string) (any, error) {
	at := bytes.Index(page, []byte(marker))
	if at < 0 {
		return nil, fmt.Errorf("页面缺少状态 %s", marker)
	}
	rest := bytes.TrimSpace(page[at+len(marker):])
	rest = bytes.TrimLeft(rest, " =:\t\r\n")
	if bytes.HasPrefix(rest, []byte("JSON.parse(")) {
		rest = bytes.TrimSpace(rest[len("JSON.parse("):])
		if len(rest) > 0 && rest[0] == '"' {
			var encoded string
			d := json.NewDecoder(bytes.NewReader(rest))
			if e := d.Decode(&encoded); e == nil {
				var v any
				d = json.NewDecoder(strings.NewReader(encoded))
				d.UseNumber()
				if e = d.Decode(&v); e == nil {
					return v, nil
				}
			}
		}
	}
	start := -1
	for i, c := range rest {
		if c == '{' || c == '[' {
			start = i
			break
		}
		if c == '<' {
			break
		}
	}
	if start < 0 {
		return nil, errors.New("页面状态不是JSON对象")
	}
	rest = rest[start:]
	depth := 0
	quoted := byte(0)
	escaped := false
	for i, c := range rest {
		if quoted != 0 {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == quoted {
				quoted = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quoted = c
			continue
		}
		if c == '{' || c == '[' {
			depth++
		}
		if c == '}' || c == ']' {
			depth--
			if depth == 0 {
				b := rest[:i+1]
				var v any
				d := json.NewDecoder(bytes.NewReader(b))
				d.UseNumber()
				if e := d.Decode(&v); e == nil {
					return v, nil
				}
				b = normalizeJSConstants(b)
				d = json.NewDecoder(bytes.NewReader(b))
				d.UseNumber()
				if e := d.Decode(&v); e != nil {
					return nil, fmt.Errorf("页面状态无法解码：%w", e)
				}
				return v, nil
			}
		}
	}
	return nil, errors.New("页面状态不完整")
}
func normalizeJSConstants(b []byte) []byte {
	var out bytes.Buffer
	in := byte(0)
	escaped := false
	for i := 0; i < len(b); {
		c := b[i]
		if in != 0 {
			out.WriteByte(c)
			i++
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == in {
				in = 0
			}
			continue
		}
		if c == '"' {
			in = c
			out.WriteByte(c)
			i++
			continue
		}
		if bytes.HasPrefix(b[i:], []byte("new Set(")) {
			start := i + len("new Set(")
			depth := 1
			quote := byte(0)
			escape := false
			end := -1
			for j := start; j < len(b); j++ {
				x := b[j]
				if quote != 0 {
					if escape {
						escape = false
					} else if x == '\\' {
						escape = true
					} else if x == quote {
						quote = 0
					}
					continue
				}
				if x == '"' || x == '\'' {
					quote = x
					continue
				}
				if x == '(' {
					depth++
				}
				if x == ')' {
					depth--
					if depth == 0 {
						end = j
						break
					}
				}
			}
			if end >= 0 {
				inner := bytes.TrimSpace(b[start:end])
				if len(inner) >= 2 && inner[0] == '[' && inner[len(inner)-1] == ']' {
					out.Write(normalizeJSConstants(inner))
					i = end + 1
					continue
				}
			}
		}
		replaced := false
		for _, token := range []string{"undefined", "NaN", "Infinity"} {
			if bytes.HasPrefix(b[i:], []byte(token)) && (i == 0 || strings.ContainsRune("[:, \n\t", rune(b[i-1]))) {
				end := i + len(token)
				if end == len(b) || strings.ContainsRune(",]} \n\t", rune(b[end])) {
					out.WriteString("null")
					i = end
					replaced = true
					break
				}
			}
		}
		if !replaced {
			out.WriteByte(c)
			i++
		}
	}
	return out.Bytes()
}
func CleanText(s string) string {
	return strings.TrimSpace(strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) }), " "))
}
func Absolute(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return ""
	}
	b, e := url.Parse(base)
	if e != nil {
		return ""
	}
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	u = b.ResolveReference(u)
	if u.Scheme != "https" && u.Scheme != "http" {
		return ""
	}
	return u.String()
}

// Page handles actual article HTML, media elements and schema.org objects; video
// adapters must use their platform state/API instead of treating a poster as video.
func Page(ctx context.Context, client *http.Client, raw, cookie string) (*model.ParseResult, error) {
	b, final, e := Get(ctx, client, raw, Headers(cookie, raw))
	if e != nil {
		return nil, e
	}
	doc, e := goquery.NewDocumentFromReader(bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	r := &model.ParseResult{SourceURL: final, Type: "article", Media: []model.Media{}, Headers: Headers("", final)}
	meta := func(key string) string {
		val, _ := doc.Find("meta[property='" + key + "'],meta[name='" + key + "']").First().Attr("content")
		return val
	}
	r.Title = meta("og:title")
	if r.Title == "" {
		r.Title = CleanText(doc.Find("title").First().Text())
	}
	r.Author = meta("author")
	body := doc.Find("article,[role='article'],#js_content,.article-content,.article_content,.RichText,.topic_content,.note,.blog_entry,.entry-content").First()
	if body.Length() > 0 {
		body.Find("script,style,nav,footer,button,form").Remove()
		r.Content = strings.TrimSpace(body.Text())
	} else {
		r.Content = meta("og:description")
		if r.Content == "" {
			r.Content = meta("description")
		}
	}
	actualArticle := body.Length() > 0 && CleanText(body.Text()) != ""
	seen := map[string]bool{}
	add := func(kind, raw, thumb string) {
		u := Absolute(final, raw)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		r.Media = append(r.Media, model.Media{Kind: kind, URL: u, ThumbnailURL: Absolute(final, thumb), Headers: Headers("", final)})
	}
	scope := body
	if scope.Length() == 0 {
		scope = doc.Selection
	}
	scope.Find("img,video,audio").Each(func(_ int, s *goquery.Selection) {
		switch goquery.NodeName(s) {
		case "img":
			if body.Length() == 0 {
				return
			}
			raw, _ := s.Attr("data-src")
			if raw == "" {
				raw, _ = s.Attr("data-original")
			}
			if raw == "" {
				raw, _ = s.Attr("src")
			}
			add("image", raw, "")
		case "video", "audio":
			src, _ := s.Attr("src")
			if src == "" {
				src, _ = s.Find("source").First().Attr("src")
			}
			thumb, _ := s.Attr("poster")
			kind := goquery.NodeName(s)
			add(kind, src, thumb)
		}
	})
	for _, key := range []string{"og:video:url", "og:video:secure_url", "og:video"} {
		u := meta(key)
		if regexp.MustCompile(`(?i)\.(mp4|m3u8|webm)(\?|$)`).MatchString(u) {
			add("video", u, meta("og:image"))
		}
	}
	doc.Find("script[type='application/ld+json']").Each(func(_ int, s *goquery.Selection) {
		var v any
		if json.Unmarshal([]byte(s.Text()), &v) != nil {
			return
		}
		Walk(v, func(m map[string]any) {
			typ := Text(m["@type"])
			if r.Author == "" {
				r.Author = Text(Path(m, "author", "name"))
			}
			if r.Title == "" {
				r.Title = Text(m["headline"])
			}
			if text := Text(m["articleBody"]); strings.TrimSpace(text) != "" {
				actualArticle = true
				if body.Length() == 0 {
					r.Content = text
				}
			}
			switch typ {
			case "VideoObject":
				add("video", Text(m["contentUrl"]), Text(m["thumbnailUrl"]))
			case "ImageObject":
				add("image", Text(m["contentUrl"]), "")
			}
		})
	})
	if !actualArticle && len(r.Media) == 0 {
		return nil, errors.New("页面未提供可提取的公开正文或媒体，可能需要Cookie")
	}
	hasVideo, hasImage := false, false
	for _, m := range r.Media {
		hasVideo = hasVideo || m.Kind == "video"
		hasImage = hasImage || m.Kind == "image"
	}
	if hasVideo && hasImage {
		r.Type = "multimedia"
	} else if hasVideo {
		r.Type = "video"
	} else if hasImage && r.Content == "" {
		r.Type = "image"
	}
	return r, nil
}
