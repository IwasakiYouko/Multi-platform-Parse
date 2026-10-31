package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/store"
)

const optionalCapPassword = "strong-test-password"
const optionalCapChangedPassword = "changed-test-password"

func optionalCapStore(t *testing.T, disabled bool) *store.Store {
	t.Helper()
	state, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	if err = state.ResetAdmin("test-admin", optionalCapPassword); err != nil {
		t.Fatal(err)
	}
	config := state.Config()
	config.Security.CapDisabled = disabled
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	return state
}

func optionalCapRequest(t *testing.T, server *Server, method, path string, body any, token, password, ip string) *httptest.ResponseRecorder {
	t.Helper()
	var data string
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		data = string(encoded)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(data))
	if ip != "" {
		request.RemoteAddr = ip + ":1234"
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "multiparse_session", Value: token})
		request.Header.Set("X-CSRF-Token", csrf(token))
	}
	if password != "" {
		request.Header.Set("X-Current-Password", password)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func optionalCapLogin(t *testing.T, server *Server, password, captcha, ip string) *httptest.ResponseRecorder {
	t.Helper()
	return optionalCapRequest(t, server, http.MethodPost, "/api/login", map[string]string{"username": "test-admin", "password": password, "cap_token": captcha}, "", "", ip)
}

func optionalCapSession(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", response.Code, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "multiparse_session" && cookie.Value != "" && cookie.HttpOnly && cookie.SameSite == http.SameSiteStrictMode {
			return cookie.Value
		}
	}
	t.Fatal("login did not issue a secure session cookie")
	return ""
}

func TestCapDisabledPasswordLoginPreservesAccountSecurity(t *testing.T) {
	state := optionalCapStore(t, true)
	var upstreamRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		http.Error(w, "Cap must not be contacted", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	config := state.Config()
	config.Security.CapURL = upstream.URL
	config.Security.CapSiteKey = "site"
	config.Security.CapSecret = "site-secret"
	if err := state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	server, err := New(state, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	// Even cached readiness from an earlier enabled policy cannot activate Cap.
	server.markCapReady(config, "Bearer old-session", true)
	if err = server.ProvisionCap(context.Background(), "test-admin-password"); err != nil {
		t.Fatal(err)
	}
	response := optionalCapRequest(t, server, http.MethodGet, "/api/bootstrap", nil, "", "", "")
	var bootstrap struct {
		Enabled *bool  `json:"cap_enabled"`
		Ready   *bool  `json:"cap_ready"`
		Setup   *bool  `json:"setup_required"`
		Message string `json:"setup_message"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &bootstrap); err != nil || response.Code != http.StatusOK || bootstrap.Enabled == nil || *bootstrap.Enabled || bootstrap.Ready == nil || *bootstrap.Ready || bootstrap.Setup == nil || *bootstrap.Setup || bootstrap.Message != "" {
		t.Fatalf("disabled bootstrap misrepresents protection: %s (%v)", response.Body.String(), err)
	}
	for _, path := range []string{"/captcha/site/challenge", "/captcha/site/redeem"} {
		if response = optionalCapRequest(t, server, http.MethodPost, path, map[string]any{}, "", "", ""); response.Code != http.StatusNotFound {
			t.Fatalf("disabled CAPTCHA route %s: %d", path, response.Code)
		}
	}
	if response = optionalCapRequest(t, server, http.MethodGet, "/healthz", nil, "", "", ""); response.Code != http.StatusOK {
		t.Fatalf("disabled health: %d", response.Code)
	}
	token := optionalCapSession(t, optionalCapLogin(t, server, optionalCapPassword, "", ""))
	if response = optionalCapRequest(t, server, http.MethodGet, "/api/config", nil, token, "", ""); response.Code != http.StatusForbidden {
		t.Fatalf("initial password restriction bypassed: %d", response.Code)
	}
	account := map[string]string{"current_password": optionalCapPassword, "new_password": optionalCapChangedPassword}
	request := httptest.NewRequest(http.MethodPost, "/api/account", strings.NewReader(`{"current_password":"`+optionalCapPassword+`","new_password":"`+optionalCapChangedPassword+`"}`))
	request.AddCookie(&http.Cookie{Name: "multiparse_session", Value: token})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("account change without CSRF accepted: %d", response.Code)
	}
	if response = optionalCapRequest(t, server, http.MethodPost, "/api/account", account, token, "", ""); response.Code != http.StatusOK {
		t.Fatalf("forced password change failed: %d %s", response.Code, response.Body.String())
	}
	if _, err = state.Session(token); err == nil {
		t.Fatal("password change retained initial session")
	}
	token = optionalCapSession(t, optionalCapLogin(t, server, optionalCapChangedPassword, "", ""))
	if response = optionalCapRequest(t, server, http.MethodGet, "/api/config", nil, token, "", ""); response.Code != http.StatusOK {
		t.Fatalf("password-only session cannot access config: %d", response.Code)
	}
	const bannedIP = "198.51.100.20"
	for attempt := range 5 {
		if response = optionalCapLogin(t, server, "wrong-password", "", bannedIP); response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password attempt %d: %d %s", attempt+1, response.Code, response.Body.String())
		}
	}
	ban, err := state.BanStatus(bannedIP)
	if err != nil || ban.Failures != 5 || time.Until(ban.BannedUntil) < 59*time.Minute {
		t.Fatalf("password-only failures did not impose one-hour ban: %+v %v", ban, err)
	}
	if response = optionalCapLogin(t, server, optionalCapChangedPassword, "", bannedIP); response.Code != http.StatusTooManyRequests {
		t.Fatalf("banned IP logged in with correct password: %d", response.Code)
	}
	if upstreamRequests.Load() != 0 {
		t.Fatalf("disabled Cap received %d requests", upstreamRequests.Load())
	}
}

func TestCapEnabledWithoutExplicitDisableFailsClosed(t *testing.T) {
	state := optionalCapStore(t, false)
	server, err := New(state, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response := optionalCapLogin(t, server, optionalCapPassword, "", "")
	if response.Code != http.StatusForbidden || len(response.Result().Cookies()) != 0 {
		t.Fatalf("default policy allowed password-only login: %d %s", response.Code, response.Body.String())
	}
	// Old JSON has no disable field and must still enforce CAPTCHA.
	encoded, err := json.Marshal(state.Config())
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatal(err)
	}
	var security map[string]json.RawMessage
	if err = json.Unmarshal(legacy["security"], &security); err != nil {
		t.Fatal(err)
	}
	delete(security, "cap_disabled")
	legacy["security"], err = json.Marshal(security)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var config model.Config
	if err = json.Unmarshal(encoded, &config); err != nil {
		t.Fatal(err)
	}
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	response = optionalCapLogin(t, server, optionalCapPassword, "", "")
	if response.Code != http.StatusForbidden || len(response.Result().Cookies()) != 0 {
		t.Fatalf("legacy configuration bypassed CAPTCHA: %d", response.Code)
	}
	outage := httptest.NewServer(http.NotFoundHandler())
	config.Security.CapURL = outage.URL
	config.Security.CapSiteKey = "site"
	config.Security.CapSecret = "site-secret"
	outage.Close()
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	server.markCapReady(config, "Bearer previous-session", true)
	response = optionalCapLogin(t, server, optionalCapPassword, "site:outage-token", "")
	if response.Code != http.StatusForbidden || len(response.Result().Cookies()) != 0 {
		t.Fatalf("configured outage bypassed CAPTCHA: %d", response.Code)
	}
	var sessions int
	if err = state.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("fail-closed login created sessions: %d %v", sessions, err)
	}
}

// optionalCapService models the official API's state rather than accepting echoed settings.
type optionalCapService struct {
	mu          sync.Mutex
	failure     string
	level       int
	created     bool
	requests    int
	verified    int
	authEntered chan struct{}
	authRelease chan struct{}
}

func (service *optionalCapService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.requests++
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/auth/login" {
		if service.authEntered != nil {
			close(service.authEntered)
			<-service.authRelease
		}
		var body struct {
			Key string `json:"admin_key"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Key != "local-admin-password" || service.failure == "auth" {
			http.Error(w, "invalid admin key", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"session_token":"official-session","hashed_token":"official-hash"}`))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/server/") && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		http.Error(w, "admin session required", http.StatusUnauthorized)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /server/keys":
		service.created = true
		_, _ = w.Write([]byte(`{"siteKey":"site","secretKey":"site-secret"}`))
	case "PUT /server/keys/site/config":
		var body struct {
			Level           int    `json:"obfuscationLevel"`
			Instrumentation bool   `json:"instrumentation"`
			Protocol        string `json:"protocol"`
		}
		if !service.created || json.NewDecoder(r.Body).Decode(&body) != nil || !body.Instrumentation || body.Protocol != "sha256-pow" {
			http.Error(w, "invalid policy", http.StatusBadRequest)
			return
		}
		service.level = body.Level
		if service.failure == "configure" {
			http.Error(w, "configuration unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	case "GET /server/keys/site":
		level := service.level
		if service.failure == "readback" {
			level = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{"config": map[string]any{"instrumentation": true, "obfuscationLevel": level, "protocol": "sha256-pow"}}})
	case "GET /health":
		if service.failure == "health" {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	case "POST /site/siteverify":
		var body struct {
			Secret   string `json:"secret"`
			Response string `json:"response"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Secret != "site-secret" || body.Response != "site:valid-token" || service.failure == "verify" {
			_, _ = w.Write([]byte(`{"success":false}`))
			return
		}
		service.verified++
		_, _ = w.Write([]byte(`{"success":true}`))
	default:
		http.NotFound(w, r)
	}
}

func optionalCapConfiguredSession(t *testing.T, state *store.Store, server *Server) string {
	t.Helper()
	if err := state.ChangeAccount("", optionalCapPassword, optionalCapChangedPassword); err != nil {
		t.Fatal(err)
	}
	return optionalCapSession(t, optionalCapLogin(t, server, optionalCapChangedPassword, "", ""))
}

func TestCapEnablePreparesBeforeCommitAndPreservesDisabledSessionsOnFailure(t *testing.T) {
	state := optionalCapStore(t, true)
	service := &optionalCapService{}
	upstream := httptest.NewServer(service)
	defer upstream.Close()
	config := state.Config()
	config.Security.CapURL = upstream.URL
	if err := state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "cap-admin-key")
	preparationFails := false
	server, err := New(state, Hooks{CapAdminKeyFile: keyFile, PrepareConfig: func(old, new model.Config) (func(), func(), error) {
		if preparationFails {
			return nil, nil, errors.New("runtime unavailable")
		}
		return nil, nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	token := optionalCapConfiguredSession(t, state, server)
	candidate := state.Config()
	candidate.Security.CapDisabled = false
	for _, failure := range []string{"missing-key", "wrong-key", "auth", "configure", "readback", "health", "runtime"} {
		t.Run(failure, func(t *testing.T) {
			service.mu.Lock()
			service.failure = failure
			service.mu.Unlock()
			if failure != "missing-key" {
				key := "local-admin-password\n"
				if failure == "wrong-key" {
					key = "different-admin-password"
				}
				if err := os.WriteFile(keyFile, []byte(key), 0600); err != nil {
					t.Fatal(err)
				}
			}
			preparationFails = failure == "runtime"
			response := optionalCapRequest(t, server, http.MethodPut, "/api/config", candidate, token, optionalCapChangedPassword, "")
			if response.Code != http.StatusConflict {
				t.Fatalf("failed enable committed: %d %s", response.Code, response.Body.String())
			}
			actual := state.Config()
			if !actual.Security.CapDisabled || actual.Security.CapSiteKey != "" || actual.Security.CapSecret != "" {
				t.Fatalf("failed enable changed stored policy: %+v", actual.Security)
			}
			if response = optionalCapRequest(t, server, http.MethodGet, "/api/config", nil, token, "", ""); response.Code != http.StatusOK {
				t.Fatalf("failed enable locked out existing session: %d", response.Code)
			}
			if response = optionalCapRequest(t, server, http.MethodGet, "/api/bootstrap", nil, "", "", ""); !strings.Contains(response.Body.String(), `"cap_enabled":false`) || !strings.Contains(response.Body.String(), `"cap_ready":false`) || !strings.Contains(response.Body.String(), `"setup_required":false`) {
				t.Fatalf("failed enable claims protection: %s", response.Body.String())
			}
			optionalCapSession(t, optionalCapLogin(t, server, optionalCapChangedPassword, "", ""))
		})
	}
	preparationFails = false
	service.mu.Lock()
	service.failure = ""
	service.mu.Unlock()
	response := optionalCapRequest(t, server, http.MethodPut, "/api/config", candidate, token, optionalCapChangedPassword, "")
	if response.Code != http.StatusOK || state.Config().Security.CapDisabled {
		t.Fatalf("prepared enable failed: %d %s", response.Code, response.Body.String())
	}
	if response = optionalCapRequest(t, server, http.MethodGet, "/api/bootstrap", nil, "", "", ""); !strings.Contains(response.Body.String(), `"cap_enabled":true`) || !strings.Contains(response.Body.String(), `"cap_ready":true`) {
		t.Fatalf("prepared enable not ready: %s", response.Body.String())
	}
	if response = optionalCapLogin(t, server, optionalCapChangedPassword, "", "203.0.113.1"); response.Code != http.StatusForbidden {
		t.Fatalf("enabled policy accepted missing CAPTCHA: %d", response.Code)
	}
	optionalCapSession(t, optionalCapLogin(t, server, optionalCapChangedPassword, "site:valid-token", "203.0.113.1"))
	if response = optionalCapLogin(t, server, optionalCapChangedPassword, "site:valid-token", "203.0.113.1"); response.Code != http.StatusForbidden {
		t.Fatalf("enabled policy accepted replayed CAPTCHA: %d", response.Code)
	}
	// Disabling remains a sensitive, current-password-authenticated mutation.
	candidate = state.Config()
	candidate.Security.CapDisabled = true
	if response = optionalCapRequest(t, server, http.MethodPut, "/api/config", candidate, token, "", ""); response.Code != http.StatusForbidden || state.Config().Security.CapDisabled {
		t.Fatalf("Cap disabled without current password: %d", response.Code)
	}
	service.mu.Lock()
	requests := service.requests
	service.mu.Unlock()
	if response = optionalCapRequest(t, server, http.MethodPut, "/api/config", candidate, token, optionalCapChangedPassword, ""); response.Code != http.StatusOK {
		t.Fatalf("authenticated disable failed: %d %s", response.Code, response.Body.String())
	}
	optionalCapSession(t, optionalCapLogin(t, server, optionalCapChangedPassword, "", "203.0.113.2"))
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.requests != requests || service.verified != 1 {
		t.Fatalf("disable contacted upstream or replay reached verifier: requests %d -> %d, verifications %d", requests, service.requests, service.verified)
	}
}

func TestEnablingCapSerializesPasswordOnlyLogin(t *testing.T) {
	state := optionalCapStore(t, true)
	service := &optionalCapService{authEntered: make(chan struct{}), authRelease: make(chan struct{})}
	upstream := httptest.NewServer(service)
	defer upstream.Close()
	config := state.Config()
	config.Security.CapURL = upstream.URL
	if err := state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "cap-admin-key")
	if err := os.WriteFile(keyFile, []byte("local-admin-password"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := New(state, Hooks{CapAdminKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	token := optionalCapConfiguredSession(t, state, server)
	candidate := state.Config()
	candidate.Security.CapDisabled = false
	enabled := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		enabled <- optionalCapRequest(t, server, http.MethodPut, "/api/config", candidate, token, optionalCapChangedPassword, "")
	}()
	select {
	case <-service.authEntered:
	case <-time.After(5 * time.Second):
		close(service.authRelease)
		t.Fatal("enable did not begin Cap preparation")
	}
	loggedIn := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		loggedIn <- optionalCapLogin(t, server, optionalCapChangedPassword, "", "203.0.113.3")
	}()
	close(service.authRelease)
	if response := <-enabled; response.Code != http.StatusOK {
		t.Fatalf("enable failed: %d %s", response.Code, response.Body.String())
	}
	if response := <-loggedIn; response.Code != http.StatusForbidden || len(response.Result().Cookies()) != 0 {
		t.Fatalf("in-flight password-only login survived enable: %d %s", response.Code, response.Body.String())
	}
}
