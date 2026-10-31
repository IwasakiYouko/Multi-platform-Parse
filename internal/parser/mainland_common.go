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
	"strings"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
)

const mainlandMobileUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"

func mainlandPost(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, body []byte, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return fmt.Errorf("平台响应超过 16 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("平台接口 HTTP %d（可能需要有效登录 Cookie 或访问权限）", resp.StatusCode)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err = dec.Decode(target); err != nil {
		return fmt.Errorf("平台接口未返回有效 JSON: %w", err)
	}
	return nil
}
func mainlandPostJSON(ctx context.Context, c *http.Client, u string, h map[string]string, v, target any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h["Content-Type"] = "application/json"
	return mainlandPost(ctx, c, u, h, b, target)
}
func mainlandFirst(values ...any) string {
	for _, v := range values {
		if s := Text(v); s != "" {
			return s
		}
	}
	return ""
}
func mainlandURL(raw, base string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	b, _ := url.Parse(base)
	if b != nil {
		u = b.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}
func mainlandDoc(source string) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(strings.NewReader(source))
}
func mainlandText(source string) string {
	d, e := mainlandDoc(html.UnescapeString(source))
	if e != nil {
		return strings.TrimSpace(source)
	}
	d.Find("script,style,noscript").Remove()
	d.Find("br").ReplaceWithHtml("\n")
	d.Find("p,div,li,h1,h2,h3,blockquote").Each(func(_ int, s *goquery.Selection) { s.AppendHtml("\n") })
	return strings.TrimSpace(d.Text())
}
func mainlandAdd(r *model.ParseResult, kind, raw string, details map[string]any) {
	u := mainlandURL(raw, r.SourceURL)
	if u == "" {
		return
	}
	for _, m := range r.Media {
		if m.URL == u {
			return
		}
	}
	m := model.Media{Kind: kind, URL: u, Headers: Headers("", r.SourceURL)}
	if details != nil {
		m.Width = int(Number(details["width"]))
		m.Height = int(Number(details["height"]))
		m.Duration = Number(details["duration"])
		m.ThumbnailURL = mainlandURL(mainlandFirst(details["thumbnail"], details["cover"]), r.SourceURL)
	}
	r.Media = append(r.Media, m)
}
func mainlandBody(r *model.ParseResult, source string) error {
	d, err := mainlandDoc(source)
	if err != nil {
		return err
	}
	d.Find("img,video,audio").Each(func(_ int, s *goquery.Selection) {
		kind := goquery.NodeName(s)
		if s.ParentsFiltered("video,audio").Length() > 0 {
			return
		}
		if kind == "img" {
			for _, key := range []string{"data-original", "data-actualsrc", "data-src", "src"} {
				raw, _ := s.Attr(key)
				if mainlandURL(raw, r.SourceURL) != "" {
					if !strings.Contains(raw, "/equation") {
						mainlandAdd(r, "image", raw, nil)
					}
					break
				}
			}
			return
		}
		urls := mainlandMediaSources(s, r.SourceURL)
		if len(urls) == 0 {
			return
		}
		before := len(r.Media)
		poster, _ := s.Attr("poster")
		mainlandAdd(r, kind, urls[0], map[string]any{"thumbnail": poster})
		if len(r.Media) > before {
			r.Media[before].BackupURLs = urls[1:]
		}
	})
	r.Content = mainlandText(source)
	return nil
}

func mainlandMediaSources(s *goquery.Selection, base string) []string {
	var urls []string
	appendURL := func(raw string) {
		u := mainlandURL(raw, base)
		if u == "" {
			return
		}
		for _, existing := range urls {
			if existing == u {
				return
			}
		}
		urls = append(urls, u)
	}
	src, _ := s.Attr("src")
	appendURL(src)
	kind := goquery.NodeName(s)
	s.ChildrenFiltered("source").Each(func(_ int, source *goquery.Selection) {
		mime, _ := source.Attr("type")
		mime = strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
		switch mime {
		case "", "application/vnd.apple.mpegurl", "application/x-mpegurl":
		case "video/mp4", "video/webm", "video/ogg":
			if kind != "video" {
				return
			}
		case "audio/mpeg", "audio/mp4", "audio/webm", "audio/ogg", "audio/wav", "audio/x-wav":
			if kind != "audio" {
				return
			}
		default:
			return
		}
		raw, _ := source.Attr("src")
		appendURL(raw)
	})
	return urls
}
func mainlandFinish(r *model.ParseResult, expectedVideo bool) (*model.ParseResult, error) {
	video, image := false, false
	for _, m := range r.Media {
		if m.Kind == "video" {
			video = true
		} else if m.Kind == "image" {
			image = true
		}
	}
	if expectedVideo && !video {
		return nil, fmt.Errorf("%s 视频播放地址不可用；内容可能需要登录、已删除或受访问限制", r.Platform)
	}
	if r.Type == "" {
		switch {
		case video && image:
			r.Type = "multimedia"
		case video:
			r.Type = "video"
		case image:
			r.Type = "image"
		default:
			r.Type = "article"
		}
	}
	if strings.TrimSpace(r.Content) == "" && len(r.Media) == 0 {
		return nil, fmt.Errorf("%s 未返回可读取的正文或媒体；请检查链接和登录 Cookie", r.Platform)
	}
	if r.Title == "" {
		r.Title = r.Content
		if runes := []rune(r.Title); len(runes) > 100 {
			r.Title = string(runes[:100])
		}
	}
	return r, nil
}
func mainlandResult(id, raw string) *model.ParseResult {
	return &model.ParseResult{Platform: id, SourceURL: raw, Headers: Headers("", raw)}
}
func mainlandCookie(cookie, name string) string {
	for _, entry := range strings.Split(cookie, ";") {
		pair := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(pair) == 2 && pair[0] == name {
			return strings.TrimSpace(pair[1])
		}
	}
	return ""
}
