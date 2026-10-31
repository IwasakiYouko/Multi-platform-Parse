package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"

	"multiparse/internal/model"
	"multiparse/internal/store"
)

const secretSentinel = "********"

func redacted(c model.Config) model.Config {
	c.OneBot.Token = secretSentinel
	if c.Security.CapSecret != "" {
		c.Security.CapSecret = secretSentinel
	}
	if u, e := url.Parse(c.Proxy.URL); e == nil && u.User != nil {
		c.Proxy.URL = secretSentinel
	}
	for id, p := range c.Platforms {
		if p.Cookie != "" {
			p.Cookie = secretSentinel
			c.Platforms[id] = p
		}
	}
	return c
}
func restoreSecrets(c *model.Config, old model.Config) {
	if c.OneBot.Token == secretSentinel {
		c.OneBot.Token = old.OneBot.Token
	}
	if c.Security.CapSecret == secretSentinel {
		c.Security.CapSecret = old.Security.CapSecret
	}
	if c.Proxy.URL == secretSentinel {
		c.Proxy.URL = old.Proxy.URL
	}
	for id, p := range c.Platforms {
		if p.Cookie == secretSentinel {
			p.Cookie = old.Platforms[id].Cookie
			c.Platforms[id] = p
		}
	}
}
func sensitive(old, c model.Config) bool {
	if !reflect.DeepEqual(old.Server, c.Server) || old.Security != c.Security || !reflect.DeepEqual(old.OneBot, c.OneBot) || old.Proxy != c.Proxy {
		return true
	}
	for id, p := range c.Platforms {
		if old.Platforms[id].Cookie != p.Cookie {
			return true
		}
	}
	return false
}
func (s *Server) applyConfig(r *http.Request, c model.Config) error {
	old := s.st.Config()
	if err := store.ValidateConfig(c); err != nil {
		return err
	}
	var apply, discard func()
	if s.hooks.PrepareConfig != nil {
		var err error
		apply, discard, err = s.hooks.PrepareConfig(old, c)
		if err != nil {
			return fmt.Errorf("配置预检查失败，原配置未变：%w", err)
		}
	}
	committed := false
	defer func() {
		if !committed && discard != nil {
			discard()
		}
	}()
	enablingCap := old.Security.CapDisabled && !c.Security.CapDisabled
	capChange := !c.Security.CapDisabled && (enablingCap || old.Security.CapLevel != c.Security.CapLevel || old.Security.CapSiteKey != c.Security.CapSiteKey || old.Security.CapSecret != c.Security.CapSecret || old.Security.CapURL != c.Security.CapURL)
	s.cap.mu.RLock()
	auth, wasReady := s.cap.auth, s.cap.ready
	s.cap.mu.RUnlock()
	oldAuth := auth
	rollbackCap := func(cause error) error {
		if capChange && !old.Security.CapDisabled {
			if err := s.restoreCap(old, oldAuth, wasReady); err != nil {
				return fmt.Errorf("%w；Cap恢复验证失败，登录保持关闭：%v", cause, err)
			}
		} else if enablingCap {
			s.markCapReady(old, oldAuth, false)
		}
		return cause
	}
	if enablingCap {
		key, err := os.ReadFile(s.hooks.CapAdminKeyFile)
		if err != nil {
			return errors.New("无法读取本机CAP_ADMIN_KEY_FILE，Cap保持关闭")
		}
		c, auth, err = s.capCredentials(r.Context(), c, strings.TrimSpace(string(key)))
		if err != nil {
			return err
		}
	}
	if capChange {
		if auth == "" {
			return errors.New("Cap管理会话不可用，请使用本机CAP_ADMIN_KEY_FILE重新启动初始化")
		}
		s.cap.mu.Lock()
		s.cap.ready = false
		s.cap.mu.Unlock()
		if err := s.configureCap(r.Context(), c, auth); err != nil {
			return rollbackCap(err)
		}
		if enablingCap && !s.capHealthy(r.Context(), c) {
			return rollbackCap(errors.New("Cap服务健康检查失败，Cap保持关闭"))
		}
	}
	if err := s.st.SaveConfig(c); err != nil {
		return rollbackCap(err)
	}
	if apply != nil {
		apply()
	}
	committed = true
	if c.Security.CapDisabled {
		s.markCapReady(c, auth, false)
	} else if capChange {
		s.markCapReady(c, auth, true)
	}
	if s.hooks.ConfigChanged != nil {
		s.hooks.ConfigChanged(old, c)
	}
	return nil
}
func (s *Server) api(w http.ResponseWriter, r *http.Request, a store.Session, token string) {
	method := r.Method
	path := r.URL.Path
	switch {
	case path == "/api/me" && method == "GET":
		reply(w, 200, map[string]any{"username": a.Username, "must_change_password": a.MustChangePassword, "csrf_token": csrf(token)})
		return
	case path == "/api/logout" && method == "POST":
		if err := s.st.Logout(token); err != nil {
			failure(w, 500, "退出失败")
			return
		}
		clearCookie(w, r, s.st.Config())
		reply(w, 200, map[string]bool{"ok": true})
		return
	case path == "/api/account" && method == "POST":
		var b struct {
			Username string `json:"username"`
			Current  string `json:"current_password"`
			New      string `json:"new_password"`
		}
		if err := decode(w, r, &b); err != nil {
			failure(w, 400, err.Error())
			return
		}
		if len(b.Current) > 1024 || len(b.New) > 1024 {
			failure(w, 400, "密码过长")
			return
		}
		if !s.passwordSlot(w) {
			return
		}
		defer func() { <-s.passwordSlots }()
		if err := s.st.ChangeAccount(b.Username, b.Current, b.New); err != nil {
			if errors.Is(err, store.ErrCredentials) {
				ip, e := clientIP(r, s.st.Config())
				if e != nil || s.st.RecordFailure(ip.String()) != nil {
					failure(w, 503, "安全存储不可用")
					return
				}
			}
			failure(w, 400, err.Error())
			return
		}
		clearCookie(w, r, s.st.Config())
		reply(w, 200, map[string]bool{"ok": true})
		return
	case path == "/api/config" && method == "GET":
		reply(w, 200, redacted(s.st.Config()))
		return
	case path == "/api/config" && method == "PUT":
		var c model.Config
		if err := decode(w, r, &c); err != nil {
			failure(w, 400, err.Error())
			return
		}
		s.changeMu.Lock()
		defer s.changeMu.Unlock()
		old := s.st.Config()
		restoreSecrets(&c, old)
		if sensitive(old, c) && !s.reauth(w, r, a, "") {
			return
		}
		if err := s.applyConfig(r, c); err != nil {
			failure(w, 409, err.Error())
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	case path == "/api/onebot/token" && method == "POST":
		var b struct {
			Password string `json:"current_password"`
		}
		if err := decode(w, r, &b); err != nil {
			failure(w, 400, err.Error())
			return
		}
		if !s.reauth(w, r, a, b.Password) {
			return
		}
		generated, err := store.RandomToken()
		if err != nil {
			failure(w, 500, "安全随机数不可用")
			return
		}
		s.changeMu.Lock()
		defer s.changeMu.Unlock()
		c := s.st.Config()
		c.OneBot.Token = generated
		if err = s.applyConfig(r, c); err != nil {
			failure(w, 409, err.Error())
			return
		}
		reply(w, 200, map[string]string{"token": generated})
		return
	case path == "/api/status" && method == "GET":
		if s.hooks.Status == nil {
			failure(w, 503, "运行状态不可用")
			return
		}
		reply(w, 200, s.hooks.Status())
		return
	case path == "/api/tasks" && method == "GET":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		tasks, err := s.st.ListTasks(limit)
		if err != nil {
			failure(w, 500, "任务记录读取失败")
			return
		}
		reply(w, 200, tasks)
		return
	case path == "/api/cache" && method == "GET":
		entries, err := s.st.ListCache()
		if err != nil {
			failure(w, 500, "缓存记录读取失败")
			return
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			var size int64
			for _, f := range e.Files {
				size += f.Size
			}
			out = append(out, map[string]any{"key": e.Key, "platform": e.Platform, "url": e.URL, "title": e.Result.Title, "type": e.Result.Type, "size": size, "bytes": size, "file_count": len(e.Files), "created_at": e.CreatedAt, "expires_at": e.ExpiresAt})
		}
		reply(w, 200, out)
		return
	case path == "/api/cache/clear" && method == "POST":
		var b struct {
			All bool `json:"all"`
		}
		if err := decode(w, r, &b); err != nil {
			failure(w, 400, err.Error())
			return
		}
		if s.hooks.ClearCache == nil {
			failure(w, 503, "缓存服务不可用")
			return
		}
		if err := s.hooks.ClearCache(b.All); err != nil {
			failure(w, 409, err.Error())
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	case path == "/api/bans" && method == "GET":
		bans, err := s.st.ListBans()
		if err != nil {
			failure(w, 500, "封禁记录读取失败")
			return
		}
		reply(w, 200, bans)
		return
	case path == "/api/bans" && method == "DELETE":
		ip := r.URL.Query().Get("ip")
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			failure(w, 400, "必须指定有效IP")
			return
		}
		if err = s.st.Unban(addr.Unmap().String()); err != nil {
			failure(w, 500, "解除封禁失败")
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	case path == "/api/proxy/test" && method == "POST":
		var b struct {
			Platform string `json:"platform"`
		}
		if err := decode(w, r, &b); err != nil {
			failure(w, 400, err.Error())
			return
		}
		if _, ok := model.PlatformByID(b.Platform); !ok {
			failure(w, 400, "未知平台")
			return
		}
		if s.hooks.TestProxy == nil {
			failure(w, 503, "代理测试不可用")
			return
		}
		if err := s.hooks.TestProxy(r.Context(), b.Platform); err != nil {
			failure(w, 502, err.Error())
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	case path == "/api/parse" && method == "POST":
		var b struct {
			URL string `json:"url"`
		}
		if err := decode(w, r, &b); err != nil {
			failure(w, 400, err.Error())
			return
		}
		if len(b.URL) > 8192 || strings.TrimSpace(b.URL) == "" {
			failure(w, 400, "请输入有效链接")
			return
		}
		if s.hooks.Submit == nil {
			failure(w, 503, "解析服务不可用")
			return
		}
		id, err := s.hooks.Submit(r.Context(), b.URL)
		if err != nil {
			failure(w, 400, err.Error())
			return
		}
		reply(w, 202, map[string]string{"id": id})
		return
	case path == "/api/platforms" && method == "GET":
		reply(w, 200, model.Platforms)
		return
	}
	failure(w, 404, "接口或请求方法不存在")
}
