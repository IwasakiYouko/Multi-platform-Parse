package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"multiparse/internal/model"
)

func TestEncryptedConfigSessionsAndTTL(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	initial, err := os.Stat(filepath.Join(dir, "initial-admin.txt"))
	if err != nil || initial.Mode().Perm() != 0600 {
		t.Fatalf("initial credentials permissions: %v %v", initial, err)
	}
	cfg := s.Config()
	cfg.Platforms["bilibili"] = model.PlatformPolicy{Enabled: true, Cookie: "test-cookie-secret"}
	if err = s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var encrypted []byte
	if err = s.DB().QueryRow("SELECT payload FROM settings WHERE id=1").Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("test-cookie-secret")) {
		t.Fatal("secret persisted as plaintext")
	}
	snapshot := s.Config()
	snapshot.Platforms["bilibili"] = model.PlatformPolicy{}
	if s.Config().Platforms["bilibili"].Cookie != "test-cookie-secret" {
		t.Fatal("configuration snapshot aliases live state")
	}
	if err = s.ResetAdmin("test-admin", "strong-test-password"); err != nil {
		t.Fatal(err)
	}
	a, err := s.Authenticate("test-admin", "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.NewSession(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(token); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.DB().QueryRow("SELECT hash FROM sessions").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("session token stored in plaintext")
	}
	if err = s.ChangeAccount("test-admin", "strong-test-password", "different-test-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(token); err == nil {
		t.Fatal("password change failed to revoke session")
	}
	created := time.Now().Add(-2 * time.Hour)
	if err = s.PutCache(model.CacheEntry{Key: "cache-test", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err = s.PutTask(model.Task{ID: "task-test", CacheKey: "cache-test", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	cfg = s.Config()
	cfg.Cache.TTLHours = 1
	if err = s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if e, err := s.GetCache("cache-test"); err != nil || e != nil {
		t.Fatalf("shortened TTL retrieval: %#v %v", e, err)
	}
	entries, err := s.ListCache()
	if err != nil || len(entries) != 1 || entries[0].ExpiresAt.Sub(entries[0].CreatedAt) != time.Hour {
		t.Fatalf("TTL metadata: %#v %v", entries, err)
	}
	if err = s.DeleteCache("cache-test"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.DB().QueryRow("SELECT count(*) FROM tasks WHERE cache_key='cache-test'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("related tasks not removed: %d %v", count, err)
	}
}

func TestBanAndPersistentLimit(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for range 5 {
		if err = s.RecordFailure("203.0.113.8"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.BanStatus("203.0.113.8")
	if err != nil || !b.BannedUntil.After(time.Now().Add(59*time.Minute)) {
		t.Fatalf("default ban: %#v %v", b, err)
	}
	if err = s.Unban(b.IP); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		ok, err := s.AllowRequest("bucket", 1, time.Minute)
		if err != nil || ok != (i == 0) {
			t.Fatalf("request %d allowed=%v err=%v", i, ok, err)
		}
	}
}

func TestLegacyConfigUpgradeDropsCapReplays(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate v1.x database with cap_replays table and legacy cap_* JSON fields.
	if _, err = s.DB().Exec(`CREATE TABLE cap_replays(hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	legacyJSON := []byte(`{"server":{"listen":"::","port":10492},"onebot":{"token":"123456789012345678901234567890"},"security":{"cap_disabled":false,"cap_url":"http://127.0.0.1:3000","cap_site_key":"site","cap_secret":"sec","cap_level":4,"failures":5,"window_minutes":15,"ban_minutes":60,"session_hours":12},"proxy":{"mode":"direct"},"media":{"max_bytes":83886080,"max_duration_seconds":1500,"max_images":30,"quality":720,"concurrency":2,"transcode_concurrency":1,"max_disk_bytes":2147483648,"auto_download":true,"show_cover":true,"show_info":true,"oversize_mode":"link","transport":"file"},"cache":{"ttl_hours":24},"policy":{"enabled":true,"private_enabled":true,"cooldown_seconds":30}}`)
	enc, err := s.encrypt(legacyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB().Exec(`UPDATE settings SET payload=? WHERE id=1`, enc); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	cfg := upgraded.Config()
	if !cfg.Media.CleanURL || !cfg.Policy.GroupReply || cfg.Media.MessageTemplate != model.DefaultMessageTemplate {
		t.Fatalf("upgraded defaults missing: clean_url=%v group_reply=%v template=%q", cfg.Media.CleanURL, cfg.Policy.GroupReply, cfg.Media.MessageTemplate)
	}
	if cfg.Platforms["bilibili"].TemplateMode != "inherit" || cfg.Platforms["bilibili"].CoverMode != "inherit" {
		t.Fatalf("upgraded platform policy not normalized: %+v", cfg.Platforms["bilibili"])
	}
	var tableCount int
	if err = upgraded.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='cap_replays'`).Scan(&tableCount); err != nil || tableCount != 0 {
		t.Fatalf("cap_replays table not dropped: count=%d err=%v", tableCount, err)
	}
}
