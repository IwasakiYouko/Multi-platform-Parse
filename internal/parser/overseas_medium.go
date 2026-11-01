package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func init() { Register("medium", overseasMedium) }

const mediumGraphQLQuery = `
query P($id: ID!) {
  post(id: $id) {
    title
    isLocked
    content {
      bodyModel {
        paragraphs {
          type
          text
          href
          markups {
            type
            start
            end
            href
          }
          metadata {
            id
            originalWidth
            originalHeight
          }
          codeBlockMetadata {
            lang
          }
          iframe {
            mediaResource {
              href
              iframeSrc
              title
            }
          }
          mixtapeMetadata {
            href
          }
        }
      }
    }
  }
}
`

var mediumPostID = regexp.MustCompile(`(?:^|-)([0-9a-f]{8,12})$`)

func mediumInput(raw string) (string, string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Port() != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("medium: invalid article URL")
	}
	host := strings.ToLower(u.Hostname())
	if host != "medium.com" && !strings.HasSuffix(host, ".medium.com") {
		return "", "", fmt.Errorf("medium: unsupported article host")
	}
	parts := strings.Split(strings.TrimRight(u.Path, "/"), "/")
	match := mediumPostID.FindStringSubmatch(parts[len(parts)-1])
	if len(match) != 2 {
		return "", "", fmt.Errorf("medium: unsupported article URL")
	}
	return u.String(), match[1], nil
}

func overseasMedium(ctx context.Context, c *http.Client, raw, _ string) (*model.ParseResult, error) {
	if _, _, err := mediumInput(raw); err != nil {
		return nil, err
	}
	gateway, err := netx.NewProcessProxy(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("medium: controlled Chrome gateway: %w", err)
	}
	defer gateway.Close()
	// curl-cffi 0.16.3's "chrome" alias is Chrome 150. Both TLS and HTTP/2
	// come from the browser profile; the mandatory proxy keeps DNS and policy in netx.
	chrome, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), tlsclient.WithClientProfile(profiles.Chrome_150), tlsclient.WithRandomTLSExtensionOrder(), tlsclient.WithDisableHttp3(), tlsclient.WithTimeoutSeconds(30), tlsclient.WithNotFollowRedirects(), tlsclient.WithProxyUrl(gateway.URL()))
	if err != nil {
		return nil, fmt.Errorf("medium: could not initialize controlled Chrome client")
	}
	defer chrome.CloseIdleConnections()
	return mediumWithHTTP(ctx, raw, func(request *http.Request) (*http.Response, error) {
		req, err := fhttp.NewRequestWithContext(request.Context(), request.Method, request.URL.String(), request.Body)
		if err != nil {
			return nil, err
		}
		for key, values := range request.Header {
			req.Header[key] = values
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36")
		req.Header.Set("sec-ch-ua", `"Not_A Brand";v="99", "Chromium";v="150", "Google Chrome";v="150"`)
		req.Header.Set("sec-ch-ua-mobile", "?0")
		req.Header.Set("sec-ch-ua-platform", `"Windows"`)
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header[fhttp.HeaderOrderKey] = []string{"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "user-agent", "accept", "content-type", "accept-language"}
		resp, err := chrome.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// tls-client errors may contain the authenticated local proxy URL.
			return nil, fmt.Errorf("controlled Chrome request failed")
		}
		return &http.Response{StatusCode: resp.StatusCode, Header: http.Header(resp.Header), Body: resp.Body, Request: request}, nil
	})
}

// Fixtures inject only the HTTP boundary, never select an insecure production route.
func mediumWithHTTP(ctx context.Context, raw string, do func(*http.Request) (*http.Response, error)) (*model.ParseResult, error) {
	source, id, err := mediumInput(raw)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal([]any{map[string]any{"operationName": "P", "variables": map[string]string{"id": id}, "query": mediumGraphQLQuery}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://medium.com/_/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	resp, err := do(req)
	if err != nil {
		return nil, fmt.Errorf("medium: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("medium: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 16<<20 {
		return nil, fmt.Errorf("medium: API response exceeds 16 MiB")
	}
	var envelope []any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err = decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("medium: invalid GraphQL JSON: %w", err)
	}
	if len(envelope) != 1 {
		return nil, fmt.Errorf("medium: malformed GraphQL response")
	}
	if errors := Array(Path(envelope[0], "errors")); len(errors) > 0 {
		return nil, fmt.Errorf("medium: API: %s", Text(Path(errors[0], "message")))
	}
	data := Obj(Path(envelope[0], "data"))
	if data == nil {
		return nil, fmt.Errorf("medium: missing GraphQL post data")
	}
	post, present := data["post"]
	if !present {
		return nil, fmt.Errorf("medium: missing GraphQL post")
	}
	if post == nil {
		return nil, ContentUnavailableError{Reason: "medium: article does not exist or was deleted"}
	}
	r := &model.ParseResult{Platform: "medium", SourceURL: source, Title: Text(Path(post, "title")), Type: "article"}
	mediumRender(r, Array(Path(post, "content", "bodyModel", "paragraphs")))
	if strings.TrimSpace(r.Content) == "" && len(r.Media) == 0 {
		return nil, fmt.Errorf("medium: no article body exposed (locked or unavailable)")
	}
	result, err := overseasFinish(r)
	if err == nil {
		result.Type = "article"
	}
	return result, err
}

func mediumMarkup(paragraph any) (string, string) {
	var spans []overseasRichSpan
	for _, markup := range Array(Path(paragraph, "markups")) {
		open, close := "", ""
		switch Text(Path(markup, "type")) {
		case "STRONG":
			open, close = "**", "**"
		case "EM":
			open, close = "*", "*"
		case "CODE":
			open, close = "`", "`"
		case "A":
			if href := overseasURL(Path(markup, "href")); href != "" {
				open, close = "[", "]("+href+")"
			}
		}
		if open != "" {
			spans = append(spans, overseasRichSpan{int(Number(Path(markup, "start"))), int(Number(Path(markup, "end"))), open, close})
		}
	}
	return overseasRichText(Text(Path(paragraph, "text")), spans, nil, true)
}

func mediumRender(r *model.ParseResult, paragraphs []any) {
	var markdown, plain strings.Builder
	previous, ordered := "", 0
	for index, p := range paragraphs {
		kind, text := Text(Path(p, "type")), Text(Path(p, "text"))
		if index == 0 && (kind == "H2" || kind == "H3" || kind == "H4") && strings.TrimSpace(text) == strings.TrimSpace(r.Title) {
			continue
		}
		md, readable := mediumMarkup(p)
		switch kind {
		case "IMG":
			id := Text(Path(p, "metadata", "id"))
			if id == "" {
				continue
			}
			image := "https://miro.medium.com/v2/" + id
			overseasAdd(r, model.Media{Kind: "image", URL: image, Width: int(Number(Path(p, "metadata", "originalWidth"))), Height: int(Number(Path(p, "metadata", "originalHeight")))})
			md, readable = "![]("+image+")", "Image: "+image
			if caption := strings.TrimSpace(text); caption != "" {
				md += "\n*" + caption + "*"
				readable += "\n" + caption
			}
		case "PRE":
			md = "```" + Text(Path(p, "codeBlockMetadata", "lang")) + "\n" + text + "\n```"
			readable = text
		case "IFRAME":
			href := overseasFirst(Path(p, "iframe", "mediaResource", "href"), Path(p, "iframe", "mediaResource", "iframeSrc"))
			if overseasURL(href) == "" {
				continue
			}
			label := overseasFirst(Path(p, "iframe", "mediaResource", "title"), href)
			md, readable = "["+label+"]("+href+")", label+" ("+href+")"
		case "MIXTAPE_EMBED":
			if href := overseasURL(Path(p, "mixtapeMetadata", "href")); href != "" {
				md, readable = "["+md+"]("+href+")", readable+" ("+href+")"
			}
		case "H2":
			md = "## " + md
		case "H3":
			md = "### " + md
		case "H4":
			md = "#### " + md
		case "BQ", "PQ":
			md = "> " + strings.ReplaceAll(md, "\n", "\n> ")
		}
		if strings.TrimSpace(md) == "" {
			continue
		}
		if kind == "ULI" {
			md = "- " + md
			ordered = 0
		} else if kind == "OLI" {
			if previous == "OLI" {
				ordered++
			} else {
				ordered = 1
			}
			md = fmt.Sprintf("%d. %s", ordered, md)
		} else {
			ordered = 0
		}
		if markdown.Len() > 0 {
			separator := "\n\n"
			if (kind == "ULI" || kind == "OLI") && (previous == "ULI" || previous == "OLI") {
				separator = "\n"
			}
			markdown.WriteString(separator)
			plain.WriteString(separator)
		}
		markdown.WriteString(md)
		plain.WriteString(readable)
		previous = kind
	}
	r.MarkdownContent, r.Content = strings.TrimSpace(markdown.String()), strings.TrimSpace(plain.String())
}
