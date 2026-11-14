package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"multiparse/internal/model"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func newPreparation(t *testing.T) *preparation {
	t.Helper()
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(m.root, ".stage-")
	if err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	return &preparation{manager: m, cfg: cfg, dir: dir}
}
func fixtureClient(files map[string][]byte) *http.Client {
	return &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		data, ok := files[r.URL.Path]
		status := 200
		if !ok {
			status = 404
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: -1, Header: make(http.Header), Request: r}, nil
	})}
}
func TestKeysAndSafeRemoval(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../other", strings.Repeat("g", 64), strings.Repeat("A", 64), ""} {
		if validKey(key) || m.Remove(key) == nil {
			t.Errorf("unsafe key accepted: %q", key)
		}
	}
	key := strings.Repeat("a", 64)
	path := filepath.Join(m.Root(), key)
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "image.png"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m.used = 1
	if err = m.Remove(key); err != nil {
		t.Fatal(err)
	}
	if usage, _ := m.Usage(); usage != 0 {
		t.Fatalf("leaked accounting: %d", usage)
	}
	if err = os.Symlink(t.TempDir(), path); err != nil {
		t.Fatal(err)
	}
	if err = m.Remove(key); err == nil {
		t.Fatal("removed symlink cache key")
	}
}
func TestBoundedDownloadAndCleanup(t *testing.T) {
	p := newPreparation(t)
	p.client = fixtureClient(map[string][]byte{"/too-big": bytes.Repeat([]byte{1}, 20), "/small": []byte("12345678")})
	if _, _, err := p.download(context.Background(), "https://cdn.example/too-big", nil, 10, ".bin"); err == nil {
		t.Fatal("unbounded download accepted")
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed download leaked file")
	}
	if used, _ := p.manager.Usage(); used != 0 {
		t.Fatalf("failed download leaked %d bytes", used)
	}
	path, _, err := p.download(context.Background(), "https://cdn.example/small", nil, 10, ".bin")
	if err != nil {
		t.Fatal(err)
	}
	if used, _ := p.manager.Usage(); used != 8 {
		t.Fatalf("usage = %d", used)
	}
	if err = p.discard(path); err != nil {
		t.Fatal(err)
	}
	p.cfg.Media.MaxDiskBytes = 4
	if _, _, err = p.download(context.Background(), "https://cdn.example/small", nil, 10, ".bin"); err == nil {
		t.Fatal("disk limit ignored")
	}
	if used, _ := p.manager.Usage(); used != 0 {
		t.Fatalf("disk-limit failure leaked %d bytes", used)
	}
}
func TestBoundedProcessWriter(t *testing.T) {
	p := newPreparation(t)
	file, err := os.Create(filepath.Join(p.dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cancelled := false
	writer := &mediaWriter{file: file, prep: p, limit: 5, cancel: func() { cancelled = true }}
	if n, err := writer.Write([]byte("1234")); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	if n, err := writer.Write([]byte("56")); err == nil || n != 0 || !cancelled {
		t.Fatal("process writer failed to enforce byte budget")
	}
	if used, _ := p.manager.Usage(); used != 4 {
		t.Fatalf("usage = %d", used)
	}
}
func TestActualImageTypeAndGIFBudgets(t *testing.T) {
	p := newPreparation(t)
	path := filepath.Join(p.dir, "input.bin")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := p.prepareImage(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "image" || !strings.HasSuffix(a.Path, ".png") || a.Width != 2 || a.Height != 3 {
		t.Fatalf("wrong actual image: %+v", a)
	}
	palette := color.Palette{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	encoded.Reset()
	if err = gif.EncodeAll(&encoded, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{20, 30}}); err != nil {
		t.Fatal(err)
	}
	gifPath := filepath.Join(p.dir, "animated.gif")
	if err = os.WriteFile(gifPath, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if duration, err := validateGIF(context.Background(), gifPath, 1); err != nil || duration != 0.5 {
		t.Fatalf("GIF duration %v: %v", duration, err)
	}
	if _, err = validateGIF(context.Background(), gifPath, 0); err == nil {
		t.Fatal("GIF duration limit ignored")
	}
	bad := append([]byte(nil), encoded.Bytes()...)
	bad = append(bad, 0)
	if err = os.WriteFile(gifPath, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = validateGIF(context.Background(), gifPath, 1); err == nil {
		t.Fatal("GIF trailing payload accepted")
	}
}
func TestHLSLocalizesResourcesAndRanges(t *testing.T) {
	p := newPreparation(t)
	manifest := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-KEY:METHOD=AES-128,URI=\"../key\",IV=0x1\n#EXTINF:1,\n#EXT-X-BYTERANGE:4@0\nshared.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:4\nshared.ts\n#EXT-X-ENDLIST\n"
	p.client = fixtureClient(map[string][]byte{"/video/shared.ts": []byte{0x47, 1, 2, 3, 4, 5, 6, 7}, "/key": bytes.Repeat([]byte{1}, 16)})
	path := filepath.Join(p.dir, "source.m3u8")
	if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	localized, err := p.localizeHLS(context.Background(), path, "https://cdn.example/video/source.m3u8", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(localized)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "https:") || strings.Contains(text, "../") || !strings.Contains(text, "#EXT-X-BYTERANGE:4@4") || !strings.Contains(text, ".key\"") {
		t.Fatalf("unsafe/incorrect localized manifest: %s", text)
	}
	if len(p.hlsCache) != 2 {
		t.Fatalf("shared byte-range resources downloaded repeatedly: %d", len(p.hlsCache))
	}
}
func TestHLSRejectsUnsafeAndUnboundedManifests(t *testing.T) {
	for _, reference := range []string{"file:///etc/passwd", "http://cdn.example/a.ts", "data:text/plain,a", "x\".ts"} {
		if _, err := resolveHLS("https://cdn.example/a/index.m3u8", reference); err == nil {
			t.Errorf("accepted HLS reference %q", reference)
		}
	}
	for _, raw := range []string{"0", "-1", "1@-1", "9223372036854775807@1", "1@2@3"} {
		if validByteRange(raw) {
			t.Errorf("accepted byte range %q", raw)
		}
	}
	if !validByteRange("12@34") || !validByteRange("12") {
		t.Fatal("valid byte range rejected")
	}
	p := newPreparation(t)
	p.client = fixtureClient(nil)
	path := filepath.Join(p.dir, "live.m3u8")
	if err := os.WriteFile(path, []byte("#EXTM3U\n#EXTINF:2000,\na.ts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.localizeHLS(context.Background(), path, "https://cdn.example/live.m3u8", nil, 0); err == nil {
		t.Fatal("over-duration live HLS accepted")
	}
}
func TestDownloadIncompleteContentLengthCleanup(t *testing.T) {
	p := newPreparation(t)
	p.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Length", "100")
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(bytes.Repeat([]byte{1}, 20))),
			ContentLength: 100,
			Header:        header,
			Request:       r,
		}, nil
	})}
	_, _, err := p.download(context.Background(), "https://cdn.example/truncated.mp4", nil, 1024, ".bin")
	if err == nil || !strings.Contains(err.Error(), "媒体下载不完整") {
		t.Fatalf("expected incomplete download error, got: %v", err)
	}
	if !IsRetryable(err) {
		t.Fatalf("incomplete download error must be retryable: %v", err)
	}
	if used, usageErr := p.manager.Usage(); usageErr != nil || used != 0 || p.bytes != 0 {
		t.Fatalf("incomplete download leaked quota: manager=%d prep=%d err=%v", used, p.bytes, usageErr)
	}
	entries, readErr := os.ReadDir(p.dir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("incomplete download left staging files: %v (err=%v)", entries, readErr)
	}
}
func TestIsRetryableAndStderrSanitization(t *testing.T) {
	nonRetryable := []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		errInvalidReservation,
		errMediaDiskLimit,
		errMediaAccountingOverflow,
		io.ErrShortWrite,
		&os.PathError{Op: "open", Path: "/tmp/x", Err: errors.New("permission denied")},
		&os.LinkError{Op: "rename", Old: "a", New: "b", Err: errors.New("cross-device")},
		&os.SyscallError{Syscall: "write", Err: errors.New("enospc")},
		permanentError(errors.New("未安装ffmpeg")),
		errors.New("媒体实际时长超过限制"),
		errors.New("GIF时长超过限制"),
		errors.New("HLS总时长超过限制"),
		errors.New("媒体超过下载上限 1024 字节；可提高限制或使用file模式"),
		errors.New("处理后的媒体超过大小限制"),
		errors.New("媒体大小超过发送限制"),
		errors.New("图片像素数量超过安全限制"),
		errors.New("媒体分辨率超出安全限制"),
		fmt.Errorf("图片数量超过限制 %d", 9),
		errors.New("提取媒体路径越界"),
	}
	for _, err := range nonRetryable {
		if IsRetryable(err) {
			t.Errorf("expected non-retryable error, got true for: %v", err)
		}
	}
	retryable := []error{
		errors.New("媒体下载失败（目标、DNS、代理或平台策略拒绝）"),
		errors.New("媒体下载返回HTTP 502"),
		errors.New("媒体流读取失败"),
		errors.New("媒体内容为空"),
		errors.New("媒体下载不完整（预期 100 字节，实际 20 字节）"),
		errors.New("媒体地址返回文本而非实际媒体"),
		errors.New("ffprobe无法识别媒体或媒体文件已损坏"),
		errors.New("ffprobe返回无效媒体信息"),
		errors.New("媒体流数量无效"),
		errors.New("下载内容不含音视频"),
		errors.New("ffmpeg媒体处理失败"),
		errors.New("ffmpeg未产生媒体"),
		fmt.Errorf("生成视频封面失败: %w", errors.New("ffmpeg媒体处理失败")),
		errors.New("图片头无效或格式不支持"),
		errors.New("图片数据损坏"),
		errors.New("图片地址实际返回非图片媒体"),
		errors.New("HLS片段不是二进制媒体"),
	}
	for _, err := range retryable {
		if !IsRetryable(err) {
			t.Errorf("expected retryable error, got false for: %v", err)
		}
	}
	p := newPreparation(t)
	raw := "  " + strings.Repeat("前序噪音", 200) + " open " + p.dir + "/0001.bin from https://cdn.example/v.mp4?sig=secret via socks5h://user:pass@127.0.0.1:1080 failed: Invalid data found  \n"
	sanitized := p.sanitizeStderr(raw)
	if strings.Contains(sanitized, p.dir) || strings.Contains(sanitized, "sig=secret") || strings.Contains(sanitized, "user:pass") {
		t.Fatalf("stderr leaked sensitive path or URL: %q", sanitized)
	}
	if !strings.Contains(sanitized, "[stage]/0001.bin") || !strings.Contains(sanitized, "[url]") || !strings.HasSuffix(sanitized, "Invalid data found") {
		t.Fatalf("unexpected sanitized stderr: %q", sanitized)
	}
	if utf8.RuneCountInString(sanitized) != 600 {
		t.Fatalf("expected tail 600 runes, got %d", utf8.RuneCountInString(sanitized))
	}
}
