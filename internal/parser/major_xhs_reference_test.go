package parser

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestXHSReferenceActivationVectors(t *testing.T) {
	state := xhsSignState{Timestamp: 1764896636081, Seed: 0x12345678, Offset: 17, Sequence: 19, Window: 1100, FingerprintCounter: 7, B3: "0123456789abcdef", Xray: "fedcba9876543210", XraySequence: 5, Direction: 37}
	a1 := "19ab1e5ce48c3b3c5c50e2c040a801cfcca0a4ac50000571046"
	body := []byte(`{"client_public_key_base64":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	// Fixed xhshow 0.2.0 crypto.py/client.py vector with explicit random inputs.
	want := "XYS_2UQhPsHCH0c1PUhMHjIj2erjwjQhyoPTqBPT49pjHjIj2eHjwjQgynEDJ74AHjIj2ePjwjQTJdPIPAZlg94aGLTl/L4Aq0D9n0mm2LTrzemk8eQeqgp+pLMawepnJ/4x2bSonLWUy0pm+FDF8r88arzgLeqlN98rLgSI+bpsySQy+9zyLAmjJBbHPMppzSY+/nR64/zd+jTo/UTz8MpyqD+p4g83PDbhzLTOPaRpPpmnPrkHaMDU8MYPznD7cSz+c9EIqMQCLDkcpnbLP9IUJDT/Jfznnfl0yLLIaSQQyAmOarEaLSz+qdSlqppjye8p2epFpSpg8sRM/dYAwaHVHdWFH0ijJ9Qx8n+FHdF="
	if got := xhsActivationXS(a1, body, state); got != want {
		t.Fatalf("activation signature vector mismatch: %s", got)
	}
	fingerprint := xhsFingerprintB1(state)
	digest := md5.Sum([]byte(fingerprint))
	if len(fingerprint) != 760 || hex.EncodeToString(digest[:]) != "8b2b6c8df7080dec8bef48146ea5aa00" {
		t.Fatalf("fingerprint vector mismatch: %s", hex.EncodeToString(digest[:]))
	}
	headers := xhsActivationHeaders(a1, body, state)
	if headers["x-t"] != "1764896636081" || headers["x-xray-traceid"] != "cd7604be58800005fedcba9876543210" || headers["xy-direction"] != "37" || headers["x-mns"] != "unload" {
		t.Fatalf("trace/header vector mismatch: %#v", headers)
	}
	raw, err := base64.NewEncoding(xhsSignAlphabet).DecodeString(headers["x-s-common"])
	if err != nil {
		t.Fatal(err)
	}
	var common map[string]any
	if err = json.Unmarshal(raw, &common); err != nil {
		t.Fatal(err)
	}
	if common["x5"] != a1 || common["x8"] != fingerprint || common["x11"] != "normal" {
		t.Fatalf("common signature lost fields: %#v", common)
	}
	if xhsActivationXS(a1, append(append([]byte(nil), body...), ' '), state) == want {
		t.Fatal("signature failed to bind exact body bytes")
	}
}

func TestXHSReferenceRequestedNote(t *testing.T) {
	state := map[string]any{"note": map[string]any{"firstNoteId": "other", "noteDetailMap": map[string]any{"other": map[string]any{"note": map[string]any{"noteId": "other", "type": "normal"}}, "wanted": map[string]any{"note": map[string]any{"noteId": "wanted", "type": "normal"}}}}}
	note, err := majorXHSRequestedNote(state, "wanted")
	if err != nil || Text(note["noteId"]) != "wanted" {
		t.Fatalf("wrong requested note: %#v %v", note, err)
	}
	if _, err = majorXHSRequestedNote(state, "missing"); err == nil {
		t.Fatal("recommendation substituted for missing note")
	}
	h5 := map[string]any{"noteData": map[string]any{"data": map[string]any{"noteData": map[string]any{"noteId": "other"}}}}
	if _, err = majorXHSRequestedNote(h5, "wanted"); err == nil {
		t.Fatal("wrong mobile note accepted")
	}
	if !referenceUnavailable(majorXHSLocation("https://www.xiaohongshu.com/404/item")) {
		t.Fatal("deleted note not unavailable")
	}
	if referenceUnavailable(majorXHSLocation("https://www.xiaohongshu.com/404/sec")) {
		t.Fatal("risk-control page treated as deleted")
	}
}

func TestXHSReferenceRawLiveAndCodecOrder(t *testing.T) {
	image := "https://sns-img.example/0123456789abcdef0123456789abcdef/trace/image!resize?token=ignored"
	note := map[string]any{"type": "normal", "title": "album", "desc": "Hello #猫[话题]# #狗[话题]# end", "imageList": []any{
		map[string]any{"urlDefault": image, "infoList": []any{map[string]any{"imageScene": "H5_PRV", "url": "https://cdn.example/thumb.jpg"}}, "width": 120, "height": 240},
		map[string]any{"urlDefault": image, "livePhoto": true, "width": 120, "height": 240, "stream": map[string]any{"h264": []any{}, "av1": []any{map[string]any{"masterUrl": "https://cdn.example/av1.mp4", "duration": 3000}, map[string]any{"masterUrl": "https://cdn.example/larger.mp4", "width": 9999}}, "h265": []any{map[string]any{"masterUrl": "https://cdn.example/hevc.mp4"}}}},
		map[string]any{"urlDefault": image, "livePhoto": true, "stream": map[string]any{}},
	}}
	r, err := majorXHSParseNote(note)
	if err != nil {
		t.Fatal(err)
	}
	raw := "https://sns-img-hw.xhscdn.com/trace/image?imageView2/format/jpg"
	if r.Type != "image" || r.Content != "Hello#猫 #狗 end" || len(r.Media) != 2 {
		t.Fatalf("wrong album: %#v", r)
	}
	if r.Media[0].URL != raw || r.Media[0].ThumbnailURL != "https://cdn.example/thumb.jpg" {
		t.Fatalf("raw image reconstruction lost: %#v", r.Media[0])
	}
	live := r.Media[1]
	if live.Kind != "livephoto" || live.URL != raw || live.VideoURL != "https://cdn.example/av1.mp4" || live.Width != 120 || live.Height != 240 || live.Duration != 3 {
		t.Fatalf("live pairing/stream ordering lost: %#v", live)
	}
	if _, err = majorXHSParseNote(map[string]any{"type": "unknown", "imageList": note["imageList"]}); err == nil {
		t.Fatal("unsupported type accepted")
	}
}

func TestXHSReferenceWholeExtractionFallback(t *testing.T) {
	var attempts []string
	client := &http.Client{Transport: majorTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "edith.xiaohongshu.com" {
			attempts = append(attempts, "activate")
			if req.Method != "POST" || req.URL.Path != xhsActivateURI || !strings.HasPrefix(req.Header.Get("X-S"), "XYS_") {
				t.Error("unsigned guest activation")
			}
			if strings.Contains(req.Header.Get("Cookie"), "configured-secret") {
				t.Error("configured cookie leaked into guest activation")
			}
			body, _ := io.ReadAll(req.Body)
			var payload map[string]string
			if json.Unmarshal(body, &payload) != nil {
				t.Error("invalid activation body")
			}
			key, err := base64.StdEncoding.DecodeString(payload["client_public_key_base64"])
			if err != nil || len(key) != 32 {
				t.Error("invalid activation key")
			}
			resp := majorTestResponse(req, `{"success":true}`)
			resp.Header.Add("Set-Cookie", "web_session=guest-session; Path=/; Secure")
			return resp, nil
		}
		if req.URL.RawQuery != "xsec_token=keep%2Ftoken&xsec_source=pc_share" {
			t.Error("signed URL changed")
		}
		if strings.Contains(req.Header.Get("User-Agent"), "iPhone") {
			attempts = append(attempts, "mobile")
			if req.Header.Get("Cookie") != "" {
				t.Error("anonymous mobile fallback has cookie")
			}
			return majorTestResponse(req, `window.__INITIAL_STATE__={"noteData":{"data":{"noteData":{"noteId":"wanted","type":"normal","title":"Mobile","imageList":[{"url":"https://cdn.example/photo.jpg","livePhoto":false}]}}}};`), nil
		}
		attempts = append(attempts, req.Header.Get("Cookie"))
		// A valid outer state but malformed video must fall back, not escape the stage.
		return majorTestResponse(req, `window.__INITIAL_STATE__={"note":{"firstNoteId":"wanted","noteDetailMap":{"wanted":{"note":{"noteId":"wanted","type":"video","video":{"media":{"stream":{}}}}}}}};`), nil
	})}
	r, err := majorXHS(context.Background(), client, "https://www.xiaohongshu.com/explore/wanted?xsec_token=keep%2Ftoken&xsec_source=pc_share", "web_session=configured-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(attempts, "|") != "web_session=configured-secret|activate|web_session=guest-session|mobile" || r.Title != "Mobile" || len(r.FallbackErrors) != 2 {
		t.Fatalf("wrong stages: %#v %#v", attempts, r)
	}
}

func TestXHSReferenceActivationFailureRedaction(t *testing.T) {
	c := &http.Client{Transport: majorTestTransport(func(req *http.Request) (*http.Response, error) {
		resp := majorTestResponse(req, "secret-session-response")
		resp.StatusCode = 403
		return resp, nil
	})}
	_, err := majorXHSNewSession(context.Background(), c)
	if err == nil || strings.Contains(err.Error(), "secret-session-response") {
		t.Fatalf("activation failure missing or leaked body: %v", err)
	}
}

func TestXHSReferenceVideoSelectionAndThumbnail(t *testing.T) {
	note := map[string]any{
		"type": "video",
		"imageList": []any{map[string]any{
			"urlDefault": "https://cdn.example/cover.jpg!resize",
			"urlPre":     "https://cdn.example/preview.jpg",
		}},
		"video": map[string]any{"media": map[string]any{"stream": map[string]any{
			"h264": []any{
				map[string]any{"masterUrl": "https://cdn.example/first.mp4", "width": 640, "height": 360, "duration": 12500, "backupUrls": []any{"https://cdn.example/backup.mp4"}},
				map[string]any{"masterUrl": "https://cdn.example/larger.mp4", "width": 3840, "height": 2160},
			},
			"av1": []any{map[string]any{"masterUrl": "https://cdn.example/av1.mp4"}},
		}}},
	}
	r, err := majorXHSParseNote(note)
	if err != nil {
		t.Fatal(err)
	}
	media := r.Media[0]
	if media.URL != "https://cdn.example/first.mp4" || media.Width != 640 || media.Duration != 12.5 || media.ThumbnailURL != "https://cdn.example/cover.jpg!resize" || len(media.BackupURLs) != 1 {
		t.Fatalf("video codec/order/thumbnail selection lost: %#v", media)
	}
	delete(note, "imageList")
	if _, err = majorXHSParseNote(note); err == nil {
		t.Fatal("malformed video escaped stage validation")
	}
}
