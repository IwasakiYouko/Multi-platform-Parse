package web

import (
	"context"
	"encoding/base64"
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

func TestCaptchaFailClosedAndOfficialProvision(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(st, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if err = srv.verifyCap(context.Background(), "site:token:secret"); err == nil {
		t.Fatal("unconfigured captcha bypass")
	}
	verified := false
	capServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "session_token": "test-token", "hashed_token": "test-hash"})
		case "/server/keys":
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			decoded, e := base64.StdEncoding.DecodeString(auth)
			if e != nil || !strings.Contains(string(decoded), "test-token") {
				t.Error("official session authorization malformed")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"siteKey": "site", "secretKey": "test-site-secret"})
		case "/server/keys/site/config":
			var config map[string]any
			_ = json.NewDecoder(r.Body).Decode(&config)
			if config["obfuscationLevel"] != float64(4) || config["instrumentation"] != true {
				t.Error("real instrumentation level not configured")
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
		case "/server/keys/site":
			_, _ = w.Write([]byte(`{"key":{"config":{"instrumentation":true,"obfuscationLevel":4,"protocol":"sha256-pow"}}}`))
		case "/site/siteverify":
			verified = true
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer capServer.Close()
	cfg := st.Config()
	cfg.Security.CapURL = capServer.URL
	if err = st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err = srv.ProvisionCap(context.Background(), "test-admin-password"); err != nil {
		t.Fatal(err)
	}
	if err = srv.verifyCap(context.Background(), "site:token:secret"); err != nil || !verified {
		t.Fatalf("official verification failed: %v", err)
	}
	if err = srv.verifyCap(context.Background(), "site:token:secret"); err == nil {
		t.Fatal("replayed token accepted")
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
}

func TestBundledCaptchaAssetsWithoutSidecar(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server, err := New(st, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, name := range []string{"widget.js", "cap_wasm.js", "cap_wasm_bg.wasm", "hashwx.wasm", "pako_inflate.min.js"} {
		request := httptest.NewRequest(http.MethodGet, "/captcha/assets/"+name, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.Len() < 100 {
			t.Fatalf("bundled asset %s: status=%d bytes=%d", name, response.Code, response.Body.Len())
		}
		if strings.HasSuffix(name, ".wasm") && !strings.HasPrefix(response.Body.String(), "\x00asm") {
			t.Fatalf("bundled asset %s is not WebAssembly", name)
		}
	}
}
