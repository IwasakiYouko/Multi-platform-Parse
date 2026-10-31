package store

import "time"

// Tasks are not replayed after restart: replaying an uncertain send can duplicate
// QQ messages. Preserve their original expiry while making the terminal state true.
func (s *Store) CancelInterruptedTasks(now time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET payload=json_set(payload,'$.status','cancelled','$.message','服务重启，未完成任务已取消','$.updated_at',?) WHERE json_extract(payload,'$.status') IN ('queued','running')`, now.UTC().Format(time.RFC3339Nano))
	return err
}
