package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/store"
)

//go:embed assets/*
var capAssets embed.FS
var capAssetHandler = http.StripPrefix("/captcha", http.FileServer(http.FS(capAssets)))

type capState struct {
	mu    sync.RWMutex
	auth  string
	site  string
	level int
	base  string
	ready bool
}

func localClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (s *Server) capJSON(ctx context.Context, cfg model.Config, method, path, auth string, body any, out any) error {
	if err := store.ValidateConfig(cfg); err != nil {
		return err
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.Security.CapURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := s.capClient.Do(req)
	if err != nil {
		return errors.New("Cap服务不可达，请检查本机服务")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return errors.New("Cap响应读取失败")
	}
	if len(b) > 2*1024*1024 {
		return errors.New("Cap响应过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Cap服务拒绝请求（HTTP %d）", resp.StatusCode)
	}
	if out != nil {
		if err = json.Unmarshal(b, out); err != nil {
			return errors.New("Cap响应格式无效")
		}
	}
	return nil
}

// ProvisionCap uses only the official admin API; the admin password is never retained.
func (s *Server) ProvisionCap(ctx context.Context, adminKey string) error {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	cfg := s.st.Config()
	if len(adminKey) < 12 {
		return errors.New("Cap ADMIN_KEY至少12字符")
	}
	var login struct {
		Success bool   `json:"success"`
		Token   string `json:"session_token"`
		Hash    string `json:"hashed_token"`
	}
	if err := s.capJSON(ctx, cfg, "POST", "/auth/login", "", map[string]string{"admin_key": adminKey}, &login); err != nil {
		return err
	}
	if !login.Success || login.Token == "" || login.Hash == "" {
		return errors.New("Cap管理员认证失败")
	}
	b, _ := json.Marshal(map[string]string{"token": login.Token, "hash": login.Hash})
	auth := "Bearer " + base64.StdEncoding.EncodeToString(b)
	if cfg.Security.CapSiteKey == "" || cfg.Security.CapSecret == "" {
		var key struct {
			Site   string `json:"siteKey"`
			Secret string `json:"secretKey"`
		}
		if err := s.capJSON(ctx, cfg, "POST", "/server/keys", auth, map[string]any{"name": "Multi-platform-Parse 登录验证", "instrumentation": true, "protocol": "sha256-pow"}, &key); err != nil {
			return err
		}
		if key.Site == "" || key.Secret == "" {
			return errors.New("Cap创建站点失败")
		}
		cfg.Security.CapSiteKey = key.Site
		cfg.Security.CapSecret = key.Secret
		if err := s.st.SaveConfig(cfg); err != nil {
			return err
		}
	}
	s.cap.mu.Lock()
	wasReady := s.cap.ready
	s.cap.ready = false
	s.cap.mu.Unlock()
	if err := s.configureCap(ctx, cfg, auth); err != nil {
		if restoreErr := s.restoreCap(cfg, auth, wasReady); restoreErr != nil {
			return fmt.Errorf("%w；Cap恢复验证失败，登录保持关闭：%v", err, restoreErr)
		}
		return err
	}
	s.markCapReady(cfg, auth, true)
	return nil
}
func (s *Server) configureCap(ctx context.Context, cfg model.Config, auth string) error {
	var result struct {
		Success bool `json:"success"`
	}
	// cap_level is instrumentation obfuscationLevel, not proof-of-work difficulty.
	body := map[string]any{"instrumentation": true, "obfuscationLevel": cfg.Security.CapLevel, "protocol": "sha256-pow", "difficulty": 4, "challengeCount": 80}
	if err := s.capJSON(ctx, cfg, "PUT", "/server/keys/"+url.PathEscape(cfg.Security.CapSiteKey)+"/config", auth, body, &result); err != nil {
		return err
	}
	if !result.Success {
		return errors.New("Cap未接受检测等级设置")
	}
	var actual struct {
		Key struct {
			Config struct {
				Instrumentation bool   `json:"instrumentation"`
				Level           int    `json:"obfuscationLevel"`
				Protocol        string `json:"protocol"`
			} `json:"config"`
		} `json:"key"`
	}
	if err := s.capJSON(ctx, cfg, "GET", "/server/keys/"+url.PathEscape(cfg.Security.CapSiteKey), auth, nil, &actual); err != nil {
		return err
	}
	if !actual.Key.Config.Instrumentation || actual.Key.Config.Level != cfg.Security.CapLevel || actual.Key.Config.Protocol != "sha256-pow" {
		return errors.New("Cap检测等级读回不匹配")
	}
	return nil
}
func (s *Server) markCapReady(cfg model.Config, auth string, ready bool) {
	s.cap.mu.Lock()
	defer s.cap.mu.Unlock()
	s.cap.auth = auth
	s.cap.site = cfg.Security.CapSiteKey
	s.cap.level = cfg.Security.CapLevel
	s.cap.base = cfg.Security.CapURL
	s.cap.ready = ready
}

// A canceled browser request must not cancel restoration of the enforced Cap policy.
func (s *Server) restoreCap(cfg model.Config, auth string, wasReady bool) error {
	s.cap.mu.Lock()
	s.cap.ready = false
	s.cap.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.configureCap(ctx, cfg, auth); err != nil {
		return err
	}
	s.markCapReady(cfg, auth, wasReady)
	return nil
}
func (s *Server) capConfigured(cfg model.Config) bool {
	s.cap.mu.RLock()
	defer s.cap.mu.RUnlock()
	return s.cap.ready && s.cap.site == cfg.Security.CapSiteKey && s.cap.level == cfg.Security.CapLevel && s.cap.base == cfg.Security.CapURL && cfg.Security.CapSecret != ""
}
func (s *Server) capAvailable(ctx context.Context, cfg model.Config) bool {
	if !s.capConfigured(cfg) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(cfg.Security.CapURL, "/")+"/health", nil)
	if e != nil {
		return false
	}
	resp, e := s.capClient.Do(req)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
func (s *Server) verifyCap(ctx context.Context, token string) error {
	cfg := s.st.Config()
	if !s.capConfigured(cfg) {
		return errors.New("Cap尚未初始化，登录已关闭")
	}
	if len(token) < 10 || len(token) > 1024 || !strings.HasPrefix(token, cfg.Security.CapSiteKey+":") {
		return errors.New("请先完成Cap验证")
	}
	if err := s.st.ConsumeCapToken(token); err != nil {
		return errors.New("验证码已使用或不可验证，请重新完成验证")
	}
	var result struct {
		Success bool `json:"success"`
	}
	if err := s.capJSON(ctx, cfg, "POST", "/"+url.PathEscape(cfg.Security.CapSiteKey)+"/siteverify", "", map[string]string{"secret": cfg.Security.CapSecret, "response": token}, &result); err != nil {
		return err
	}
	if !result.Success {
		return errors.New("验证码无效，请重新完成验证")
	}
	return nil
}
func (s *Server) captcha(w http.ResponseWriter, r *http.Request, ip string) {
	path := strings.TrimPrefix(r.URL.Path, "/captcha")
	switch path {
	case "/assets/widget.js", "/assets/cap_wasm_bg.wasm", "/assets/cap_wasm.js", "/assets/hashwx.wasm", "/assets/pako_inflate.min.js":
		if r.Method != "GET" && r.Method != "HEAD" {
			failure(w, 405, "方法不允许")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		capAssetHandler.ServeHTTP(w, r)
		return
	}
	cfg := s.st.Config()
	prefix := "/" + cfg.Security.CapSiteKey + "/"
	if cfg.Security.CapSiteKey == "" || (path != prefix+"challenge" && path != prefix+"redeem") || r.Method != "POST" {
		failure(w, 404, "未找到")
		return
	}
	if !sameOrigin(r, cfg) {
		failure(w, 403, "请求来源无效")
		return
	}
	if !s.capConfigured(cfg) {
		failure(w, 503, "Cap尚未初始化")
		return
	}
	if !s.limit(w, "captcha:"+ip, 60, time.Minute) {
		return
	}
	target := strings.TrimRight(cfg.Security.CapURL, "/") + path
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, http.MaxBytesReader(w, r.Body, 256*1024))
	if err != nil {
		failure(w, 502, "Cap请求失败")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", r.UserAgent())
	req.Header.Set("X-Forwarded-For", ip)
	req.Header.Set("X-Real-IP", ip)
	req.Header.Set("CF-Connecting-IP", ip)
	resp, err := s.capClient.Do(req)
	if err != nil {
		failure(w, 503, "Cap服务不可达，登录已关闭")
		return
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(b) > 8*1024*1024 {
		failure(w, 502, "Cap响应无效")
		return
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(b)
}
