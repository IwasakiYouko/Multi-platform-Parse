package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"multiparse/internal/model"
)

func init() { Register("medium", overseasMedium) }
func overseasMedium(ctx context.Context, c *http.Client, raw, cookie string) (*model.ParseResult, error) {
	body, final, err := Get(ctx, c, raw, Headers(cookie, "https://medium.com/"))
	if err != nil {
		return nil, fmt.Errorf("medium: %w", err)
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r := &model.ParseResult{Platform: "medium", SourceURL: final, Type: "article"}
	article := doc.Find("article").First()
	if article.Length() == 0 {
		article = doc.Find("[data-testid='postContent']").First()
	}
	r.Title = CleanText(article.Find("h1").First().Text())
	r.Author = CleanText(doc.Find("[data-testid='authorName']").First().Text())
	if r.Author == "" {
		r.Author, _ = doc.Find("meta[name='author']").Attr("content")
	}
	states := overseasStates(body)
	// The legacy JSON endpoint still carries the complete paragraph model on many
	// publications. Never depend on OpenGraph descriptions for article content.
	parsed, parseErr := url.Parse(final)
	if parseErr == nil {
		q := parsed.Query()
		q.Set("format", "json")
		parsed.RawQuery = q.Encode()
		if data, _, fetchErr := Get(ctx, c, parsed.String(), Headers(cookie, final)); fetchErr == nil {
			if start := bytes.IndexByte(data, '{'); start >= 0 {
				var state any
				if json.Unmarshal(data[start:], &state) == nil {
					states = append(states, state)
				}
			}
		}
	}
	locked := false
	for _, state := range states {
		overseasWalk(state, func(m map[string]any) {
			postID := Text(m["id"])
			if postID != "" && strings.Contains(final, postID) {
				if m["isPreview"] == true || m["isLimitedState"] == true {
					locked = true
				}
				if m["isLocked"] == true && Path(m, "viewerEdge", "canRead") == false {
					locked = true
				}
			}
			post := Obj(Path(m, "payload", "value"))
			if post == nil {
				return
			}
			paragraphs := Array(Path(post, "content", "bodyModel", "paragraphs"))
			if len(paragraphs) == 0 {
				return
			}
			r.Title = overseasFirst(post["title"], r.Title)
			if post["isPreview"] == true || post["isLimitedState"] == true {
				locked = true
				return
			}
			var text []string
			for _, paragraph := range paragraphs {
				p := Obj(paragraph)
				if s := Text(p["text"]); s != "" && s != r.Title {
					text = append(text, s)
				}
				if id := Text(Path(p, "metadata", "id")); id != "" && (Number(p["type"]) == 4 || Path(p, "metadata", "originalWidth") != nil) {
					overseasAdd(r, model.Media{Kind: "image", URL: "https://miro.medium.com/v2/resize:fit:2400/" + url.PathEscape(id), Width: int(Number(Path(p, "metadata", "originalWidth"))), Height: int(Number(Path(p, "metadata", "originalHeight")))})
				}
			}
			if len(text) > 0 {
				r.Content = strings.Join(text, "\n\n")
			}
			userID := Text(post["creatorId"])
			if name := Text(Path(m, "payload", "references", "User", userID, "name")); name != "" {
				r.Author = name
			}
		})
	}
	if article.Find("[data-testid='paywall'],[data-testid='meteredContent'],.meteredContent").Length() > 0 {
		// A meteredContent wrapper alone can hold readable content for subscribers;
		// require a visible paywall action before rejecting it.
		if article.Find("a[href*='/m/signin'],a[href*='/plans'],button[data-testid='upgrade-button']").Length() > 0 {
			locked = true
		}
	}
	if doc.Find("[data-testid='paywall'],[data-testid='paywallContent']").Length() > 0 {
		locked = true
	}
	if locked {
		return nil, fmt.Errorf("medium: member-only article body is locked; configure a valid cookie with access instead of returning the preview")
	}
	if r.Content == "" {
		var paragraphs []string
		article.Find("h2,h3,h4,p,blockquote,pre,li,figcaption").Each(func(_ int, s *goquery.Selection) {
			if s.ParentsFiltered("blockquote,pre,li").Length() > 0 {
				return
			}
			if text := strings.TrimSpace(s.Text()); text != "" {
				paragraphs = append(paragraphs, text)
			}
		})
		r.Content = strings.Join(paragraphs, "\n\n")
	}
	article.Find("figure img").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		if original, ok := s.Attr("data-src"); ok {
			src = original
		}
		if srcset, ok := s.Attr("srcset"); ok {
			entries := strings.Split(srcset, ",")
			fields := strings.Fields(strings.TrimSpace(entries[len(entries)-1]))
			if len(fields) > 0 {
				src = fields[0]
			}
		}
		overseasAdd(r, model.Media{Kind: "image", URL: Absolute(final, src)})
	})
	article.Find("video").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		if src == "" {
			src, _ = s.Find("source").First().Attr("src")
		}
		poster, _ := s.Attr("poster")
		overseasAdd(r, model.Media{Kind: "video", URL: Absolute(final, src), ThumbnailURL: Absolute(final, poster)})
	})
	// Third-party iframe players are article links, never mislabeled media files.
	article.Find("iframe").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		if address := overseasURL(Absolute(final, src)); address != "" {
			r.Content += "\n\n嵌入内容：" + address
		}
	})
	if strings.TrimSpace(r.Content) == "" {
		return nil, fmt.Errorf("medium: no actual article body was exposed; URL may be a publication index, login page, or unavailable article")
	}
	if r.Title == "" {
		r.Title = CleanText(doc.Find("title").Text())
	}
	result, err := overseasFinish(r)
	if err == nil {
		result.Type = "article"
	}
	return result, err
}
