package parser

// Provider protocols adapted from https://github.com/z-mio/ParseHub (MIT).
// Copyright (c) 2024 梓澪
//
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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"multiparse/internal/model"
)

func articleCanonical(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func articleWeixinCanonical(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	clean := url.Values{}
	for _, key := range []string{"__biz", "mid", "idx", "sn"} {
		if value := query.Get(key); value != "" {
			clean.Set(key, value)
		}
	}
	u.RawQuery = clean.Encode()
	u.Fragment = ""
	return u.String()
}

func articleFinish(r *model.ParseResult, expectedVideo bool) (*model.ParseResult, error) {
	for i := range r.Media {
		media := &r.Media[i]
		switch media.Kind {
		case "image":
			media.Ext = "jpg"
			if r.Platform == "weixin" {
				if u, err := url.Parse(media.URL); err == nil && u.Query().Get("wx_fmt") != "" {
					media.Ext = u.Query().Get("wx_fmt")
				}
			}
		case "gif":
			media.Ext = "gif"
			if r.Platform == "douban" {
				media.Ext = "mp4"
			}
		case "video":
			media.Ext = "mp4"
		}
	}
	title := r.Title
	result, err := mainlandFinish(r, expectedVideo)
	if result != nil {
		result.Title = title
	}
	return result, err
}

func articleAddCoolapkImage(r *model.ParseResult, raw string) {
	kind := "image"
	if strings.Contains(raw, ".gif") {
		kind = "gif"
	}
	before := len(r.Media)
	mainlandAdd(r, kind, raw, nil)
	if len(r.Media) > before {
		r.Media[before].Headers = map[string]string{"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"}
	}
}

func articleDoubanHeaders(r *model.ParseResult) {
	for i := range r.Media {
		r.Media[i].Headers = Headers("", "https://www.douban.com/")
	}
}

func articleDoubanDuration(value any) float64 {
	if s, ok := value.(string); ok {
		seconds := 0
		for _, part := range strings.Split(s, ":") {
			if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return 0
			}
			n, err := strconv.Atoi(part)
			if err != nil {
				return 0
			}
			seconds = seconds*60 + n
		}
		return float64(seconds)
	}
	return Number(value)
}

func articleDoubanJSON(ctx context.Context, c *http.Client, endpoint string, headers map[string]string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("豆瓣话题请求失败: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("豆瓣话题响应超过 16 MiB")
	}
	if response.StatusCode != http.StatusOK {
		var failure any
		_ = json.Unmarshal(data, &failure)
		if message := Text(Path(failure, "localized_message")); message != "" {
			return fmt.Errorf("豆瓣解析失败: %s", message)
		}
		if message := Text(Path(failure, "msg")); message != "" {
			return fmt.Errorf("豆瓣获取话题内容失败: %s", message)
		}
		return fmt.Errorf("豆瓣获取话题内容失败: HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("豆瓣话题接口未返回有效 JSON: %w", err)
	}
	return nil
}

var articleBBCodeTag = regexp.MustCompile(`(?i)\[(/?)(img|url|b|i|s|u|mask|quote|code|user|size|color|align|left|right|center|photo)(?:=([^\]]*))?\]`)

type articleBBCodeNode struct {
	tag, value, text string
	children         []*articleBBCodeNode
}

// BBCode semantics follow ParseHub's MIT-licensed Bangumi provider. Code stays literal.
func articleBBCodeHTML(source string) (string, []string) {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	root := &articleBBCodeNode{}
	stack := []*articleBBCodeNode{root}
	position := 0
	for _, match := range articleBBCodeTag.FindAllStringSubmatchIndex(source, -1) {
		current := stack[len(stack)-1]
		closing := source[match[2]:match[3]] != ""
		tag := strings.ToLower(source[match[4]:match[5]])
		current.children = append(current.children, &articleBBCodeNode{text: source[position:match[0]]})
		position = match[1]
		if current.tag == "code" && !(closing && tag == "code") {
			current.children = append(current.children, &articleBBCodeNode{text: source[match[0]:match[1]]})
			continue
		}
		if closing {
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].tag == tag {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		node := &articleBBCodeNode{tag: tag}
		if match[6] >= 0 {
			node.value = source[match[6]:match[7]]
		}
		current.children = append(current.children, node)
		stack = append(stack, node)
	}
	current := stack[len(stack)-1]
	current.children = append(current.children, &articleBBCodeNode{text: source[position:]})
	var images []string
	return articleRenderBBCode(root, &images), images
}

func articleRenderBBCode(node *articleBBCodeNode, images *[]string) string {
	if node.tag == "code" {
		var body strings.Builder
		for _, child := range node.children {
			body.WriteString(child.text)
		}
		return "<pre><code>" + html.EscapeString(strings.Trim(body.String(), "\n")) + "</code></pre>"
	}
	var body strings.Builder
	body.WriteString(strings.ReplaceAll(html.EscapeString(node.text), "\n", "<br>"))
	for _, child := range node.children {
		body.WriteString(articleRenderBBCode(child, images))
	}
	content := body.String()
	switch node.tag {
	case "img", "photo":
		var text strings.Builder
		for _, child := range node.children {
			text.WriteString(child.text)
		}
		raw := strings.TrimSpace(text.String())
		if raw == "" {
			return ""
		}
		if node.tag == "photo" {
			raw = "https://lain.bgm.tv/pic/photo/l/" + strings.TrimLeft(raw, "/")
		}
		u := mainlandURL(raw, "https://bgm.tv/")
		if u == "" {
			return ""
		}
		*images = append(*images, u)
		return articleImageHTML(u)
	case "url":
		if node.value != "" {
			if u := mainlandURL(node.value, "https://bgm.tv/"); u != "" {
				return `<a href="` + html.EscapeString(u) + `">` + content + "</a>"
			}
		}
	case "b":
		return "<strong>" + content + "</strong>"
	case "i":
		return "<em>" + content + "</em>"
	case "s":
		return "<del>" + content + "</del>"
	case "quote":
		return "<blockquote>" + content + "</blockquote>"
	case "user":
		return "@" + content
	}
	return content
}

func articleContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func articleImageHTML(raw string) string {
	return `<img src="` + html.EscapeString(raw) + `">`
}
