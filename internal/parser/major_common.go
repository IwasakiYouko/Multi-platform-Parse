// Native platform extractors adapted from ParseHub (https://github.com/z-mio/ParseHub).
// MIT License
// Copyright (c) 2024 梓澪
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
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
	"strings"

	"multiparse/internal/model"
)

const majorUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
const majorMobileUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1"

func majorHeaders(cookie, referer, raw string, domains ...string) map[string]string {
	h := Headers("", referer)
	h["User-Agent"] = majorUA
	u, err := url.Parse(raw)
	if err == nil {
		for _, d := range domains {
			if u.Hostname() == d || strings.HasSuffix(u.Hostname(), "."+d) {
				if cookie != "" {
					h["Cookie"] = cookie
				}
				break
			}
		}
	}
	return h
}
func majorMediaHeaders(referer string) map[string]string {
	return map[string]string{"User-Agent": majorUA, "Referer": referer}
}
func majorURL(v any) string {
	return majorURLDepth(v, 0)
}
func majorURLDepth(v any, depth int) string {
	if depth >= 32 {
		return ""
	}
	switch value := v.(type) {
	case string:
		if strings.HasPrefix(value, "//") {
			return "https:" + value
		}
		if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
			return value
		}
	case []any:
		for _, item := range value {
			if uri := majorURLDepth(item, depth+1); uri != "" {
				return uri
			}
		}
	case map[string]any:
		for _, key := range []string{"url", "url_list", "urlList", "masterUrl", "master_url", "baseUrl", "base_url", "src"} {
			if item, exists := value[key]; exists {
				if uri := majorURLDepth(item, depth+1); uri != "" {
					return uri
				}
			}
		}
	}
	return ""
}
func majorURLs(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s := majorURL(x); s != "" {
				out = append(out, s)
			}
		}
	} else if s := majorURL(v); s != "" {
		out = append(out, s)
	}
	return out
}
func majorFirstText(values ...any) string {
	for _, v := range values {
		if s := Text(v); s != "" {
			return s
		}
	}
	return ""
}
func majorObject(v any) map[string]any {
	if s, ok := v.(string); ok {
		var x map[string]any
		if json.Unmarshal([]byte(s), &x) == nil {
			return x
		}
	}
	return Obj(v)
}
func majorResultType(r *model.ParseResult) {
	image, video := false, false
	for _, m := range r.Media {
		image = image || m.Kind == "image"
		video = video || m.Kind == "video"
	}
	switch {
	case image && video:
		r.Type = "multimedia"
	case video:
		r.Type = "video"
	case image:
		r.Type = "image"
	default:
		r.Type = "article"
	}
}
func majorPost(ctx context.Context, c *http.Client, endpoint string, h map[string]string, body any, target any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("platform request rejected: HTTP %d", resp.StatusCode)
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 16*1024*1024 {
		return fmt.Errorf("platform response exceeds 16 MiB")
	}
	if err = json.Unmarshal(b, target); err != nil {
		return fmt.Errorf("platform returned invalid JSON: %w", err)
	}
	return nil
}

var majorScript = regexp.MustCompile(`(?is)<script\b[^>]*\bid\s*=\s*["']([^"']+)["'][^>]*>(.*?)</script>`)

func majorStates(page []byte, markers ...string) []any {
	var states []any
	for _, marker := range markers {
		if s, err := DecodeState(page, marker); err == nil {
			states = append(states, s)
		}
	}
	for _, m := range majorScript.FindAllSubmatch(page, -1) {
		switch string(m[1]) {
		case "RENDER_DATA", "__NEXT_DATA__", "__UNIVERSAL_DATA_FOR_REHYDRATION__", "SIGI_STATE":
			var s any
			if json.Unmarshal(m[2], &s) == nil {
				states = append(states, s)
				continue
			}
			if decoded, err := url.PathUnescape(string(m[2])); err == nil && json.Unmarshal([]byte(decoded), &s) == nil {
				states = append(states, s)
			}
		}
	}
	return states
}
func majorCookie(cookie, name string) string {
	for _, piece := range strings.Split(cookie, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(piece), "=")
		if ok && k == name {
			return v
		}
	}
	return ""
}
func majorSetCookie(cookie, name, value string) string {
	var out []string
	for _, piece := range strings.Split(cookie, ";") {
		piece = strings.TrimSpace(piece)
		key, _, ok := strings.Cut(piece, "=")
		if ok && key != name {
			out = append(out, piece)
		}
	}
	out = append(out, name+"="+value)
	return strings.Join(out, "; ")
}
func majorSafeRequestError(err error) string {
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return requestError.Err.Error()
	}
	return err.Error()
}
