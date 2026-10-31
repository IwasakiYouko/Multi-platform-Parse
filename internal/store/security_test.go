package store

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"multiparse/internal/model"
)

func TestInterruptedAdminBootstrapRecovers(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	assertCredentialsLogged := func(credentials []byte) {
		t.Helper()
		var record struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
			t.Fatal("expected exactly one initial credential log record")
		}
		fields := strings.Split(string(credentials), "\n")
		if record.Username != strings.TrimPrefix(fields[0], "username: ") || record.Password != strings.TrimPrefix(fields[1], "password: ") {
			t.Fatal("logged credentials do not match the initialized account")
		}
		logs.Reset()
	}
	directory := t.TempDir()
	initial, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, "initial-admin.txt")
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	assertCredentialsLogged(before)
	if _, err = initial.DB().Exec("DELETE FROM admin"); err != nil {
		t.Fatal(err)
	}
	if err = initial.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	assertCredentialsLogged(after)
	if bytes.Equal(before, after) {
		t.Fatal("interrupted credentials were not replaced")
	}
	fields := strings.Split(string(after), "\n")
	username := strings.TrimPrefix(fields[0], "username: ")
	password := strings.TrimPrefix(fields[1], "password: ")
	account, err := recovered.Authenticate(username, password)
	if err != nil || !account.MustChangePassword {
		t.Fatalf("recovered credentials: %#v %v", account, err)
	}
	info, err := os.Stat(filename)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v %v", info, err)
	}
	if err = recovered.Close(); err != nil {
		t.Fatal(err)
	}
	existing, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	unchanged, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(after, unchanged) {
		t.Fatal("existing account credentials regenerated")
	}
	if logs.Len() != 0 {
		t.Fatal("existing administrator credentials were logged again")
	}
}

func TestSessionRetrievalClampsCurrentLifetime(t *testing.T) {
	state, err := Open(t.TempDir())
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
	config := state.Config()
	config.Security.SessionHours = 1
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-2 * time.Hour)
	if _, err = state.DB().Exec("UPDATE sessions SET created_at=?,expires_at=? WHERE hash=?", created.Unix(), created.Add(12*time.Hour).Unix(), digest(token)); err != nil {
		t.Fatal(err)
	}
	if _, err = state.Session(token); err != ErrSession {
		t.Fatalf("session escaped current lifetime: %v", err)
	}
	fresh, session, err := state.NewSession(account)
	if err != nil {
		t.Fatal(err)
	}
	if session.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatal("new session used previous lifetime")
	}
	if _, err = state.Session(fresh); err != nil {
		t.Fatal(err)
	}
}

func TestTTLIncreaseDoesNotReviveExpiredRecords(t *testing.T) {
	state, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	config := state.Config()
	config.Cache.TTLHours = 1
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-2 * time.Hour)
	if err = state.PutCache(model.CacheEntry{Key: "expired", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err = state.PutTask(model.Task{ID: "expired-task", CacheKey: "expired", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err = state.PutCache(model.CacheEntry{Key: "invalidated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	config.Cache.TTLHours = 24
	if err = state.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"expired", "invalidated"} {
		if entry, err := state.GetCache(key); err != nil || entry != nil {
			t.Fatalf("record %s resurrected: %#v %v", key, entry, err)
		}
	}
	tasks, err := state.ListTasks(100)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("expired tasks resurrected: %#v %v", tasks, err)
	}
}

func TestSessionInsertionSerializesLifetimeChanges(t *testing.T) {
	state, err := Open(t.TempDir())
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
	config := state.Config()
	config.Security.SessionHours = 1
	state.authMu.Lock()
	authLocked := true
	defer func() {
		if authLocked {
			state.authMu.Unlock()
		}
	}()
	inserted := make(chan error, 1)
	go func() { _, _, err := state.NewSession(account); inserted <- err }()
	deadline := time.Now().Add(2 * time.Second)
	readLocked := false
	for time.Now().Before(deadline) {
		if !state.mu.TryLock() {
			readLocked = true
			break
		}
		state.mu.Unlock()
		runtime.Gosched()
	}
	if !readLocked {
		t.Fatal("session insertion did not retain configuration read lock")
	}
	saved := make(chan error, 1)
	go func() { saved <- state.SaveConfig(config) }()
	state.authMu.Unlock()
	authLocked = false
	if err = <-inserted; err != nil {
		t.Fatal(err)
	}
	if err = <-saved; err != nil {
		t.Fatal(err)
	}
	var created, expires int64
	if err = state.DB().QueryRow("SELECT created_at,expires_at FROM sessions").Scan(&created, &expires); err != nil {
		t.Fatal(err)
	}
	if expires > created+3600 {
		t.Fatal("concurrent login escaped lifetime shortening")
	}
}
