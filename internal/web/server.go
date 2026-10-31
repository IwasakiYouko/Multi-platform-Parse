package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/store"
)

type Hooks struct {
	Status          func() any
	CapAdminKeyFile string
	PrepareConfig   func(old, new model.Config) (apply func(), discard func(), err error)
	ConfigChanged   func(old, new model.Config)
	ClearCache      func(all bool) error
	TestProxy       func(context.Context, string) error
	Submit          func(context.Context, string) (string, error)
}
type Server struct {
	st            *store.Store
	hooks         Hooks
	capClient     *http.Client
	cap           capState
	changeMu      sync.RWMutex
	passwordSlots chan struct{}
}

func New(st *store.Store, h Hooks) (*Server, error) {
	if st == nil {
		return nil, errors.New("store required")
	}
	if err := store.ValidateConfig(st.Config()); err != nil {
		return nil, err
	}
	return &Server{st: st, hooks: h, capClient: localClient(), passwordSlots: make(chan struct{}, 2)}, nil
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }
func (s *Server) Close() error          { s.capClient.CloseIdleConnections(); return nil }
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("请求JSON无效或包含未知字段")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("请求只能包含一个JSON对象")
	}
	return nil
}
func inNetworks(ip netip.Addr, networks []string) bool {
	for _, raw := range networks {
		if p, e := netip.ParsePrefix(raw); e == nil && p.Contains(ip) {
			return true
		}
		if p, e := netip.ParseAddr(raw); e == nil && p.Unmap() == ip.Unmap() {
			return true
		}
	}
	return false
}
func peerIP(r *http.Request) netip.Addr {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}
func trustedRequest(r *http.Request, c model.Config) bool {
	return inNetworks(peerIP(r), c.Server.TrustedProxies)
}
func clientIP(r *http.Request, c model.Config) (netip.Addr, error) {
	ip := peerIP(r)
	if !ip.IsValid() {
		return ip, errors.New("无效客户端地址")
	}
	if !inNetworks(ip, c.Server.TrustedProxies) {
		return ip, nil
	}
	xff := r.Header.Get("X-Forwarded-For")
	if len(xff) > 4096 {
		return ip, errors.New("代理地址链过长")
	}
	if xff == "" {
		return ip, nil
	}
	parts := strings.Split(xff, ",")
	if len(parts) > 32 {
		return ip, errors.New("代理地址链过长")
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if !inNetworks(ip, c.Server.TrustedProxies) {
			break
		}
		next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return ip, errors.New("代理地址链无效")
		}
		ip = next.Unmap()
	}
	return ip, nil
}
func effectiveHTTPS(r *http.Request, c model.Config) bool {
	if r.TLS != nil {
		return true
	}
	return trustedRequest(r, c) && r.Header.Get("X-Forwarded-Proto") == "https"
}
func sameOrigin(r *http.Request, c model.Config) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	expected := c.Server.ExternalURL
	if expected == "" {
		scheme := "http"
		if effectiveHTTPS(r, c) {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return strings.EqualFold(strings.TrimSuffix(origin, "/"), strings.TrimSuffix(expected, "/"))
}
func csrf(token string) string {
	h := sha256.Sum256([]byte("multiparse-csrf-v1:" + token))
	return hex.EncodeToString(h[:])
}
func sessionCookie(r *http.Request) string {
	c, err := r.Cookie("multiparse_session")
	if err != nil {
		return ""
	}
	return c.Value
}
func clearCookie(w http.ResponseWriter, r *http.Request, c model.Config) {
	http.SetCookie(w, &http.Cookie{Name: "multiparse_session", Value: "", Path: "/", HttpOnly: true, Secure: effectiveHTTPS(r, c), SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
func (s *Server) limit(w http.ResponseWriter, key string, max int, window time.Duration) bool {
	ok, err := s.st.AllowRequest(key, max, window)
	if err != nil {
		failure(w, 503, "安全存储不可用，请稍后重试")
		return false
	}
	if !ok {
		w.Header().Set("Retry-After", "60")
		failure(w, 429, "请求过于频繁，请稍后重试")
		return false
	}
	return true
}
func (s *Server) passwordSlot(w http.ResponseWriter) bool {
	select {
	case s.passwordSlots <- struct{}{}:
		return true
	default:
		failure(w, 429, "登录繁忙，请稍后重试")
		return false
	}
}
func (s *Server) reauth(w http.ResponseWriter, r *http.Request, a store.Session, password string) bool {
	if password == "" {
		password = r.Header.Get("X-Current-Password")
	}
	if len(password) > 1024 {
		failure(w, 400, "密码过长")
		return false
	}
	if !s.passwordSlot(w) {
		return false
	}
	defer func() { <-s.passwordSlots }()
	if _, err := s.st.Authenticate(a.Username, password); err != nil {
		ip, e := clientIP(r, s.st.Config())
		if e != nil || s.st.RecordFailure(ip.String()) != nil {
			failure(w, 503, "安全存储不可用")
			return false
		}
		failure(w, 403, "当前密码错误，请重新认证")
		return false
	}
	return true
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" {
		if r.Method != "GET" {
			failure(w, 405, "方法不允许")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	cfg := s.st.Config()
	addr, err := clientIP(r, cfg)
	if err != nil {
		failure(w, 400, err.Error())
		return
	}
	ip := addr.String()
	if len(cfg.Server.AdminNetworks) > 0 && !inNetworks(addr, cfg.Server.AdminNetworks) {
		failure(w, 403, "此地址不允许访问管理面板")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/captcha/") {
		s.captcha(w, r, ip)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		failure(w, 404, "未找到")
		return
	}
	if !s.limit(w, "api:"+ip, 240, time.Minute) {
		return
	}
	if r.Method != "GET" && !sameOrigin(r, cfg) {
		failure(w, 403, "请求来源无效")
		return
	}
	if r.Method != "GET" && r.URL.Path != "/api/logout" && r.URL.Path != "/api/login" {
		ban, e := s.st.BanStatus(ip)
		if e != nil {
			failure(w, 503, "安全存储不可用")
			return
		}
		if time.Now().Before(ban.BannedUntil) {
			failure(w, 429, "此IP暂时封禁")
			return
		}
	}
	switch r.URL.Path {
	case "/api/bootstrap":
		if r.Method != "GET" {
			failure(w, 405, "方法不允许")
			return
		}
		s.changeMu.RLock()
		defer s.changeMu.RUnlock()
		cfg = s.st.Config()
		enabled := !cfg.Security.CapDisabled
		ready := s.capAvailable(r.Context(), cfg)
		message := ""
		if enabled && !ready {
			message = "Cap未就绪：请检查配置中的本机Cap服务、持久化Redis与CAP_ADMIN_KEY_FILE；初始化完成前禁止登录。验证资源已内置，无需CDN。"
		}
		reply(w, 200, map[string]any{"cap_enabled": enabled, "cap_ready": ready, "cap_site_key": cfg.Security.CapSiteKey, "cap_widget_url": "/captcha/assets/widget.js", "cap_endpoint": "/captcha/" + cfg.Security.CapSiteKey + "/", "cap_level": cfg.Security.CapLevel, "setup_required": enabled && !ready, "setup_message": message})
		return
	case "/api/login":
		if r.Method != "POST" {
			failure(w, 405, "方法不允许")
			return
		}
		s.login(w, r, ip)
		return
	}
	token := sessionCookie(r)
	session, err := s.st.Session(token)
	if err != nil {
		failure(w, 401, "请重新登录")
		return
	}
	if session.MustChangePassword && r.URL.Path != "/api/me" && r.URL.Path != "/api/account" && r.URL.Path != "/api/logout" {
		failure(w, 403, "首次登录必须先修改密码")
		return
	}
	if r.Method != "GET" {
		want := csrf(token)
		got := r.Header.Get("X-CSRF-Token")
		if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			failure(w, 403, "CSRF令牌无效，请刷新后重试")
			return
		}
	}
	if r.Method != "GET" && !s.limit(w, "mutation:"+token, 60, time.Minute) {
		return
	}
	s.api(w, r, session, token)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request, ip string) {
	ban, err := s.st.BanStatus(ip)
	if err != nil {
		failure(w, 503, "安全存储不可用")
		return
	}
	if time.Now().Before(ban.BannedUntil) {
		failure(w, 429, "登录失败次数过多，此IP暂时封禁")
		return
	}
	if !s.limit(w, "login:"+ip, 15, time.Minute) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		CapToken string `json:"cap_token"`
	}
	if err = decode(w, r, &body); err != nil {
		failure(w, 400, err.Error())
		return
	}
	if len(body.Username) > 64 || len(body.Password) > 1024 {
		failure(w, 400, "用户名或密码过长")
		return
	}
	// Keep CAPTCHA policy unchanged until the authenticated session is issued.
	s.changeMu.RLock()
	defer s.changeMu.RUnlock()
	if err = s.verifyCap(r.Context(), body.CapToken); err != nil {
		if !s.limit(w, "captcha-failure:"+ip, 10, time.Minute) {
			return
		}
		failure(w, 403, err.Error())
		return
	}
	accountKey := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(body.Username))))
	if !s.limit(w, "account:"+hex.EncodeToString(accountKey[:]), 15, time.Minute) {
		return
	}
	if !s.passwordSlot(w) {
		return
	}
	defer func() { <-s.passwordSlots }()
	a, err := s.st.Authenticate(body.Username, body.Password)
	if err != nil {
		if e := s.st.RecordFailure(ip); e != nil {
			failure(w, 503, "安全存储不可用")
			return
		}
		failure(w, 401, "用户名或密码错误")
		return
	}
	token, session, err := s.st.NewSession(a)
	if err != nil {
		failure(w, 503, "无法创建会话")
		return
	}
	if err = s.st.Unban(ip); err != nil {
		_ = s.st.Logout(token)
		failure(w, 503, "安全存储不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "multiparse_session", Value: token, Path: "/", HttpOnly: true, Secure: effectiveHTTPS(r, s.st.Config()), SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt})
	reply(w, 200, map[string]any{"username": a.Username, "must_change_password": a.MustChangePassword, "csrf_token": csrf(token)})
}
