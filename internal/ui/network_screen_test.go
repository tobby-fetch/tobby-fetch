// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// certPair generates a self-contained pair for the screen tests.
func certPair(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             t0.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{cn},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// fileCert is a tlsadmin.ServerCert backed by a pair on disk, reloaded on
// every read — the netx behaviour the screen relies on, reduced to what
// these tests need to observe.
type fileCert struct {
	certFile   string
	keyFile    string
	selfSigned bool
}

func (f *fileCert) load() *tls.Certificate {
	pair, err := tls.LoadX509KeyPair(f.certFile, f.keyFile)
	if err != nil {
		return nil
	}
	return &pair
}

func (f *fileCert) Fingerprint() string {
	pair := f.load()
	if pair == nil {
		return ""
	}
	sum := sha256.Sum256(pair.Certificate[0])
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
func (f *fileCert) SelfSigned() bool { return f.selfSigned }
func (f *fileCert) Source() string   { return f.certFile }
func (f *fileCert) TLSConfig() *tls.Config {
	return &tls.Config{ //nolint:gosec // G402: a stub for the reload accessor; no handshake happens in these tests
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			pair := f.load()
			if pair == nil {
				return nil, os.ErrInvalid
			}
			return pair, nil
		},
	}
}

// stubEgress is a tlsadmin.Egress over fixed printable state.
type stubEgress struct {
	proxy     string
	auth      bool
	roots     int
	exclusive bool
}

func (s stubEgress) ProxyURL() string         { return s.proxy }
func (s stubEgress) ProxyAuthenticated() bool { return s.auth }
func (s stubEgress) ExtraRoots() int          { return s.roots }
func (s stubEgress) ExclusiveTrust() bool     { return s.exclusive }
func (s stubEgress) Describe() string         { return "proxy " + s.proxy + ", 2 authorities" }

// writePair installs a pair in a temporary directory and returns the two
// configured paths.
func writePair(t *testing.T, certPEM, keyPEM []byte) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// postCertPair submits the replacement form as a multipart upload — the
// shape the screen uses so a private key never travels through a field
// the server can echo back (NFR-015).
func postCertPair(t *testing.T, mux *http.ServeMux, c *http.Cookie, csrf string, certPEM, keyPEM []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("csrf", csrf); err != nil {
		t.Fatal(err)
	}
	for _, part := range []struct {
		field, name string
		data        []byte
	}{{"certificate", "tls.crt", certPEM}, {"key", "tls.key", keyPEM}} {
		if part.data == nil {
			continue
		}
		w, err := mw.CreateFormFile(part.field, part.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(part.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/network/certificate", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(c)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestNetworkScreenReportsTheServedCertificate is the reporting half of
// FR-082: the screen names the fingerprint, the SANs and the expiry —
// everything an operator otherwise digs out of openssl.
func TestNetworkScreenReportsTheServedCertificate(t *testing.T) {
	certPEM, keyPEM := certPair(t, "tobby.example.com", t0.Add(365*24*time.Hour))
	certFile, keyFile := writePair(t, certPEM, keyPEM)
	u := newTestUIWithOptions(t, &Options{
		ServerCert:     &fileCert{certFile: certFile, keyFile: keyFile},
		ServerCertFile: certFile,
		ServerKeyFile:  keyFile,
		Egress:         stubEgress{proxy: "http://proxy.example.com:3128", auth: true, roots: 2},
	}, nil)
	u.Now = func() time.Time { return t0 }
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/admin/network", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/admin/network = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		"tobby.example.com",
		"127.0.0.1",
		certFile,
		"http://proxy.example.com:3128",
		`action="/admin/network/certificate"`,
		`name="certificate"`,
		`name="key"`,
		`type="file"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/admin/network misses %q", want)
		}
	}
	// NFR-015: no key material anywhere on the page, in any form.
	if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, string(keyPEM[:40])) {
		t.Error("the screen rendered private key material")
	}
}

// TestNetworkScreenCallsOutTheSelfSignedPosture: a generated certificate
// is a degraded posture and must never read like a configured one
// (FR-082, FR-075 spirit — nothing silent).
func TestNetworkScreenCallsOutTheSelfSignedPosture(t *testing.T) {
	certPEM, keyPEM := certPair(t, "localhost", t0.Add(365*24*time.Hour))
	certFile, keyFile := writePair(t, certPEM, keyPEM)
	u := newTestUIWithOptions(t, &Options{
		ServerCert: &fileCert{certFile: certFile, keyFile: keyFile, selfSigned: true},
	}, nil)
	u.Now = func() time.Time { return t0 }
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/admin/network", nil).Body.String()
	if !strings.Contains(body, "self-signed") {
		t.Error("a self-signed certificate is not called out as one")
	}
	// No configured pair: the replacement control is inert, with the two
	// settings to configure named.
	if strings.Contains(body, `action="/admin/network/certificate"`) {
		t.Error("the replacement form is live on an instance with no configured pair")
	}
	if !strings.Contains(body, "server.tls.certFile") {
		t.Error("the inert state does not name the setting that would enable replacement")
	}
}

// TestNetworkScreenSaysPlainHTTP: an instance listening in the clear says
// so — it is a posture, not an absence.
func TestNetworkScreenSaysPlainHTTP(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	body := get(t, mux, c, "/admin/network", nil).Body.String()
	if !strings.Contains(body, "plain HTTP") {
		t.Errorf("a plain-HTTP instance does not say so: %s", body)
	}
}

// TestNetworkScreenWarnsOnExpiry: the screen is where an operator learns
// a certificate is about to stop working, not the client that fails.
func TestNetworkScreenWarnsOnExpiry(t *testing.T) {
	for name, tc := range map[string]struct {
		notAfter time.Time
		want     string
	}{
		"expired":  {t0.Add(-time.Minute), "expired on"},
		"expiring": {t0.Add(5 * 24 * time.Hour), "expires in 5 days"},
	} {
		t.Run(name, func(t *testing.T) {
			certPEM, keyPEM := certPair(t, "tobby.example.com", tc.notAfter)
			certFile, keyFile := writePair(t, certPEM, keyPEM)
			u := newTestUIWithOptions(t, &Options{
				ServerCert: &fileCert{certFile: certFile, keyFile: keyFile},
			}, nil)
			u.Now = func() time.Time { return t0 }
			mux := mount(u)
			c := login(t, mux, "alexis", "pw-admin")
			if body := get(t, mux, c, "/admin/network", nil).Body.String(); !strings.Contains(body, tc.want) {
				t.Errorf("the screen does not warn: want %q", tc.want)
			}
		})
	}
}

// TestNetworkCertificateReplaced is the FR-082 admin-UI half end to end:
// the uploaded pair becomes the pair on disk — which is the pair netx
// re-reads — the change is audited, and the new fingerprint is confirmed.
func TestNetworkCertificateReplaced(t *testing.T) {
	oldCert, oldKey := certPair(t, "old.example.com", t0.Add(24*time.Hour))
	certFile, keyFile := writePair(t, oldCert, oldKey)
	logs := &strings.Builder{}
	u := newTestUIWithOptions(t, &Options{
		ServerCert:     &fileCert{certFile: certFile, keyFile: keyFile},
		ServerCertFile: certFile,
		ServerKeyFile:  keyFile,
	}, logs)
	u.Now = func() time.Time { return t0 }
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	newCert, newKey := certPair(t, "new.example.com", t0.Add(365*24*time.Hour))
	w := postCertPair(t, mux, c, csrfOf(t, u, c), newCert, newKey)
	if w.Code != http.StatusOK {
		t.Fatalf("replacement = %d: %s", w.Code, w.Body.String())
	}
	onDisk, err := os.ReadFile(certFile) //nolint:gosec // G304: the path is the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, newCert) {
		t.Error("the configured certificate was not replaced")
	}
	// The screen re-reads through the certificate accessor, so it shows
	// what is actually served now.
	if !strings.Contains(w.Body.String(), "new.example.com") {
		t.Error("the screen still shows the previous certificate")
	}
	trail := logs.String()
	if !strings.Contains(trail, `"action":"config.server_certificate"`) ||
		!strings.Contains(trail, `"actor":"alexis"`) ||
		!strings.Contains(trail, `"outcome":"success"`) {
		t.Errorf("no FR-094 record for the certificate change: %s", trail)
	}
	// NFR-015: nothing derived from the key reaches the trail, and no PEM
	// block does either.
	if strings.Contains(trail, "PRIVATE KEY") || strings.Contains(trail, "BEGIN") {
		t.Error("key material leaked into the audit trail")
	}
}

// TestNetworkCertificateRefusalKeepsServing is the property the screen
// exists to keep: an incoherent pair is refused, the files on disk are
// untouched, and the instance keeps presenting what it had.
func TestNetworkCertificateRefusalKeepsServing(t *testing.T) {
	goodCert, goodKey := certPair(t, "good.example.com", t0.Add(24*time.Hour))
	certFile, keyFile := writePair(t, goodCert, goodKey)
	logs := &strings.Builder{}
	u := newTestUIWithOptions(t, &Options{
		ServerCert:     &fileCert{certFile: certFile, keyFile: keyFile},
		ServerCertFile: certFile,
		ServerKeyFile:  keyFile,
	}, logs)
	u.Now = func() time.Time { return t0 }
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	otherCert, _ := certPair(t, "other.example.com", t0.Add(24*time.Hour))
	w := postCertPair(t, mux, c, csrfOf(t, u, c), otherCert, goodKey)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched pair = %d, want 422", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "TBY-NET-004") {
		t.Error("the refusal does not carry its stable code")
	}
	// The screen still describes a serving instance with the old
	// certificate: nothing was written.
	if !strings.Contains(body, "good.example.com") {
		t.Error("the screen lost the certificate that is still being served")
	}
	onDisk, err := os.ReadFile(certFile) //nolint:gosec // G304: the path is the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, goodCert) {
		t.Error("a refused replacement touched the served certificate")
	}
	if !strings.Contains(logs.String(), `"outcome":"failure"`) {
		t.Error("a refused replacement left no FR-094 record")
	}
}

// TestNetworkCertificateRefusedWithoutFiles: a submission with no upload
// at all is a coded refusal, not a panic — hand-crafted requests reach
// handlers too.
func TestNetworkCertificateRefusedWithoutFiles(t *testing.T) {
	certPEM, keyPEM := certPair(t, "good.example.com", t0.Add(24*time.Hour))
	certFile, keyFile := writePair(t, certPEM, keyPEM)
	u := newTestUIWithOptions(t, &Options{
		ServerCert:     &fileCert{certFile: certFile, keyFile: keyFile},
		ServerCertFile: certFile,
		ServerKeyFile:  keyFile,
	}, nil)
	u.Now = func() time.Time { return t0 }
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	if w := postCertPair(t, mux, c, csrfOf(t, u, c), certPEM, nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing key upload = %d, want 422", w.Code)
	}
	w := postForm(t, mux, c, "/admin/network/certificate", "csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-NET-004") {
		t.Errorf("non-multipart submission = %d, want 422 TBY-NET-004", w.Code)
	}
}

// TestNetworkScreenReachableFromTheAdminSubnav: the screen has to be
// findable, and the three admin surfaces link to each other (FR-062).
func TestNetworkScreenReachableFromTheAdminSubnav(t *testing.T) {
	u := newTestUIWithOptions(t, &Options{}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	for _, page := range []string{"/admin/accounts", "/admin/retriever", "/admin/network"} {
		if !strings.Contains(get(t, mux, c, page, nil).Body.String(), `href="/admin/network"`) {
			t.Errorf("%s does not link to /admin/network", page)
		}
	}
}
