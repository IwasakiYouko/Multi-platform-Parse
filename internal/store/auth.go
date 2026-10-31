package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/argon2"
)

var ErrCredentials = errors.New("用户名或密码错误")
var ErrSession = errors.New("登录已失效")

type Account struct {
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
	Version            int64  `json:"-"`
}
type Session struct {
	Account
	ExpiresAt time.Time `json:"expires_at"`
}
type Ban struct {
	IP          string    `json:"ip"`
	Failures    int       `json:"failures"`
	WindowStart time.Time `json:"window_start"`
	BannedUntil time.Time `json:"banned_until"`
}

func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func hashPassword(p string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(p), salt, 3, 64*1024, 2, 32)
	return "$argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}
func verifyPassword(hash, p string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=2" {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}
func validateAccount(username, password string) error {
	if len(username) < 3 || len(username) > 64 || strings.TrimSpace(username) != username {
		return errors.New("用户名必须为3–64字符，不能包含首尾空格")
	}
	for _, r := range username {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("用户名不能包含空白或控制字符")
		}
	}
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("密码必须为12–1024字节")
	}
	return nil
}
func (s *Store) bootstrapAdmin() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Reserve the SQLite writer before reconciling the once-only credentials file.
	if _, err = tx.Exec("UPDATE admin SET version=version WHERE id=1"); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM admin").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	username := "admin"
	password, err := RandomToken()
	if err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.dir, ".initial-admin-*")
	if err != nil {
		return fmt.Errorf("write initial admin credentials: %w", err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	_, err = fmt.Fprintf(file, "username: %s\npassword: %s\n首次登录必须修改密码。凭据文件随后自动删除。\n", username, password)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temporary, filepath.Join(s.dir, "initial-admin.txt")); err != nil {
		return err
	}
	directory, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr = directory.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO admin(id,username,password,must_change,version) VALUES(1,?,?,1,1)", username, hash); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	slog.Warn("首次初始化管理员；请立即修改密码并保护包含初始密码的日志", "username", username, "password", password)
	return nil
}
func (s *Store) Authenticate(username, password string) (Account, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.authenticate(username, password)
}
func (s *Store) authenticate(username, password string) (Account, error) {
	var a Account
	var h string
	err := s.db.QueryRow("SELECT username,password,must_change,version FROM admin WHERE id=1").Scan(&a.Username, &h, &a.MustChangePassword, &a.Version)
	if err != nil {
		return a, err
	}
	valid := verifyPassword(h, password)
	uh := sha256.Sum256([]byte(username))
	ah := sha256.Sum256([]byte(a.Username))
	if !valid || subtle.ConstantTimeCompare(uh[:], ah[:]) != 1 {
		return Account{}, ErrCredentials
	}
	return a, nil
}
func (s *Store) NewSession(a Account) (string, Session, error) {
	token, err := RandomToken()
	if err != nil {
		return "", Session{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.authMu.Lock()
	defer s.authMu.Unlock()
	created := time.Now()
	expiry := created.Add(time.Duration(s.cfg.Security.SessionHours) * time.Hour)
	var version int64
	if err = s.db.QueryRow("SELECT version FROM admin WHERE id=1").Scan(&version); err != nil {
		return "", Session{}, err
	}
	if version != a.Version {
		return "", Session{}, ErrSession
	}
	_, err = s.db.Exec("INSERT INTO sessions(hash,version,created_at,expires_at) VALUES(?,?,?,?)", digest(token), version, created.Unix(), expiry.Unix())
	return token, Session{Account: a, ExpiresAt: expiry}, err
}
func (s *Store) Session(token string) (Session, error) {
	if len(token) < 32 || len(token) > 256 {
		return Session{}, ErrSession
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var session Session
	var created, expires int64
	err := s.db.QueryRow("SELECT a.username,a.must_change,a.version,s.created_at,s.expires_at FROM sessions s JOIN admin a ON a.id=1 AND a.version=s.version WHERE s.hash=?", digest(token)).Scan(&session.Username, &session.MustChangePassword, &session.Version, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrSession
	}
	if err != nil {
		return session, err
	}
	session.ExpiresAt = expiration(time.Unix(created, 0), time.Unix(expires, 0), s.cfg.Security.SessionHours)
	if !time.Now().Before(session.ExpiresAt) {
		return Session{}, ErrSession
	}
	return session, nil
}
func (s *Store) Logout(token string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE hash=?", digest(token))
	return err
}
func (s *Store) ChangeAccount(username, current, newPassword string) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	var oldName string
	if err := s.db.QueryRow("SELECT username FROM admin WHERE id=1").Scan(&oldName); err != nil {
		return err
	}
	a, err := s.authenticate(oldName, current)
	if err != nil {
		return err
	}
	if username == "" {
		username = oldName
	}
	if newPassword == "" {
		if a.MustChangePassword {
			return errors.New("首次登录必须设置新密码")
		}
		if username == oldName {
			return errors.New("用户名和密码均未改变")
		}
		newPassword = current
	} else if current == newPassword {
		return errors.New("新密码不能与旧密码相同")
	}
	return s.resetAdmin(username, newPassword, false)
}
func (s *Store) ResetAdmin(username, password string) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	return s.resetAdmin(username, password, true)
}
func (s *Store) resetAdmin(username, password string, mustChange bool) error {
	if err := validateAccount(username, password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE admin SET username=?,password=?,must_change=?,version=version+1 WHERE id=1", username, hash, mustChange); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(s.dir, "initial-admin.txt")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func (s *Store) BanStatus(ip string) (Ban, error) {
	var b Ban
	var start, end int64
	b.IP = ip
	err := s.db.QueryRow("SELECT failures,window_start,banned_until FROM bans WHERE ip=?", ip).Scan(&b.Failures, &start, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return b, nil
	}
	b.WindowStart = time.Unix(start, 0).UTC()
	b.BannedUntil = time.Unix(end, 0).UTC()
	return b, err
}
func (s *Store) RecordFailure(ip string) error {
	cfg := s.Config().Security
	s.authMu.Lock()
	defer s.authMu.Unlock()
	b, err := s.BanStatus(ip)
	if err != nil {
		return err
	}
	now := time.Now()
	if now.Sub(b.WindowStart) > time.Duration(cfg.WindowMinutes)*time.Minute {
		b.Failures = 0
		b.WindowStart = now
	}
	b.Failures++
	if b.Failures >= cfg.Failures {
		b.BannedUntil = now.Add(time.Duration(cfg.BanMinutes) * time.Minute)
	}
	_, err = s.db.Exec("INSERT INTO bans(ip,failures,window_start,banned_until) VALUES(?,?,?,?) ON CONFLICT(ip) DO UPDATE SET failures=excluded.failures,window_start=excluded.window_start,banned_until=excluded.banned_until", ip, b.Failures, b.WindowStart.Unix(), b.BannedUntil.Unix())
	return err
}
func (s *Store) Unban(ip string) error {
	if ip == "" {
		_, err := s.db.Exec("DELETE FROM bans")
		return err
	}
	_, err := s.db.Exec("DELETE FROM bans WHERE ip=?", ip)
	return err
}
func (s *Store) ListBans() ([]Ban, error) {
	rows, err := s.db.Query("SELECT ip,failures,window_start,banned_until FROM bans WHERE banned_until>? ORDER BY banned_until DESC", time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ban{}
	for rows.Next() {
		var b Ban
		var st, ex int64
		if err = rows.Scan(&b.IP, &b.Failures, &st, &ex); err != nil {
			return nil, err
		}
		b.WindowStart = time.Unix(st, 0).UTC()
		b.BannedUntil = time.Unix(ex, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Store) ConsumeCapToken(token string) error {
	_, err := s.db.Exec("INSERT INTO cap_replays(hash,expires_at) VALUES(?,?)", digest(token), time.Now().Add(3*time.Hour).Unix())
	return err
}

// AllowRequest uses persistent bounded fixed windows; storage errors reject requests.
func (s *Store) AllowRequest(bucket string, max int, window time.Duration) (bool, error) {
	bucket = digest(bucket)
	s.authMu.Lock()
	defer s.authMu.Unlock()
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var count int
	var expires int64
	err = tx.QueryRow("SELECT count,expires_at FROM request_limits WHERE bucket=?", bucket).Scan(&count, &expires)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if expires <= now {
		count = 0
		expires = now + int64(window/time.Second)
	}
	if count >= max {
		return false, nil
	}
	_, err = tx.Exec("INSERT INTO request_limits(bucket,count,expires_at) VALUES(?,?,?) ON CONFLICT(bucket) DO UPDATE SET count=excluded.count,expires_at=excluded.expires_at", bucket, count+1, expires)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
