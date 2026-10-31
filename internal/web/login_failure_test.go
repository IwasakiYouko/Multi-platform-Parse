package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"multiparse/internal/store"
)

func TestCaptchaFailureDoesNotConsumePasswordAttempts(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := st.Config()
	cfg.Security.Failures = 1
	if err = st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/login", strings.NewReader(`{"username":"admin","password":"wrong-password","cap_token":"invalid"}`))
		req.RemoteAddr = "203.0.113.42:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("CAPTCHA error became password ban: HTTP%d %s", rec.Code, rec.Body.String())
		}
	}
	ban, err := st.BanStatus("203.0.113.42")
	if err != nil {
		t.Fatal(err)
	}
	if ban.Failures != 0 || time.Now().Before(ban.BannedUntil) {
		t.Fatalf("CAPTCHA error consumed password attempt: %+v", ban)
	}
}
