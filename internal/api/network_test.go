// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

var apiNow = time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)

// apiPair generates a certificate/key pair for these tests.
func apiPair(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(11),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             apiNow.Add(-time.Hour),
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

// diskCert is a tlsadmin.ServerCert over a pair on disk, reloaded on every
// read — the netx behaviour, reduced to what these tests observe.
type diskCert struct {
	certFile, keyFile string
	selfSigned        bool
}

func (d *diskCert) Fingerprint() string { return "AA:BB:CC" }
func (d *diskCert) SelfSigned() bool    { return d.selfSigned }
func (d *diskCert) Source() string      { return d.certFile }
func (d *diskCert) TLSConfig() *tls.Config {
	return &tls.Config{ //nolint:gosec // G402: a stub for the accessor under test; no handshake happens here
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			p, err := tls.LoadX509KeyPair(d.certFile, d.keyFile)
			if err != nil {
				return nil, err
			}
			return &p, nil
		},
	}
}

type apiEgress struct{}

func (apiEgress) ProxyURL() string         { return "http://proxy.example.com:3128" }
func (apiEgress) ProxyAuthenticated() bool { return true }
func (apiEgress) ExtraRoots() int          { return 2 }
func (apiEgress) ExclusiveTrust() bool     { return false }
func (apiEgress) Describe() string         { return "proxy http://proxy.example.com:3128" }

// stubPublisher replays a scripted publication answer.
type stubPublisher struct {
	gotRef string
	gotDoc string
	res    *engine.PublishResult
	err    error
}

func (s *stubPublisher) PublishRecipe(_ context.Context, ref string, doc []byte) (*engine.PublishResult, error) {
	s.gotRef, s.gotDoc = ref, string(doc)
	return s.res, s.err
}

// newNetworkAPI mounts the network and publication endpoints over real
// accounts.
func newNetworkAPI(t *testing.T, o *api.NetworkOptions, p api.Publisher) (*http.ServeMux, *strings.Builder) {
	t.Helper()
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		name string
		role auth.Role
		pass string
	}{
		{"root", auth.RoleAdmin, "pw-admin"},
		{"op", auth.RoleOperator, "pw-op"},
	} {
		if err := accounts.AddAccount(a.name, a.role, a.pass, apiNow); err != nil {
			t.Fatal(err)
		}
	}
	logs := &strings.Builder{}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.NewJSONHandler(logs, nil)))
	o.Now = func() time.Time { return apiNow }
	api.RegisterNetwork(a, o)
	api.RegisterPublish(a, p)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, logs
}

// writeAPIPair installs a pair in a temporary directory.
func writeAPIPair(t *testing.T, certPEM, keyPEM []byte) (certFile, keyFile string) {
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

// TestNetworkGetExposesThePublicIdentityOnly is the NFR-015 acceptance on
// this endpoint: the certificate's own identity is reported, and nothing
// about the private key is — not its bytes, not a length, not a digest.
func TestNetworkGetExposesThePublicIdentityOnly(t *testing.T) {
	certPEM, keyPEM := apiPair(t, "tobby.example.com", apiNow.Add(365*24*time.Hour))
	certFile, keyFile := writeAPIPair(t, certPEM, keyPEM)
	mux, _ := newNetworkAPI(t, &api.NetworkOptions{
		Cert:     &diskCert{certFile: certFile, keyFile: keyFile},
		CertFile: certFile, KeyFile: keyFile,
		Egress: apiEgress{},
	}, nil)

	w := call(t, mux, http.MethodGet, "/api/v1/network", "root", "pw-admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/network = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"tls":true`, `"fingerprint_sha256":"AA:BB:CC"`, `"replaceable":true`,
		"tobby.example.com", "127.0.0.1",
		`"proxy":"http://proxy.example.com:3128"`, `"proxy_authenticated":true`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the posture document misses %s", want)
		}
	}
	// The key is nowhere in the response, in any form.
	if strings.Contains(body, "PRIVATE KEY") || strings.Contains(body, "BEGIN") {
		t.Error("the posture document carries PEM material")
	}
	for _, forbidden := range []string{"key_fingerprint", "key_digest", "private_key", "key_sha256"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the posture document exposes %q — nothing derived from the key may be reported", forbidden)
		}
	}
}

// TestNetworkGetOnAPlainHTTPInstance: no certificate is a posture, and
// the document says so rather than omitting the question.
func TestNetworkGetOnAPlainHTTPInstance(t *testing.T) {
	mux, _ := newNetworkAPI(t, &api.NetworkOptions{}, nil)
	w := call(t, mux, http.MethodGet, "/api/v1/network", "root", "pw-admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"tls":false`) || !strings.Contains(w.Body.String(), `"replaceable":false`) {
		t.Errorf("plain-HTTP posture = %s", w.Body.String())
	}
}

// TestNetworkPutReplacesAndAudits is the FR-082 API half, mirroring the
// screen (FR-061): the configured pair is replaced, the response carries
// the new certificate and nothing about the key, and the change is
// recorded (FR-094).
func TestNetworkPutReplacesAndAudits(t *testing.T) {
	oldCert, oldKey := apiPair(t, "old.example.com", apiNow.Add(24*time.Hour))
	certFile, keyFile := writeAPIPair(t, oldCert, oldKey)
	mux, logs := newNetworkAPI(t, &api.NetworkOptions{
		Cert:     &diskCert{certFile: certFile, keyFile: keyFile},
		CertFile: certFile, KeyFile: keyFile,
	}, nil)

	newCert, newKey := apiPair(t, "new.example.com", apiNow.Add(365*24*time.Hour))
	body, err := json.Marshal(map[string]string{"certificate": string(newCert), "key": string(newKey)})
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, mux, http.MethodPut, "/api/v1/network/certificate", "root", "pw-admin", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "new.example.com") {
		t.Errorf("the response does not describe the installed certificate: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY") {
		t.Error("the response echoed the submitted key")
	}
	onDisk, err := os.ReadFile(certFile) //nolint:gosec // G304: the path is the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, newCert) {
		t.Error("the configured certificate was not replaced")
	}
	trail := logs.String()
	if !strings.Contains(trail, `"action":"config.server_certificate"`) || !strings.Contains(trail, `"outcome":"success"`) {
		t.Errorf("no FR-094 record: %s", trail)
	}
	if strings.Contains(trail, "PRIVATE KEY") || strings.Contains(trail, "BEGIN") {
		t.Error("key material reached the audit trail")
	}
}

// TestNetworkPutRefusalKeepsServing: an incoherent pair answers the
// coded problem document and leaves the served files alone.
func TestNetworkPutRefusalKeepsServing(t *testing.T) {
	goodCert, goodKey := apiPair(t, "good.example.com", apiNow.Add(24*time.Hour))
	certFile, keyFile := writeAPIPair(t, goodCert, goodKey)
	mux, logs := newNetworkAPI(t, &api.NetworkOptions{
		Cert:     &diskCert{certFile: certFile, keyFile: keyFile},
		CertFile: certFile, KeyFile: keyFile,
	}, nil)

	otherCert, _ := apiPair(t, "other.example.com", apiNow.Add(24*time.Hour))
	body, err := json.Marshal(map[string]string{"certificate": string(otherCert), "key": string(goodKey)})
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, mux, http.MethodPut, "/api/v1/network/certificate", "root", "pw-admin", string(body))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched pair = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), string(taxonomy.CodeServerCertReplace)) {
		t.Errorf("problem document = %s", w.Body.String())
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

	// A body that is not a document at all is the same coded refusal, not
	// a 500.
	w = call(t, mux, http.MethodPut, "/api/v1/network/certificate", "root", "pw-admin", "not json")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("malformed body = %d, want 422", w.Code)
	}
}

// TestPublishAPIMirrorsTheScreen is the FR-061 parity of R-40: the same
// document, the same digest, the same §8 no-op, the same audit action.
func TestPublishAPIMirrorsTheScreen(t *testing.T) {
	pub := &stubPublisher{res: &engine.PublishResult{
		Reference: "registry.example.com/cookbook/wordpress:6.8.2",
		Digest:    "sha256:c0ffee",
	}}
	mux, logs := newNetworkAPI(t, &api.NetworkOptions{}, pub)

	body := `{"reference":"registry.example.com/cookbook/wordpress:6.8.2","document":"kind: Recipe\n"}`
	w := call(t, mux, http.MethodPost, "/api/v1/recipes/publish", "op", "pw-op", body)
	if w.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}
	if pub.gotDoc != "kind: Recipe\n" {
		t.Errorf("the document was altered on the way: %q", pub.gotDoc)
	}
	for _, want := range []string{`"digest":"sha256:c0ffee"`, `"unchanged":false`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the response misses %s: %s", want, w.Body.String())
		}
	}
	// ADR-0007: nothing in the contract may read as a signature.
	if strings.Contains(w.Body.String(), "signature") || strings.Contains(w.Body.String(), "signed") {
		t.Error("the publication response suggests Tobby signed something")
	}
	if !strings.Contains(logs.String(), `"action":"recipe.publish"`) {
		t.Errorf("no FR-094 publication record: %s", logs.String())
	}
}

// TestPublishAPIRefusals: the immutability refusal keeps its stable code
// and its 409, an empty submission never reaches the engine, and a
// transport failure names the host.
func TestPublishAPIRefusals(t *testing.T) {
	immutable := taxonomy.New(taxonomy.CodeTagImmutable, taxonomy.Params{
		"reference": "registry.example.com/cookbook/wordpress:6.8.2",
		"published": "sha256:aaa", "candidate": "sha256:bbb",
	})
	mux, logs := newNetworkAPI(t, &api.NetworkOptions{}, &stubPublisher{err: immutable})
	body := `{"reference":"registry.example.com/cookbook/wordpress:6.8.2","document":"kind: Recipe\n"}`
	w := call(t, mux, http.MethodPost, "/api/v1/recipes/publish", "op", "pw-op", body)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), string(taxonomy.CodeTagImmutable)) {
		t.Errorf("immutability refusal = %d %s", w.Code, w.Body.String())
	}
	// A policy barrier is recorded as "denied", not as a plain failure.
	if !strings.Contains(logs.String(), `"outcome":"denied"`) {
		t.Errorf("a policy refusal was not recorded as denied: %s", logs.String())
	}

	empty := &stubPublisher{}
	mux2, _ := newNetworkAPI(t, &api.NetworkOptions{}, empty)
	w = call(t, mux2, http.MethodPost, "/api/v1/recipes/publish", "op", "pw-op", `{"reference":"","document":""}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty submission = %d, want 422", w.Code)
	}
	if empty.gotRef != "" {
		t.Error("the engine was called with an empty submission")
	}
	w = call(t, mux2, http.MethodPost, "/api/v1/recipes/publish", "op", "pw-op", "not json")
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("malformed body = %d, want 422", w.Code)
	}

	mux3, _ := newNetworkAPI(t, &api.NetworkOptions{}, &stubPublisher{err: errors.New("dial tcp: timeout")})
	w = call(t, mux3, http.MethodPost, "/api/v1/recipes/publish", "op", "pw-op", body)
	if !strings.Contains(w.Body.String(), string(taxonomy.CodeRegistryUnreachable)) ||
		!strings.Contains(w.Body.String(), "registry.example.com") {
		t.Errorf("a transport failure did not name the host: %s", w.Body.String())
	}
}

// TestPublishAPIWithoutAPublisher: an instance wired without a publishing
// side answers the taxonomized internal error rather than panicking.
func TestPublishAPIWithoutAPublisher(t *testing.T) {
	mux, _ := newNetworkAPI(t, &api.NetworkOptions{}, nil)
	w := call(t, mux, http.MethodPost, "/api/v1/recipes/publish", "op", "pw-op",
		`{"reference":"registry.example.com/cookbook/x:1","document":"kind: Recipe\n"}`)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("= %d, want 500", w.Code)
	}
}
