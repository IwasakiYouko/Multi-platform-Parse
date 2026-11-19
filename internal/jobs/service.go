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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"multiparse/internal/media"
	"multiparse/internal/model"
	"multiparse/internal/netx"
	"multiparse/internal/onebot"
	"multiparse/internal/parser"
	"multiparse/internal/policy"
	"multiparse/internal/store"
)

type Sender interface {
	Send(context.Context, model.Target, []model.Segment) error
	SendForward(context.Context, model.Target, []model.ForwardNode) error
	UploadFile(context.Context, model.Target, string, string) error
}

type EmojiReactor interface {
	SetMsgEmojiLike(ctx context.Context, botID, messageID, emojiID string, set bool) error
}

const reactionEmojiEyes = "128064"

type work struct {
	task        model.Task
	target      model.Target
	config      model.Config
	cooldownKey string
	onDone      func()
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
	cookieMu         sync.Mutex
	cookieAlerts     map[string]model.CookieAlert
	resolve          func(context.Context, model.Config, string) (*model.ParseResult, error)
	prepare          func(context.Context, model.Config, model.ParseResult, string) ([]model.Artifact, error)
}

func New(st *store.Store, m *media.Manager, sender Sender) *Service {
	if err := st.CancelInterruptedTasks(time.Now().UTC()); err != nil {
		slog.Error("恢复中断任务记录失败", "error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		st:           st,
		media:        m,
		sender:       sender,
		ctx:          ctx,
		cancel:       cancel,
		queue:        make(chan work, 64),
		active:       map[string]running{},
		changed:      make(chan struct{}),
		leases:       map[string]int{},
		pending:      map[string]bool{},
		flights:      map[string]*flight{},
		cooldowns:    make(map[string]cooldownEntry),
		cookieAlerts: make(map[string]model.CookieAlert),
	}
	s.signingKey = []byte(model.NewID() + model.NewID())
	s.resolve = parser.Resolve
	if m != nil {
		s.prepare = m.Prepare
	}
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

func (s *Service) recordCookieHealth(platform string, cfg model.Config, expired bool) {
	if strings.TrimSpace(cfg.Platforms[platform].Cookie) == "" {
		return
	}
	s.cookieMu.Lock()
	defer s.cookieMu.Unlock()
	if expired {
		s.cookieAlerts[platform] = model.CookieAlert{
			Platform:   platform,
			Reason:     "已配置的登录 Cookie 疑似失效，已回退至游客通道",
			DetectedAt: time.Now().UTC(),
		}
	} else {
		delete(s.cookieAlerts, platform)
	}
}

func (s *Service) CookieAlerts() []model.CookieAlert {
	s.cookieMu.Lock()
	defer s.cookieMu.Unlock()
	out := make([]model.CookieAlert, 0, len(s.cookieAlerts))
	for _, alert := range s.cookieAlerts {
		out = append(out, alert)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Platform < out[j].Platform })
	return out
}

func (s *Service) Reconfigure(old, next model.Config) {
	s.mu.Lock()
	for _, r := range s.active {
		if why, _ := policy.Check(next, r.target, r.platform); why != "" {
			r.cancel()
		}
	}
	s.notify()
	s.mu.Unlock()
	s.cookieMu.Lock()
	for id, nextPol := range next.Platforms {
		if old.Platforms[id].Cookie != nextPol.Cookie || strings.TrimSpace(nextPol.Cookie) == "" {
			delete(s.cookieAlerts, id)
		}
	}
	s.cookieMu.Unlock()
	if old.Cache.TTLHours != next.Cache.TTLHours {
		if err := s.Cleanup(false); err != nil {
			slog.Error("缓存期限变更清理失败", "error", err)
		}
	}
}

func (s *Service) HandleEvent(e model.Event, links []string) {
	if len(links) == 0 {
		return
	}
	cfg := s.st.Config()
	maxLinks := cfg.Policy.MaxLinksPerMsg
	if maxLinks <= 0 {
		maxLinks = 3
	}
	seenCanonical := make(map[string]bool, len(links))
	deduped := make([]string, 0, min(len(links), maxLinks))
	for _, raw := range links {
		canon := policy.CleanTrackingURL(netx.NormalizeURL(raw))
		if canon == "" || seenCanonical[canon] {
			continue
		}
		seenCanonical[canon] = true
		deduped = append(deduped, raw)
		if len(deduped) >= maxLinks {
			break
		}
	}
	if len(deduped) == 0 {
		return
	}

	reactor, canReact := s.sender.(EmojiReactor)
	useReaction := canReact && cfg.Policy.EmojiReaction && e.BotID != "" && e.MessageID != ""
	var remaining atomic.Int32
	var reactionSet atomic.Bool

	onTaskDone := func() {
		if remaining.Add(-1) == 0 && reactionSet.Load() {
			go func() {
				rCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
				defer cancel()
				_ = reactor.SetMsgEmojiLike(rCtx, e.BotID, e.MessageID, reactionEmojiEyes, false)
			}()
		}
	}

	accepted := 0
	remaining.Store(1) // sentinel until loop finishes
	for _, raw := range deduped {
		remaining.Add(1)
		if _, err := s.submitWithCallback(s.ctx, e.Target, raw, onTaskDone); err != nil {
			remaining.Add(-1)
			slog.Warn("消息解析未入队", "reason", safeError(err))
		} else {
			accepted++
		}
	}
	if accepted > 0 && useReaction {
		reactionSet.Store(true)
		go func() {
			rCtx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			defer cancel()
			_ = reactor.SetMsgEmojiLike(rCtx, e.BotID, e.MessageID, reactionEmojiEyes, true)
		}()
	}
	onTaskDone()
}

func (s *Service) Submit(ctx context.Context, target model.Target, raw string) (string, error) {
	return s.submitWithCallback(ctx, target, raw, nil)
}

func (s *Service) submitWithCallback(ctx context.Context, target model.Target, raw string, onDone func()) (string, error) {
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
		if !silent && (target.UserID != 0 || target.GroupID != 0) && s.sender != nil {
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
	case s.queue <- work{task: task, target: target, config: cfg, cooldownKey: guard, onDone: onDone}:
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
				if w.onDone != nil {
					w.onDone()
				}
				return
			}
			s.execute(w)
			if w.onDone != nil {
				w.onDone()
			}
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
	var canonicalCooldownKey string
	defer func() {
		s.settleCooldown(w.cooldownKey, w.task.ID, succeeded)
		s.settleCooldown(canonicalCooldownKey, w.task.ID, succeeded)
	}()
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
	key := parserCacheKey(w.task.Platform, w.task.URL, w.config)
	w.task.CacheKey = key
	entry, release, err := s.acquire(ctx, w.config, w.task.URL, key)
	if err == nil {
		defer release()
		w.task.CacheKey = entry.Key
		_ = s.st.PutTask(w.task)
		// P1: Prevent duplicate group spam when different short links resolve to the same canonical work.
		canonURL := policy.CleanTrackingURL(policy.EffectiveSourceURL(entry.Result))
		var dupErr error
		canonicalCooldownKey, dupErr = s.bindCanonicalCooldown(w.target, canonURL, w.cooldownKey, w.task.ID, s.st.Config().Policy.CooldownSeconds)
		if dupErr != nil {
			s.finish(&w.task, "rejected", dupErr.Error())
			return
		}
		err = policy.MediaCheck(s.st.Config(), entry.Result, entry.Files)
	}
	if err == nil {
		if reason, _ := policy.Check(s.st.Config(), w.target, w.task.Platform); reason != "" {
			err = errors.New(reason)
		} else if (w.target.UserID != 0 || w.target.GroupID != 0) && s.sender != nil {
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
		if ctx.Err() == nil && (w.target.UserID != 0 || w.target.GroupID != 0) && s.sender != nil {
			current := s.st.Config()
			if !(w.target.GroupID != 0 && current.Policy.GroupSilentError) {
				if reason, _ := policy.Check(current, w.target, w.task.Platform); reason == "" {
					c, done := context.WithTimeout(ctx, 15*time.Second)
					_ = s.sender.Send(c, w.target, []model.Segment{{Type: "text", Data: map[string]any{"text": "解析或发送失败：" + safeError(err)}}})
					done()
				}
			}
		}
		return
	}
	s.finish(&w.task, "completed", "")
	succeeded = true
}

func parserCacheKey(platform, raw string, cfg model.Config) string {
	input := fmt.Sprintf("%s|%s|quality=%d|bytes=%d|download=%t", platform, raw, cfg.Media.Quality, cfg.Media.MaxBytes, cfg.Media.AutoDownload)
	if platform != "bilibili" && platform != "douyin" {
		input += "|parser=parsehub-2.3.15"
	}
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

func formatDurationBrief(seconds float64) string {
	total := int(seconds + 0.5)
	if total <= 0 {
		return "0:00"
	}
	h := total / 3600
	m := (total % 3600) / 60
	sec := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

func canGracefullyDegrade(cfg model.Config, r *model.ParseResult) bool {
	if cfg.Media.OversizeMode != "link" || r == nil {
		return false
	}
	if strings.TrimSpace(r.Title) == "" && strings.TrimSpace(r.Content) == "" && strings.TrimSpace(r.MarkdownContent) == "" {
		return false
	}
	for _, m := range r.Media {
		if m.Kind == "livephoto" {
			return false
		}
	}
	return true
}

func degradeOverDurationMetadata(cfg model.Config, r *model.ParseResult) bool {
	if !canGracefullyDegrade(cfg, r) {
		return false
	}
	maxDur := 0.0
	for _, m := range r.Media {
		if m.Kind == "video" && m.Duration > float64(cfg.Media.MaxDurationSeconds) && m.Duration > maxDur {
			maxDur = m.Duration
			if r.CoverURL == "" && m.ThumbnailURL != "" {
				r.CoverURL = m.ThumbnailURL
			}
		}
	}
	if maxDur <= 0 {
		return false
	}
	r.DegradedReason = fmt.Sprintf("💡 视频时长（%s）超过直发上限（%s），已略过视频直发，请点击上方链接观看", formatDurationBrief(maxDur), formatDurationBrief(float64(cfg.Media.MaxDurationSeconds)))
	r.Media = nil
	r.Extraction = nil
	return true
}

func isOversizeOrDurationPrepareErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "媒体超过下载上限") ||
		strings.Contains(msg, "媒体大小超过发送限制") ||
		strings.Contains(msg, "处理后的媒体超过大小限制") ||
		strings.Contains(msg, "媒体实际时长超过限制") ||
		strings.Contains(msg, "HLS总时长超过限制")
}

func (s *Service) findCanonicalCacheLocked(platform, canonURL string, cfg model.Config) *model.CacheEntry {
	if canonURL == "" {
		return nil
	}
	entries, err := s.st.ListCache()
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	ttl := time.Duration(cfg.Cache.TTLHours) * time.Hour
	for i := range entries {
		e := &entries[i]
		if e.Platform != platform || s.pending[e.Key] {
			continue
		}
		expires := e.ExpiresAt
		if shorter := e.CreatedAt.Add(ttl); shorter.Before(expires) {
			expires = shorter
		}
		if !now.Before(expires) {
			continue
		}
		if parserCacheKey(e.Platform, e.URL, cfg) != e.Key {
			continue
		}
		if policy.CleanTrackingURL(policy.EffectiveSourceURL(e.Result)) == canonURL && s.filesExist(e) {
			copied := *e
			return &copied
		}
	}
	return nil
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

		resolveFn := s.resolve
		if resolveFn == nil {
			resolveFn = parser.Resolve
		}
		prepareFn := s.prepare
		if prepareFn == nil && s.media != nil {
			prepareFn = s.media.Prepare
		}
		var result *model.ParseResult
		var files []model.Artifact
		var err error
		var canonicalHit *model.CacheEntry

		for attempt := range 2 {
			files = nil
			result, err = resolveFn(ctx, cfg, raw)
			if err != nil {
				break
			}
			s.recordCookieHealth(result.Platform, cfg, result.CookieExpired)

			// P1: Reuse an existing cached work if a different short link or tracking URL resolved to the same canonical work.
			canonURL := policy.CleanTrackingURL(policy.EffectiveSourceURL(*result))
			if canonURL != "" {
				s.cacheMu.Lock()
				if existing := s.findCanonicalCacheLocked(result.Platform, canonURL, cfg); existing != nil && existing.Key != key {
					s.leases[existing.Key]++
					canonicalHit = existing
				}
				s.cacheMu.Unlock()
				if canonicalHit != nil {
					break
				}
			}

			// P4: Gracefully degrade over-duration videos into a rich Message 1 link card instead of failing.
			degradeOverDurationMetadata(cfg, result)

			if err = policy.MediaCheck(cfg, *result, nil); err != nil {
				break
			}
			if !cfg.Media.AutoDownload {
				break
			}
			files, err = prepareFn(ctx, cfg, *result, key)
			if err == nil {
				break
			}
			if canGracefullyDegrade(cfg, result) && result.Type == "video" && isOversizeOrDurationPrepareErr(err) {
				if result.CoverURL == "" && len(result.Media) > 0 {
					result.CoverURL = result.Media[0].ThumbnailURL
				}
				if strings.Contains(err.Error(), "时长") {
					result.DegradedReason = fmt.Sprintf("💡 视频时长超过直发上限（%s），已略过视频直发，请点击上方链接观看", formatDurationBrief(float64(cfg.Media.MaxDurationSeconds)))
				} else {
					result.DegradedReason = "💡 视频体积超过群内直发上限，已略过视频直发，请点击上方链接观看"
				}
				result.Media = nil
				result.Extraction = nil
				_ = s.media.Remove(key)
				if result.CoverURL != "" {
					files, _ = prepareFn(ctx, cfg, *result, key)
				} else {
					files = nil
				}
				err = nil
				break
			}
			if attempt == 0 && result.Extraction == nil && ctx.Err() == nil && media.IsRetryable(err) {
				slog.Warn("媒体处理失败，尝试重新解析并获取新节点", "platform", result.Platform, "reason", safeError(err))
				continue
			}
			break
		}

		if canonicalHit != nil {
			s.cacheMu.Lock()
			s.leases[key]--
			if s.leases[key] <= 0 {
				delete(s.leases, key)
			}
			delete(s.flights, key)
			close(f.done)
			s.cacheMu.Unlock()
			return canonicalHit, func() { s.release(canonicalHit.Key) }, nil
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
		if f.VideoPath != "" {
			if _, err := os.Stat(f.VideoPath); err != nil {
				return false
			}
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

func (s *Service) DeleteCacheEntry(key string) error {
	if len(key) != 64 || key != strings.ToLower(key) {
		return errors.New("无效的缓存键")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return errors.New("无效的缓存键")
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.leases[key] > 0 {
		s.pending[key] = true
		if e, err := s.st.GetCache(key); err == nil && e != nil {
			e.ExpiresAt = time.Now().UTC()
			_ = s.st.PutCache(*e)
		}
		return nil
	}
	return s.removeLocked(key)
}

func (s *Service) Preview(ctx context.Context, raw string) (*model.ParseResult, error) {
	if len(raw) > 8192 || strings.TrimSpace(raw) == "" {
		return nil, errors.New("请输入有效链接")
	}
	raw = netx.NormalizeURL(raw)
	if _, ok := model.Identify(raw); !ok {
		return nil, errors.New("不支持的平台链接")
	}
	previewCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resolveFn := s.resolve
	if resolveFn == nil {
		resolveFn = parser.Resolve
	}
	cfg := s.st.Config()
	res, err := resolveFn(previewCtx, cfg, raw)
	if err != nil {
		return nil, errors.New(safeError(err))
	}
	s.recordCookieHealth(res.Platform, cfg, res.CookieExpired)
	return res, nil
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

type deliverMediaItem struct {
	seq   int
	kind  string // "image" or "video"
	path  string
	thumb string
}

type deliverUploadItem struct {
	seq  int
	path string
}

func forwardNickname(r model.ParseResult) string {
	platformName := r.Platform
	if p, ok := model.PlatformByID(r.Platform); ok && p.Name != "" {
		platformName = p.Name
	}
	author := strings.TrimSpace(r.Author)
	if author == "" {
		return truncate(platformName, 32)
	}
	return truncate(platformName+" · "+author, 40)
}

func (s *Service) buildMediaSegment(e *model.CacheEntry, item deliverMediaItem, target model.Target) model.Segment {
	data := map[string]any{"file": s.mediaLocation(e, item.path, target)}
	if item.kind == "video" && item.thumb != "" {
		data["thumb"] = s.mediaLocation(e, item.thumb, target)
	}
	return model.Segment{Type: item.kind, Data: data}
}

func (s *Service) deliver(ctx context.Context, target model.Target, e *model.CacheEntry) error {
	if err := s.allowed(target, e.Platform); err != nil {
		return err
	}
	cfg := s.st.Config()
	r := e.Result
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.allowed(target, e.Platform); err != nil {
			return err
		}
		return policy.MediaCheck(s.st.Config(), r, e.Files)
	}

	var standaloneCover string
	var mediaItems []deliverMediaItem
	var uploads []deliverUploadItem
	seenImage := make(map[string]bool)
	seenVideo := make(map[string]bool)

	seqCounter := 0
	nextSeq := func() int {
		seqCounter++
		return seqCounter
	}
	addImageItem := func(p string) {
		k := filepath.Clean(p)
		if !seenImage[k] {
			seenImage[k] = true
			mediaItems = append(mediaItems, deliverMediaItem{seq: nextSeq(), kind: "image", path: p})
		}
	}
	addVideoItem := func(p, thumb string) {
		k := filepath.Clean(p)
		if !seenVideo[k] {
			seenVideo[k] = true
			mediaItems = append(mediaItems, deliverMediaItem{seq: nextSeq(), kind: "video", path: p, thumb: thumb})
		}
	}
	addUploadItem := func(p string) {
		k := filepath.Clean(p)
		for _, u := range uploads {
			if filepath.Clean(u.path) == k {
				return
			}
		}
		uploads = append(uploads, deliverUploadItem{seq: nextSeq(), path: p})
	}

	for _, f := range e.Files {
		switch f.Kind {
		case "cover":
			if standaloneCover == "" {
				standaloneCover = f.Path
			}
		case "audio", "file":
			addUploadItem(f.Path)
		case "image", "gif":
			if f.Size > cfg.Media.MaxBytes && cfg.Media.OversizeMode == "file" {
				addUploadItem(f.Path)
			} else {
				addImageItem(f.Path)
			}
		case "livephoto":
			if f.Size > cfg.Media.MaxBytes && cfg.Media.OversizeMode == "file" {
				addUploadItem(f.Path)
			} else {
				addImageItem(f.Path)
			}
			if f.VideoPath != "" {
				if f.VideoSize > cfg.Media.MaxBytes && cfg.Media.OversizeMode == "file" {
					addUploadItem(f.VideoPath)
				} else {
					addVideoItem(f.VideoPath, f.ThumbPath)
				}
			}
		case "video":
			if f.Size > cfg.Media.MaxBytes && cfg.Media.OversizeMode == "file" {
				addUploadItem(f.Path)
			} else {
				addVideoItem(f.Path, f.ThumbPath)
			}
		}
	}

	// Option A: If the post has exactly 1 visual item and it is an image, send it in Message 1
	// and do not repeat the identical image in Message 2.
	showCover := policy.EffectiveShowCover(cfg, e.Platform)
	coverPath := ""
	if len(mediaItems) == 1 && mediaItems[0].kind == "image" {
		coverPath = mediaItems[0].path
		mediaItems = nil
	} else if showCover {
		if standaloneCover != "" {
			coverPath = standaloneCover
		} else {
			for _, f := range e.Files {
				if (f.Kind == "image" || f.Kind == "gif" || f.Kind == "livephoto") && !(f.Size > cfg.Media.MaxBytes && cfg.Media.OversizeMode == "file") {
					coverPath = f.Path
					break
				}
				if f.Kind == "video" && f.ThumbPath != "" && !(f.Size > cfg.Media.MaxBytes && cfg.Media.OversizeMode == "file") {
					coverPath = f.ThumbPath
					break
				}
			}
		}
	}

	// Message 1: [optional group reply] + [cover] + [formatted metadata text]
	var initial []model.Segment
	if coverPath != "" {
		initial = append(initial, model.Segment{Type: "image", Data: map[string]any{"file": s.mediaLocation(e, coverPath, target)}})
	}
	if text := policy.FormatMessage(cfg, r, e.Files); text != "" {
		initial = append(initial, model.Segment{Type: "text", Data: map[string]any{"text": text}})
	} else if len(e.Files) == 0 {
		fallbackURL := policy.DisplayResultURL(cfg, r)
		if fallbackURL == "" {
			fallbackURL = r.SourceURL
		}
		initial = append(initial, model.Segment{Type: "text", Data: map[string]any{"text": fallbackURL}})
	}
	if len(initial) != 0 && cfg.Policy.GroupReply && target.GroupID != 0 && target.MessageID != "" {
		initial = append([]model.Segment{{Type: "reply", Data: map[string]any{"id": target.MessageID}}}, initial...)
	}
	if len(initial) != 0 {
		if err := check(); err != nil {
			return err
		}
		if err := s.sender.Send(ctx, target, initial); err != nil {
			return fmt.Errorf("发送图文消息失败: %w", err)
		}
	}

	videoIdx := 0
	sendDirect := func(item deliverMediaItem) error {
		if err := check(); err != nil {
			return err
		}
		// Re-check runtime MaxBytes in case admin tightened policy mid-flight.
		current := s.st.Config()
		if current.Media.OversizeMode == "file" {
			if st, statErr := os.Stat(item.path); statErr == nil && st.Size() > current.Media.MaxBytes {
				return s.sender.UploadFile(ctx, target, s.mediaLocation(e, item.path, target), filepath.Base(item.path))
			}
		}
		seg := s.buildMediaSegment(e, item, target)
		if item.kind == "video" {
			videoIdx++
		}
		if err := s.sender.Send(ctx, target, []model.Segment{seg}); err != nil {
			if item.kind == "video" {
				return fmt.Errorf("发送视频 %d 失败: %w", videoIdx, err)
			}
			return fmt.Errorf("发送图片失败: %w", err)
		}
		return nil
	}

	sendUpload := func(up deliverUploadItem) error {
		if err := check(); err != nil {
			return err
		}
		location := s.mediaLocation(e, up.path, target)
		return s.sender.UploadFile(ctx, target, location, filepath.Base(up.path))
	}

	// Message 2: Single media direct (preserving sequence with uploads) OR Multi-media merged forward.
	if len(mediaItems) <= 1 || !cfg.Media.ForwardEnabled {
		mIdx, uIdx := 0, 0
		for mIdx < len(mediaItems) || uIdx < len(uploads) {
			if mIdx < len(mediaItems) && (uIdx >= len(uploads) || mediaItems[mIdx].seq < uploads[uIdx].seq) {
				if err := sendDirect(mediaItems[mIdx]); err != nil {
					return err
				}
				mIdx++
			} else {
				if err := sendUpload(uploads[uIdx]); err != nil {
					return err
				}
				uIdx++
			}
		}
		return nil
	}

	if err := check(); err != nil {
		return err
	}
	nickname := forwardNickname(r)
	buildNodes := func(items []deliverMediaItem, includeFullText bool) []model.ForwardNode {
		var nodes []model.ForwardNode
		if includeFullText {
			if fullText, truncated := policy.FullTextForForward(cfg, r); truncated && fullText != "" {
				nodes = append(nodes, model.ForwardNode{
					UserID:   target.BotID,
					Nickname: nickname,
					Content:  []model.Segment{{Type: "text", Data: map[string]any{"text": fullText}}},
				})
			}
		}
		for _, item := range items {
			nodes = append(nodes, model.ForwardNode{
				UserID:   target.BotID,
				Nickname: nickname,
				Content:  []model.Segment{s.buildMediaSegment(e, item, target)},
			})
		}
		return nodes
	}

	forwardErr := s.sender.SendForward(ctx, target, buildNodes(mediaItems, true))
	if forwardErr != nil {
		var apiErr *onebot.APIError
		var imagesOnly, videosOnly []deliverMediaItem
		for _, item := range mediaItems {
			if item.kind == "video" {
				videosOnly = append(videosOnly, item)
			} else {
				imagesOnly = append(imagesOnly, item)
			}
		}
		// Item 2 compatibility fallback: only when OneBot explicitly rejected a mixed image+video
		// forward (never on uncertain socket timeout), split into images forward + standalone videos.
		if errors.As(forwardErr, &apiErr) && len(imagesOnly) > 0 && len(videosOnly) > 0 {
			if len(imagesOnly) >= 2 {
				if err := check(); err != nil {
					return err
				}
				if err := s.sender.SendForward(ctx, target, buildNodes(imagesOnly, true)); err != nil {
					return fmt.Errorf("发送图片合并转发失败: %w", err)
				}
			} else if len(imagesOnly) == 1 && filepath.Clean(imagesOnly[0].path) != filepath.Clean(coverPath) {
				if err := sendDirect(imagesOnly[0]); err != nil {
					return err
				}
			}
			for _, vid := range videosOnly {
				if err := sendDirect(vid); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("发送合并转发媒体失败: %w", forwardErr)
		}
	}

	for _, up := range uploads {
		if err := sendUpload(up); err != nil {
			return err
		}
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
