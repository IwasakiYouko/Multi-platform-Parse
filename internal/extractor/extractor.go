// Package extractor runs the pinned yt-dlp backend behind the supplied HTTP policy.
package extractor

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

//go:embed launcher.py
var launcher string

const Engine = "yt-dlp"
const maxJSON = 8 << 20
const maxFiles = 64

var snapchatPath = regexp.MustCompile(`^/@[a-zA-Z0-9._-]+/(?:spotlight/)?[a-zA-Z0-9_-]+/?$`)

// Normalize rejects non-reference content routes and retains only reference parameters.
func Normalize(platform, raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Port() != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("yt-dlp: invalid platform URL")
	}
	host := strings.ToLower(u.Hostname())
	u.Host = host
	path := strings.TrimPrefix(u.Path, "/")
	allowed := func(domain string) bool { return host == domain || strings.HasSuffix(host, "."+domain) }
	keep := map[string]bool{}
	switch platform {
	case "youtube":
		if !(allowed("youtube.com") || host == "youtu.be") || path == "" || strings.HasPrefix(path, "live") || strings.HasPrefix(path, "post") || strings.HasPrefix(path, "@") {
			return "", errors.New("youtube: unsupported content URL")
		}
		if host == "m.youtube.com" {
			u.Host = "www.youtube.com"
		}
		keep = map[string]bool{"v": true, "list": true, "index": true}
	case "facebook":
		if !allowed("facebook.com") || !(path == "watch" && u.Query().Get("v") != "" || strings.HasPrefix(path, "share/v") || strings.HasPrefix(path, "share/r") || strings.Contains(path, "/videos/") || strings.HasPrefix(path, "reel/")) {
			return "", errors.New("facebook: unsupported content URL")
		}
		keep["v"] = true
	case "snapchat":
		if (host != "snapchat.com" && host != "www.snapchat.com") || !snapchatPath.MatchString(u.Path) {
			return "", errors.New("snapchat: unsupported content URL")
		}
	default:
		return "", errors.New("yt-dlp: unsupported platform")
	}
	q := u.Query()
	for key := range q {
		if !keep[key] {
			q.Del(key)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

type request struct {
	Mode     string          `json:"mode"`
	Platform string          `json:"platform"`
	URL      string          `json:"url,omitempty"`
	Cookie   string          `json:"cookie,omitempty"`
	Proxy    string          `json:"proxy"`
	Runtime  string          `json:"runtime"`
	FFmpeg   string          `json:"ffmpeg"`
	FFprobe  string          `json:"ffprobe"`
	Dir      string          `json:"dir"`
	Limit    int64           `json:"limit"`
	Info     json.RawMessage `json:"info,omitempty"`
}

type response struct {
	Info  json.RawMessage `json:"info"`
	Files []string        `json:"files"`
	Error string          `json:"error"`
}

func Resolve(ctx context.Context, client *http.Client, platform, raw, cookie string) (*model.ParseResult, error) {
	normalized, err := Normalize(platform, raw)
	if err != nil {
		return nil, err
	}
	if platform != "youtube" {
		cookie = ""
	}
	if len(cookie) > 64<<10 {
		return nil, errors.New("youtube: cookie exceeds 64KiB limit")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "multiparse-extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out, err := run(ctx, client, request{Mode: "resolve", Platform: platform, URL: normalized, Cookie: cookie, Dir: dir, Limit: 16 << 20})
	if err != nil {
		return nil, err
	}
	return result(platform, normalized, out.Info)
}

func result(platform, raw string, data json.RawMessage) (*model.ParseResult, error) {
	if len(data) == 0 || len(data) > maxJSON {
		return nil, errors.New("yt-dlp: metadata exceeds limit or is empty")
	}
	var info map[string]json.RawMessage
	if err := json.Unmarshal(data, &info); err != nil || info == nil {
		return nil, errors.New("yt-dlp: invalid metadata")
	}
	text := func(key string) string { var s string; _ = json.Unmarshal(info[key], &s); return s }
	live := text("live_status")
	if live == "is_live" || live == "is_upcoming" || live == "was_live" {
		return nil, errors.New("yt-dlp: live and upcoming videos are unsupported")
	}
	if text("_type") == "playlist" {
		var entries []json.RawMessage
		if json.Unmarshal(info["entries"], &entries) != nil || len(entries) == 0 {
			return nil, errors.New("yt-dlp: playlist entries are empty")
		}
		return result(platform, raw, entries[0])
	}
	var width, height int
	var duration float64
	_ = json.Unmarshal(info["width"], &width)
	_ = json.Unmarshal(info["height"], &height)
	_ = json.Unmarshal(info["duration"], &duration)
	if text("title") == "" || (len(info["formats"]) == 0 && text("url") == "") {
		return nil, errors.New("yt-dlp: metadata has no title or media formats")
	}
	// The media URL is deliberately a direct selected format, never the webpage URL.
	mediaURL := text("url")
	direct := func(address string) bool { return httpURL(address) && address != raw && address != text("webpage_url") }
	if !direct(mediaURL) {
		var formats []struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(info["formats"], &formats)
		for i := len(formats) - 1; i >= 0; i-- {
			if direct(formats[i].URL) {
				mediaURL = formats[i].URL
				break
			}
		}
	}
	if !direct(mediaURL) {
		return nil, errors.New("yt-dlp: no HTTP media format")
	}
	r := &model.ParseResult{Platform: platform, SourceURL: raw, Title: text("title"), Content: text("description"), Author: text("uploader"), Type: "video", Extraction: &model.Extraction{Engine: Engine, InfoJSON: data}}
	r.Media = []model.Media{{Kind: "video", URL: mediaURL, ThumbnailURL: text("thumbnail"), Width: width, Height: height, Duration: duration, Ext: text("ext")}}
	return r, nil
}

func httpURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.User == nil && u.Hostname() != "" && (u.Scheme == "https" || u.Scheme == "http")
}

// Download uses the retained info JSON, not another extraction of the source page.
// limit bounds the aggregate private working set, including fragment/merge intermediates.
func Download(ctx context.Context, client *http.Client, extraction model.Extraction, platform, cookie, dir string, limit int64) ([]string, error) {
	if extraction.Engine != Engine || len(extraction.InfoJSON) == 0 || len(extraction.InfoJSON) > maxJSON || !json.Valid(extraction.InfoJSON) {
		return nil, errors.New("yt-dlp: invalid extraction metadata")
	}
	if platform != "youtube" && platform != "facebook" && platform != "snapchat" {
		return nil, errors.New("yt-dlp: unsupported platform")
	}
	if limit <= 0 {
		return nil, errors.New("yt-dlp: positive aggregate byte limit is required")
	}
	if platform != "youtube" {
		cookie = ""
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("yt-dlp: output directory must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, errors.New("yt-dlp: output directory contains a symlink")
	}
	stage, err := os.MkdirTemp(root, ".yt-dlp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	out, err := run(ctx, client, request{Mode: "download", Platform: platform, Cookie: cookie, Dir: stage, Limit: limit, Info: extraction.InfoJSON})
	if err != nil {
		return nil, err
	}
	paths, err := safeFiles(stage, out.Files, limit)
	if err != nil {
		return nil, err
	}
	var moved []string
	for _, path := range paths {
		// A private random basename avoids overwriting a caller's existing artifact.
		target := filepath.Join(root, filepath.Base(stage)+"-"+filepath.Base(path))
		if err := os.Rename(path, target); err != nil {
			for _, p := range moved {
				_ = os.Remove(p)
			}
			return nil, err
		}
		moved = append(moved, target)
	}
	return moved, nil
}

func safeFiles(dir string, files []string, limit int64) ([]string, error) {
	if len(files) == 0 || len(files) > maxFiles {
		return nil, errors.New("yt-dlp: no bounded media output")
	}
	var total int64
	seen := map[string]bool{}
	var paths []string
	for _, name := range files {
		if name != filepath.Base(name) || name == "." || strings.ContainsAny(name, `/\\`) {
			return nil, errors.New("yt-dlp: unsafe output path")
		}
		if seen[name] {
			return nil, errors.New("yt-dlp: duplicate output path")
		}
		seen[name] = true
		path := filepath.Join(dir, name)
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > limit-total {
			return nil, errors.New("yt-dlp: output is not bounded regular media")
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".mp4", ".mkv", ".webm", ".mov", ".m4a", ".mp3", ".ogg", ".opus", ".flac", ".wav", ".aac", ".ts":
		default:
			return nil, errors.New("yt-dlp: unsupported output media extension")
		}
		total += st.Size()
		paths = append(paths, path)
	}
	return paths, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		b.overflow = true
		return 0, errors.New("yt-dlp: process output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func run(ctx context.Context, client *http.Client, in request) (*response, error) {
	// Only the scoped yt-dlp jar may attach cookies; never transport configuration.
	ctx = netx.WithExplicitCookies(ctx)
	python := os.Getenv("YTDLP_PYTHON")
	if python == "" {
		python = "/opt/yt-dlp/bin/python"
	}
	python, err := exec.LookPath(python)
	if err != nil {
		return nil, errors.New("yt-dlp runtime missing: install pinned runtime or set YTDLP_PYTHON")
	}
	in.Runtime = os.Getenv("YTDLP_JS_RUNTIME")
	if in.Runtime == "" {
		in.Runtime = "/usr/local/bin/deno"
	}
	in.Runtime, err = exec.LookPath(in.Runtime)
	if err != nil {
		return nil, errors.New("yt-dlp runtime missing: install Deno or set YTDLP_JS_RUNTIME")
	}
	in.FFmpeg, err = exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("yt-dlp runtime missing: install ffmpeg")
	}
	in.FFprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		return nil, errors.New("yt-dlp runtime missing: install ffprobe")
	}
	proxy, err := netx.NewProcessProxy(ctx, client)
	if err != nil {
		return nil, err
	}
	defer proxy.Close()
	in.Proxy = proxy.URL()
	payload, err := json.Marshal(in)
	if err != nil {
		return nil, errors.New("yt-dlp: invalid request")
	}
	cmd := exec.CommandContext(ctx, python, "-I", "-c", launcher)
	cmd.Dir = in.Dir
	cmd.Env = []string{"PATH=" + filepath.Dir(in.FFmpeg) + ":" + filepath.Dir(in.Runtime) + ":/usr/bin:/bin", "HOME=" + in.Dir, "TMPDIR=" + in.Dir, "XDG_CACHE_HOME=" + in.Dir, "DENO_DIR=" + filepath.Join(in.Dir, "deno-cache"), "DENO_NO_UPDATE_CHECK=1", "NO_PROXY=", "no_proxy=", "HTTP_PROXY=" + in.Proxy, "HTTPS_PROXY=" + in.Proxy, "ALL_PROXY=" + in.Proxy, "http_proxy=" + in.Proxy, "https_proxy=" + in.Proxy, "all_proxy=" + in.Proxy, "LC_ALL=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(payload)
	stdout := &boundedBuffer{limit: maxJSON + 4096}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		})
	}
	cmd.Cancel = func() error { kill(); return nil }
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		return nil, errors.New("yt-dlp: cannot start Python runtime")
	}
	stop := make(chan struct{})
	watchDone := make(chan struct{})
	var budgetErr error
	go func() {
		defer close(watchDone)
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				kill()
				return
			case <-tick.C:
				if e := stageBudget(in.Dir, in.Limit); e != nil {
					budgetErr = e
					kill()
					return
				}
				if e := processBudget(cmd.Process.Pid); e != nil {
					budgetErr = e
					kill()
					return
				}
			}
		}
	}()
	err = cmd.Wait()
	close(stop)
	<-watchDone
	// Successful parents must not leave a child holding files or gateway sockets.
	kill()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if budgetErr != nil {
		return nil, budgetErr
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("yt-dlp: process output exceeds limit")
	}
	if err != nil {
		return nil, errors.New("yt-dlp: backend failed (runtime, extraction, or security policy); verify pinned Python/yt-dlp/EJS/Deno installation")
	}
	var out response
	if json.Unmarshal(stdout.Bytes(), &out) != nil {
		return nil, errors.New("yt-dlp: invalid bounded backend response")
	}
	if out.Error != "" {
		return nil, fmt.Errorf("yt-dlp: %s", safeError(out.Error))
	}
	if err := stageBudget(in.Dir, in.Limit); err != nil {
		return nil, err
	}
	return &out, nil
}

func safeError(code string) string {
	switch code {
	case "runtime_missing":
		return "pinned yt-dlp2026.10.26.220202.dev0/EJS0.8.0 runtime missing or mismatched"
	case "budget":
		return "aggregate download budget exceeded"
	case "live":
		return "live and upcoming videos are unsupported"
	case "metadata_limit":
		return "metadata exceeds 8MiB limit"
	case "policy":
		return "backend security policy rejected operation"
	default:
		return "video extraction or download failed"
	}
}

func stageBudget(dir string, limit int64) error {
	var total int64
	count := 0
	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		count++
		if count > maxFiles || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("yt-dlp: unsafe or excessive working files")
		}
		st, err := entry.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !st.Mode().IsRegular() || st.Size() > limit-total {
			return errors.New("yt-dlp: aggregate working byte limit exceeded")
		}
		total += st.Size()
		return nil
	})
}

// Linux RSS is sampled with the aggregate disk budget; V8 cannot use RLIMIT_AS.
func processBudget(pid int) error {
	pending := []int{pid}
	count := 0
	var pages int64
	maxPages := int64(1<<30) / int64(os.Getpagesize())
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		count++
		if count > 5 {
			return errors.New("yt-dlp: process count limit exceeded")
		}
		base := "/proc/" + strconv.Itoa(current)
		data, err := os.ReadFile(base + "/statm")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("yt-dlp: cannot enforce process memory budget")
		}
		fields := bytes.Fields(data)
		if len(fields) < 2 {
			return errors.New("yt-dlp: invalid process memory accounting")
		}
		resident, err := strconv.ParseInt(string(fields[1]), 10, 64)
		if err != nil || resident < 0 || resident > maxPages-pages {
			return errors.New("yt-dlp: 1GiB process memory limit exceeded")
		}
		pages += resident
		tasks, err := os.ReadDir(base + "/task")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(tasks) > 128 {
			return errors.New("yt-dlp: cannot enforce bounded child threads")
		}
		// A child belongs to its spawning thread, not necessarily the process leader.
		for _, task := range tasks {
			children, err := os.ReadFile(base + "/task/" + task.Name() + "/children")
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return errors.New("yt-dlp: cannot enforce child process budget")
			}
			for _, value := range bytes.Fields(children) {
				child, err := strconv.Atoi(string(value))
				if err != nil || child <= 0 {
					return errors.New("yt-dlp: invalid child process accounting")
				}
				pending = append(pending, child)
			}
		}
	}
	return nil
}
