// Package media stages native downloads and publishes complete, local media only.
// ffmpeg and ffprobe must be installed in PATH. Both run without network protocols.
package media

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

const maxNativeBytes int64 = 4 << 30
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	errInvalidReservation = errors.New("无效磁盘预留")
	errMediaDiskLimit = errors.New("媒体缓存达到磁盘上限，请清理缓存或提高磁盘限制")
	errMediaAccountingOverflow = errors.New("媒体磁盘计数溢出")
)

type Manager struct {
	root       string
	mu         sync.Mutex
	used       int64
	downloads  int
	transcodes int
	changed    chan struct{}
}
type preparation struct {
	manager      *Manager
	cfg          model.Config
	client       *http.Client
	dir          string
	bytes        int64
	sequence     int
	hlsResources int
	hlsCache     map[string]hlsCached
}

func validKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil && strings.ToLower(key) == key
}
func New(root string) (*Manager, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("媒体根目录必须是实际目录")
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	m := &Manager{root: absolute, changed: make(chan struct{})}
	// A new manager is created only at startup, before any preparations exist.
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			if err := os.RemoveAll(filepath.Join(absolute, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	m.used, err = treeSize(absolute)
	if err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) Root() string { return m.root }
func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("媒体缓存不允许符号链接")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("媒体缓存包含非普通文件")
		}
		if info.Size() < 0 || total > math.MaxInt64-info.Size() {
			return errors.New("媒体磁盘计数溢出")
		}
		total += info.Size()
		return nil
	})
	return total, err
}
func (m *Manager) Usage() (int64, error) { m.mu.Lock(); defer m.mu.Unlock(); return m.used, nil }
func (m *Manager) Remove(key string) error {
	if !validKey(key) {
		return errors.New("无效缓存键")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path := filepath.Join(m.root, key)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("缓存路径不是实际目录")
	}
	size, err := treeSize(path)
	if err != nil {
		return err
	}
	if err = os.RemoveAll(path); err != nil {
		return err
	}
	m.used -= size
	if m.used < 0 {
		m.used = 0
	}
	return nil
}
func (m *Manager) acquire(ctx context.Context, transcode bool, limit int) (func(), error) {
	if limit < 1 {
		return nil, errors.New("媒体并发限制必须为正数")
	}
	for {
		m.mu.Lock()
		count := &m.downloads
		if transcode {
			count = &m.transcodes
		}
		if *count < limit {
			(*count)++
			m.mu.Unlock()
			return func() {
				m.mu.Lock()
				if transcode {
					m.transcodes--
				} else {
					m.downloads--
				}
				close(m.changed)
				m.changed = make(chan struct{})
				m.mu.Unlock()
			}, nil
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
func (p *preparation) claim(n int64) error {
	if n < 0 {
		return errInvalidReservation
	}
	m := p.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used > p.cfg.Media.MaxDiskBytes || n > p.cfg.Media.MaxDiskBytes-m.used {
		return errMediaDiskLimit
	}
	if p.bytes > math.MaxInt64-n {
		return errMediaAccountingOverflow
	}
	m.used += n
	p.bytes += n
	return nil
}
func (p *preparation) unclaim(n int64) {
	if n <= 0 {
		return
	}
	p.manager.mu.Lock()
	p.manager.used -= n
	p.bytes -= n
	p.manager.mu.Unlock()
}
func (p *preparation) next(extension string) string {
	p.sequence++
	return filepath.Join(p.dir, fmt.Sprintf("%04d%s", p.sequence, extension))
}
func (p *preparation) discard(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	p.unclaim(info.Size())
	return nil
}
func (p *preparation) hardLimit() int64 {
	if p.cfg.Media.OversizeMode == "file" {
		return min(p.cfg.Media.MaxDiskBytes, maxNativeBytes)
	}
	return p.cfg.Media.MaxBytes
}

func (m *Manager) Prepare(ctx context.Context, cfg model.Config, result model.ParseResult, key string) (_ []model.Artifact, err error) {
	if !validKey(key) {
		return nil, errors.New("无效缓存键")
	}
	if cfg.Media.MaxBytes < 1024 || cfg.Media.MaxBytes > maxNativeBytes || cfg.Media.MaxDiskBytes < cfg.Media.MaxBytes || cfg.Media.MaxImages < 1 || cfg.Media.MaxImages > 200 || cfg.Media.MaxDurationSeconds < 1 || cfg.Media.MaxDurationSeconds > 86400 || cfg.Media.Concurrency < 1 || cfg.Media.Concurrency > 16 || cfg.Media.TranscodeConcurrency < 1 || cfg.Media.TranscodeConcurrency > 4 || (cfg.Media.OversizeMode != "file" && cfg.Media.OversizeMode != "link") {
		return nil, errors.New("无效媒体资源限制")
	}
	if len(result.Media) == 0 && result.Extraction == nil {
		return nil, nil
	}
	if len(result.Media) > cfg.Media.MaxImages+16 {
		return nil, errors.New("媒体数量超出限制")
	}
	images := 0
	for _, item := range result.Media {
		if item.Kind == "image" || item.Kind == "gif" || item.Kind == "livephoto" {
			images++
		}
		if item.Duration > float64(cfg.Media.MaxDurationSeconds) {
			return nil, errors.New("媒体时长超过限制")
		}
	}
	if images > cfg.Media.MaxImages {
		return nil, fmt.Errorf("图片数量超过限制 %d", cfg.Media.MaxImages)
	}
	release, err := m.acquire(ctx, false, cfg.Media.Concurrency)
	if err != nil {
		return nil, err
	}
	defer release()
	timeout := 30 * time.Minute
	if d := time.Duration(cfg.Media.MaxDurationSeconds) * time.Second * 2; d > timeout {
		timeout = d
	}
	if timeout > 2*time.Hour {
		timeout = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := netx.Client(cfg, result.Platform)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	dir, err := os.MkdirTemp(m.root, ".stage-")
	if err != nil {
		return nil, err
	}
	p := &preparation{manager: m, cfg: cfg, client: client, dir: dir}
	published := false
	defer func() {
		if !published {
			cleanupErr := os.RemoveAll(dir)
			if cleanupErr == nil {
				p.unclaim(p.bytes)
			} else {
				err = errors.Join(err, fmt.Errorf("清理媒体临时目录失败: %w", cleanupErr))
			}
		}
	}()
	artifacts := make([]model.Artifact, 0, len(result.Media))
	if result.Extraction != nil {
		artifacts, err = p.prepareExtraction(ctx, result)
		if err != nil {
			return nil, err
		}
	} else {
		for _, item := range result.Media {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			headers := mediaHeaders(result, item)
			a, prepareErr := p.prepareItem(ctx, item, headers)
			if prepareErr != nil {
				return nil, prepareErr
			}
			artifacts = append(artifacts, a)
		}
	}
	actualImages := 0
	for _, a := range artifacts {
		if a.Kind == "image" || a.Kind == "gif" || a.Kind == "livephoto" || (a.Kind == "file" && a.Width > 0 && a.ThumbPath == "") {
			actualImages++
		}
	}
	if actualImages > cfg.Media.MaxImages {
		return nil, errors.New("实际图片数量超过限制")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	keep := make(map[string]bool, len(artifacts)*3)
	for _, a := range artifacts {
		keep[a.Path] = true
		if a.VideoPath != "" {
			keep[a.VideoPath] = true
		}
		if a.ThumbPath != "" {
			keep[a.ThumbPath] = true
		}
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		return nil, readErr
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if !keep[path] {
			if err = p.discard(path); err != nil {
				return nil, err
			}
		}
	}
	destination := filepath.Join(m.root, key)
	m.mu.Lock()
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		m.mu.Unlock()
		if statErr != nil {
			return nil, statErr
		}
		return nil, errors.New("缓存键已存在，拒绝覆盖")
	}
	err = os.Rename(dir, destination)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	published = true
	for i := range artifacts {
		artifacts[i].Path = filepath.Join(destination, filepath.Base(artifacts[i].Path))
		if artifacts[i].VideoPath != "" {
			artifacts[i].VideoPath = filepath.Join(destination, filepath.Base(artifacts[i].VideoPath))
		}
		if artifacts[i].ThumbPath != "" {
			artifacts[i].ThumbPath = filepath.Join(destination, filepath.Base(artifacts[i].ThumbPath))
		}
	}
	return artifacts, nil
}

func (p *preparation) download(ctx context.Context, raw string, headers map[string]string, limit int64, extension string) (string, string, error) {
	if limit < 1 || limit > maxNativeBytes {
		return "", "", errors.New("无效下载上限")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", "", errors.New("媒体URL无效")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept-Encoding", "identity")
	response, err := p.client.Do(req)
	if err != nil {
		return "", "", errors.New("媒体下载失败（目标、DNS、代理或平台策略拒绝）")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("媒体下载返回HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return "", "", fmt.Errorf("媒体超过下载上限 %d 字节；可提高限制或使用file模式", limit)
	}
	path := p.next(extension)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", "", err
	}
	successful := false
	var written int64
	defer func() {
		file.Close()
		if !successful {
			if os.Remove(path) == nil {
				p.unclaim(written)
			}
		}
	}()
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			return "", "", err
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if int64(n) > limit-written {
				return "", "", fmt.Errorf("媒体超过下载上限 %d 字节；可提高限制或使用file模式", limit)
			}
			if err = p.claim(int64(n)); err != nil {
				return "", "", err
			}
			count, writeErr := file.Write(buffer[:n])
			written += int64(count)
			if count < n {
				p.unclaim(int64(n - count))
			}
			if writeErr != nil {
				return "", "", writeErr
			}
			if count != n {
				return "", "", io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", "", errors.New("媒体流读取失败")
		}
		if n == 0 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			default:
			}
		}
	}
	if written == 0 {
		return "", "", errors.New("媒体内容为空")
	}
	if err = file.Sync(); err != nil {
		return "", "", err
	}
	if err = file.Close(); err != nil {
		return "", "", err
	}
	successful = true
	return path, response.Request.URL.String(), nil
}
