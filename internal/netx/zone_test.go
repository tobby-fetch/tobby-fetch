// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package netx_test

// A miniature enterprise zone, built for the FR-080/FR-081 acceptance and
// reused by the wiring proof.
//
// The shape is the one the requirement describes, not an approximation of
// it: direct egress does not work, an authenticated forward proxy is the
// only route out, and the origin presents a certificate from a private
// authority no public root store knows.
//
// "Direct egress does not work" is enforced twice, because one mechanism
// alone would be a test that passes for the wrong reason:
//
//   - The origin is reached by a name under .invalid, the TLD RFC 6761
//     guarantees will never resolve. A path that dials it directly gets a
//     DNS failure. Only the proxy knows the real loopback address.
//   - The origin's listener counts accepted connections and the proxy
//     counts the tunnels it opened. Every assertion compares the two: if
//     any fetch path had found a second way in, the origin would have
//     accepted a connection the proxy never made.
//
// Nothing here mocks the OCI protocol. The origin's /v2/ is Tobby's own
// embedded registry, seeded and read by go-containerregistry exactly as a
// standard client would.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

const (
	// originHost is the name every fetch path is given. It resolves
	// nowhere: reaching it is only possible through the proxy, which
	// holds the mapping onto the real listener.
	originHost = "registry.tobby.invalid"

	proxyUser = "tobby"
	proxyPass = "s3cr3t-proxy-password"
)

// zone is one assembled test zone.
type zone struct {
	// Addr is "registry.tobby.invalid:<port>" — what recipes, imports
	// and URLs name.
	Addr string
	// CAPEM is the private authority the origin's certificate chains to
	// (FR-081): the value of network.tls.ca.
	CAPEM string
	// ProxyURL is the forward proxy, credential-free (FR-080).
	ProxyURL string

	proxy    *testProxy
	origin   *httptest.Server
	accepted *atomic.Int64
	store    *store.Store

	// mu guards files, the static surface tests add documents to
	// between fetches.
	mu    sync.RWMutex
	files map[string][]byte
}

// newZone assembles the zone: a private CA, an origin serving Tobby's
// registry plus the plain-HTTP surfaces (Helm repository, retriever
// document, trust root) behind that CA, and an authenticated proxy in
// front of everything.
func newZone(t *testing.T) *zone {
	t.Helper()

	ca, leaf := newPrivatePKI(t, originHost)

	st, err := store.Open(t.Context(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing the origin store: %v", cerr)
		}
	})

	z := &zone{accepted: &atomic.Int64{}, store: st}

	mux := http.NewServeMux()
	mux.Handle("/v2/", st.APIHandler())
	z.origin = httptest.NewUnstartedServer(mux)
	z.origin.Listener = &countingListener{Listener: z.origin.Listener, accepted: z.accepted}
	z.origin.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	z.origin.StartTLS()
	t.Cleanup(z.origin.Close)

	_, port, err := net.SplitHostPort(z.origin.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	z.Addr = net.JoinHostPort(originHost, port)
	z.CAPEM = string(ca)

	// The static surfaces are registered after the address is known:
	// the Helm index has to point at absolute URLs on this very origin.
	z.mountStatic(mux)

	z.proxy = newTestProxy(t, map[string]string{z.Addr: z.origin.Listener.Addr().String()})
	z.ProxyURL = z.proxy.URL()
	return z
}

// URL builds an https URL on the origin, by its unresolvable name.
func (z *zone) URL(path string) string {
	return "https://" + z.Addr + path
}

// Store exposes the origin's registry store, for tests that seed content
// through it rather than over the wire.
func (z *zone) Store() *store.Store { return z.store }

// Snapshot captures the two counters an assertion compares.
func (z *zone) Snapshot() (accepted, tunnels int64) {
	return z.accepted.Load(), z.proxy.Tunnels()
}

// AssertRoutedThroughProxy is the FR-080 assertion, applied to one fetch
// path: it reached the origin, it reached it through the authenticated
// proxy, and it opened no connection the proxy did not open for it.
func (z *zone) AssertRoutedThroughProxy(t *testing.T, what string, before, beforeTunnels int64) {
	t.Helper()
	accepted, tunnels := z.Snapshot()
	if tunnels == beforeTunnels {
		t.Errorf("%s: the proxy opened no tunnel — the path did not use the shared transport", what)
	}
	if accepted-before != tunnels-beforeTunnels {
		t.Errorf("%s: the origin accepted %d connections for %d proxy tunnels — a request bypassed the shared transport",
			what, accepted-before, tunnels-beforeTunnels)
	}
	if n := z.proxy.Unauthenticated(); n != 0 {
		t.Errorf("%s: %d requests reached the proxy without credentials", what, n)
	}
}

// mountStatic registers the non-registry surfaces of the origin: a Helm
// chart repository (FR-024), the retriever document (FR-010), and a trust
// root served by URL (RECIPE-SPEC §12.3). They are ordinary web
// resources, which is exactly why they are the outbound paths most likely
// to be left on a transport of their own.
func (z *zone) mountStatic(mux *http.ServeMux) {
	files := map[string][]byte{}
	chart := chartArchive("gitea", "1.2.3")
	files["/charts/gitea-1.2.3.tgz"] = chart
	files["/charts/index.yaml"] = []byte(fmt.Sprintf(
		"apiVersion: v1\nentries:\n  gitea:\n    - version: %q\n      urls:\n        - %q\n      digest: %q\n",
		"1.2.3", z.URL("/charts/gitea-1.2.3.tgz"), sha256Hex(chart)))

	z.mu.Lock()
	z.files = files
	z.mu.Unlock()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		z.mu.RLock()
		data, ok := z.files[r.URL.Path]
		z.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
}

// Serve publishes one static file on the origin, so a test can put its
// own retriever document or trust root there.
func (z *zone) Serve(path string, data []byte) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.files[path] = data
}

// countingListener counts the TCP connections the origin accepts. It is
// half of the no-bypass proof: compared against the proxy's tunnel count,
// it turns "did anything reach the origin another way?" into arithmetic.
type countingListener struct {
	net.Listener
	accepted *atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

// testProxy is a real forward proxy: it refuses anything without
// Proxy-Authorization (FR-080's "proxies requiring authentication"),
// tunnels CONNECT to the origin, and forwards absolute-URI requests.
//
// It resolves the origin's unresolvable name from a table it was handed.
// That is the whole trick: the proxy is the only holder of the route.
type testProxy struct {
	srv     *httptest.Server
	routes  map[string]string
	tunnels atomic.Int64
	plain   atomic.Int64
	nocreds atomic.Int64

	mu    sync.Mutex
	hosts []string
}

func newTestProxy(t *testing.T, routes map[string]string) *testProxy {
	t.Helper()
	p := &testProxy{routes: routes}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

// URL is the credential-free proxy URL an operator configures.
func (p *testProxy) URL() string { return p.srv.URL }

// Tunnels counts CONNECT tunnels opened.
func (p *testProxy) Tunnels() int64 { return p.tunnels.Load() }

// Plain counts forwarded absolute-URI requests.
func (p *testProxy) Plain() int64 { return p.plain.Load() }

// Unauthenticated counts requests that arrived without credentials.
func (p *testProxy) Unauthenticated() int64 { return p.nocreds.Load() }

// Hosts lists the destinations the proxy was asked for, in order.
func (p *testProxy) Hosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.hosts...)
}

func (p *testProxy) serve(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		p.nocreds.Add(1)
		w.Header().Set("Proxy-Authenticate", `Basic realm="tobby-test-proxy"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	p.mu.Lock()
	p.hosts = append(p.hosts, r.Host)
	p.mu.Unlock()

	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.forward(w, r)
}

// authorized checks the Basic proxy credentials. Go's transport sends
// them from the userinfo of the proxy URL it is handed per request — the
// only place the password ever exists (FR-080, NFR-015).
func (p *testProxy) authorized(r *http.Request) bool {
	const prefix = "Basic "
	h := r.Header.Get("Proxy-Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(h[len(prefix):])
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return ok && user == proxyUser && pass == proxyPass
}

// tunnel implements CONNECT: dial the mapped destination, hand the
// hijacked client socket the two halves of a byte pipe.
func (p *testProxy) tunnel(w http.ResponseWriter, r *http.Request) {
	target, ok := p.routes[r.Host]
	if !ok {
		http.Error(w, "no route to "+r.Host, http.StatusBadGateway)
		return
	}
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "not hijackable", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = upstream.Close()
		_ = client.Close()
		return
	}
	p.tunnels.Add(1)
	var once sync.Once
	shut := func() {
		once.Do(func() {
			_ = upstream.Close()
			_ = client.Close()
		})
	}
	go func() { _, _ = io.Copy(upstream, client); shut() }()
	go func() { _, _ = io.Copy(client, upstream); shut() }()
}

// forward implements the absolute-URI form, used for plain-HTTP
// destinations.
func (p *testProxy) forward(w http.ResponseWriter, r *http.Request) {
	target, ok := p.routes[r.Host]
	if !ok {
		http.Error(w, "no route to "+r.Host, http.StatusBadGateway)
		return
	}
	p.plain.Add(1)
	out := *r.URL
	out.Host = target
	out.Scheme = "http"
	req, err := http.NewRequestWithContext(r.Context(), r.Method, out.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() //nolint:errcheck // test proxy, read side
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// requireBlockedDirectEgress is the precondition every acceptance
// assertion rests on: the origin's name must not resolve. A resolver that
// answers for .invalid — some captive networks do — would leave the
// counter comparison as the only guard, so say so rather than pretend the
// zone is what it claims.
func requireBlockedDirectEgress(t *testing.T, addr string) {
	t.Helper()
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(t.Context(), "tcp", addr)
	if err == nil {
		_ = conn.Close()
		t.Skipf("this resolver answers for %s, so direct egress is not blocked here; the zone cannot be assembled", addr)
	}
}

// newPrivatePKI issues a root authority and a leaf certificate for host —
// the private PKI of FR-081, generated per test rather than committed.
func newPrivatePKI(t *testing.T, host string) (caPEM []byte, leaf tls.Certificate) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Tobby test private authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), leaf
}

// chartArchive fabricates a dependency-free Helm chart package.
func chartArchive(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := []struct{ path, body string }{
		{name + "/Chart.yaml", fmt.Sprintf("name: %s\nversion: %s\n", name, version)},
		{name + "/values.yaml", "replicaCount: 1\n"},
	}
	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Name: f.path, Mode: 0o644, Size: int64(len(f.body))})
		_, _ = tw.Write([]byte(f.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
