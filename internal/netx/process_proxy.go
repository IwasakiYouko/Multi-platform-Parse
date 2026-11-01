package netx

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"multiparse/internal/model"
)

// ProcessProxy lets a scoped extractor use the same checked network route as Go.
// It never resolves or dials a CONNECT destination outside the guarded client.
type ProcessProxy struct {
	client        *http.Client
	ctx           context.Context
	server        *http.Server
	address       string
	authorization string
	mu            sync.Mutex
	connections   map[net.Conn]struct{}
	closed        bool
	once          sync.Once
	done          chan struct{}
}

func NewProcessProxy(ctx context.Context, client *http.Client) (*ProcessProxy, error) {
	if client == nil || client.Transport == nil {
		return nil, errors.New("extractor requires a controlled HTTP transport")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		listener.Close()
		return nil, err
	}
	password := hex.EncodeToString(secret[:])
	address := (&url.URL{Scheme: "http", Host: listener.Addr().String(), User: url.UserPassword("multiparse", password)}).String()
	p := &ProcessProxy{client: client, ctx: ctx, address: address, authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("multiparse:"+password)), connections: make(map[net.Conn]struct{}), done: make(chan struct{})}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() { _ = p.server.Serve(listener) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.done:
		}
	}()
	return p, nil
}

// URL contains a per-invocation credential and must not be logged or persisted.
func (p *ProcessProxy) URL() string { return p.address }
func (p *ProcessProxy) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		for connection := range p.connections {
			_ = connection.Close()
		}
		p.mu.Unlock()
		_ = p.server.Close()
		close(p.done)
	})
	return nil
}
func (p *ProcessProxy) track(connection net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = connection.Close()
		return false
	}
	p.connections[connection] = struct{}{}
	return true
}
func (p *ProcessProxy) release(connection net.Conn) {
	_ = connection.Close()
	p.mu.Lock()
	delete(p.connections, connection)
	p.mu.Unlock()
}
func (p *ProcessProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values("Proxy-Authorization")
	if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(p.authorization)) != 1 {
		w.Header().Set("Proxy-Authenticate", `Basic realm="multiparse extractor"`)
		http.Error(w, "Proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	defer func() { stop(); cancel() }()
	if ctx.Value(explicitCookieKey{}) == true {
		ctx = context.WithValue(ctx, processCookieKey{}, true)
	}
	if r.Method == http.MethodConnect {
		p.connect(ctx, w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil {
		http.Error(w, "Only absolute HTTP requests and CONNECT are supported", http.StatusBadRequest)
		return
	}
	request := r.Clone(ctx)
	request.RequestURI = ""
	request.Host = ""
	stripProcessHopHeaders(request.Header)
	response, err := p.client.Transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "Controlled egress request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	stripProcessHopHeaders(response.Header)
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
func stripProcessHopHeaders(headers http.Header) {
	for _, connection := range headers.Values("Connection") {
		for _, key := range strings.Split(connection, ",") {
			headers.Del(strings.TrimSpace(key))
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(key)
	}
}
func processProxyDial(ctx context.Context, client *http.Client, address string) (net.Conn, error) {
	guard, ok := client.Transport.(*guardedTransport)
	if !ok {
		return nil, errors.New("CONNECT requires the guarded network client")
	}
	u, err := parseURL("https://" + address)
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid CONNECT destination")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, err
	}
	if err := checkPlatform(guard.cfg, guard.platform, u.String()); err != nil {
		return nil, err
	}
	route := guard.platform
	if platform, known := model.Identify(u.String()); known {
		route = platform.ID
	}
	guard.mu.Lock()
	transport := guard.transports[route]
	if transport == nil {
		policy, err := newPolicy(guard.cfg, route)
		if err != nil {
			guard.mu.Unlock()
			return nil, err
		}
		transport = httpTransport(policy)
		guard.transports[route] = transport
	}
	guard.mu.Unlock()
	return transport.DialContext(ctx, "tcp", address)
}
func (p *ProcessProxy) connect(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	upstream, err := processProxyDial(ctx, p.client, r.Host)
	if err != nil {
		http.Error(w, "Controlled egress connection failed", http.StatusBadGateway)
		return
	}
	if !p.track(upstream) {
		return
	}
	defer p.release(upstream)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT is unavailable", http.StatusInternalServerError)
		return
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if !p.track(connection) {
		return
	}
	defer p.release(connection)
	if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffered.Flush(); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, buffered); finished <- struct{}{} }()
	go func() { _, _ = io.Copy(connection, upstream); finished <- struct{}{} }()
	select {
	case <-finished:
	case <-ctx.Done():
	}
}
