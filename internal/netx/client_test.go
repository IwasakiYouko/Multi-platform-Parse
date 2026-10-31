package netx

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"multiparse/internal/model"
)

func TestPublicDestinations(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.2.3.4", "100.64.0.1", "169.254.169.254", "192.168.1.1", "198.18.0.1", "192.0.2.1", "224.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1", "2001::1"} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted reserved IP %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "::ffff:8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("rejected public IP %s", raw)
		}
	}
	if _, err := validateIPs([]netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}); err == nil {
		t.Fatal("mixed public/private DNS answer accepted")
	}
}
func TestCheckURLRejectsLocalWithoutNetwork(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://127.0.0.1/", "http://[::ffff:127.0.0.1]/", "http://localhost/", "http://a.local/", "https://user:secret@example.com/", "http://[fe80::1%25eth0]/", "http://example.com:0/"} {
		if err := CheckURL(context.Background(), raw); err == nil {
			t.Errorf("accepted unsafe URL %s", raw)
		}
	}
}
func TestNormalizePreservesSignedQuery(t *testing.T) {
	input := "https://EXAMPLE.com/a%2Fb?X-Signature=A%2Bb&b=2&a=1#fragment"
	got := NormalizeURL(input)
	if got != "https://example.com/a%2Fb?X-Signature=A%2Bb&b=2&a=1" {
		t.Fatalf("changed signed URL: %s", got)
	}
	if got := NormalizeURL("BV1xx411c7mD"); got != "https://www.bilibili.com/video/BV1xx411c7mD" {
		t.Fatal(got)
	}
	if got := NormalizeURL("bv1xx411c7mD"); got != "https://www.bilibili.com/video/BV1xx411c7mD" {
		t.Fatal(got)
	}
}
func TestRedirectPolicyAndCredentials(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Platforms["bilibili"] = model.PlatformPolicy{Enabled: true, Cookie: "secret=value"}
	client, err := Client(cfg, "bilibili")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	first, _ := http.NewRequest(http.MethodGet, "https://www.bilibili.com/a", nil)
	second, _ := http.NewRequest(http.MethodGet, "https://api.bilibili.com/b", nil)
	second.Header.Set("Cookie", "secret=value")
	second.Header.Set("Authorization", "Bearer secret")
	second.Header.Set("X-Custom-Secret", "hidden")
	second.Header.Set("User-Agent", "test")
	if err := client.CheckRedirect(second, []*http.Request{first}); err != nil {
		t.Fatal(err)
	}
	if second.Header.Get("Cookie") != "" || second.Header.Get("Authorization") != "" || second.Header.Get("X-Custom-Secret") != "" || second.Header.Get("User-Agent") != "test" || second.Context().Value(redirectCredentialKey{}) != true {
		t.Fatal("cross-host credentials were retained")
	}
	third, _ := http.NewRequest(http.MethodGet, "https://api.bilibili.com/c", nil)
	third.Header.Set("Cookie", "secret=value")
	if err := client.CheckRedirect(third, []*http.Request{first, second}); err != nil || third.Header.Get("Cookie") != "" || third.Context().Value(redirectCredentialKey{}) != true {
		t.Fatal("later redirect restored credentials")
	}
	policy := cfg.Platforms["youtube"]
	policy.Enabled = false
	cfg.Platforms["youtube"] = policy
	blocked, err := Client(cfg, "bilibili")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.CloseIdleConnections()
	target, _ := http.NewRequest(http.MethodGet, "https://www.youtube.com/watch?v=x", nil)
	if err := blocked.CheckRedirect(target, []*http.Request{first}); err == nil {
		t.Fatal("disabled redirect target accepted")
	}
}
func TestCookieScopesAndNoEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	cfg := model.DefaultConfig()
	cfg.Proxy.Mode = "direct"
	p, err := newPolicy(cfg, "youtube")
	if err != nil || p.proxyURL != nil {
		t.Fatal("direct mode inherited environment")
	}
	for raw, want := range map[string]bool{"https://yuanbao.tencent.com/api/weixin/get_parse_result": true, "https://channels.weixin.qq.com/": false, "https://evil.tencent.com/": false} {
		u, _ := url.Parse(raw)
		if cookieScope("weixin_channels", u) != want {
			t.Errorf("wrong cookie scope: %s", raw)
		}
	}
	cfg.Proxy.Mode = "all"
	cfg.Proxy.URL = ""
	if _, err := Client(cfg, "youtube"); err == nil {
		t.Fatal("missing proxy silently fell back")
	}
	cfg.Proxy.URL = "socks5h://user:secret@127.0.0.1:1080"
	p, err = newPolicy(cfg, "youtube")
	if err != nil || p.proxyURL == nil || p.remoteDNS == nil {
		t.Fatal("admin private proxy rejected or remote DNS absent")
	}
	if err != nil && strings.Contains(err.Error(), "secret") {
		t.Fatal("proxy error exposed credential")
	}
}
