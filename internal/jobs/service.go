package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"multiparse/internal/media"
	"multiparse/internal/model"
	"multiparse/internal/netx"
	"multiparse/internal/parser"
	"multiparse/internal/policy"
	"multiparse/internal/store"
)

type Sender interface {
	Send(context.Context, model.Target, []model.Segment) error
	UploadFile(context.Context, model.Target, string, string) error
}
type work struct {
	task        model.Task
	target      model.Target
	config      model.Config
	cooldownKey string
}
type flight struct {
	done chan struct{}
	err  error
}
type running struct {
	platform string
	target   model.Target
	cancel   context.CancelFunc
}
type Service struct {
	st               *store.Store
	media            *media.Manager
	sender           Sender
	ctx              context.Context
	cancel           context.CancelFunc
	queue            chan work
	wg               sync.WaitGroup
	mu               sync.Mutex
	active           map[string]running
	slots            int
	changed          chan struct{}
	cacheMu          sync.Mutex
	leases           map[string]int
	pending          map[string]bool
	flights          map[string]*flight
	lastCleanupError string
	signingKey       []byte
	cooldowns        map[string]cooldownEntry
}

func New(st *store.Store, m *media.Manager, sender Sender) *Service {
	if err := st.CancelInterruptedTasks(time.Now().UTC()); err != nil {
		slog.Error("恢复中断任务记录失败", "error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{st: st, media: m, sender: sender, ctx: ctx, cancel: cancel, queue: make(chan work, 64), active: map[string]running{}, changed: make(chan struct{}), leases: map[string]int{}, pending: map[string]bool{}, flights: map[string]*flight{}}
	s.signingKey = []byte(model.NewID() + model.NewID())
	s.cooldowns = make(map[string]cooldownEntry)
	for range 16 {
		s.wg.Add(1)
		go s.worker()
	}
	s.wg.Add(1)
	go s.janitor()
	return s
}
func (s *Service) Close() error {
	s.cancel()
	s.mu.Lock()
	s.notify()
	s.mu.Unlock()
	s.wg.Wait()
	return s.Cleanup(false)
}
func (s *Service) notify() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Service) Counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active), len(s.queue)
}
func (s *Service) Reconfigure(old, new model.Config) {
	s.mu.Lock()
	for _, r := range s.active {
		if why, _ := policy.Check(new, r.target, r.platform); why != "" {
			r.cancel()
		}
	}
	s.notify()
	s.mu.Unlock()
	if old.Cache.TTLHours != new.Cache.TTLHours {
		if err := s.Cleanup(false); err != nil {
			slog.Error("缓存期限变更清理失败", "error", err)
		}
	}
}
func (s *Service) HandleEvent(e model.Event, links []string) {
	for _, raw := range links {
		if _, err := s.Submit(s.ctx, e.Target, raw); err != nil {
			slog.Warn("消息解析未入队", "reason", safeError(err))
		}
	}
}
func (s *Service) Submit(ctx context.Context, target model.Target, raw string) (string, error) {
	if len(raw) > 8192 {
		return "", errors.New("链接过长")
	}
	raw = netx.NormalizeURL(raw)
	p, ok := model.Identify(raw)
	if !ok {
		return "", errors.New("不支持的平台链接")
	}
	cfg := s.st.Config()
	if reason, silent := policy.Check(cfg, target, p.ID); reason != "" {
		if !silent && (target.UserID != 0 || target.GroupID != 0) {
			sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			_ = s.sender.Send(sendCtx, target, []model.Segment{{Type: "text", Data: map[string]any{"text": policy.Message(cfg, p.ID, reason)}}})
		}
		return "", errors.New(reason)
	}
	now := time.Now().UTC()
	id := model.NewID()
	guard, guardErr := s.reserveCooldown(target, raw, id, cfg.Policy.CooldownSeconds)
	if guardErr != nil {
		return "", guardErr
	}
	queued := false
	defer func() {
		if !queued {
			s.settleCooldown(guard, id, false)
		}
	}()
	task := model.Task{ID: id, BotID: target.BotID, GroupID: target.GroupID, UserID: target.UserID, Platform: p.ID, URL: raw, Status: "queued", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Duration(cfg.Cache.TTLHours) * time.Hour)}
	if err := s.st.PutTask(task); err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		task.Status = "cancelled"
		task.Message = "请求已取消"
		_ = s.st.PutTask(task)
		return "", ctx.Err()
	case <-s.ctx.Done():
		task.Status = "cancelled"
		task.Message = "服务已停止"
		_ = s.st.PutTask(task)
		return "", errors.New("服务已停止")
	case s.queue <- work{task: task, target: target, config: cfg, cooldownKey: guard}:
		queued = true
		return id, nil
	default:
		task.Status = "rejected"
		task.Message = "任务队列已满"
		_ = s.st.PutTask(task)
		return "", errors.New("任务队列已满，请稍后重试")
	}
}
func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case w := <-s.queue:
			if !s.takeSlot() {
				s.finish(&w.task, "cancelled", "服务已停止")
				s.settleCooldown(w.cooldownKey, w.task.ID, false)
				return
			}
			s.execute(w)
			s.mu.Lock()
			s.slots--
			s.notify()
			s.mu.Unlock()
		}
	}
}
func (s *Service) takeSlot() bool {
	for {
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			return false
		}
		if s.slots < s.st.Config().Media.Concurrency {
			s.slots++
			s.mu.Unlock()
			return true
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-s.ctx.Done():
			return false
		}
	}
}
func (s *Service) finish(t *model.Task, status, message string) {
	t.Status = status
	t.Message = safeError(errors.New(message))
	t.UpdatedAt = time.Now().UTC()
	t.ExpiresAt = t.UpdatedAt.Add(time.Duration(s.st.Config().Cache.TTLHours) * time.Hour)
	if err := s.st.PutTask(*t); err != nil {
		slog.Error("任务记录保存失败", "error", err)
	}
}
func (s *Service) execute(w work) {
	succeeded := false
	defer func() { s.settleCooldown(w.cooldownKey, w.task.ID, succeeded) }()
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Minute)
	defer cancel()
	s.mu.Lock()
	s.active[w.task.ID] = running{platform: w.task.Platform, target: w.target, cancel: cancel}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, w.task.ID); s.mu.Unlock() }()
	if reason, _ := policy.Check(s.st.Config(), w.target, w.task.Platform); reason != "" {
		s.finish(&w.task, "rejected", reason)
		return
	}
	w.task.Status = "running"
	w.task.UpdatedAt = time.Now().UTC()
	_ = s.st.PutTask(w.task)
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|quality=%d|bytes=%d|download=%t", w.task.Platform, w.task.URL, w.config.Media.Quality, w.config.Media.MaxBytes, w.config.Media.AutoDownload)))
	key := hex.EncodeToString(hash[:])
	w.task.CacheKey = key
	entry, release, err := s.acquire(ctx, w.config, w.task.URL, key)
	if err == nil {
		defer release()
		_ = s.st.PutTask(w.task)
		err = policy.MediaCheck(s.st.Config(), entry.Result, entry.Files)
	}
	if err == nil {
		if reason, _ := policy.Check(s.st.Config(), w.target, w.task.Platform); reason != "" {
			err = errors.New(reason)
		} else if w.target.UserID != 0 || w.target.GroupID != 0 {
			err = s.deliver(ctx, w.target, entry)
		}
	}
	if err != nil {
		status := "failed"
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
		}
		s.finish(&w.task, status, safeError(err))
		// A send timeout is uncertain: never retry the original media group.
		if ctx.Err() == nil && (w.target.UserID != 0 || w.target.GroupID != 0) {
			current := s.st.Config()
			if reason, _ := policy.Check(current, w.target, w.task.Platform); reason == "" {
				c, done := context.WithTimeout(ctx, 15*time.Second)
				_ = s.sender.Send(c, w.target, []model.Segment{{Type: "text", Data: map[string]any{"text": "解析或发送失败：" + safeError(err)}}})
				done()
			}
		}
		return
	}
	s.finish(&w.task, "completed", "")
	succeeded = true
}
func (s *Service) acquire(ctx context.Context, cfg model.Config, raw, key string) (*model.CacheEntry, func(), error) {
	for {
		s.cacheMu.Lock()
		if s.pending[key] && s.leases[key] > 0 {
			s.cacheMu.Unlock()
			return nil, nil, errors.New("该缓存正在等待使用结束后清理，请稍后重试")
		}
		if !s.pending[key] {
			e, err := s.st.GetCache(key)
			if err != nil {
				s.cacheMu.Unlock()
				return nil, nil, err
			}
			if e != nil && s.filesExist(e) {
				s.leases[key]++
				s.cacheMu.Unlock()
				return e, func() { s.release(key) }, nil
			}
		}
		if f, ok := s.flights[key]; ok {
			s.cacheMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-f.done:
				if f.err != nil {
					return nil, nil, f.err
				}
				continue
			}
		}
		if s.leases[key] > 0 {
			s.pending[key] = true
			s.cacheMu.Unlock()
			return nil, nil, errors.New("过期缓存仍在使用，请稍后重试")
		}
		if removeErr := s.media.Remove(key); removeErr != nil {
			s.pending[key] = true
			s.lastCleanupError = removeErr.Error()
			s.cacheMu.Unlock()
			return nil, nil, removeErr
		}
		if removeErr := s.st.DeleteCache(key); removeErr != nil {
			s.cacheMu.Unlock()
			return nil, nil, removeErr
		}
		delete(s.pending, key)
		f := &flight{done: make(chan struct{})}
		s.flights[key] = f
		s.leases[key]++
		s.cacheMu.Unlock()
		result, err := parser.Resolve(ctx, cfg, raw)
		var files []model.Artifact
		if err == nil {
			err = policy.MediaCheck(cfg, *result, nil)
		}
		if err == nil && cfg.Media.AutoDownload {
			files, err = s.media.Prepare(ctx, cfg, *result, key)
		}
		now := time.Now().UTC()
		var e *model.CacheEntry
		if err == nil {
			e = &model.CacheEntry{Key: key, Platform: result.Platform, URL: raw, Result: *result, Files: files, CreatedAt: now, ExpiresAt: now.Add(time.Duration(cfg.Cache.TTLHours) * time.Hour)}
		}
		s.cacheMu.Lock()
		if err == nil {
			if s.pending[key] {
				e.ExpiresAt = now
			}
			err = s.st.PutCache(*e)
		}
		if err != nil {
			if cleanupErr := s.media.Remove(key); cleanupErr != nil {
				s.pending[key] = true
				s.lastCleanupError = cleanupErr.Error()
			}
			s.leases[key]--
			if s.leases[key] == 0 {
				delete(s.leases, key)
			}
			f.err = err
			delete(s.flights, key)
			close(f.done)
			s.cacheMu.Unlock()
			return nil, nil, err
		}
		delete(s.flights, key)
		close(f.done)
		s.cacheMu.Unlock()
		return e, func() { s.release(key) }, nil
	}
}
func (s *Service) filesExist(e *model.CacheEntry) bool {
	for _, f := range e.Files {
		if _, err := os.Stat(f.Path); err != nil {
			return false
		}
		if f.ThumbPath != "" {
			if _, err := os.Stat(f.ThumbPath); err != nil {
				return false
			}
		}
	}
	return true
}
func (s *Service) release(key string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.leases[key]--
	if s.leases[key] <= 0 {
		delete(s.leases, key)
		if s.pending[key] {
			if err := s.removeLocked(key); err != nil {
				s.lastCleanupError = err.Error()
			}
		}
	}
}
func (s *Service) removeLocked(key string) error {
	if err := s.media.Remove(key); err != nil {
		return err
	}
	if err := s.st.DeleteCache(key); err != nil {
		return err
	}
	delete(s.pending, key)
	return nil
}
func (s *Service) Cleanup(all bool) error {
	s.pruneCooldowns()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entries, err := s.st.ListCache()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	ttl := time.Duration(s.st.Config().Cache.TTLHours) * time.Hour
	if all {
		for key := range s.leases {
			s.pending[key] = true
		}
	}
	var errs []error
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		known[entry.Key] = true
	}
	for _, e := range entries {
		expires := e.ExpiresAt
		if shorter := e.CreatedAt.Add(ttl); shorter.Before(expires) {
			expires = shorter
		}
		if !all && now.Before(expires) && !s.pending[e.Key] {
			continue
		}
		s.pending[e.Key] = true
		if s.leases[e.Key] > 0 {
			e.ExpiresAt = now
			if putErr := s.st.PutCache(e); putErr != nil {
				errs = append(errs, putErr)
			}
			continue
		}
		if e := s.removeLocked(e.Key); e != nil {
			errs = append(errs, e)
		}
	}
	for key := range s.pending {
		if !known[key] && s.leases[key] == 0 {
			if e := s.removeLocked(key); e != nil {
				errs = append(errs, e)
			}
		}
	}
	dirs, scanErr := os.ReadDir(s.media.Root())
	if scanErr != nil {
		errs = append(errs, scanErr)
	} else {
		for _, dir := range dirs {
			key := dir.Name()
			if !dir.IsDir() || len(key) != 64 || known[key] || s.leases[key] > 0 {
				continue
			}
			if _, decodeErr := hex.DecodeString(key); decodeErr != nil {
				continue
			}
			if e := s.removeLocked(key); e != nil {
				s.pending[key] = true
				errs = append(errs, e)
			}
		}
	}
	if err := s.st.PruneRecords(now); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		s.lastCleanupError = errors.Join(errs...).Error()
		return errors.Join(errs...)
	}
	s.lastCleanupError = ""
	return nil
}
func (s *Service) CleanupStatus() (int, string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return len(s.pending), s.lastCleanupError
}
func (s *Service) janitor() {
	defer s.wg.Done()
	if err := s.Cleanup(false); err != nil {
		slog.Warn("启动清理未完成", "error", err)
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.Cleanup(false); err != nil {
				slog.Warn("缓存清理未完成", "error", err)
			}
		}
	}
}
func (s *Service) allowed(target model.Target, platform string) error {
	if reason, _ := policy.Check(s.st.Config(), target, platform); reason != "" {
		return errors.New(reason)
	}
	return nil
}
func (s *Service) deliver(ctx context.Context, target model.Target, e *model.CacheEntry) error {
	if err := s.allowed(target, e.Platform); err != nil {
		return err
	}
	cfg := s.st.Config()
	r := e.Result
	if cfg.Media.ShowInfo {
		var b strings.Builder
		if r.Title != "" {
			fmt.Fprintf(&b, "【%s】\n", truncate(r.Title, 300))
		}
		if r.Author != "" {
			fmt.Fprintf(&b, "作者：%s\n", truncate(r.Author, 100))
		}
		if r.Content != "" {
			fmt.Fprintln(&b, truncate(r.Content, 1800))
		}
		fmt.Fprintf(&b, "链接：%s", r.SourceURL)
		if err := s.sender.Send(ctx, target, []model.Segment{{Type: "text", Data: map[string]any{"text": b.String()}}}); err != nil {
			return err
		}
	}
	for _, f := range e.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.allowed(target, e.Platform); err != nil {
			return err
		}
		if err := policy.MediaCheck(s.st.Config(), r, []model.Artifact{f}); err != nil {
			return err
		}
		if cfg.Media.ShowCover && f.Kind == "video" && f.ThumbPath != "" {
			if err := s.sender.Send(ctx, target, []model.Segment{{Type: "image", Data: map[string]any{"file": s.mediaLocation(e, f.ThumbPath, target)}}}); err != nil {
				return err
			}
		}
		if err := s.allowed(target, e.Platform); err != nil {
			return err
		}
		location := s.mediaLocation(e, f.Path, target)
		if f.Kind == "file" || (f.Size > s.st.Config().Media.MaxBytes && s.st.Config().Media.OversizeMode == "file") {
			if err := s.sender.UploadFile(ctx, target, location, filepath.Base(f.Path)); err != nil {
				return err
			}
			continue
		}
		kind := f.Kind
		if kind == "audio" {
			if err := s.sender.UploadFile(ctx, target, location, filepath.Base(f.Path)); err != nil {
				return err
			}
			continue
		}
		if kind != "video" && kind != "image" {
			return errors.New("无法发送未知媒体类型")
		}
		data := map[string]any{"file": location}
		if kind == "video" && f.ThumbPath != "" {
			data["thumb"] = s.mediaLocation(e, f.ThumbPath, target)
		}
		if err := s.sender.Send(ctx, target, []model.Segment{{Type: kind, Data: data}}); err != nil {
			return err
		}
	}
	if !cfg.Media.ShowInfo && len(e.Files) == 0 {
		return s.sender.Send(ctx, target, []model.Segment{{Type: "text", Data: map[string]any{"text": r.SourceURL}}})
	}
	return nil
}
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	// Network errors may contain signed URLs or proxy userinfo; do not persist them.
	for _, prefix := range []string{"https://", "http://", "socks5://", "socks5h://"} {
		for {
			i := strings.Index(text, prefix)
			if i < 0 {
				break
			}
			j := i
			for j < len(text) && !strings.ContainsRune(" \n\t\"'<>）)", rune(text[j])) {
				j++
			}
			text = text[:i] + "[地址已隐藏]" + text[j:]
		}
	}
	return truncate(text, 500)
}
