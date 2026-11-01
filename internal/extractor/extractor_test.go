package extractor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"multiparse/internal/model"
	"multiparse/internal/netx"
)

func TestReferenceURLNormalization(t *testing.T) {
	cases := []struct{ platform, input, want string }{
		{"youtube", "https://m.youtube.com/watch?v=abc&list=PL1&index=2&feature=share#time", "https://www.youtube.com/watch?index=2&list=PL1&v=abc"},
		{"youtube", "https://youtu.be/abc?si=tracking", "https://youtu.be/abc"},
		{"facebook", "www.facebook.com/watch?v=123&ref=share", "https://www.facebook.com/watch?v=123"},
		{"facebook", "https://www.facebook.com/author/videos/123/?mibextid=tracking", "https://www.facebook.com/author/videos/123/"},
		{"snapchat", "https://www.snapchat.com/@name/spotlight/abc?share_id=tracking", "https://www.snapchat.com/@name/spotlight/abc"},
	}
	for _, c := range cases {
		got, err := Normalize(c.platform, c.input)
		if err != nil || got != c.want {
			t.Fatalf("%s: %q, %v; want %q", c.input, got, err, c.want)
		}
	}
	for _, c := range []struct{ platform, raw string }{
		{"youtube", "https://youtube.com.evil.example/watch?v=123"},
		{"youtube", "https://user:secret@youtube.com/watch?v=123"},
		{"youtube", "https://youtube.com/@channel"},
		{"youtube", "https://youtube.com/live/123"},
		{"youtube", "https://youtube.com/post/123"},
		{"facebook", "https://facebook.com/author"},
		{"snapchat", "https://snapchat.com/spotlight/abc"},
		{"snapchat", "https://snapchat.com/@name"},
		{"generic", "https://example.org/video"},
	} {
		if _, err := Normalize(c.platform, c.raw); err == nil {
			t.Fatalf("accepted non-reference URL %q", c.raw)
		}
	}
}

func TestMetadataRetainsInfoAndDirectFormat(t *testing.T) {
	data := json.RawMessage(`{"id":"abc","title":"A title","description":"Body","uploader":"Writer","webpage_url":"https://www.youtube.com/watch?v=abc","thumbnail":"https://cdn.example/cover.jpg","duration":12.5,"width":1920,"height":1080,"ext":"mp4","formats":[{"url":"https://cdn.example/video.mp4?token=retained","format_id":"137","vcodec":"avc1","acodec":"none"}],"custom_metadata":{"must":"survive"}}`)
	r, err := result("youtube", "https://www.youtube.com/watch?v=abc", data)
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "A title" || r.Content != "Body" || r.Author != "Writer" || r.Media[0].Width != 1920 || r.Media[0].Duration != 12.5 {
		t.Fatalf("metadata lost: %#v", r)
	}
	if r.Media[0].URL != "https://cdn.example/video.mp4?token=retained" || r.Media[0].URL == r.SourceURL {
		t.Fatal("webpage masqueraded as media")
	}
	if r.Extraction == nil || r.Extraction.Engine != Engine || string(r.Extraction.InfoJSON) != string(data) {
		t.Fatal("sanitized full metadata was not retained")
	}
}

func TestMetadataPlaylistLiveAndBounds(t *testing.T) {
	r, err := result("facebook", "https://www.facebook.com/reel/123", json.RawMessage(`{"_type":"playlist","entries":[{"id":"first","title":"First","description":"one","url":"https://cdn.example/first.mp4"},{"title":"Second","url":"https://cdn.example/second.mp4"}]}`))
	if err != nil || r.Title != "First" || strings.Contains(string(r.Extraction.InfoJSON), "Second") {
		t.Fatalf("first playlist entry lost: %#v %v", r, err)
	}
	for _, data := range []string{
		`{"_type":"playlist","entries":[]}`,
		`{"title":"Live","url":"https://cdn.example/a.mp4","live_status":"is_live"}`,
		`{"title":"Soon","url":"https://cdn.example/a.mp4","live_status":"is_upcoming"}`,
		`{"title":"Past","url":"https://cdn.example/a.mp4","live_status":"was_live"}`,
		`{"title":"No media","webpage_url":"https://youtube.com/watch?v=x"}`,
		`null`,
		strings.Repeat(" ", maxJSON+1),
	} {
		if _, err := result("youtube", "https://youtube.com/watch?v=x", json.RawMessage(data)); err == nil {
			t.Fatalf("unsafe metadata accepted: %.100s", data)
		}
	}
	buffer := &boundedBuffer{limit: 8}
	if _, err := buffer.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("9")); err == nil || !buffer.overflow || buffer.Len() != 8 {
		t.Fatal("output bound failed")
	}
}

func TestOutputPathsAndAggregateBudget(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one.mp4", "two.m4a"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("12345"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := safeFiles(dir, []string{"one.mp4", "two.m4a"}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := safeFiles(dir, []string{"one.mp4", "two.m4a"}, 9); err == nil {
		t.Fatal("per-file bounds substituted for aggregate budget")
	}
	for _, names := range [][]string{{"../one.mp4"}, {"/tmp/one.mp4"}, {`sub\one.mp4`}, {"one.mp4", "one.mp4"}, {"."}} {
		if _, err := safeFiles(dir, names, 100); err == nil {
			t.Fatalf("unsafe output accepted: %#v", names)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "one.mp4"), filepath.Join(dir, "link.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := safeFiles(dir, []string{"link.mp4"}, 100); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := stageBudget(dir, 100); err == nil {
		t.Fatal("working symlink accepted")
	}
	if err := os.Remove(filepath.Join(dir, "link.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := stageBudget(dir, 9); err == nil {
		t.Fatal("working aggregate budget ignored")
	}
}

func TestDownloadRejectsUserEngineAndMetadata(t *testing.T) {
	for _, extraction := range []model.Extraction{{Engine: "generic"}, {Engine: Engine, InfoJSON: json.RawMessage(`null trailing`)}, {Engine: Engine, InfoJSON: json.RawMessage(strings.Repeat(" ", maxJSON+1))}} {
		if _, err := Download(context.Background(), nil, extraction, "youtube", "", t.TempDir(), 100); err == nil {
			t.Fatal("invalid engine or metadata accepted")
		}
	}
}

func pythonForTest(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for isolated launcher boundary tests")
	}
	return python
}

// Run the real embedded policy definitions without importing optional yt-dlp packages.
func pythonPolicy(t *testing.T, code string, input any) string {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	program := "scope={'__name__':'policy_test'}\nexec(" + strconv.Quote(launcher) + ",scope)\nglobals().update(scope)\n" + code
	cmd := exec.Command(pythonForTest(t), "-I", "-c", program)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("policy regression: %v\n%s", err, out)
	}
	return string(out)
}

func TestPythonCookieScopeAndSanitization(t *testing.T) {
	pythonPolicy(t, `
jar = cookies("SID=secret; HSID=other")
assert ".youtube.com\tTRUE\t/\tFALSE\t0\tSID\tsecret" in jar.getvalue()
clean = scrub({"http_headers":{"Cookie":"SID=secret","Authorization":"token","Referer":"https://youtube.com/"},"cookies":"secret","proxy":"http://credential@proxy","formats":[{"url":"https://cdn.example/file?signature=keep","filename":"../../escape"}],"description":"secret"})
assert clean["description"] == "secret"
assert clean["http_headers"] == {"Referer":"https://youtube.com/"}
assert clean["formats"] == [{"url":"https://cdn.example/file?signature=keep"}]
assert "proxy" not in clean and "cookies" not in clean
short_cookie = cookies("wide=1")
assert "\twide\t1\n" in short_cookie.getvalue()
operational = {"id":"v1","title":"Part 1","description":"1","protocol":"https","url":"https://cdn.example/v1.mp4?signature=a1b1&expire=123","manifest_url":"https://cdn.example/1.m3u8?token=1"}
retained = scrub({**operational,"http_headers":{"Cookie":"wide=1","Proxy-Authorization":"Basic 1","Referer":"https://youtube.com/watch?v=v1"},"formats":[{"id":"f1","url":"https://cdn.example/f1.mp4?signature=1"}]})
for key, value in operational.items():
    assert retained[key] == value, (key, retained[key], value)
assert retained["formats"][0] == {"id":"f1","url":"https://cdn.example/f1.mp4?signature=1"}
assert retained["http_headers"] == {"Referer":"https://youtube.com/watch?v=v1"}
for bad in [{"url":"file:///etc/passwd"},{"ext":"../../escape"},{"formats":[{"url":"http://name:secret@example.org/x"}]},{"protocol":"rtmp"}]:
    try:
        validate_info(bad)
    except PolicyError:
        pass
    else:
        raise AssertionError(bad)
`, nil)
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPythonAuditOnlyAllowsControlledGateway(t *testing.T) {
	client := &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://fixture.invalid/media" {
			return nil, errors.New("unexpected target")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("controlled")), Request: r}, nil
	})}
	proxy, err := netx.NewProcessProxy(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	pythonPolicy(t, `
import urllib.request
request = json.load(sys.stdin)
p = Policy(request)
p.install()
handler = urllib.request.ProxyHandler({"http":request["proxy"],"https":request["proxy"]})
assert urllib.request.build_opener(handler).open("http://fixture.invalid/media",timeout=2).read() == b"controlled"
for action in [lambda:socket.getaddrinfo("example.org",80),lambda:socket.socket().connect(("127.0.0.1",1)),lambda:subprocess.Popen(["/bin/sh","-c","true"]),lambda:ctypes.CDLL(None)]:
    try:
        action()
    except PolicyError:
        pass
    else:
        raise AssertionError("network or subprocess bypass accepted")
`, map[string]any{"dir": t.TempDir(), "limit": 1 << 20, "proxy": proxy.URL(), "runtime": "/usr/local/bin/deno", "ffmpeg": "/usr/bin/ffmpeg", "ffprobe": "/usr/bin/ffprobe"})
}

func TestPythonChildPolicyRejectsRemoteFFmpegAndDenoPermissions(t *testing.T) {
	pythonPolicy(t, `
p = Policy(json.load(sys.stdin))
for exe,args in [(p.ffmpeg,[p.ffmpeg,"-i","https://example.org/video","out.mp4"]),(p.deno,[p.deno,"run","--allow-net","-"]),("/bin/sh",["/bin/sh","-c","true"])]:
    try:
        p.validate_process(exe,args)
    except PolicyError:
        pass
    else:
        raise AssertionError("unsafe child accepted")
`, map[string]any{"dir": t.TempDir(), "limit": 1 << 20, "proxy": "http://user:secret@127.0.0.1:12345", "runtime": "/usr/local/bin/deno", "ffmpeg": "/usr/bin/ffmpeg", "ffprobe": "/usr/bin/ffprobe"})
}

func TestBackendCancellationKillsProcessGroup(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " required for process lifecycle boundary test")
		}
	}
	dir := t.TempDir()
	runtime := filepath.Join(dir, "sleeping-runtime")
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nsleep 60 &\nprintf '%s' \"$!\" > child.pid\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YTDLP_PYTHON", runtime)
	t.Setenv("YTDLP_JS_RUNTIME", "/bin/true")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := run(ctx, &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected network") })}, request{Mode: "resolve", Platform: "youtube", Dir: dir, Limit: 1 << 20})
		done <- err
	}()
	var pid int
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for pid == 0 {
		select {
		case err := <-done:
			t.Fatalf("process exited before cancellation: %v", err)
		case <-deadline.C:
			t.Fatal("runtime failed to create child")
		case <-tick.C:
			data, _ := os.ReadFile(filepath.Join(dir, "child.pid"))
			pid, _ = strconv.Atoi(string(data))
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("backend ignored cancellation")
	}
	// A killed child may briefly remain a zombie until the host's init reaps it.
	if err := syscall.Kill(pid, 0); err == nil {
		status, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if readErr == nil && !strings.Contains(string(status), ") Z ") {
			t.Fatalf("child %d survived process-group cancellation", pid)
		}
	}
}

func requireInstalledBackend(t *testing.T) {
	t.Helper()
	python := os.Getenv("YTDLP_PYTHON")
	if python == "" {
		python = "/opt/yt-dlp/bin/python"
	}
	deno := os.Getenv("YTDLP_JS_RUNTIME")
	if deno == "" {
		deno = "/usr/local/bin/deno"
	}
	for _, binary := range []string{python, deno, "ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip("installed pinned yt-dlp/EJS/Deno/ffmpeg runtime required: " + binary)
		}
	}
}

func TestInstalledBackendDownloadsRetainedInfoWithoutPageReplay(t *testing.T) {
	requireInstalledBackend(t)
	// A genuine 0.1-second PCM WAV exercises the real downloader, not fake media.
	wav := make([]byte, 44+1600)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 8000)
	binary.LittleEndian.PutUint32(wav[28:], 16000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 1600)
	var requests atomic.Int32
	client := &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.String() != "http://fixture.invalid/tone.wav" || r.Header.Get("Cookie") != "" {
			return nil, errors.New("page replay or supplied Facebook cookie leak")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {strconv.Itoa(len(wav))}, "Content-Type": {"audio/wav"}}, ContentLength: int64(len(wav)), Body: io.NopCloser(bytes.NewReader(wav)), Request: r}, nil
	})}
	info := json.RawMessage(`{"id":"tone","title":"Retained audio","description":"","url":"http://fixture.invalid/tone.wav","webpage_url":"http://fixture.invalid/reextract-forbidden","extractor":"facebook","extractor_key":"Facebook","ext":"wav","protocol":"http","vcodec":"none","acodec":"pcm_s16le","duration":0.1,"filename":"../../escape.wav"}`)
	files, err := Download(context.Background(), client, model.Extraction{Engine: Engine, InfoJSON: info}, "facebook", "SID=must-not-leak", t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || requests.Load() != 1 {
		t.Fatalf("unexpected download/replay: %#v, requests=%d", files, requests.Load())
	}
	data, err := os.ReadFile(files[0])
	if err != nil || !bytes.Equal(data, wav) {
		t.Fatalf("real media changed: %v", err)
	}
}

func TestInstalledBackendReferenceSnapchatRoute(t *testing.T) {
	requireInstalledBackend(t)
	var requests atomic.Int32
	client := &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.String() != "http://www.snapchat.com/spotlight/abc-def" || r.Header.Get("Cookie") != "" {
			return nil, errors.New("wrong canonical route or supplied Snapchat cookie leak")
		}
		body := `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"spotlightFeed":{"spotlightStories":[{"story":{"storyId":{"value":"abc-def"}},"metadata":{"description":"Real metadata fixture","videoMetadata":{"name":"Spotlight fixture","contentUrl":"http://cdn.fixture.invalid/video.mp4","thumbnailUrl":"http://cdn.fixture.invalid/cover.jpg","width":640,"height":360,"durationMs":1500,"creator":{"personCreator":{"username":"writer"}}}}}]}}}}</script></html>`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	result, err := Resolve(context.Background(), client, "snapchat", "http://www.snapchat.com/@writer/spotlight/abc-def?tracking=drop", "SID=must-not-leak")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || result.SourceURL != "http://www.snapchat.com/@writer/spotlight/abc-def" || result.Title != "Spotlight fixture" || result.Content != "Real metadata fixture" || result.Media[0].Duration != 1.5 || result.Extraction == nil {
		t.Fatalf("reference metadata semantics lost: %#v, requests=%d", result, requests.Load())
	}
}
