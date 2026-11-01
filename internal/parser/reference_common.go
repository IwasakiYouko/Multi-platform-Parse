package parser

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"

	"multiparse/internal/model"
)

// ContentUnavailableError identifies permanent source-state failures, not a bad
// credential or transport. Reference fallback chains may stop on this class.
type ContentUnavailableError struct{ Reason string }

func (e ContentUnavailableError) Error() string            { return e.Reason }
func (e ContentUnavailableError) ContentUnavailable() bool { return true }
func referenceUnavailable(err error) bool {
	var classified interface{ ContentUnavailable() bool }
	return errors.As(err, &classified) && classified.ContentUnavailable()
}

type referenceStage struct {
	Name string
	Run  func() (*model.ParseResult, error)
}

func referenceFallback(stages []referenceStage, stopUnavailable bool) (*model.ParseResult, error) {
	if len(stages) == 0 {
		return nil, errors.New("no parser stages available")
	}
	var attempts []model.FallbackAttempt
	var last error
	for _, stage := range stages {
		result, err := stage.Run()
		if err == nil && result == nil {
			err = errors.New("parser stage returned no result")
		}
		if err == nil {
			result.FallbackErrors = append(attempts, result.FallbackErrors...)
			return result, nil
		}
		last = err
		attempts = append(attempts, model.FallbackAttempt{Name: stage.Name, Error: err.Error()})
		if stopUnavailable && referenceUnavailable(err) {
			return nil, err
		}
	}
	if len(stages) == 1 {
		return nil, last
	}
	var summary strings.Builder
	for i, attempt := range attempts {
		if i > 0 {
			summary.WriteString("; ")
		}
		fmt.Fprintf(&summary, "%s: %s", attempt.Name, attempt.Error)
	}
	return nil, fmt.Errorf("all parser stages failed (%s): %w", summary.String(), last)
}
func referenceResultType(r *model.ParseResult) {
	image, video, audio := false, false, false
	for _, media := range r.Media {
		switch media.Kind {
		case "image", "gif", "livephoto":
			image = true
		case "video":
			video = true
		case "audio":
			audio = true
		}
	}
	switch {
	case (image && video) || (audio && (image || video)):
		r.Type = "multimedia"
	case video:
		r.Type = "video"
	case image:
		r.Type = "image"
	case audio:
		r.Type = "audio"
	default:
		r.Type = "article"
	}
}
func referenceHTML(source, baseURL string) (plain, markdown string) {
	plain, markdown, _ = referenceHTMLDocument(source, baseURL)
	return plain, markdown
}
func referenceSetHTML(r *model.ParseResult, source string) {
	plain, markdown, err := referenceHTMLDocument(source, r.SourceURL)
	r.Content, r.MarkdownContent = plain, markdown
	if err != nil {
		r.FallbackErrors = append(r.FallbackErrors, model.FallbackAttempt{Name: "richtext conversion", Error: err.Error()})
	}
}
func referenceHTMLDocument(source, baseURL string) (string, string, error) {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(source))
	if err != nil {
		return "", "", err
	}
	document.Find("script,style,noscript,form,button").Remove()
	document.Find("img").Each(func(_ int, image *goquery.Selection) {
		for _, key := range []string{"data-src", "data-original", "data-actualsrc", "src"} {
			value, _ := image.Attr(key)
			if absolute := Absolute(baseURL, value); absolute != "" {
				image.SetAttr("src", absolute)
				break
			}
		}
	})
	document.Find("a[href]").Each(func(_ int, anchor *goquery.Selection) {
		value, _ := anchor.Attr("href")
		if absolute := Absolute(baseURL, value); absolute != "" {
			anchor.SetAttr("href", absolute)
			return
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "mailto" && parsed.Scheme != "tel") {
			anchor.RemoveAttr("href")
		}
	})
	document.Find("iframe,video,audio").Each(func(_ int, media *goquery.Selection) {
		value, _ := media.Attr("src")
		if value == "" {
			value, _ = media.Find("source").First().Attr("src")
		}
		absolute := Absolute(baseURL, value)
		if absolute == "" {
			media.Remove()
			return
		}
		label := strings.TrimSpace(media.Text())
		if label == "" {
			label = absolute
		}
		media.ReplaceWithHtml(`<p><a href="` + html.EscapeString(absolute) + `">` + html.EscapeString(label) + `</a></p>`)
	})
	normalized, err := document.Html()
	if err != nil {
		return "", "", err
	}
	plain := mainlandText(normalized)
	conv := converter.NewConverter(converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), strikethrough.NewStrikethroughPlugin(), table.NewTablePlugin()))
	markdown, err := conv.ConvertString(normalized, converter.WithDomain(baseURL))
	if err != nil {
		return plain, "", err
	}
	return strings.TrimSpace(plain), strings.TrimSpace(markdown), nil
}
