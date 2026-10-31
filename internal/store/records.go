package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"multiparse/internal/model"
)

func expiration(created, requested time.Time, hours int) time.Time {
	limit := created.Add(time.Duration(hours) * time.Hour)
	if requested.IsZero() || requested.After(limit) {
		return limit
	}
	return requested
}
func (s *Store) GetCache(key string) (*model.CacheEntry, error) {
	var b []byte
	var created, expiry int64
	err := s.db.QueryRow("SELECT payload,created_at,expires_at FROM cache WHERE cache_key=?", key).Scan(&b, &created, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	end := expiration(time.Unix(created, 0), time.Unix(expiry, 0), s.Config().Cache.TTLHours)
	if !time.Now().Before(end) {
		return nil, nil
	}
	var e model.CacheEntry
	if err = json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	e.CreatedAt = time.Unix(created, 0).UTC()
	e.ExpiresAt = end.UTC()
	return &e, nil
}
func (s *Store) PutCache(e model.CacheEntry) error {
	if e.Key == "" {
		return errors.New("empty cache key")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.ExpiresAt = expiration(e.CreatedAt, e.ExpiresAt, s.cfg.Cache.TTLHours)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO cache(cache_key,created_at,expires_at,payload) VALUES(?,?,?,?) ON CONFLICT(cache_key) DO UPDATE SET created_at=excluded.created_at,expires_at=excluded.expires_at,payload=excluded.payload", e.Key, e.CreatedAt.Unix(), e.ExpiresAt.Unix(), b)
	return err
}
func (s *Store) DeleteCache(key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM tasks WHERE cache_key=?", key); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM cache WHERE cache_key=?", key); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListCache() ([]model.CacheEntry, error) {
	rows, err := s.db.Query("SELECT payload,created_at,expires_at FROM cache ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CacheEntry{}
	for rows.Next() {
		var b []byte
		var cr, ex int64
		if err = rows.Scan(&b, &cr, &ex); err != nil {
			return nil, err
		}
		var e model.CacheEntry
		if err = json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(cr, 0).UTC()
		e.ExpiresAt = time.Unix(ex, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) PutTask(t model.Task) error {
	if t.ID == "" {
		return errors.New("empty task id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	var created int64
	err := s.db.QueryRow("SELECT created_at FROM tasks WHERE id=?", t.ID).Scan(&created)
	if err == nil {
		t.CreatedAt = time.Unix(created, 0).UTC()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	t.ExpiresAt = expiration(t.CreatedAt, t.ExpiresAt, s.cfg.Cache.TTLHours)
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO tasks(id,cache_key,created_at,expires_at,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET cache_key=excluded.cache_key,expires_at=excluded.expires_at,payload=excluded.payload", t.ID, t.CacheKey, t.CreatedAt.Unix(), t.ExpiresAt.Unix(), b)
	return err
}
func (s *Store) ListTasks(limit int) ([]model.Task, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	ttl := int64(time.Duration(s.Config().Cache.TTLHours) * time.Hour / time.Second)
	now := time.Now().Unix()
	rows, err := s.db.Query("SELECT payload,created_at,expires_at FROM tasks WHERE expires_at>? AND created_at+?>? ORDER BY created_at DESC LIMIT ?", now, ttl, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Task{}
	for rows.Next() {
		var b []byte
		var cr, ex int64
		if err = rows.Scan(&b, &cr, &ex); err != nil {
			return nil, err
		}
		var t model.Task
		if err = json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		t.CreatedAt = time.Unix(cr, 0).UTC()
		t.ExpiresAt = time.Unix(ex, 0).UTC()
		out = append(out, t)
	}
	return out, rows.Err()
}

// Cache metadata is removed by the lease-aware file owner via DeleteCache, not here.
func (s *Store) PruneRecords(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{"DELETE FROM tasks WHERE expires_at<=?", "DELETE FROM sessions WHERE expires_at<=?", "DELETE FROM cap_replays WHERE expires_at<=?", "DELETE FROM request_limits WHERE expires_at<=?", "DELETE FROM bans WHERE banned_until<=? AND window_start<=?"} {
		var args []any
		args = append(args, now.Unix())
		if q == "DELETE FROM bans WHERE banned_until<=? AND window_start<=?" {
			args = append(args, now.Add(-24*time.Hour).Unix())
		}
		if _, err = tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
