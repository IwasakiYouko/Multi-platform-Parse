package onebot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"multiparse/internal/model"
)

type hubHarness struct {
	hub    *Hub
	server *httptest.Server
	config atomic.Pointer[model.Config]
}

func newHubHarness(t *testing.T, callback func(model.Event)) *hubHarness {
	t.Helper()
	x := &hubHarness{}
	config := model.Config{OneBot: model.OneBotConfig{Token: strings.Repeat("secret", 6), SelfIDs: []string{"10001", "10002"}}}
	x.config.Store(&config)
	x.hub = New(func() model.Config { return *x.config.Load() }, callback)
	x.server = httptest.NewServer(x.hub.Handler())
	t.Cleanup(func() { x.hub.Close(); x.server.Close() })
	return x
}

func (x *hubHarness) headers(id string) http.Header {
	return http.Header{
		"Authorization": {"Bearer " + x.config.Load().OneBot.Token},
		"X-Self-Id":     {id},
		"X-Client-Role": {"Universal"},
	}
}

func (x *hubHarness) dial(t *testing.T, id string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	peer, response, err := dialer.Dial("ws"+strings.TrimPrefix(x.server.URL, "http")+"/onebot/v11/ws", x.headers(id))
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, status := range x.hub.Status() {
			if status.SelfID == id {
				return peer
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("connection was not initialized")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHandshakeSecurity(t *testing.T) {
	x := newHubHarness(t, nil)
	tests := []struct {
		name   string
		change func(http.Header)
		query  string
		status int
	}{
		{"no authorization", func(h http.Header) { h.Del("Authorization") }, "", http.StatusUnauthorized},
		{"query token", func(h http.Header) { h.Del("Authorization") }, "?access_token=" + x.config.Load().OneBot.Token, http.StatusUnauthorized},
		{"trailing space", func(h http.Header) { h.Set("Authorization", "Bearer "+x.config.Load().OneBot.Token+" extra") }, "", http.StatusUnauthorized},
		{"double space", func(h http.Header) { h.Set("Authorization", "Bearer  "+x.config.Load().OneBot.Token) }, "", http.StatusUnauthorized},
		{"wrong token", func(h http.Header) { h.Set("Authorization", "Bearer invalid") }, "", http.StatusUnauthorized},
		{"duplicate authorization", func(h http.Header) { h.Add("Authorization", "Bearer invalid") }, "", http.StatusUnauthorized},
		{"browser origin", func(h http.Header) { h.Set("Origin", x.server.URL) }, "", http.StatusForbidden},
		{"wrong role", func(h http.Header) { h.Set("X-Client-Role", "Event") }, "", http.StatusBadRequest},
		{"duplicate role", func(h http.Header) { h.Add("X-Client-Role", "Universal") }, "", http.StatusBadRequest},
		{"disallowed account", func(h http.Header) { h.Set("X-Self-ID", "99999") }, "", http.StatusForbidden},
		{"malformed account", func(h http.Header) { h.Set("X-Self-ID", "010001") }, "", http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := x.headers("10001")
			test.change(headers)
			dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
			peer, response, err := dialer.Dial("ws"+strings.TrimPrefix(x.server.URL, "http")+"/onebot/v11/ws"+test.query, headers)
			if peer != nil {
				peer.Close()
			}
			if response != nil {
				defer response.Body.Close()
			}
			if err == nil || response == nil || response.StatusCode != test.status {
				t.Fatalf("handshake: status=%v err=%v, want %d", response, err, test.status)
			}
		})
	}
	_ = x.dial(t, "10001")
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	peer, response, err := dialer.Dial("ws"+strings.TrimPrefix(x.server.URL, "http")+"/onebot/v11/ws", x.headers("10001"))
	if peer != nil {
		peer.Close()
	}
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate account: response=%v err=%v", response, err)
	}
}

type receivedAction struct {
	Action string                     `json:"action"`
	Params map[string]json.RawMessage `json:"params"`
	Echo   string                     `json:"echo"`
}

func readAction(t *testing.T, peer *websocket.Conn) receivedAction {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var action receivedAction
	if err := peer.ReadJSON(&action); err != nil {
		t.Fatal(err)
	}
	return action
}

func awaitSend(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("OneBot action did not complete")
		return nil
	}
}

func testSegments(text string) []model.Segment {
	return []model.Segment{{Type: "text", Data: map[string]any{"text": text}}}
}

func TestConcurrentResponseDemux(t *testing.T) {
	x := newHubHarness(t, nil)
	peer := x.dial(t, "10001")
	const count = 12
	result := make(chan error, count)
	for i := range count {
		go func() {
			result <- x.hub.Send(context.Background(), model.Target{BotID: "10001", GroupID: 123}, testSegments(fmt.Sprint(i)))
		}()
	}
	actions := make([]receivedAction, count)
	for i := range count {
		actions[i] = readAction(t, peer)
	}
	for i := count - 1; i >= 0; i-- {
		if err := peer.WriteJSON(map[string]any{"echo": actions[i].Echo, "status": "ok", "retcode": 0, "data": map[string]any{"message_id": i}}); err != nil {
			t.Fatal(err)
		}
	}
	for range count {
		if err := awaitSend(t, result); err != nil {
			t.Fatal(err)
		}
	}
	status := x.hub.Status()
	if len(status) != 1 || status[0].SelfID != "10001" || status[0].LastSeen.Before(status[0].ConnectedAt) {
		t.Fatalf("invalid connection status: %#v", status)
	}
}

func TestSlowEventWorkerDoesNotBlockResponses(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	x := newHubHarness(t, func(model.Event) { started <- struct{}{}; <-release })
	peer := x.dial(t, "10001")
	if err := peer.WriteJSON(map[string]any{
		"post_type": "message", "message_type": "group", "self_id": "10001", "user_id": "123", "group_id": "456", "message_id": "789",
		"message": []model.Segment{{Type: "text", Data: map[string]any{"text": "BV1xx411c7mD"}}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("event was not delivered")
	}
	result := make(chan error, 1)
	go func() {
		result <- x.hub.Send(context.Background(), model.Target{BotID: "10001", UserID: 123}, testSegments("reply"))
	}()
	action := readAction(t, peer)
	if err := peer.WriteJSON(map[string]any{"echo": action.Echo, "status": "ok", "retcode": 0}); err != nil {
		t.Fatal(err)
	}
	if err := awaitSend(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestAcknowledgementAndTimeoutSemantics(t *testing.T) {
	x := newHubHarness(t, nil)
	peer := x.dial(t, "10001")
	for _, status := range []string{"failed", "async"} {
		result := make(chan error, 1)
		go func() {
			result <- x.hub.Send(context.Background(), model.Target{BotID: "10001", UserID: 123}, testSegments("reply"))
		}()
		action := readAction(t, peer)
		retcode := 100
		if status == "async" {
			retcode = 1
		}
		if err := peer.WriteJSON(map[string]any{"echo": action.Echo, "status": status, "retcode": retcode, "wording": "client reply"}); err != nil {
			t.Fatal(err)
		}
		err := awaitSend(t, result)
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.RetCode != int64(retcode) {
			t.Fatalf("missing API error: %v", err)
		}
		if errors.Is(err, ErrDeliveryUncertain) != (status == "async") {
			t.Fatalf("incorrect acknowledgement certainty: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- x.hub.Send(ctx, model.Target{BotID: "10001", UserID: 123}, testSegments("timeout")) }()
	_ = readAction(t, peer)
	if err := awaitSend(t, result); !errors.Is(err, ErrDeliveryUncertain) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout lost delivery uncertainty or cause: %v", err)
	}
	ctx, cancelBeforeWrite := context.WithCancel(context.Background())
	cancelBeforeWrite()
	if err := x.hub.Send(ctx, model.Target{BotID: "10001", UserID: 123}, testSegments("cancelled")); !errors.Is(err, context.Canceled) || errors.Is(err, ErrDeliveryUncertain) {
		t.Fatalf("cancelled-before-write should be certain: %v", err)
	}
}

func TestReconfigureRevokesPendingAndReconnects(t *testing.T) {
	x := newHubHarness(t, nil)
	peer := x.dial(t, "10001")
	result := make(chan error, 1)
	go func() {
		result <- x.hub.Send(context.Background(), model.Target{BotID: "10001", UserID: 123}, testSegments("pending"))
	}()
	_ = readAction(t, peer)
	old := *x.config.Load()
	next := old
	next.OneBot.Token = strings.Repeat("replacement", 4)
	x.config.Store(&next)
	x.hub.Reconfigure(old, next)
	if err := awaitSend(t, result); !errors.Is(err, ErrDeliveryUncertain) {
		t.Fatalf("revocation of a sent action must be uncertain: %v", err)
	}
	if status := x.hub.Status(); len(status) != 0 {
		t.Fatalf("revoked connection remains visible: %#v", status)
	}
	_ = x.dial(t, "10001")
	revoked := next
	revoked.OneBot.SelfIDs = []string{"10002"}
	x.config.Store(&revoked)
	x.hub.Reconfigure(next, revoked)
	if status := x.hub.Status(); len(status) != 0 {
		t.Fatalf("disallowed connection remains visible: %#v", status)
	}
}

func TestUploadUsesNapCatActionsAndAbsolutePaths(t *testing.T) {
	x := newHubHarness(t, nil)
	peer := x.dial(t, "10001")
	path := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []model.Target{{BotID: "10001", GroupID: 456}, {BotID: "10001", UserID: 123}} {
		result := make(chan error, 1)
		go func() { result <- x.hub.UploadFile(context.Background(), target, path, "upload.bin") }()
		action := readAction(t, peer)
		want := "upload_private_file"
		if target.GroupID != 0 {
			want = "upload_group_file"
		}
		if action.Action != want || rawString(action.Params["file"]) != path || rawString(action.Params["name"]) != "upload.bin" {
			t.Fatalf("incorrect upload action: %#v", action)
		}
		if err := peer.WriteJSON(map[string]any{"echo": action.Echo, "status": "ok", "retcode": 0}); err != nil {
			t.Fatal(err)
		}
		if err := awaitSend(t, result); err != nil {
			t.Fatal(err)
		}
	}
	fileURI := (&url.URL{Scheme: "file", Path: path}).String()
	signedURL := "http://127.0.0.1:12345/media/file.bin?expires=123&signature=a%2Fb%2Bc"
	for _, source := range []struct{ input, want string }{{fileURI, path}, {signedURL, signedURL}} {
		result := make(chan error, 1)
		go func() {
			result <- x.hub.UploadFile(context.Background(), model.Target{BotID: "10001", UserID: 123}, source.input, "upload.bin")
		}()
		action := readAction(t, peer)
		if action.Action != "upload_private_file" || rawString(action.Params["file"]) != source.want {
			t.Fatalf("URI upload source was not preserved/decoded correctly: %#v", action)
		}
		if err := peer.WriteJSON(map[string]any{"echo": action.Echo, "status": "ok", "retcode": 0}); err != nil {
			t.Fatal(err)
		}
		if err := awaitSend(t, result); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.hub.UploadFile(context.Background(), model.Target{BotID: "10001", UserID: 123}, "relative.bin", ""); err == nil {
		t.Fatal("relative upload path accepted")
	}
}

func TestDecodeEventAndBoundedDedup(t *testing.T) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"post_type":"message","message_type":"group","self_id":"10001","user_id":"123","group_id":456,"message_id":-789,"message":"BV1xx411c7mD"}`), &envelope); err != nil {
		t.Fatal(err)
	}
	event, ok := decodeEvent("10001", envelope)
	if !ok || event.BotID != "10001" || event.UserID != 123 || event.GroupID != 456 || event.MessageID != "-789" {
		t.Fatalf("numeric/string event IDs: %#v %v", event, ok)
	}
	envelope["user_id"] = json.RawMessage(`"10001"`)
	if _, ok := decodeEvent("10001", envelope); ok {
		t.Fatal("own message was not ignored")
	}
	h := New(func() model.Config { return model.Config{OneBot: model.OneBotConfig{Token: "test"}} }, nil)
	c := &connection{hub: h, selfID: "10001", tokenHash: sha256.Sum256([]byte("test")), started: true, events: make(chan model.Event, 1)}
	h.connections[c.selfID] = c
	now := time.Now()
	if !h.queueEvent(c, event, now) {
		t.Fatal("initial queue failed")
	}
	<-c.events
	if !h.queueEvent(c, event, now.Add(dedupTTL-time.Second)) || len(c.events) != 0 {
		t.Fatal("duplicate accepted")
	}
	if !h.queueEvent(c, event, now.Add(dedupTTL+time.Second)) || len(c.events) != 1 {
		t.Fatal("TTL slid on duplicate")
	}
	<-c.events
	for i := range maxDedupEntries + 10 {
		event.MessageID = fmt.Sprint(i)
		if !h.queueEvent(c, event, now.Add(dedupTTL+2*time.Second)) {
			t.Fatal("queue failed")
		}
		<-c.events
	}
	if len(h.dedup) > maxDedupEntries || len(h.dedupOrder)-h.dedupHead > maxDedupEntries {
		t.Fatalf("unbounded dedup: %d map, %d live entries", len(h.dedup), len(h.dedupOrder)-h.dedupHead)
	}
}
