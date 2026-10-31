package runtime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"multiparse/internal/model"
)

func occupiedPort(t *testing.T) (net.Listener, int) {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, l.Addr().(*net.TCPAddr).Port
}
func runningServer(t *testing.T) (*Server, model.ServerConfig) {
	t.Helper()
	_, port := occupiedPort(t)
	if port == 65535 {
		t.Skip("ephemeral port at upper boundary")
	}
	s := New(t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "running") }))
	c := model.ServerConfig{Listen: "127.0.0.1", Port: port}
	if e := s.Start(c); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s, c
}
func TestOccupiedPortRetriesAndFailedUpdatePreservesActiveListener(t *testing.T) {
	s, c := runningServer(t)
	before := s.State()
	_, port, _ := net.SplitHostPort(before.Listen)
	actual, _ := strconv.Atoi(port)
	if actual <= c.Port {
		t.Fatalf("occupied port not skipped: %d <= %d", actual, c.Port)
	}
	bad := c
	bad.Listen = "192.0.2.123"
	if _, _, e := s.Prepare(c, bad); e == nil {
		t.Fatal("unavailable interface accepted")
	}
	if s.State().Listen != before.Listen {
		t.Fatal("rejected update changed active address")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, e := client.Get(before.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	b, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(b) != "running" {
		t.Fatalf("original listener unavailable: %d %q", response.StatusCode, b)
	}
}
func TestTLSCanEnableOnSamePortAndInvalidCertificateKeepsItWorking(t *testing.T) {
	s, plain := runningServer(t)
	before := s.State()
	dir := t.TempDir()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	private, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if e = os.WriteFile(certPath, certPEM, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); e != nil {
		t.Fatal(e)
	}
	secure := plain
	secure.TLS = true
	secure.CertFile = certPath
	secure.KeyFile = keyPath
	if e = s.Apply(plain, secure); e != nil {
		t.Fatal(e)
	}
	if s.State().Listen != before.Listen {
		t.Fatal("TLS change unexpectedly changed port")
	}
	bad := secure
	bad.KeyFile = filepath.Join(dir, "missing.key")
	if e = s.Apply(secure, bad); e == nil {
		t.Fatal("missing private key accepted")
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
	response, e := client.Get(s.State().URL)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.TLS == nil || response.StatusCode != 200 {
		t.Fatalf("TLS endpoint unavailable: %d", response.StatusCode)
	}
}

func TestDefaultListenerServesIPv4AndIPv6(t *testing.T) {
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 unavailable: %v", err)
	}
	_ = probe.Close()
	_, port := occupiedPort(t)
	if port == 65535 {
		t.Skip("ephemeral port at upper boundary")
	}
	server := New(t.TempDir(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	config := model.DefaultConfig().Server
	config.Port = port
	if err = server.Start(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	_, actualPort, err := net.SplitHostPort(server.State().Listen)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for _, host := range []string{"127.0.0.1", "::1"} {
		response, err := client.Get("http://" + net.JoinHostPort(host, actualPort))
		if err != nil {
			t.Fatalf("%s cannot reach default listener: %v", host, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("%s returned HTTP %d", host, response.StatusCode)
		}
	}
}
