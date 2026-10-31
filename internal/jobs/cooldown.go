package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"multiparse/internal/model"
)

type cooldownEntry struct {
	taskID   string
	finished time.Time
}

func (s *Service) reserveCooldown(target model.Target, raw, id string, seconds int) (string, error) {
	if seconds <= 0 || (target.GroupID == 0 && target.UserID == 0) {
		return "", nil
	}
	scope := fmt.Sprintf("%s/group/%d", target.BotID, target.GroupID)
	if target.GroupID == 0 {
		scope = fmt.Sprintf("%s/private/%d", target.BotID, target.UserID)
	}
	hash := sha256.Sum256([]byte(scope + "|" + raw))
	key := hex.EncodeToString(hash[:])
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
