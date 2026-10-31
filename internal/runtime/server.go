package runtime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"multiparse/internal/model"
)

type State struct {
	PID       int       `json:"pid"`
	Instance  string    `json:"instance"`
	Listen    string    `json:"listen"`
	URL       string    `json:"url"`
	TLS       bool      `json:"tls"`
	StartedAt time.Time `json:"started_at"`
}
type Server struct {
	mu       sync.Mutex
	handler  http.Handler
	active   *http.Server
	listener net.Listener
	tls      atomic.Pointer[tls.Config]
	state    State
	dataDir  string
}
type dynamicTLSListener struct {
	net.Listener
	config *atomic.Pointer[tls.Config]
}

func (l dynamicTLSListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	if cfg := l.config.Load(); cfg != nil {
		return tls.Server(c, cfg), nil
	}
	return c, nil
}
func New(dataDir string, handler http.Handler) *Server {
	return &Server{dataDir: dataDir, handler: handler, state: State{PID: os.Getpid(), Instance: model.NewID(), StartedAt: time.Now().UTC()}}
}
func loadTLS(c model.ServerConfig) (*tls.Config, error) {
	if !c.TLS {
		return nil, nil
	}
	cert, e := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if e != nil {
		return nil, fmt.Errorf("TLS证书加载失败：%w", e)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}, nil
}
func listen(c model.ServerConfig) (net.Listener, error) {
	if c.Port < 1 || c.Port > 65535 {
		return nil, errors.New("端口超出范围")
	}
	for p := c.Port; p <= 65535; p++ {
		l, e := net.Listen("tcp", net.JoinHostPort(c.Listen, strconv.Itoa(p)))
		if e == nil {
			return l, nil
		}
		if !errors.Is(e, syscall.EADDRINUSE) {
			return nil, e
		}
	}
	return nil, errors.New("从起始端口到65535均不可用")
}
func (s *Server) Start(c model.ServerConfig) error { return s.Apply(model.ServerConfig{}, c) }
func (s *Server) Apply(old, new model.ServerConfig) error {
	apply, discard, err := s.Prepare(old, new)
	if err != nil {
		return err
	}
	defer discard()
	apply()
	return nil
}

// Prepare reserves sockets and parses certificates before settings are published.
// Call exactly one of apply/discard; both are idempotent and release the lock.
func (s *Server) Prepare(old, new model.ServerConfig) (apply func(), discard func(), err error) {
	cfg, err := loadTLS(new)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	rebind := s.listener == nil || old.Listen != new.Listen || old.Port != new.Port
	var l net.Listener
	if rebind {
		l, err = listen(new)
		if err != nil {
			s.mu.Unlock()
			return nil, nil, fmt.Errorf("监听失败，原入口保持不变：%w", err)
		}
	}
	nextState := s.state
	nextState.TLS = new.TLS
	if rebind {
		nextState.Listen = l.Addr().String()
	}
	host, port, _ := net.SplitHostPort(nextState.Listen)
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if new.TLS {
		scheme = "https"
	}
	nextState.URL = scheme + "://" + net.JoinHostPort(host, port)
	payload, err := json.MarshalIndent(nextState, "", "  ")
	temp := filepath.Join(s.dataDir, "runtime.json.pending")
	if err == nil {
		err = os.WriteFile(temp, payload, 0600)
	}
	if err != nil {
		if l != nil {
			_ = l.Close()
		}
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("运行状态准备失败：%w", err)
	}
	var once sync.Once
	discard = func() {
		once.Do(func() {
			if l != nil {
				_ = l.Close()
			}
			_ = os.Remove(temp)
			s.mu.Unlock()
		})
	}
	apply = func() {
		once.Do(func() {
			previous := s.active
			s.tls.Store(cfg)
			s.state = nextState
			if e := os.Rename(temp, filepath.Join(s.dataDir, "runtime.json")); e != nil {
				slog.Error("运行状态持久化失败，服务已使用新地址", "address", nextState.URL, "error", e)
			}
			if rebind {
				server := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
				s.listener = l
				s.active = server
				go func() {
					if e := server.Serve(dynamicTLSListener{Listener: l, config: &s.tls}); e != nil && !errors.Is(e, http.ErrServerClosed) {
						slog.Error("HTTP监听异常退出", "error", e)
					}
				}()
			} else if s.active != nil && old.TLS != new.TLS {
				s.active.SetKeepAlivesEnabled(false)
				s.active.SetKeepAlivesEnabled(true)
			}
			s.mu.Unlock()
			if rebind && previous != nil {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					if e := previous.Shutdown(ctx); e != nil {
						_ = previous.Close()
					}
				}()
			}
			slog.Info("服务配置已生效", "address", nextState.URL, "onebot", nextState.URL+"/onebot/v11/ws")
		})
	}
	return apply, discard, nil
}
func (s *Server) State() State { s.mu.Lock(); defer s.mu.Unlock(); return s.state }
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	server := s.active
	s.active = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	e := server.Shutdown(ctx)
	if e != nil {
		_ = server.Close()
	}
	_ = os.Remove(filepath.Join(s.dataDir, "runtime.json"))
	return e
}
