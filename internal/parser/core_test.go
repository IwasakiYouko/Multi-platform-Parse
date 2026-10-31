package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPageRejectsMetadataOnlyVerificationPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>访问验证</title><meta name="description" content="请登录后继续"></head><body><form>登录</form></body></html>`))
	}))
	defer server.Close()
	if _, err := Page(context.Background(), server.Client(), server.URL, ""); err == nil {
		t.Fatal("verification metadata accepted as an article")
	}
}
func TestPageKeepsMixedMediaOrderAndGroupsSourceAlternatives(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>文章</title><article><p>真实正文</p><video><source src="/first.mp4"><source src="/fallback.webm"></video><img data-src="/image.jpg"><video src="/last.mp4"></video></article></html>`))
	}))
	defer server.Close()
	result, err := Page(context.Background(), server.Client(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{server.URL + "/first.mp4", server.URL + "/image.jpg", server.URL + "/last.mp4"}
	if len(result.Media) != len(want) {
		t.Fatalf("media alternatives emitted separately: %+v", result.Media)
	}
	for i, m := range result.Media {
		if m.URL != want[i] {
			t.Fatalf("media[%d]=%s want%s", i, m.URL, want[i])
		}
	}
}
func TestDecodeStateNormalizesSetsWithoutChangingQuotedPayload(t *testing.T) {
	state, err := DecodeState([]byte(`window.__INITIAL_STATE__={"items":new Set(["a",{"value":undefined}]),"url":"https://cdn.example/v?token=%2F%22","literal":"new Set([1])"};`), "window.__INITIAL_STATE__")
	if err != nil {
		t.Fatal(err)
	}
	if Text(Path(state, "url")) != "https://cdn.example/v?token=%2F%22" || Text(Path(state, "literal")) != "new Set([1])" {
		t.Fatalf("quoted content changed: %#v", state)
	}
	values := Array(Path(state, "items"))
	if len(values) != 2 || Path(values[1], "value") != nil {
		t.Fatalf("set not normalized: %#v", values)
	}
}
