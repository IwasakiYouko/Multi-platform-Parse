package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/store"
)

func TestRejectedPreparationPreservesConfigAndSessions(t *testing.T) {
	state, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err = state.ResetAdmin("test-admin", "strong-test-password"); err != nil {
		t.Fatal(err)
	}
	account, err := state.Authenticate("test-admin", "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := state.NewSession(account)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-2 * time.Hour)
	deadline := created.Add(12 * time.Hour).Unix()
	if _, err = state.DB().Exec("UPDATE sessions SET created_at=?,expires_at=?", created.Unix(), deadline); err != nil {
		t.Fatal(err)
	}
	prepared := false
	server, err := New(state, Hooks{PrepareConfig: func(old, new model.Config) (func(), func(), error) {
		prepared = true
		if state.Config().Security.SessionHours != old.Security.SessionHours {
			t.Error("configuration published before preparation")
		}
		return nil, nil, errors.New("listener unavailable")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	config := state.Config()
	config.Security.SessionHours = 1
	request := httptest.NewRequest(http.MethodPut, "/api/config", nil)
	if err = server.applyConfig(request, config); err == nil || !prepared {
		t.Fatalf("preparation failure ignored: %v", err)
	}
	if state.Config().Security.SessionHours != 12 {
		t.Fatal("rejected config published")
	}
	var persisted int64
	if err = state.DB().QueryRow("SELECT expires_at FROM sessions").Scan(&persisted); err != nil || persisted != deadline {
		t.Fatalf("rejected config changed session deadline: %d %v", persisted, err)
	}
	if _, err = state.Session(token); err != nil {
		t.Fatalf("rejected config invalidated session: %v", err)
	}
}

func TestPreparedRuntimeIsAppliedOnlyAfterCommit(t *testing.T) {
	for _, rejectSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "storage-failure"}[rejectSave], func(t *testing.T) {
			state, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			applied, discarded, notified := false, false, false
			hooks := Hooks{
				PrepareConfig: func(old, new model.Config) (func(), func(), error) {
					if state.Config().Security.SessionHours != old.Security.SessionHours {
						t.Error("prepare observed unpublished config")
					}
					return func() {
						applied = true
						if state.Config().Security.SessionHours != new.Security.SessionHours {
							t.Error("runtime applied before durable save")
						}
					}, func() { discarded = true }, nil
				},
				ConfigChanged: func(old, new model.Config) {
					notified = true
					if !applied {
						t.Error("notification preceded runtime apply")
					}
				},
			}
			server, err := New(state, hooks)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			if rejectSave {
				if _, err = state.DB().Exec("CREATE TRIGGER reject_settings BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT,'reject'); END"); err != nil {
					t.Fatal(err)
				}
			}
			config := state.Config()
			config.Security.SessionHours = 1
			err = server.applyConfig(httptest.NewRequest(http.MethodPut, "/api/config", nil), config)
			if rejectSave {
				if err == nil || applied || notified || !discarded {
					t.Fatalf("failed commit lifecycle: applied=%v discarded=%v notified=%v err=%v", applied, discarded, notified, err)
				}
			} else if err != nil || !applied || discarded || !notified {
				t.Fatalf("successful lifecycle: applied=%v discarded=%v notified=%v err=%v", applied, discarded, notified, err)
			}
		})
	}
}

func TestPartialCapUpdateRestoresIndependentlyOfCanceledRequest(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "fail-closed"}[failRestore], func(t *testing.T) {
			state, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			server, err := New(state, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			level := 4
			restoreAttempted := false
			capServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case http.MethodPut:
					if server.capConfigured(state.Config()) {
						t.Error("login remained enabled during Cap mutation")
					}
					var body struct {
						Level int `json:"obfuscationLevel"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						http.Error(w, "bad JSON", 400)
						return
					}
					if body.Level == 4 {
						restoreAttempted = true
						if failRestore {
							http.Error(w, "restore unavailable", 503)
							return
						}
					}
					level = body.Level
					_, _ = w.Write([]byte(`{"success":true}`))
				case http.MethodGet:
					if level == 1 {
						cancel()
						http.Error(w, "readback interrupted", 503)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"key": map[string]any{"config": map[string]any{"instrumentation": true, "obfuscationLevel": level, "protocol": "sha256-pow"}}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer capServer.Close()
			old := state.Config()
			old.Security.CapURL = capServer.URL
			old.Security.CapSiteKey = "site"
			old.Security.CapSecret = "site-secret"
			if err = state.SaveConfig(old); err != nil {
				t.Fatal(err)
			}
			server.markCapReady(old, "Bearer test-session", true)
			updated := state.Config()
			updated.Security.CapLevel = 1
			request := httptest.NewRequest(http.MethodPut, "/api/config", nil).WithContext(ctx)
			if err = server.applyConfig(request, updated); err == nil {
				t.Fatal("partial update reported success")
			}
			mu.Lock()
			actual, attempted := level, restoreAttempted
			mu.Unlock()
			if !attempted {
				t.Fatal("canceled request prevented independent restore")
			}
			if state.Config().Security.CapLevel != 4 {
				t.Fatal("partial update persisted")
			}
			if failRestore {
				if server.capConfigured(old) {
					t.Fatal("failed restore reopened login")
				}
			} else if actual != 4 || !server.capConfigured(old) {
				t.Fatalf("old enforcement not restored: level=%d ready=%v", actual, server.capConfigured(old))
			}
		})
	}
}
