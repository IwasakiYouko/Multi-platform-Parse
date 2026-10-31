package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"multiparse/internal/model"
)

type Store struct {
	db     *sql.DB
	dir    string
	aead   cipher.AEAD
	mu     sync.RWMutex
	cfg    model.Config
	authMu sync.Mutex
}

func Open(dataDir string) (*Store, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "encryption.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(key)
		if err == nil {
			err = f.Sync()
		}
		ce := f.Close()
		if err == nil {
			err = ce
		}
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid encryption.key length")
	}
	if err = os.Chmod(keyPath, 0600); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, dir: dir, aead: aead}
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS settings(id INTEGER PRIMARY KEY CHECK(id=1), payload BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS admin(id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL, password TEXT NOT NULL, must_change INTEGER NOT NULL, version INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY, version INTEGER NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS bans(ip TEXT PRIMARY KEY, failures INTEGER NOT NULL, window_start INTEGER NOT NULL, banned_until INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS cap_replays(hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS cache(cache_key TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, payload BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY, cache_key TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, payload BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS tasks_cache_key ON tasks(cache_key);
 CREATE INDEX IF NOT EXISTS tasks_created ON tasks(created_at);
 CREATE TABLE IF NOT EXISTS request_limits(bucket TEXT PRIMARY KEY, count INTEGER NOT NULL, expires_at INTEGER NOT NULL);`)
	if err != nil {
		return fail(err)
	}
	if err = os.Chmod(filepath.Join(dir, "state.db"), 0600); err != nil {
		return fail(err)
	}
	var encrypted []byte
	err = db.QueryRow("SELECT payload FROM settings WHERE id=1").Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		s.cfg = model.DefaultConfig()
		if err = s.SaveConfig(s.cfg); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	} else {
		plain, e := s.decrypt(encrypted)
		if e != nil {
			return fail(fmt.Errorf("decrypt configuration: %w", e))
		}
		if e = json.Unmarshal(plain, &s.cfg); e != nil {
			return fail(e)
		}
	}
	if err = s.bootstrapAdmin(); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Store) encrypt(b []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, b, []byte("multiparse-config-v1")), nil
}
func (s *Store) decrypt(b []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(b) < n {
		return nil, errors.New("encrypted configuration truncated")
	}
	return s.aead.Open(nil, b[:n], b[n:], []byte("multiparse-config-v1"))
}
func (s *Store) Close() error    { return s.db.Close() }
func (s *Store) DB() *sql.DB     { return s.db }
func (s *Store) DataDir() string { return s.dir }
func clone(c model.Config) model.Config {
	b, _ := json.Marshal(c)
	var d model.Config
	_ = json.Unmarshal(b, &d)
	return d
}
func (s *Store) Config() model.Config { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.cfg) }
func ValidateConfig(c model.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if net.ParseIP(c.Server.Listen) == nil && c.Server.Listen != "localhost" {
		return errors.New("监听地址必须为IP或localhost")
	}
	for _, list := range [][]string{c.Server.TrustedProxies, c.Server.AdminNetworks} {
		for _, v := range list {
			if _, err := netip.ParsePrefix(v); err != nil {
				if _, e := netip.ParseAddr(v); e != nil {
					return fmt.Errorf("无效IP或CIDR：%s", v)
				}
			}
		}
	}
	if c.Server.ExternalURL != "" {
		u, e := url.Parse(c.Server.ExternalURL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
			return errors.New("外部URL必须为HTTP(S)源地址")
		}
	}
	u, e := url.Parse(c.Security.CapURL)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return errors.New("Cap地址必须为本机HTTP源地址")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return errors.New("Cap必须仅通过loopback IP访问")
	}
	if _, _, e = net.SplitHostPort(u.Host); e != nil {
		return errors.New("Cap地址必须包含端口")
	}
	if len(c.Security.CapSiteKey) > 128 {
		return errors.New("Cap站点标识过长")
	}
	for _, r := range c.Security.CapSiteKey {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return errors.New("Cap站点标识包含无效字符")
		}
	}
	if (c.Server.CertFile == "") != (c.Server.KeyFile == "") {
		return errors.New("证书和私钥必须成对配置")
	}
	if c.Server.TLS && (c.Server.CertFile == "" || c.Server.KeyFile == "") {
		return errors.New("TLS需要证书和私钥")
	}
	if c.Server.CertFile != "" {
		if _, e = tls.LoadX509KeyPair(c.Server.CertFile, c.Server.KeyFile); e != nil {
			return fmt.Errorf("证书和私钥不可读取或不匹配：%w", e)
		}
	}
	return nil
}
func (s *Store) SaveConfig(c model.Config) error {
	if err := ValidateConfig(c); err != nil {
		return err
	}
	c = clone(c)
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	enc, err := s.encrypt(plain)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", enc); err != nil {
		return err
	}
	ttl := int64(time.Duration(c.Cache.TTLHours) * time.Hour / time.Second)
	if s.cfg.Cache.TTLHours != c.Cache.TTLHours {
		oldTTL := int64(time.Duration(s.cfg.Cache.TTLHours) * time.Hour / time.Second)
		now := time.Now().Unix()
		for _, table := range []string{"cache", "tasks"} {
			if _, err = tx.Exec("UPDATE "+table+" SET expires_at=CASE WHEN expires_at<created_at+? THEN MIN(expires_at,created_at+?) ELSE created_at+? END WHERE expires_at>? AND created_at+?>?", oldTTL, ttl, ttl, now, oldTTL, now); err != nil {
				return err
			}
		}
	}
	if c.Security.SessionHours < s.cfg.Security.SessionHours {
		if _, err = tx.Exec("UPDATE sessions SET expires_at=MIN(expires_at,created_at+?)", int64(time.Duration(c.Security.SessionHours)*time.Hour/time.Second)); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.cfg = c
	return nil
}
