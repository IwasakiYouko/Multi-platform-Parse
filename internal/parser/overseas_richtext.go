package parser

/*
Portions adapted from ParseHub 2.3.15.
MIT License
Copyright (c) 2024 梓澪

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"multiparse/internal/model"
)

type overseasRichSpan struct {
	start, end  int
	open, close string
}
type overseasRichAtom struct {
	start           int
	markdown, plain string
}
type overseasRichEvent struct {
	at, other, index int
	close            bool
	text             string
}

// API offsets count UTF-16 code units, not UTF-8 bytes or Unicode code points.
func overseasRichOffset(text []rune, units int) int {
	count := 0
	for i, r := range text {
		if count >= units {
			return i
		}
		count++
		if r > 0xffff {
			count++
		}
	}
	return len(text)
}
func overseasRichText(text string, spans []overseasRichSpan, atoms []overseasRichAtom, trim bool) (string, string) {
	if len(spans) == 0 && len(atoms) == 0 {
		return text, text
	}
	runes := []rune(text)
	var events []overseasRichEvent
	for i, span := range spans {
		start, end := overseasRichOffset(runes, span.start), overseasRichOffset(runes, span.end)
		if trim {
			for start < end && unicode.IsSpace(runes[start]) {
				start++
			}
			for end > start && unicode.IsSpace(runes[end-1]) {
				end--
			}
		}
		if start >= end {
			continue
		}
		events = append(events, overseasRichEvent{start, end, i, false, span.open}, overseasRichEvent{end, start, i, true, span.close})
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.at != b.at {
			return a.at < b.at
		}
		if a.close != b.close {
			return a.close
		}
		if a.other != b.other {
			return a.other > b.other
		}
		if a.close {
			return a.index > b.index
		}
		return a.index < b.index
	})
	atomic := make(map[int]overseasRichAtom, len(atoms))
	for _, atom := range atoms {
		atom.start = overseasRichOffset(runes, atom.start)
		atomic[atom.start] = atom
	}
	var markdown, plain strings.Builder
	event := 0
	for i := 0; i <= len(runes); i++ {
		for event < len(events) && events[event].at <= i {
			markdown.WriteString(events[event].text)
			event++
		}
		if atom, ok := atomic[i]; ok {
			markdown.WriteString(atom.markdown)
			plain.WriteString(atom.plain)
			// Draft.js MEDIA/DIVIDER replaces the atomic placeholder character.
			if i < len(runes) {
				continue
			}
		}
		if i < len(runes) {
			markdown.WriteRune(runes[i])
			plain.WriteRune(runes[i])
		}
	}
	return markdown.String(), plain.String()
}

func twitterArticle(r *model.ParseResult, article any) error {
	blocks := Array(Path(article, "content_state", "blocks"))
	if len(blocks) == 0 {
		return fmt.Errorf("twitter: API exposed only an article preview, not its full body")
	}
	r.Title = Text(Path(article, "title"))
	entities := make(map[string]any)
	for _, entity := range Array(Path(article, "content_state", "entityMap")) {
		entities[Text(Path(entity, "key"))] = Path(entity, "value")
	}
	// Some Draft.js serializers encode entityMap as an object rather than key/value pairs.
	if object := Obj(Path(article, "content_state", "entityMap")); object != nil {
		entities = object
	}
	media := make(map[string]model.Media)
	for _, entity := range Array(Path(article, "media_entities")) {
		info := Path(entity, "media_info")
		var m model.Media
		switch Text(Path(info, "__typename")) {
		case "ApiImage":
			m = model.Media{Kind: "image", URL: Text(Path(info, "original_img_url")), Width: int(Number(Path(info, "original_img_width"))), Height: int(Number(Path(info, "original_img_height")))}
		case "ApiVideo":
			best, rate := "", float64(-1)
			for _, variant := range Array(Path(info, "variants")) {
				if Text(Path(variant, "content_type")) == "video/mp4" && Number(Path(variant, "bit_rate")) > rate {
					best, rate = Text(Path(variant, "url")), Number(Path(variant, "bit_rate"))
				}
			}
			m = model.Media{Kind: "video", URL: best, ThumbnailURL: Text(Path(info, "preview_image", "original_img_url")), Width: int(Number(Path(info, "preview_image", "original_img_width"))), Height: int(Number(Path(info, "preview_image", "original_img_height"))), Duration: Number(Path(info, "duration_millis")) / 1000}
		default:
			continue
		}
		if overseasURL(m.URL) == "" {
			return fmt.Errorf("twitter: article media has no accessible image or MP4")
		}
		media[Text(Path(entity, "media_id"))] = m
		overseasAdd(r, m)
	}
	var markdown, plain []string
	if cover := overseasURL(Path(article, "cover_media", "media_info", "original_img_url")); cover != "" {
		markdown = append(markdown, "![Cover Image]("+cover+")")
		plain = append(plain, "Cover Image: "+cover)
	}
	hasBody := false
	for _, block := range blocks {
		var spans []overseasRichSpan
		var atoms []overseasRichAtom
		for _, style := range Array(Path(block, "inlineStyleRanges")) {
			marker := ""
			switch Text(Path(style, "style")) {
			case "Bold":
				marker = "**"
			case "Italic":
				marker = "*"
			case "Strikethrough":
				marker = "~~"
			}
			if marker != "" {
				start := int(Number(Path(style, "offset")))
				spans = append(spans, overseasRichSpan{start, start + int(Number(Path(style, "length"))), marker, marker})
			}
		}
		for _, item := range Array(Path(block, "entityRanges")) {
			entity := entities[Text(Path(item, "key"))]
			start, length := int(Number(Path(item, "offset"))), int(Number(Path(item, "length")))
			switch Text(Path(entity, "type")) {
			case "LINK":
				if href := overseasURL(Path(entity, "data", "url")); href != "" {
					spans = append(spans, overseasRichSpan{start, start + length, "[", "](" + href + ")"})
				}
			case "DIVIDER":
				atoms = append(atoms, overseasRichAtom{start, "\n---\n", ""})
			case "MEDIA":
				items := Array(Path(entity, "data", "mediaItems"))
				if len(items) == 0 {
					continue
				}
				m, ok := media[Text(Path(items[0], "mediaId"))]
				if !ok {
					continue
				}
				md, label := "![Image]("+m.URL+")", "Image: "+m.URL
				if m.Kind == "video" {
					md, label = "[![Video]("+m.ThumbnailURL+")]("+m.URL+")", "Video: "+m.URL
				}
				atoms = append(atoms, overseasRichAtom{start, md, label})
			}
		}
		md, text := overseasRichText(Text(Path(block, "text")), spans, atoms, false)
		hasBody = hasBody || strings.TrimSpace(md) != ""
		switch Text(Path(block, "type")) {
		case "header-one":
			md = "# " + md
		case "header-two":
			md = "## " + md
		case "header-three":
			md = "### " + md
		case "blockquote":
			md = "> " + strings.ReplaceAll(md, "\n", "\n> ")
		case "ordered-list-item":
			md = "1. " + md
		case "unordered-list-item":
			md = "- " + md
		}
		markdown, plain = append(markdown, md), append(plain, text)
	}
	if !hasBody {
		return fmt.Errorf("twitter: API did not expose an article body")
	}
	r.MarkdownContent, r.Content = strings.TrimSpace(strings.Join(markdown, "\n\n")), strings.TrimSpace(strings.Join(plain, "\n\n"))
	return nil
}
