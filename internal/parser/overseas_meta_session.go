package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
	"multiparse/internal/netx"
)

// A parse owns its cookie jar; configured and challenge cookies never reach another platform.
type overseasSession struct {
	client  http.Client
	root    string
	headers map[string]string
}

func newOverseasSession(c *http.Client, root, cookie string) *overseasSession {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	s := &overseasSession{client: *c, root: root, headers: Headers("", "https://www."+root+"/")}
	s.client.Jar = jar
	if s.client.Timeout == 0 || s.client.Timeout > 30*time.Second {
		s.client.Timeout = 30 * time.Second
	}
	previous := c.CheckRedirect
	s.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !s.allowed(req.URL) {
			return fmt.Errorf("platform redirect left %s", root)
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many platform redirects")
		}
		return nil
	}
	u, _ := url.Parse("https://www." + root + "/")
	parsed := (&http.Request{Header: http.Header{"Cookie": []string{cookie}}}).Cookies()
	for _, ck := range parsed {
		ck.Domain = "." + root
		ck.Path = "/"
		ck.Secure = true
	}
	jar.SetCookies(u, parsed)
	return s
}

func (s *overseasSession) allowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return u.User == nil && u.Scheme == "https" && (host == s.root || strings.HasSuffix(host, "."+s.root))
}

func (s *overseasSession) request(ctx context.Context, method, endpoint string, form url.Values, redirects bool) ([]byte, string, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(netx.WithExplicitCookies(ctx), method, endpoint, body)
	if err != nil {
		return nil, "", err
	}
	if !s.allowed(req.URL) {
		return nil, "", fmt.Errorf("invalid %s endpoint", s.root)
	}
	for key, value := range s.headers {
		req.Header.Set(key, value)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := s.client
	if !redirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fmt.Errorf("%s request failed", s.root)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s returned HTTP %d", s.root, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > 16<<20 {
		return nil, "", fmt.Errorf("%s response exceeds 16MiB", s.root)
	}
	return data, resp.Request.URL.String(), nil
}

func (s *overseasSession) csrf(ctx context.Context, required bool) error {
	home := "https://www." + s.root + "/"
	u, _ := url.Parse(home)
	token := func() string {
		for _, ck := range s.client.Jar.Cookies(u) {
			if ck.Name == "csrftoken" && ck.Value != "" {
				return ck.Value
			}
		}
		return ""
	}
	value := token()
	if value == "" {
		if _, _, err := s.request(ctx, http.MethodGet, home, nil, true); err != nil {
			return err
		}
		value = token()
	}
	if value == "" && required {
		return fmt.Errorf("instagram: cannot obtain csrftoken")
	}
	if value != "" {
		s.headers["X-CSRFToken"] = value
	}
	return nil
}

func (s *overseasSession) graphql(ctx context.Context, doc string, variables map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(variables)
	if err != nil {
		return nil, err
	}
	data, _, err := s.request(ctx, http.MethodPost, "https://www."+s.root+"/graphql/query", url.Values{"variables": {string(encoded)}, "doc_id": {doc}, "server_timestamps": {"true"}}, false)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil || payload == nil {
		return nil, fmt.Errorf("%s: invalid GraphQL JSON", s.root)
	}
	return payload, nil
}
