package jobs

import (
	"multiparse/internal/model"
	"testing"
	"time"
)

func TestCooldownIsConversationScopedAndFailuresCanRetry(t *testing.T) {
	s := &Service{cooldowns: map[string]cooldownEntry{}}
	group := model.Target{BotID: "1", GroupID: 10, UserID: 100}
	key, err := s.reserveCooldown(group, "https://bgm.tv/blog/1", "a", 30)
	if err != nil {
		t.Fatal(err)
	}
	otherUser := group
	otherUser.UserID = 101
	if _, err = s.reserveCooldown(otherUser, "https://bgm.tv/blog/1", "duplicate", 30); err == nil {
		t.Fatal("same-group concurrent duplicate accepted")
	}
	otherGroup := group
	otherGroup.GroupID = 11
	if _, err = s.reserveCooldown(otherGroup, "https://bgm.tv/blog/1", "other", 30); err != nil {
		t.Fatalf("one group blocked another: %v", err)
	}
	s.settleCooldown(key, "a", false)
	key, err = s.reserveCooldown(group, "https://bgm.tv/blog/1", "retry", 30)
	if err != nil {
		t.Fatalf("failed request kept cooldown: %v", err)
	}
	s.settleCooldown(key, "retry", true)
	if _, err = s.reserveCooldown(group, "https://bgm.tv/blog/1", "too-soon", 30); err == nil {
		t.Fatal("successful send did not start cooldown")
	}
	s.cooldowns[key] = cooldownEntry{taskID: "retry", finished: time.Now().Add(-31 * time.Second)}
	if _, err = s.reserveCooldown(group, "https://bgm.tv/blog/1", "later", 30); err != nil {
		t.Fatalf("expired cooldown blocked request: %v", err)
	}
	s.settleCooldown(key, "retry", false)
	if _, ok := s.cooldowns[key]; !ok {
		t.Fatal("old task completion removed newer reservation")
	}
}
