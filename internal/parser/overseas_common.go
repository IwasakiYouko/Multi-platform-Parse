package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
)

// These readers consume native platform state, not OpenGraph video guesses.
func overseasStates(body []byte) []any {
	var states []any
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		raw := strings.TrimSpace(s.Text())
		if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
			var v any
			if json.Unmarshal([]byte(raw), &v) == nil {
				states = append(states, v)
			}
		}
	})
	for _, marker := range []string{"window._sharedData", "window.__additionalDataLoaded(", "window.__INITIAL_STATE__", "window.__APOLLO_STATE__", "window.SIGI_STATE", "SIGI_STATE", "__UNIVERSAL_DATA_FOR_REHYDRATION__", "__NEXT_DATA__"} {
		if v, err := DecodeState(body, marker); err == nil {
			states = append(states, v)
		}
	}
	return states
}

func overseasWalk(v any, visit func(map[string]any)) {
	overseasWalkDepth(v, visit, 0)
}
func overseasWalkDepth(v any, visit func(map[string]any), depth int) {
	if depth > 80 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		visit(x)
		for _, child := range x {
			overseasWalkDepth(child, visit, depth+1)
		}
	case []any:
		for _, child := range x {
			overseasWalkDepth(child, visit, depth+1)
		}
	case string:
		if len(x) > 1 && len(x) <= 4<<20 && (x[0] == '{' || x[0] == '[') {
			var nested any
			if json.Unmarshal([]byte(x), &nested) == nil {
				overseasWalkDepth(nested, visit, depth+1)
			}
		}
	}
}
func overseasFirst(values ...any) string {
	for _, v := range values {
		if s := Text(v); s != "" {
			return s
		}
	}
	return ""
}
func overseasURL(v any) string {
	s := strings.TrimSpace(Text(v))
	if strings.HasPrefix(s, "//") {
		s = "https:" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return ""
	}
	return u.String()
}
func overseasHeaders(referer string) map[string]string { return Headers("", referer) }
func overseasAdd(r *model.ParseResult, m model.Media) {
	m.URL = overseasURL(m.URL)
	if m.URL == "" {
		return
	}
	for _, old := range r.Media {
		if old.URL == m.URL {
			return
		}
	}
	if m.Headers == nil {
		m.Headers = overseasHeaders(r.SourceURL)
	}
	r.Media = append(r.Media, m)
}
func overseasFinish(r *model.ParseResult) (*model.ParseResult, error) {
	if len(r.Media) == 0 {
		if strings.TrimSpace(r.Content) == "" {
			return nil, fmt.Errorf("%s: no accessible post body or playable media; the post may be private, removed, login-required, or blocked in this region", r.Platform)
		}
		r.Type = "article"
	} else {
		r.Type = r.Media[0].Kind
		for _, m := range r.Media {
			if m.Kind != r.Type {
				r.Type = "multimedia"
				break
			}
		}
	}
	if r.Title == "" {
		r.Title = r.Content
	}
	if r.Title == "" {
		r.Title = r.Platform + " post"
	}
	titleRunes := []rune(r.Title)
	if len(titleRunes) > 140 {
		r.Title = string(titleRunes[:140])
	}
	return r, nil
}
func overseasBest(list any, urlKeys ...string) map[string]any {
	var best map[string]any
	var score float64 = -1
	for _, value := range Array(list) {
		m := Obj(value)
		candidate := ""
		for _, key := range urlKeys {
			if candidate = overseasURL(m[key]); candidate != "" {
				break
			}
		}
		if candidate == "" {
			continue
		}
		n := Number(m["width"])*Number(m["height"]) + Number(m["bitrate"]) + Number(m["bit_rate"])
		if strings.EqualFold(Text(Path(m, "metadata", "quality")), "hd") {
			n += 1e12
		}
		if n > score {
			best, score = m, n
		}
	}
	return best
}
func overseasPathID(raw string, kinds ...string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		for _, kind := range kinds {
			if p == kind && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}
func overseasMetadata(body []byte, r *model.ParseResult) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return
	}
	meta := func(selector string) string {
		value, _ := doc.Find(selector).First().Attr("content")
		return CleanText(value)
	}
	if r.Title == "" {
		r.Title = meta("meta[property='og:title']")
		if r.Title == "" {
			r.Title = CleanText(doc.Find("title").Text())
		}
	}
	if r.Content == "" {
		r.Content = meta("meta[property='og:description'],meta[name='description']")
	}
	if r.Author == "" {
		r.Author = meta("meta[name='author']")
	}
}
