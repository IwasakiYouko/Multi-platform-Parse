package parser

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"multiparse/internal/model"
)

type overseasTestTransport func(*http.Request) (*http.Response, error)

func (f overseasTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func overseasTestResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func overseasTestClient(body string) *http.Client {
	return &http.Client{Transport: overseasTestTransport(func(r *http.Request) (*http.Response, error) { return overseasTestResponse(r, body), nil })}
}
func overseasTestState(v string) string {
	return `<html><script type="application/json">` + v + `</script></html>`
}

func TestOverseasMediaNeverAcceptsPosterAsVideo(t *testing.T) {
	result := &model.ParseResult{Platform: "instagram", SourceURL: "https://www.instagram.com/p/AbC/"}
	err := overseasInstagramMedia(result, map[string]any{"is_video": true, "display_url": "https://cdn.example/poster.jpg"})
	if err == nil || len(result.Media) != 0 {
		t.Fatal("video poster substituted for inaccessible stream")
	}
}
