package jobs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/policy"
)

func (s *Service) mediaLocation(e *model.CacheEntry, path string, target model.Target) string {
	cfg := s.st.Config()
	if cfg.Media.Transport != "http" {
		return fileURI(path)
	}
	rel, err := filepath.Rel(filepath.Join(s.media.Root(), e.Key), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	route := "/media/" + e.Key + "/" + filepath.ToSlash(rel)
	expires := time.Now().Add(15 * time.Minute)
	if e.ExpiresAt.Before(expires) {
		expires = e.ExpiresAt
	}
	q := url.Values{"expires": []string{strconv.FormatInt(expires.Unix(), 10)}, "bot": []string{target.BotID}, "group": []string{strconv.FormatInt(target.GroupID, 10)}, "user": []string{strconv.FormatInt(target.UserID, 10)}}
	q.Set("signature", s.signature(route, q))
	return strings.TrimRight(cfg.Server.ExternalURL, "/") + route + "?" + q.Encode()
}
func (s *Service) signature(path string, q url.Values) string {
	fields := url.Values{}
	for _, key := range []string{"expires", "bot", "group", "user"} {
		fields.Set(key, q.Get(key))
	}
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = mac.Write([]byte(path + "?" + fields.Encode()))
	return hex.EncodeToString(mac.Sum(nil))
}
func (s *Service) MediaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		expires, e := strconv.ParseInt(q.Get("expires"), 10, 64)
		sig, decodeErr := hex.DecodeString(q.Get("signature"))
		expected, _ := hex.DecodeString(s.signature(r.URL.Path, q))
		if e != nil || decodeErr != nil || !hmac.Equal(sig, expected) || time.Now().Unix() >= expires {
			http.Error(w, "签名无效或已过期", http.StatusForbidden)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/media/"), "/", 2)
		if len(parts) != 2 || len(parts[0]) != 64 {
			http.NotFound(w, r)
			return
		}
		if _, e = hex.DecodeString(parts[0]); e != nil {
			http.NotFound(w, r)
			return
		}
		group, ge := strconv.ParseInt(q.Get("group"), 10, 64)
		user, ue := strconv.ParseInt(q.Get("user"), 10, 64)
		if ge != nil || ue != nil {
			http.NotFound(w, r)
			return
		}
		target := model.Target{BotID: q.Get("bot"), GroupID: group, UserID: user}
		s.cacheMu.Lock()
		entry, err := s.st.GetCache(parts[0])
		if err != nil || entry == nil || s.pending[parts[0]] {
			s.cacheMu.Unlock()
			http.NotFound(w, r)
			return
		}
		if why, _ := policy.Check(s.st.Config(), target, entry.Platform); why != "" {
			s.cacheMu.Unlock()
			http.Error(w, "平台已被禁用", http.StatusForbidden)
			return
		}
		candidate := filepath.Join(s.media.Root(), entry.Key, filepath.FromSlash(parts[1]))
		allowed := false
		for _, f := range entry.Files {
			if candidate == f.Path || (f.ThumbPath != "" && candidate == f.ThumbPath) {
				allowed = true
				break
			}
		}
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		root, rootErr := filepath.EvalSymlinks(filepath.Join(s.media.Root(), entry.Key))
		if !allowed || resolveErr != nil || rootErr != nil || !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			s.cacheMu.Unlock()
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(resolved)
		if err != nil {
			s.cacheMu.Unlock()
			http.NotFound(w, r)
			return
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			s.cacheMu.Unlock()
			http.NotFound(w, r)
			return
		}
		s.leases[entry.Key]++
		s.cacheMu.Unlock()
		defer s.release(entry.Key)
		defer f.Close()
		http.ServeContent(w, r, filepath.Base(resolved), info.ModTime(), f)
	})
}
