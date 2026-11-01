package parser

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestReferenceMetaGraphQLDoesNotFollowRedirectOrRetryOrdinaryHTTP(t *testing.T) {
	for _, status := range []int{302, 403, 429, 500} {
		calls := 0
		client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				if req.URL.String() != "https://www.instagram.com/" {
					t.Fatalf("unexpected homepage: %s", req.URL)
				}
				return referenceMetaHomepage(req), nil
			}
			if calls != 2 {
				t.Fatalf("ordinary HTTP %d triggered redirect or authenticated retry", status)
			}
			referenceInstagramVariables(t, req, "anonymous-token")
			response := referenceSocialResponse(req, "restricted or redirected")
			response.StatusCode = status
			response.Header.Set("Location", "https://www.instagram.com/accounts/login/")
			return response, nil
		})}
		if _, err := overseasInstagram(context.Background(), client, "https://www.instagram.com/p/AbC/", "sessionid=secret; csrftoken=secret-token"); err == nil || calls != 2 {
			t.Fatalf("HTTP %d must remain ordinary failure: calls=%d error=%v", status, calls, err)
		}
	}
}

func TestReferenceSocialCookieJarsRemainParseLocal(t *testing.T) {
	client := &http.Client{Transport: referenceSocialTransport(func(req *http.Request) (*http.Response, error) { return referenceSocialResponse(req, ""), nil })}
	first := newOverseasSession(client, "tiktok.com", "sessionid=first")
	second := newOverseasSession(client, "tiktok.com", "sessionid=second")
	u, _ := url.Parse("https://www.tiktok.com/")
	first.client.Jar.SetCookies(u, []*http.Cookie{{Name: "challenge", Value: "local", Path: "/"}})
	for _, cookie := range second.client.Jar.Cookies(u) {
		if cookie.Name == "challenge" || cookie.Name == "sessionid" && cookie.Value != "second" {
			t.Fatalf("cookie escaped its parse: %#v", cookie)
		}
	}
	foreign, _ := url.Parse("https://www.instagram.com/")
	if len(first.client.Jar.Cookies(foreign)) != 0 || client.Jar != nil {
		t.Fatal("scoped cookies leaked to another platform or shared client")
	}
}
