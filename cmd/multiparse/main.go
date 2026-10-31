package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"multiparse/internal/jobs"
	"multiparse/internal/media"
	"multiparse/internal/model"
	"multiparse/internal/netx"
	"multiparse/internal/onebot"
	appruntime "multiparse/internal/runtime"
	"multiparse/internal/store"
	"multiparse/internal/ui"
	"multiparse/internal/web"
)

const version = "1.1.1"

func main() {
	if err := run(); err != nil {
		slog.Error("操作失败", "error", err)
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("multiparse "+command, flag.ContinueOnError)
	data := fs.String("data", env("DATA_DIR", "./data"), "管理数据目录")
	cache := fs.String("cache", env("CACHE_DIR", "./media"), "媒体缓存目录（需让NapCat可读取）")
	username := fs.String("username", "admin", "重置管理员用户名")
	ip := fs.String("ip", "", "要解除封禁的IP")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	if command == "health" {
		return health(*data)
	}
	if command == "cap-run" {
		return capRun()
	}
	absData, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	absCache, err := filepath.Abs(*cache)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(absData, 0700); err != nil {
		return err
	}
	if command == "serve" {
		unlock, e := lockDataDir(absData)
		if e != nil {
			return e
		}
		defer unlock()
	}
	st, err := store.Open(absData)
	if err != nil {
		return err
	}
	defer st.Close()
	if err = bootstrap(st, absData); err != nil {
		return err
	}
	keyFile := env("CAP_ADMIN_KEY_FILE", filepath.Join(absData, "cap-admin.key"))
	if err = ensureSecret(keyFile); err != nil {
		return err
	}
	switch command {
	case "init":
		fmt.Printf("初始化完成。初始管理员凭据：%s\nCap管理密钥：%s\n", filepath.Join(absData, "initial-admin.txt"), keyFile)
		return nil
	case "admin-reset":
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 1025))
		if e != nil {
			return e
		}
		password := strings.TrimSpace(string(b))
		if len(password) < 12 || len(password) > 1024 {
			return errors.New("通过标准输入提供12–1024字符的新密码")
		}
		if e = st.ResetAdmin(*username, password); e != nil {
			return e
		}
		fmt.Println("管理员已重置，旧会话已撤销")
		return nil
	case "unban":
		if net.ParseIP(*ip) == nil {
			return errors.New("必须通过--ip指定有效IP")
		}
		if e := st.Unban(*ip); e != nil {
			return e
		}
		fmt.Println("已解除封禁")
		return nil
	case "serve":
	default:
		return errors.New("命令仅支持serve、init、health、admin-reset、unban、version")
	}
	manager, err := media.New(absCache)
	if err != nil {
		return err
	}
	var service *jobs.Service
	hub := onebot.New(st.Config, func(event model.Event) {
		if service != nil {
			service.HandleEvent(event, onebot.Extract(event))
		}
	})
	defer hub.Close()
	service = jobs.New(st, manager, hub)
	defer service.Close()
	var runtime *appruntime.Server
	started := time.Now()
	hooks := web.Hooks{
		CapAdminKeyFile: keyFile,
		Status: func() any {
			state := runtime.State()
			c := st.Config()
			base := state.URL
			if c.Server.ExternalURL != "" {
				base = strings.TrimRight(c.Server.ExternalURL, "/")
			}
			ws := strings.TrimRight(base, "/") + "/onebot/v11/ws"
			if strings.HasPrefix(ws, "https:") {
				ws = "wss:" + strings.TrimPrefix(ws, "https:")
			} else if strings.HasPrefix(ws, "http:") {
				ws = "ws:" + strings.TrimPrefix(ws, "http:")
			}
			entries, e := st.ListCache()
			if e != nil {
				entries = nil
			}
			bytes, e := manager.Usage()
			if e != nil {
				bytes = 0
			}
			active, queued := service.Counts()
			pending, cleanupError := service.CleanupStatus()
			return map[string]any{"version": version, "uptime_seconds": int64(time.Since(started).Seconds()), "listen": state.Listen, "url": state.URL, "onebot_ws_url": ws, "bots": hub.Status(), "cache_entries": len(entries), "cache_bytes": bytes, "active_tasks": active, "queue_size": queued, "pending_cleanup": pending, "cleanup_error": cleanupError, "cap_admin_key_file": keyFile, "data_dir": absData, "cache_dir": absCache}
		},
		PrepareConfig: func(old, new model.Config) (func(), func(), error) { return runtime.Prepare(old.Server, new.Server) },
		ConfigChanged: func(old, new model.Config) {
			if old.Server.Listen != new.Server.Listen || old.Server.Port != new.Server.Port || old.Server.TLS != new.Server.TLS {
				revoked := old
				revoked.OneBot.Token = "revoked:" + old.OneBot.Token
				hub.Reconfigure(revoked, new)
			} else {
				hub.Reconfigure(old, new)
			}
			service.Reconfigure(old, new)
		},
		ClearCache: service.Cleanup,
		TestProxy:  func(ctx context.Context, platform string) error { return netx.ProxyTest(ctx, st.Config(), platform) },
		Submit:     func(ctx context.Context, raw string) (string, error) { return service.Submit(ctx, model.Target{}, raw) },
	}
	admin, err := web.New(st, hooks)
	if err != nil {
		return err
	}
	defer admin.Close()
	mux := http.NewServeMux()
	mux.Handle("/api/", admin.Handler())
	mux.Handle("/captcha/", admin.Handler())
	mux.Handle("/healthz", admin.Handler())
	mux.Handle("/onebot/v11/ws", hub.Handler())
	mux.Handle("/media/", service.MediaHandler())
	mux.Handle("/", ui.Handler())
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := st.Config()
		if c.Server.TLS && r.TLS == nil {
			http.Error(w, "请使用HTTPS/WSS连接", http.StatusUpgradeRequired)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path == "/healthz" {
			w.Header().Set("X-Multiparse-Instance", runtime.State().Instance)
		}
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		mux.ServeHTTP(w, r)
	})
	runtime = appruntime.New(absData, handler)
	if err = runtime.Start(st.Config().Server); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var capWG sync.WaitGroup
	if st.Config().Security.CapDisabled {
		slog.Warn("Cap验证码已关闭；密码登录与失败封禁仍然生效，请限制管理入口")
	} else {
		capWG.Add(1)
		go func() {
			defer capWG.Done()
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			warned := false
			for {
				if st.Config().Security.CapDisabled {
					return
				}
				key, e := os.ReadFile(keyFile)
				if e == nil {
					attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
					e = admin.ProvisionCap(attempt, strings.TrimSpace(string(key)))
					cancel()
				}
				if e == nil {
					if cfg := st.Config(); !cfg.Security.CapDisabled {
						slog.Info("Cap已就绪", "level", cfg.Security.CapLevel)
					}
					return
				}
				if !warned {
					slog.Warn("Cap尚未就绪，登录保持关闭；检查Cap与Valkey服务以及密钥挂载")
					warned = true
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	<-ctx.Done()
	capWG.Wait()
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return runtime.Close(shutdown)
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func ensureSecret(path string) error {
	if b, e := os.ReadFile(path); e == nil {
		if len(strings.TrimSpace(string(b))) < 32 {
			return errors.New("Cap管理密钥文件内容无效")
		}
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(e, os.ErrExist) {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = f.WriteString(hex.EncodeToString(b) + "\n")
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func bootstrap(st *store.Store, data string) error {
	marker := filepath.Join(data, "bootstrap.done")
	if _, e := os.Stat(marker); e == nil {
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	c := st.Config()
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		c.Server.Listen = v
	}
	if v := os.Getenv("START_PORT"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil {
			return e
		}
		c.Server.Port = n
	}
	if v := os.Getenv("EXTERNAL_URL"); v != "" {
		c.Server.ExternalURL = v
	}
	if v := os.Getenv("CAP_ENABLED"); v != "" {
		enabled, e := strconv.ParseBool(v)
		if e != nil {
			return fmt.Errorf("CAP_ENABLED必须为有效布尔值：%w", e)
		}
		c.Security.CapDisabled = !enabled
	}
	if v := os.Getenv("CAP_URL"); v != "" {
		c.Security.CapURL = v
	}
	if v := os.Getenv("CAP_SITE_KEY"); v != "" {
		c.Security.CapSiteKey = v
	}
	if v := os.Getenv("CAP_SECRET"); v != "" {
		c.Security.CapSecret = v
	}
	if e := c.Validate(); e != nil {
		return e
	}
	if e := st.SaveConfig(c); e != nil {
		return e
	}
	return os.WriteFile(marker, []byte("1\n"), 0600)
}
func health(data string) error {
	b, e := os.ReadFile(filepath.Join(data, "runtime.json"))
	if e != nil {
		return e
	}
	var state appruntime.State
	if e = json.Unmarshal(b, &state); e != nil {
		return e
	}
	u, e := url.Parse(state.URL)
	if e != nil {
		return e
	}
	if u.Hostname() == "" || state.Instance == "" {
		return errors.New("运行状态无效")
	}
	tr := &http.Transport{Proxy: nil}
	if state.TLS {
		// Trust the configured certificate rather than disabling certificate checks.
		st, e := store.Open(data)
		if e != nil {
			return e
		}
		c := st.Config()
		st.Close()
		pem, e := os.ReadFile(c.Server.CertFile)
		if e != nil {
			return e
		}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("无法加载健康检查TLS信任证书")
		}
		serverName := u.Hostname()
		pair, certErr := tls.LoadX509KeyPair(c.Server.CertFile, c.Server.KeyFile)
		if certErr != nil {
			return certErr
		}
		leaf, certErr := x509.ParseCertificate(pair.Certificate[0])
		if certErr != nil {
			return certErr
		}
		if len(leaf.DNSNames) > 0 {
			serverName = leaf.DNSNames[0]
		} else if len(leaf.IPAddresses) > 0 {
			serverName = leaf.IPAddresses[0].String()
		}
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	defer tr.CloseIdleConnections()
	response, e := client.Get(strings.TrimRight(state.URL, "/") + "/healthz")
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查HTTP %d", response.StatusCode)
	}
	if response.Header.Get("X-Multiparse-Instance") != state.Instance {
		return errors.New("运行端口不属于当前实例")
	}
	fmt.Println("healthy")
	return nil
}
