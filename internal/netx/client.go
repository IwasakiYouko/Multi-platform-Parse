// Package netx provides public-destination-only HTTP clients. Proxied clients
// depend on Cloudflare DNS-over-HTTPS (1.1.1.1:443, cloudflare-dns.com), reached
// only through the configured proxy; target DNS never falls back to local DNS.
package netx

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/net/publicsuffix"
	"multiparse/internal/model"
)

var publicV6 = netip.MustParsePrefix("2000::/3")
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:2::/48"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:10::/28"), netip.MustParsePrefix("2002::/16"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !publicV6.Contains(ip) {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" {
		return nil, errors.New("仅允许不含认证信息的HTTP(S)地址")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if strings.ContainsAny(host, "%\\\x00\r\n\t ") {
		return nil, errors.New("无效目标主机")
	}
	for _, suffix := range []string{"localhost", ".localhost", ".local", ".internal", ".home", ".lan", ".invalid", ".test", ".onion"} {
		if host == suffix || strings.HasSuffix(host, suffix) {
			return nil, errors.New("禁止访问本地或保留主机")
		}
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("无效目标端口")
		}
	}
	if ip, e := netip.ParseAddr(host); e == nil && !publicIP(ip) {
		return nil, errors.New("禁止访问私有或特殊用途IP")
	}
	return u, nil
}

func localResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if ip, e := netip.ParseAddr(host); e == nil {
		if !publicIP(ip) {
			return nil, errors.New("禁止访问私有或特殊用途IP")
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", strings.TrimSuffix(host, "."))
	if err != nil {
		return nil, fmt.Errorf("目标DNS解析失败: %w", err)
	}
	return validateIPs(ips)
}

func validateIPs(ips []netip.Addr) ([]netip.Addr, error) {
	if len(ips) == 0 {
		return nil, errors.New("目标DNS未返回IP地址")
	}
	for i, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("目标DNS包含私有或特殊用途IP")
		}
		ips[i] = ip.Unmap()
	}
	return ips, nil
}

// CheckURL validates a standalone URL using local DNS. Client also validates every
// actual dial, pins the checked IP, and independently validates redirects.
func CheckURL(ctx context.Context, raw string) error {
	u, err := parseURL(raw)
	if err != nil {
		return err
	}
	_, err = localResolve(ctx, u.Hostname())
	return err
}

func NormalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if canonical, ok := model.BilibiliURL(raw); ok {
		return canonical
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String()
}

type connectionPolicy struct {
	proxyURL  *url.URL
	direct    net.Dialer
	remoteDNS *http.Client
}

func newPolicy(cfg model.Config, platform string) (*connectionPolicy, error) {
	p := &connectionPolicy{direct: net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}}
	useProxy := false
	switch cfg.Proxy.Mode {
	case "", "direct":
	case "all":
		useProxy = true
	case "overseas":
		if entry, ok := cfg.Platforms[platform]; ok {
			useProxy = entry.Overseas
		} else if entry, ok := model.PlatformByID(platform); ok {
			useProxy = entry.Overseas
		}
	default:
		return nil, errors.New("未知代理模式")
	}
	if !useProxy {
		return p, nil
	}
	u, err := url.Parse(cfg.Proxy.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("代理地址无效")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("代理端口无效")
		}
	}
	p.proxyURL = u
	// Resolve through the proxy, then pin public IPs. Passing an unchecked hostname
	// to a proxy permits DNS rebinding; ordinary local DNS also leaks target names.
	p.remoteDNS = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Proxy: nil, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return p.tunnel(ctx, network, "1.1.1.1:443")
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "cloudflare-dns.com"},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DNS服务不允许重定向") }}
	return p, nil
}

func (p *connectionPolicy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if p.proxyURL == nil {
		return localResolve(ctx, host)
	}
	if ip, e := netip.ParseAddr(host); e == nil {
		return validateIPs([]netip.Addr{ip})
	}
	var ips []netip.Addr
	for _, typ := range []string{"A", "AAAA"} {
		endpoint := "https://cloudflare-dns.com/dns-query?name=" + url.QueryEscape(strings.TrimSuffix(host, ".")) + "&type=" + typ
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/dns-json")
		res, err := p.remoteDNS.Do(req)
		if err != nil {
			return nil, fmt.Errorf("代理远程DNS失败: %w", err)
		}
		var answer struct {
			Status int
			Answer []struct {
				Type int
				Data string
			}
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
		res.Body.Close()
		if readErr != nil || res.StatusCode != http.StatusOK || len(data) > 64<<10 {
			return nil, errors.New("代理远程DNS响应无效")
		}
		if err := json.Unmarshal(data, &answer); err != nil || answer.Status != 0 {
			return nil, errors.New("代理远程DNS查询失败")
		}
		for _, a := range answer.Answer {
			if a.Type == 1 || a.Type == 28 {
				ip, err := netip.ParseAddr(a.Data)
				if err != nil {
					return nil, errors.New("代理DNS返回无效IP")
				}
				ips = append(ips, ip)
			}
		}
	}
	return validateIPs(ips)
}

func (p *connectionPolicy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var last error
	for _, ip := range ips {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		target := net.JoinHostPort(ip.String(), port)
		var conn net.Conn
		if p.proxyURL == nil {
			conn, err = p.direct.DialContext(ctx, network, target)
		} else {
			conn, err = p.tunnel(ctx, network, target)
		}
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, fmt.Errorf("目标连接失败: %w", last)
}

func (p *connectionPolicy) tunnel(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := p.proxyURL
	if u == nil {
		return nil, errors.New("未配置代理，拒绝回退直连")
	}
	proxyAddress := u.Host
	if u.Port() == "" {
		port := "1080"
		if u.Scheme == "http" {
			port = "80"
		}
		if u.Scheme == "https" {
			port = "443"
		}
		proxyAddress = net.JoinHostPort(u.Hostname(), port)
	}
	if u.Scheme == "socks5" || u.Scheme == "socks5h" {
		var auth *proxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: password}
		}
		d, err := proxy.SOCKS5("tcp", proxyAddress, auth, &p.direct)
		if err != nil {
			return nil, errors.New("SOCKS5代理配置无效")
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("SOCKS5代理不支持取消")
		}
		conn, err := cd.DialContext(ctx, network, address)
		if err != nil {
			return nil, errors.New("SOCKS5代理连接失败")
		}
		return conn, nil
	}
	conn, err := p.direct.DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, errors.New("HTTP代理连接失败")
	}
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	if u.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
		conn = secure
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, errors.New("HTTPS代理TLS连接失败")
		}
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if u.User != nil {
		password, _ := u.User.Password()
		request.SetBasicAuth(u.User.Username(), password)
		request.Header.Set("Proxy-Authorization", request.Header.Get("Authorization"))
		request.Header.Del("Authorization")
	}
	if err := request.Write(conn); err != nil {
		return nil, errors.New("HTTP代理握手失败")
	}
	reader := bufio.NewReader(io.LimitReader(conn, 64<<10))
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, errors.New("HTTP代理响应无效")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP代理拒绝隧道: %d", response.StatusCode)
	}
	// CONNECT response has no body. The target has not received bytes yet, so no
	// tunneled data can have been buffered while reading these headers.
	if reader.Buffered() != 0 {
		return nil, errors.New("HTTP代理返回意外隧道数据")
	}
	conn.SetDeadline(time.Time{})
	success = true
	return conn, nil
}

type redirectCredentialKey struct{}
type guardedTransport struct {
	mu         sync.Mutex
	transports map[string]*http.Transport
	cfg        model.Config
	platform   string
	jar        *cookiejar.Jar
}

func (g *guardedTransport) CloseIdleConnections() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, t := range g.transports {
		t.CloseIdleConnections()
	}
}
func cookieScope(platform string, u *url.URL) bool {
	if platform == "weixin_channels" {
		return strings.EqualFold(u.Hostname(), "yuanbao.tencent.com")
	}
	target, ok := model.Identify(u.String())
	return ok && target.ID == platform
}
func httpTransport(policy *connectionPolicy) *http.Transport {
	return &http.Transport{Proxy: nil, DialContext: policy.dial, ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 12 * time.Second,
		ResponseHeaderTimeout: 25 * time.Second, IdleConnTimeout: 45 * time.Second, MaxIdleConns: 24, MaxIdleConnsPerHost: 4,
		MaxConnsPerHost: 8, MaxResponseHeaderBytes: 1 << 20}
}
func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := parseURL(req.URL.String()); err != nil {
		return nil, err
	}
	if err := checkPlatform(g.cfg, g.platform, req.URL.String()); err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	r.Host = ""
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")
	target, ok := model.Identify(r.URL.String())
	if r.Context().Value(redirectCredentialKey{}) == true {
		r.Header.Del("Cookie")
	} else if r.Context().Value(processCookieKey{}) == true && r.Context().Value(explicitCookieKey{}) == true {
		// The isolated extractor owns a domain/path-scoped jar; never inject the
		// application's configured Cookie or discard server-session CDN cookies.
	} else if !cookieScope(g.platform, r.URL) {
		r.Header.Del("Cookie")
		for _, cookie := range g.jar.Cookies(r.URL) {
			r.AddCookie(cookie)
		}
	} else if configured := g.cfg.Platforms[g.platform].Cookie; configured != "" && r.Context().Value(explicitCookieKey{}) != true {
		cookieRequest := &http.Request{Header: http.Header{"Cookie": []string{configured}}}
		for _, cookie := range cookieRequest.Cookies() {
			if _, err := r.Cookie(cookie.Name); err == http.ErrNoCookie {
				r.AddCookie(cookie)
			}
		}
	}
	route := g.platform
	if ok {
		route = target.ID
	}
	g.mu.Lock()
	transport := g.transports[route]
	if transport == nil {
		policy, err := newPolicy(g.cfg, route)
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		transport = httpTransport(policy)
		g.transports[route] = transport
	}
	g.mu.Unlock()
	return transport.RoundTrip(r)
}
func checkPlatform(cfg model.Config, original, raw string) error {
	if policy, ok := cfg.Platforms[original]; ok && (!policy.Enabled || policy.NSFWBlocked) {
		return errors.New("源平台已禁用或被内容策略阻止")
	}
	if p, ok := model.Identify(raw); ok {
		if policy, exists := cfg.Platforms[p.ID]; exists && (!policy.Enabled || policy.NSFWBlocked) {
			return fmt.Errorf("跳转目标平台 %s 被策略阻止", p.ID)
		}
	}
	return nil
}

func Client(cfg model.Config, platform string) (*http.Client, error) {
	policy, err := newPolicy(cfg, platform)
	if err != nil {
		return nil, err
	}
	if err := checkPlatform(cfg, platform, ""); err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	snapshot := cfg
	snapshot.Platforms = make(map[string]model.PlatformPolicy, len(cfg.Platforms))
	for id, p := range cfg.Platforms {
		snapshot.Platforms[id] = p
	}
	client := &http.Client{Transport: &guardedTransport{transports: map[string]*http.Transport{platform: httpTransport(policy)}, cfg: snapshot, platform: platform, jar: jar}, Jar: jar, Timeout: 5 * time.Minute}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("重定向次数过多")
		}
		if _, err := parseURL(req.URL.String()); err != nil {
			return err
		}
		if err := checkPlatform(snapshot, platform, req.URL.String()); err != nil {
			return err
		}
		crossedHost := false
		if len(via) > 0 {
			initialHost := via[0].URL.Host
			crossedHost = !strings.EqualFold(req.URL.Host, initialHost)
			for _, previous := range via {
				if !strings.EqualFold(previous.URL.Host, initialHost) {
					crossedHost = true
				}
			}
		}
		if crossedHost {
			clean := make(http.Header)
			for _, key := range []string{"Accept", "Accept-Encoding", "Accept-Language", "User-Agent", "Range", "If-Range"} {
				if values := req.Header.Values(key); len(values) > 0 {
					clean[key] = append([]string(nil), values...)
				}
			}
			req.Header = clean
			*req = *req.WithContext(context.WithValue(req.Context(), redirectCredentialKey{}, true))
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("拒绝HTTPS降级重定向")
		}
		return nil
	}
	return client, nil
}

func ProxyTest(ctx context.Context, cfg model.Config, platform string) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	client, err := Client(cfg, platform)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	p, ok := model.PlatformByID(platform)
	if !ok || len(p.Domains) == 0 {
		return errors.New("请选择受支持的平台")
	}
	endpoint := "https://" + p.Domains[0] + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("网络连通性检查失败（检查代理、DNS及平台策略）")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return fmt.Errorf("网络检查目标返回HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return err
}
