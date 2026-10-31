package store

import (
	"multiparse/internal/model"
	"testing"
	"time"
)

func TestInterruptedTasksBecomeCancelledWithoutRetentionExtension(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(time.Hour)
	for _, status := range []string{"queued", "running", "completed", "failed"} {
		if err = s.PutTask(model.Task{ID: status, Status: status, CreatedAt: now, UpdatedAt: now, ExpiresAt: end}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CancelInterruptedTasks(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]model.Task{}
	for _, task := range tasks {
		got[task.ID] = task
	}
	for _, id := range []string{"queued", "running"} {
		task := got[id]
		if task.Status != "cancelled" || !task.ExpiresAt.Equal(end) {
			t.Fatalf("bad interruption recovery: %+v", task)
		}
	}
	for _, id := range []string{"completed", "failed"} {
		if got[id].Status != id {
			t.Fatalf("terminal status changed: %+v", got[id])
		}
	}
}
