package netx

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"multiparse/internal/model"
)

type processProxyTestTransport func(*http.Request) (*http.Response, error)

func (f processProxyTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func processProxyRequestClient(t *testing.T, raw string) *http.Client {
	t.Helper()
	address, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(address)}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}
func TestProcessProxyRefusesUnauthenticatedRequests(t *testing.T) {
	var contacted atomic.Int32
	proxy, err := NewProcessProxy(context.Background(), &http.Client{Transport: processProxyTestTransport(func(r *http.Request) (*http.Response, error) {
		contacted.Add(1)
		return nil, fmt.Errorf("must not forward")
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL())
	address.User = nil
	response, err := processProxyRequestClient(t, address.String()).Get("http://example.com/media")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired || contacted.Load() != 0 {
		t.Fatalf("unauthenticated egress: status=%d requests=%d", response.StatusCode, contacted.Load())
	}
}
func TestProcessProxyPreservesAnonymousAndExplicitCookieBoundaries(t *testing.T) {
	for _, mode := range []string{"configured", "explicit", "anonymous", "gateway-cdn", "native-cdn"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Header.Get("Cookie")) }))
			defer server.Close()
			cfg := model.DefaultConfig()
			entry := cfg.Platforms["youtube"]
			entry.Cookie = "session=configured"
			cfg.Platforms["youtube"] = entry
			jar, _ := cookiejar.New(nil)
			target := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
			}}
			defer target.CloseIdleConnections()
			guard := &guardedTransport{cfg: cfg, platform: "youtube", jar: jar, transports: map[string]*http.Transport{"youtube": target}}
			ctx := context.Background()
			want := "session=configured"
			if mode == "explicit" || strings.HasSuffix(mode, "-cdn") {
				ctx = WithExplicitCookies(ctx)
				want = "guest=anonymous"
			}
			if mode == "anonymous" {
				ctx = WithoutCookies(ctx)
				want = ""
			}
			if mode == "native-cdn" {
				want = ""
			}
			proxy, err := NewProcessProxy(ctx, &http.Client{Transport: guard})
			if err != nil {
				t.Fatal(err)
			}
			defer proxy.Close()
			targetURL := "http://www.youtube.com/item"
			if strings.HasSuffix(mode, "-cdn") {
				targetURL = "http://cdn.example/item"
			}
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
			if mode != "configured" {
				request.Header.Set("Cookie", "guest=anonymous")
			}
			outbound := processProxyRequestClient(t, proxy.URL())
			if mode == "native-cdn" {
				outbound = &http.Client{Transport: guard, Timeout: 3 * time.Second}
			}
			response, err := outbound.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || string(data) != want {
				t.Fatalf("cookie boundary failed: status=%d cookie=%q want=%q", response.StatusCode, data, want)
			}
		})
	}
}
func TestProcessProxyRejectsPrivateConnectAndClosesOnCancellation(t *testing.T) {
	cfg := model.DefaultConfig()
	controlled, err := Client(cfg, "youtube")
	if err != nil {
		t.Fatal(err)
	}
	defer controlled.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy, err := NewProcessProxy(ctx, controlled)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL())
	connection, err := net.DialTimeout("tcp", address.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	password, _ := address.User.Password()
	authorization := base64.StdEncoding.EncodeToString([]byte(address.User.Username() + ":" + password))
	if _, err = fmt.Fprintf(connection, "CONNECT 127.0.0.1:9 HTTP/1.1\r\nHost: 127.0.0.1:9\r\nProxy-Authorization: Basic %s\r\n\r\n", authorization); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(3 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("private CONNECT accepted: %d", response.StatusCode)
	}
	cancel()
	select {
	case <-proxy.done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled extractor left proxy active")
	}
	if _, err = processProxyRequestClient(t, proxy.URL()).Get("http://example.com/"); err == nil {
		t.Fatal("closed proxy still permits requests")
	}
}
