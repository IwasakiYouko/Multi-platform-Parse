package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"multiparse/internal/store"
)

func TestPasswordLoginFailureTriggersIPBan(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := st.Config()
	cfg.Security.Failures = 2
	if err = st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	server, err := New(st, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	for attempt := range 2 {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
		req.RemoteAddr = "203.0.113.42:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got HTTP %d, want 401: %s", attempt+1, rec.Code, rec.Body.String())
		}
	}

	ban, err := st.BanStatus("203.0.113.42")
	if err != nil {
		t.Fatal(err)
	}
	if ban.Failures != 2 || !time.Now().Before(ban.BannedUntil) {
		t.Fatalf("expected IP ban after 2 failed attempts: %+v", ban)
	}

	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	req.RemoteAddr = "203.0.113.42:12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("banned IP got HTTP %d, want 429", rec.Code)
	}
}
