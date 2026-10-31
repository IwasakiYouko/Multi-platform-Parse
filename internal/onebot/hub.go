// Package onebot implements an authenticated OneBot v11 reverse-WebSocket hub.
package onebot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"multiparse/internal/model"
)

const (
	maxConnections        = 64
	maxPending            = 64
	eventQueueSize        = 128
	maxFrameBytes         = 1 << 20
	writeTimeout          = 10 * time.Second
	responseTimeout       = 30 * time.Second
	uploadResponseTimeout = 5 * time.Minute
	pongTimeout           = 90 * time.Second
	pingInterval          = 25 * time.Second
	dedupTTL              = 10 * time.Minute
	maxDedupEntries       = 10000
)

var (
	ErrClosed            = errors.New("OneBot hub is closed")
	ErrNotConnected      = errors.New("OneBot account is not connected")
	ErrBackpressure      = errors.New("OneBot request queue is full; action was not sent")
	ErrDeliveryUncertain = errors.New("OneBot action may have executed; do not automatically retry")
)

// APIError is a negative acknowledgement returned by the connected OneBot client.
type APIError struct {
	Action  string
	Status  string
	RetCode int64
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("OneBot %s failed (status=%s retcode=%d): %s", e.Action, e.Status, e.RetCode, e.Message)
}

// DeliveryError preserves both the original cause and uncertain-delivery status.
// A timeout after a socket write began is not evidence that QQ rejected the action.
type DeliveryError struct {
	Action string
	Cause  error
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("OneBot %s: %v: %v", e.Action, ErrDeliveryUncertain, e.Cause)
}

func (e *DeliveryError) Unwrap() error        { return e.Cause }
func (e *DeliveryError) Is(target error) bool { return target == ErrDeliveryUncertain }

// Hub accepts one Universal reverse-WebSocket connection per allowed account.
type Hub struct {
	config      func() model.Config
	onEvent     func(model.Event)
	mu          sync.Mutex
	auth        model.OneBotConfig
	closed      bool
	connections map[string]*connection
	dedup       map[string]time.Time
	dedupOrder  []dedupEntry
	dedupHead   int
}

type dedupEntry struct {
	key     string
	expires time.Time
}

type connection struct {
	hub         *Hub
	selfID      string
	tokenHash   [32]byte
	connectedAt time.Time
	lastSeen    atomic.Int64
	started     bool // guarded by hub.mu
	mu          sync.Mutex
	ws          *websocket.Conn
	closed      bool
	done        chan struct{}
	out         chan *apiRequest
	controls    chan controlFrame
	events      chan model.Event
	pending     map[string]*apiRequest
	sequence    atomic.Uint64
	prefix      string
}

type controlFrame struct {
	kind int
	data []byte
}

type apiRequest struct {
	action  string
	echo    string
	payload []byte
	ctx     context.Context
	writing bool // guarded by connection.mu; remains true after a successful write
	result  chan error
}

// New creates a hub. Reconfigure must be called after saving configuration changes.
func New(config func() model.Config, onEvent func(model.Event)) *Hub {
	h := &Hub{
		config:      config,
		onEvent:     onEvent,
		connections: make(map[string]*connection),
		dedup:       make(map[string]time.Time),
	}
	if config != nil {
		h.auth = copyAuth(config().OneBot)
	}
	return h
}

func copyAuth(a model.OneBotConfig) model.OneBotConfig {
	a.SelfIDs = append([]string(nil), a.SelfIDs...)
	return a
}

// Handler serves only /onebot/v11/ws on the application's shared HTTP listener.
func (h *Hub) Handler() http.Handler { return http.HandlerFunc(h.serveWS) }

func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/onebot/v11/ws" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// This endpoint is machine-to-machine. NapCat omits Origin; browsers must not
	// use it, even from the admin origin. Tokens in query strings are never accepted.
	if len(r.Header.Values("Origin")) != 0 {
		http.Error(w, "browser WebSocket connections are not allowed", http.StatusForbidden)
		return
	}
	selfValues := r.Header.Values("X-Self-ID")
	roleValues := r.Header.Values("X-Client-Role")
	if len(selfValues) != 1 || !validAccountID(selfValues[0]) || len(roleValues) != 1 || roleValues[0] != "Universal" {
		http.Error(w, "X-Self-ID and Universal X-Client-Role are required", http.StatusBadRequest)
		return
	}
	selfID := selfValues[0]
	authorization := r.Header.Values("Authorization")
	var latest model.OneBotConfig
	if h.config != nil {
		latest = h.config().OneBot
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "hub closed", http.StatusServiceUnavailable)
		return
	}
	if !validBearer(authorization, h.auth.Token) || (h.config != nil && !validBearer(authorization, latest.Token)) {
		h.mu.Unlock()
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !allowedSelfID(h.auth, selfID) || (h.config != nil && !allowedSelfID(latest, selfID)) {
		h.mu.Unlock()
		http.Error(w, "account is not allowed", http.StatusForbidden)
		return
	}
	if _, exists := h.connections[selfID]; exists {
		h.mu.Unlock()
		http.Error(w, "account already connected", http.StatusConflict)
		return
	}
	if len(h.connections) >= maxConnections {
		h.mu.Unlock()
		http.Error(w, "connection limit reached", http.StatusServiceUnavailable)
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		h.mu.Unlock()
		http.Error(w, "cannot initialize connection", http.StatusInternalServerError)
		return
	}
	c := &connection{
		hub:       h,
		selfID:    selfID,
		tokenHash: sha256.Sum256([]byte(h.auth.Token)),
		done:      make(chan struct{}),
		out:       make(chan *apiRequest, maxPending),
		controls:  make(chan controlFrame, 8),
		events:    make(chan model.Event, eventQueueSize),
		pending:   make(map[string]*apiRequest),
		prefix:    hex.EncodeToString(random[:]),
	}
	// Reserve before upgrading, so concurrent handshakes cannot replace one bot.
	h.connections[selfID] = c
	h.mu.Unlock()

	upgrader := websocket.Upgrader{
		HandshakeTimeout:  writeTimeout,
		ReadBufferSize:    4096,
		WriteBufferSize:   4096,
		EnableCompression: false,
		CheckOrigin:       func(r *http.Request) bool { return len(r.Header.Values("Origin")) == 0 },
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		c.shutdown(err)
		return
	}
	h.mu.Lock()
	if h.closed || h.connections[selfID] != c || !h.authorizedLocked(c) {
		h.mu.Unlock()
		ws.Close()
		c.shutdown(ErrNotConnected)
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		h.mu.Unlock()
		ws.Close()
		return
	}
	c.ws = ws
	c.connectedAt = time.Now()
	c.lastSeen.Store(c.connectedAt.UnixNano())
	c.started = true
	c.mu.Unlock()
	h.mu.Unlock()
	ws.SetReadLimit(maxFrameBytes)
	if err := ws.SetReadDeadline(time.Now().Add(pongTimeout)); err != nil {
		c.shutdown(err)
		return
	}
	ws.SetPongHandler(func(string) error {
		c.lastSeen.Store(time.Now().UnixNano())
		return ws.SetReadDeadline(time.Now().Add(pongTimeout))
	})
	ws.SetPingHandler(func(data string) error {
		select {
		case c.controls <- controlFrame{kind: websocket.PongMessage, data: []byte(data)}:
			return nil
		default:
			return errors.New("OneBot control queue is full")
		}
	})
	ws.SetCloseHandler(func(code int, text string) error {
		return &websocket.CloseError{Code: code, Text: text}
	})
	go c.writeLoop()
	go c.eventLoop()
	c.readLoop()
}

func validBearer(values []string, expected string) bool {
	if len(values) != 1 || expected == "" || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || strings.ContainsAny(token, " \t\r\n,") {
		return false
	}
	actualHash := sha256.Sum256([]byte(token))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(actualHash[:], expectedHash[:]) == 1
}

func allowedSelfID(config model.OneBotConfig, id string) bool {
	if len(config.SelfIDs) == 0 {
		return true
	}
	for _, allowed := range config.SelfIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

func (h *Hub) authorizedLocked(c *connection) bool {
	current := sha256.Sum256([]byte(h.auth.Token))
	return h.auth.Token != "" && subtle.ConstantTimeCompare(current[:], c.tokenHash[:]) == 1 && allowedSelfID(h.auth, c.selfID)
}

func (h *Hub) current(c *connection) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closed && h.connections[c.selfID] == c && c.started && h.authorizedLocked(c)
}

func (h *Hub) account(id string) (*connection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	c := h.connections[id]
	if c == nil || !c.started || !h.authorizedLocked(c) {
		return nil, ErrNotConnected
	}
	return c, nil
}

// Send waits for the OneBot acknowledgement and never retries an action.
func (h *Hub) Send(ctx context.Context, target model.Target, segments []model.Segment) error {
	if len(segments) == 0 {
		return errors.New("cannot send an empty message")
	}
	if len(segments) > 200 {
		return errors.New("too many message segments")
	}
	action, params, err := targetAction(target, "send_group_msg", "send_private_msg")
	if err != nil {
		return err
	}
	params["message"] = segments
	params["auto_escape"] = false
	c, err := h.account(target.BotID)
	if err != nil {
		return err
	}
	return c.call(ctx, action, params)
}

// UploadFile uses NapCat's group/private upload actions. Its source must be a
// trusted HTTP(S) URL, an absolute local path, or a local file URI. The caller
// controls signed URL lifetime; local paths must also exist inside NapCat.
func (h *Hub) UploadFile(ctx context.Context, target model.Target, path, name string) error {
	remote := false
	if !filepath.IsAbs(path) {
		source, err := url.Parse(path)
		if err != nil {
			return errors.New("invalid OneBot upload source")
		}
		switch strings.ToLower(source.Scheme) {
		case "http", "https":
			if source.Hostname() == "" || source.User != nil || source.Opaque != "" {
				return errors.New("invalid OneBot HTTP upload source")
			}
			remote = true
			if name == "" {
				name = filepath.Base(source.Path)
				if name == "." || name == "/" || name == "" {
					name = "download"
				}
			}
		case "file":
			if source.User != nil || source.Opaque != "" || (source.Host != "" && !strings.EqualFold(source.Host, "localhost")) || source.RawQuery != "" || source.Fragment != "" {
				return errors.New("OneBot file URI must identify an absolute local file")
			}
			path = filepath.FromSlash(source.Path)
		default:
			return errors.New("OneBot upload requires an absolute path, local file URI or trusted HTTP(S) URL")
		}
	}
	if !remote {
		if !filepath.IsAbs(path) {
			return errors.New("OneBot file upload requires an absolute local path")
		}
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("OneBot upload file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("OneBot upload path must be a regular file")
		}
		if name == "" {
			name = filepath.Base(path)
		}
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00\r\n") || len(name) > 255 {
		return errors.New("OneBot upload name must be a filename")
	}
	action, params, err := targetAction(target, "upload_group_file", "upload_private_file")
	if err != nil {
		return err
	}
	params["file"] = path
	params["name"] = name
	c, err := h.account(target.BotID)
	if err != nil {
		return err
	}
	return c.call(ctx, action, params)
}

func targetAction(target model.Target, groupAction, privateAction string) (string, map[string]any, error) {
	if !validAccountID(target.BotID) || target.GroupID < 0 || target.UserID < 0 {
		return "", nil, errors.New("invalid OneBot target")
	}
	if target.GroupID > 0 {
		return groupAction, map[string]any{"group_id": target.GroupID}, nil
	}
	if target.UserID > 0 {
		return privateAction, map[string]any{"user_id": target.UserID}, nil
	}
	return "", nil, errors.New("OneBot target needs a group or user ID")
}

func (c *connection) call(ctx context.Context, action string, params map[string]any) error {
	if ctx == nil {
		return errors.New("OneBot action requires a context")
	}
	timeout := responseTimeout
	if action == "upload_group_file" || action == "upload_private_file" {
		timeout = uploadResponseTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	echo := fmt.Sprintf("%s-%d", c.prefix, c.sequence.Add(1))
	payload, err := json.Marshal(struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
		Echo   string         `json:"echo"`
	}{action, params, echo})
	if err != nil {
		return fmt.Errorf("encode OneBot action: %w", err)
	}
	if len(payload) > maxFrameBytes {
		return errors.New("OneBot action exceeds the message size limit")
	}
	request := &apiRequest{action: action, echo: echo, payload: payload, ctx: ctx, result: make(chan error, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrNotConnected
	}
	if len(c.pending) >= maxPending {
		c.mu.Unlock()
		return ErrBackpressure
	}
	c.pending[echo] = request
	select {
	case c.out <- request:
		c.mu.Unlock()
	default:
		delete(c.pending, echo)
		c.mu.Unlock()
		return ErrBackpressure
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		c.finish(request, ctx.Err(), true)
		return <-request.result
	case <-c.done:
		return <-request.result
	}
}

// finish's buffered result channel is written exactly once while removing pending.
// It is never closed, so reader, cancellation and shutdown cannot race a send/close.
func (c *connection) finish(request *apiRequest, err error, uncertain bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[request.echo] != request {
		return
	}
	delete(c.pending, request.echo)
	if uncertain && request.writing {
		err = &DeliveryError{Action: request.action, Cause: err}
	}
	request.result <- err
}

func (c *connection) writeLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case frame := <-c.controls:
			if err := c.ws.WriteControl(frame.kind, frame.data, time.Now().Add(writeTimeout)); err != nil {
				c.shutdown(err)
				return
			}
		case <-ticker.C:
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				c.shutdown(err)
				return
			}
		case request := <-c.out:
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			if c.pending[request.echo] != request {
				c.mu.Unlock()
				continue
			}
			if err := request.ctx.Err(); err != nil {
				delete(c.pending, request.echo)
				request.result <- err
				c.mu.Unlock()
				continue
			}
			request.writing = true
			c.mu.Unlock()
			deadline := time.Now().Add(writeTimeout)
			if until, ok := request.ctx.Deadline(); ok && until.Before(deadline) {
				deadline = until
			}
			if err := c.ws.SetWriteDeadline(deadline); err != nil {
				c.shutdown(err)
				return
			}
			if err := c.ws.WriteMessage(websocket.TextMessage, request.payload); err != nil {
				c.shutdown(err)
				return
			}
		}
	}
}

func (c *connection) shutdown(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for echo, request := range c.pending {
		err := cause
		if request.writing {
			err = &DeliveryError{Action: request.action, Cause: cause}
		}
		request.result <- err
		delete(c.pending, echo)
	}
	close(c.done)
	ws := c.ws
	c.mu.Unlock()
	if ws != nil {
		ws.Close()
	}
	c.hub.mu.Lock()
	if c.hub.connections[c.selfID] == c {
		delete(c.hub.connections, c.selfID)
	}
	c.hub.mu.Unlock()
}

func (c *connection) eventLoop() {
	for {
		select {
		case <-c.done:
			return
		case event := <-c.events:
			if !c.hub.current(c) {
				continue
			}
			if !invokeEvent(c.hub.onEvent, event) {
				c.shutdown(errors.New("OneBot event callback panicked"))
				return
			}
		}
	}
}

func invokeEvent(callback func(model.Event), event model.Event) (ok bool) {
	ok = true
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if callback != nil {
		callback(event)
	}
	return ok
}

// Status returns connected accounts, with LastSeen updated by frames and pongs.
func (h *Hub) Status() []model.BotStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]model.BotStatus, 0, len(h.connections))
	for _, c := range h.connections {
		if c.started && h.authorizedLocked(c) {
			result = append(result, model.BotStatus{SelfID: c.selfID, ConnectedAt: c.connectedAt, LastSeen: time.Unix(0, c.lastSeen.Load())})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SelfID < result[j].SelfID })
	return result
}

// Reconfigure revokes existing and in-flight connections immediately when their
// token changes or their account is removed. Unrelated settings do not reconnect.
func (h *Hub) Reconfigure(old, next model.Config) {
	h.mu.Lock()
	h.auth = copyAuth(next.OneBot)
	var revoked []*connection
	for id, c := range h.connections {
		if !h.authorizedLocked(c) {
			delete(h.connections, id)
			revoked = append(revoked, c)
		}
	}
	h.mu.Unlock()
	for _, c := range revoked {
		c.shutdown(errors.New("OneBot credentials or account permission revoked"))
	}
}

// Close disconnects all accounts and completes every pending request. An event
// callback already running must honor its application's own cancellation policy.
func (h *Hub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	all := make([]*connection, 0, len(h.connections))
	for id, c := range h.connections {
		all = append(all, c)
		delete(h.connections, id)
	}
	h.dedup = make(map[string]time.Time)
	h.dedupOrder = nil
	h.dedupHead = 0
	h.mu.Unlock()
	for _, c := range all {
		c.shutdown(ErrClosed)
	}
	return nil
}
