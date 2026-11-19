package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/policy"
)

type cooldownEntry struct {
	taskID   string
	finished time.Time
}

func cooldownKeyFor(target model.Target, raw string) string {
	if target.GroupID == 0 && target.UserID == 0 {
		return ""
	}
	scope := fmt.Sprintf("%s/group/%d", target.BotID, target.GroupID)
	if target.GroupID == 0 {
		scope = fmt.Sprintf("%s/private/%d", target.BotID, target.UserID)
	}
	normalized := policy.WorkIdentityURL(raw)
	if normalized == "" {
		normalized = raw
	}
	hash := sha256.Sum256([]byte(scope + "|" + normalized))
	return hex.EncodeToString(hash[:])
}

func (s *Service) reserveCooldown(target model.Target, raw, id string, seconds int) (string, error) {
	if seconds <= 0 || (target.GroupID == 0 && target.UserID == 0) {
		return "", nil
	}
	key := cooldownKeyFor(target, raw)
	now := time.Now()
	ttl := time.Duration(seconds) * time.Second
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.cooldowns[key]; ok && (previous.finished.IsZero() || now.Before(previous.finished.Add(ttl))) {
		return "", errors.New("相同内容正在处理或处于会话冷却期")
	}
	if len(s.cooldowns) >= 2048 {
		s.pruneCooldownsLocked(now, ttl)
	}
	if len(s.cooldowns) >= 4096 {
		return "", errors.New("会话限流记录已满，请稍后重试")
	}
	s.cooldowns[key] = cooldownEntry{taskID: id}
	return key, nil
}

// bindCanonicalCooldown checks whether a resolved short-link target was already recently
// delivered in the same session, and binds the canonical key to this task if not.
func (s *Service) bindCanonicalCooldown(target model.Target, canonicalURL, initialKey, id string, seconds int) (string, error) {
	if seconds <= 0 || canonicalURL == "" || (target.GroupID == 0 && target.UserID == 0) {
		return "", nil
	}
	key := cooldownKeyFor(target, canonicalURL)
	if key == "" || key == initialKey {
		return "", nil
	}
	now := time.Now()
	ttl := time.Duration(seconds) * time.Second
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.cooldowns[key]; ok && previous.taskID != id && (previous.finished.IsZero() || now.Before(previous.finished.Add(ttl))) {
		return "", errors.New("相同作品正在处理或处于会话冷却期")
	}
	if len(s.cooldowns) >= 2048 {
		s.pruneCooldownsLocked(now, ttl)
	}
	if len(s.cooldowns) >= 4096 {
		return "", errors.New("会话限流记录已满，请稍后重试")
	}
	s.cooldowns[key] = cooldownEntry{taskID: id}
	return key, nil
}

func (s *Service) settleCooldown(key, id string, success bool) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cooldowns[key]
	if !ok || entry.taskID != id {
		return
	}
	if !success {
		delete(s.cooldowns, key)
		return
	}
	entry.finished = time.Now()
	s.cooldowns[key] = entry
}

func (s *Service) pruneCooldownsLocked(now time.Time, ttl time.Duration) {
	for key, e := range s.cooldowns {
		if !e.finished.IsZero() && !now.Before(e.finished.Add(ttl)) {
			delete(s.cooldowns, key)
		}
	}
}

func (s *Service) pruneCooldowns() {
	ttl := time.Duration(s.st.Config().Policy.CooldownSeconds) * time.Second
	s.mu.Lock()
	s.pruneCooldownsLocked(time.Now(), ttl)
	s.mu.Unlock()
}
