package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"multiparse/internal/model"
	"multiparse/internal/store"
)

func TestTrustedProxyAndOrigin(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Server.TrustedProxies = []string{"127.0.0.0/8", "10.0.0.0/8"}
	r := httptest.NewRequest("POST", "http://panel.test/api/config", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "192.0.2.9, 10.1.1.1")
	ip, err := clientIP(r, cfg)
	if err != nil || ip.String() != "192.0.2.9" {
		t.Fatalf("trusted chain: %v %v", ip, err)
	}
	r.RemoteAddr = "198.51.100.7:1234"
	ip, err = clientIP(r, cfg)
	if err != nil || ip.String() != "198.51.100.7" {
		t.Fatalf("untrusted forwarded header: %v %v", ip, err)
	}
	r.Header.Set("Origin", "https://attacker.test")
	if sameOrigin(r, cfg) {
		t.Fatal("cross origin accepted")
	}
	r.Header.Set("Origin", "http://panel.test")
	if !sameOrigin(r, cfg) {
		t.Fatal("same origin rejected")
	}
}

func TestForcedSessionAndCSRF(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.ResetAdmin("test-admin", "strong-test-password"); err != nil {
		t.Fatal(err)
	}
	a, err := st.Authenticate("test-admin", "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := st.NewSession(a)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for _, path := range []string{"/api/config", "/api/status"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "multiparse_session", Value: token})
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("forced session accessed %s: %d", path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/logout", nil)
	r.AddCookie(&http.Cookie{Name: "multiparse_session", Value: token})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d", w.Code)
	}
	// Verify /captcha/ routes are completely removed.
	r = httptest.NewRequest("GET", "/captcha/assets/widget.js", nil)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("removed captcha route returned %d, want 404", w.Code)
	}
}

func TestConfigSaveSensitivityPreviewAndCacheDelete(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.ResetAdmin("admin", "initial-strong-pass"); err != nil {
		t.Fatal(err)
	}
	if err = st.ChangeAccount("admin", "initial-strong-pass", "active-strong-pass"); err != nil {
		t.Fatal(err)
	}

	var deletedKey string
	srv, err := New(st, Hooks{
		DeleteCache: func(key string) error {
			deletedKey = key
			return nil
		},
		Preview: func(_ context.Context, raw string) (*model.ParseResult, error) {
			return &model.ParseResult{
				Platform:  "douyin",
				SourceURL: "https://www.douyin.com/video/123?utm_source=copy",
				Title:     "抖音作品标题",
				Author:    "作者A",
				Content:   "抖音作品标题",
				Type:      "video",
				Headers:   map[string]string{"Cookie": "secret=1"},
				Media:     []model.Media{{Kind: "video", URL: "https://cdn.example/v.mp4", Headers: map[string]string{"Cookie": "secret=1"}}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	loginReq := httptest.NewRequest(http.MethodPost, "http://panel.test/api/login", strings.NewReader(`{"username":"admin","password":"active-strong-pass"}`))
	loginReq.RemoteAddr = "198.51.100.10:1234"
	loginReq.Header.Set("Origin", "http://panel.test")
	loginRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == "multiparse_session" {
			sessionCookie = c
		}
	}
	var loginResp struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.Unmarshal(loginRec.Body.Bytes(), &loginResp)

	doReq := func(method, target string, body any, currentPassword string) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "http://panel.test"+target, bytes.NewReader(raw))
		req.RemoteAddr = "198.51.100.10:1234"
		req.AddCookie(sessionCookie)
		if method != http.MethodGet {
			req.Header.Set("Origin", "http://panel.test")
			req.Header.Set("X-CSRF-Token", loginResp.CSRF)
		}
		if currentPassword != "" {
			req.Header.Set("X-Current-Password", currentPassword)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Non-sensitive template update succeeds WITHOUT X-Current-Password.
	getRec := doReq(http.MethodGet, "/api/config", nil, "")
	var draft model.Config
	if err := json.Unmarshal(getRec.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	pol := draft.Platforms["douyin"]
	pol.TemplateMode = "custom"
	pol.MessageTemplate = "{title}"
	draft.Platforms["douyin"] = pol
	if rec := doReq(http.MethodPut, "/api/config", draft, ""); rec.Code != http.StatusOK {
		t.Fatalf("non-sensitive update required password or failed: %d %s", rec.Code, rec.Body.String())
	}

	// Sensitive cookie update requires X-Current-Password.
	pol.Cookie = "new-secret-cookie"
	draft.Platforms["douyin"] = pol
	if rec := doReq(http.MethodPut, "/api/config", draft, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("sensitive cookie update accepted without password: %d", rec.Code)
	}
	if rec := doReq(http.MethodPut, "/api/config", draft, "active-strong-pass"); rec.Code != http.StatusOK {
		t.Fatalf("sensitive cookie update with password failed: %d %s", rec.Code, rec.Body.String())
	}

	// Preview endpoint renders message and strips sensitive headers.
	prevRec := doReq(http.MethodPost, "/api/parse/preview", map[string]string{"url": "https://v.douyin.com/abc/"}, "")
	if prevRec.Code != http.StatusOK {
		t.Fatalf("preview failed: %d %s", prevRec.Code, prevRec.Body.String())
	}
	if strings.Contains(prevRec.Body.String(), "secret=1") {
		t.Fatalf("preview leaked sensitive headers: %s", prevRec.Body.String())
	}
	var prevBody struct {
		Rendered string `json:"rendered"`
		CleanURL string `json:"clean_url"`
	}
	_ = json.Unmarshal(prevRec.Body.Bytes(), &prevBody)
	if prevBody.Rendered != "【抖音作品标题】" || prevBody.CleanURL != "https://www.douyin.com/video/123" {
		t.Fatalf("unexpected preview payload: %+v", prevBody)
	}

	// Single cache deletion endpoint.
	targetKey := strings.Repeat("b", 64)
	if rec := doReq(http.MethodDelete, "/api/cache?key="+targetKey, nil, ""); rec.Code != http.StatusOK || deletedKey != targetKey {
		t.Fatalf("cache delete failed: code=%d key=%q", rec.Code, deletedKey)
	}
}
