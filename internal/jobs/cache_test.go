package jobs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"multiparse/internal/media"
	"multiparse/internal/model"
	"multiparse/internal/store"
)

func cacheService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	st, e := store.Open(filepath.Join(root, "data"))
	if e != nil {
		t.Fatal(e)
	}
	m, e := media.New(filepath.Join(root, "media"))
	if e != nil {
		t.Fatal(e)
	}
	s := New(st, m, nil)
	t.Cleanup(func() { _ = s.Close(); _ = st.Close() })
	return s
}
func seedCache(t *testing.T, s *Service, key string, created time.Time) model.CacheEntry {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	t.Helper()
	dir := filepath.Join(s.media.Root(), key)
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "image.jpg")
	if e := os.WriteFile(path, []byte("cached image"), 0600); e != nil {
		t.Fatal(e)
	}
	entry := model.CacheEntry{Key: key, Platform: "bilibili", URL: "https://www.bilibili.com/video/BV1GJ411x7h7", Result: model.ParseResult{Platform: "bilibili", Title: "cached", Type: "image"}, Files: []model.Artifact{{Kind: "image", Path: path, Size: 12}}, CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour)}
	if e := s.st.PutCache(entry); e != nil {
		t.Fatal(e)
	}
	task := model.Task{ID: model.NewID(), Platform: "bilibili", CacheKey: key, Status: "completed", CreatedAt: created, UpdatedAt: created, ExpiresAt: entry.ExpiresAt}
	if e := s.st.PutTask(task); e != nil {
		t.Fatal(e)
	}
	return entry
}
func TestExpirationRemovesFilesAndAssociatedRecords(t *testing.T) {
	s := cacheService(t)
	key := strings.Repeat("a", 64)
	entry := seedCache(t, s, key, time.Now().Add(-25*time.Hour))
	if got, e := s.st.GetCache(key); e != nil || got != nil {
		t.Fatalf("expired cache must not be reusable: %v %v", got, e)
	}
	if e := s.Cleanup(false); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(entry.Files[0].Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("expired file retained: %v", e)
	}
	var caches, tasks int
	_ = s.st.DB().QueryRow("SELECT COUNT(*) FROM cache WHERE cache_key=?", key).Scan(&caches)
	_ = s.st.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE cache_key=?", key).Scan(&tasks)
	if caches != 0 || tasks != 0 {
		t.Fatalf("associated records retained: cache=%d tasks=%d", caches, tasks)
	}
}
func TestClearInvalidatesLeasedCacheBeforePhysicalDeletion(t *testing.T) {
	s := cacheService(t)
	key := strings.Repeat("b", 64)
	entry := seedCache(t, s, key, time.Now())
	_, release, e := s.acquire(context.Background(), s.st.Config(), entry.URL, key)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Cleanup(true); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(entry.Files[0].Path); e != nil {
		t.Fatalf("active file deleted prematurely: %v", e)
	}
	if got, e := s.st.GetCache(key); e != nil || got != nil {
		t.Fatalf("cleared cache still reusable: %v %v", got, e)
	}
	if _, _, e = s.acquire(context.Background(), s.st.Config(), entry.URL, key); e == nil {
		t.Fatal("clearing cache accepted new consumer")
	}
	release()
	if _, e = os.Stat(entry.Files[0].Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("released expired file retained: %v", e)
	}
}
func TestSignedMediaCannotBypassPlatformDisableOrSignature(t *testing.T) {
	s := cacheService(t)
	key := strings.Repeat("c", 64)
	entry := seedCache(t, s, key, time.Now())
	cfg := s.st.Config()
	cfg.Media.Transport = "http"
	cfg.Server.ExternalURL = "https://bot.example"
	if e := s.st.SaveConfig(cfg); e != nil {
		t.Fatal(e)
	}
	signed := s.mediaLocation(&entry, entry.Files[0].Path, model.Target{GroupID: 123, UserID: 456})
	req := httptest.NewRequest(http.MethodGet, signed, nil)
	rec := httptest.NewRecorder()
	s.MediaHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "cached image" {
		t.Fatalf("valid signed content unavailable: %d %q", rec.Code, rec.Body.String())
	}
	tampered := httptest.NewRequest(http.MethodGet, signed+"0", nil)
	rec = httptest.NewRecorder()
	s.MediaHandler().ServeHTTP(rec, tampered)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered signature accepted: %d", rec.Code)
	}
	p := cfg.Platforms["bilibili"]
	p.Enabled = false
	cfg.Platforms["bilibili"] = p
	if e := s.st.SaveConfig(cfg); e != nil {
		t.Fatal(e)
	}
	rec = httptest.NewRecorder()
	s.MediaHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled platform still serves cached media: %d", rec.Code)
	}
}
func TestAcquireRetriesOnceOnRetryableMediaFailure(t *testing.T) {
	s := cacheService(t)
	resolveCalls, prepareCalls := 0, 0
	s.resolve = func(_ context.Context, _ model.Config, raw string) (*model.ParseResult, error) {
		resolveCalls++
		return &model.ParseResult{
			Platform:  "douyin",
			SourceURL: raw,
			Type:      "video",
			Title:     fmt.Sprintf("attempt-%d", resolveCalls),
			Media:     []model.Media{{Kind: "video", URL: fmt.Sprintf("https://cdn%d.example/video.mp4", resolveCalls)}},
		}, nil
	}
	s.prepare = func(_ context.Context, _ model.Config, result model.ParseResult, key string) ([]model.Artifact, error) {
		prepareCalls++
		if prepareCalls == 1 {
			return nil, errors.New("ffmpeg媒体处理失败")
		}
		dir := filepath.Join(s.media.Root(), key)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		videoPath := filepath.Join(dir, "0001.mp4")
		if err := os.WriteFile(videoPath, []byte("mp4data"), 0600); err != nil {
			return nil, err
		}
		return []model.Artifact{{Kind: "video", Path: videoPath, Size: 7, Duration: 1, Width: 64, Height: 48}}, nil
	}
	key1 := strings.Repeat("d", 64)
	entry, release, err := s.acquire(context.Background(), s.st.Config(), "https://www.douyin.com/video/1001", key1)
	if err != nil {
		t.Fatalf("acquire should succeed on second attempt: %v", err)
	}
	defer release()
	if resolveCalls != 2 || prepareCalls != 2 {
		t.Fatalf("expected 2 resolve and 2 prepare calls, got resolve=%d prepare=%d", resolveCalls, prepareCalls)
	}
	if entry.Result.Title != "attempt-2" || len(entry.Files) != 1 {
		t.Fatalf("expected second resolve result to be cached, got: %+v", entry)
	}
	if cached, cacheErr := s.st.GetCache(key1); cacheErr != nil || cached == nil {
		t.Fatalf("expected cache entry persisted after retry: cached=%v err=%v", cached, cacheErr)
	}

	resolveCalls, prepareCalls = 0, 0
	s.prepare = func(_ context.Context, _ model.Config, _ model.ParseResult, _ string) ([]model.Artifact, error) {
		prepareCalls++
		return nil, errors.New("媒体实际时长超过限制")
	}
	key2 := strings.Repeat("e", 64)
	if _, _, err = s.acquire(context.Background(), s.st.Config(), "https://www.douyin.com/video/1002", key2); err == nil {
		t.Fatal("expected non-retryable prepare error")
	}
	if resolveCalls != 1 || prepareCalls != 1 {
		t.Fatalf("non-retryable error must not trigger re-resolve: resolve=%d prepare=%d", resolveCalls, prepareCalls)
	}
	if cached, _ := s.st.GetCache(key2); cached != nil {
		t.Fatalf("failed non-retryable acquire must not write cache: %+v", cached)
	}
}
