// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package netx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestSuppliedCertificateIsPresented is the first half of the FR-082
// acceptance: an administrator-supplied pair is what the listener serves,
// verified by a client that trusts nothing else.
func TestSuppliedCertificateIsPresented(t *testing.T) {
	dir := t.TempDir()
	caPEM, certPath, keyPath, wantFingerprint := writeServerPair(t, dir)

	sc, err := NewServerCert(config.ServerTLS{CertFile: certPath, KeyFile: keyPath}, "")
	if err != nil {
		t.Fatal(err)
	}
	if sc.SelfSigned() {
		t.Error("SelfSigned() = true for a supplied certificate")
	}
	if sc.Source() != certPath {
		t.Errorf("Source() = %q, want %q", sc.Source(), certPath)
	}
	if sc.Fingerprint() != wantFingerprint {
		t.Errorf("Fingerprint() = %s, want %s", sc.Fingerprint(), wantFingerprint)
	}

	body := serveAndFetch(t, sc, caPEM)
	if body != "ok" {
		t.Errorf("body = %q", body)
	}
}

// TestSelfSignedFallbackIsGeneratedAndFingerprinted is the second half:
// with no pair supplied, a certificate is generated, it works, and its
// fingerprint is reportable — the value FR-082 requires to be logged, so
// an operator can compare what the instance presents with what their
// client saw.
func TestSelfSignedFallbackIsGeneratedAndFingerprinted(t *testing.T) {
	state := t.TempDir()
	sc, err := NewServerCert(config.ServerTLS{Enabled: true}, state)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.SelfSigned() {
		t.Fatal("SelfSigned() = false with no certificate supplied")
	}
	fingerprint := sc.Fingerprint()
	if len(fingerprint) != 95 || !strings.Contains(fingerprint, ":") {
		t.Errorf("Fingerprint() = %q, want colon-separated SHA-256 hex", fingerprint)
	}

	leaf := parseLeaf(t, sc)
	if body := serveAndFetch(t, sc, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})); body != "ok" {
		t.Errorf("the generated certificate did not serve: body = %q", body)
	}

	// The loopback names an operator reaches a fresh instance by must be
	// covered, or the very first connection fails on the name.
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("the generated certificate does not cover localhost: %v", err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("the generated certificate does not cover 127.0.0.1: %v", err)
	}

	t.Run("the fingerprint survives a restart", func(t *testing.T) {
		again, rerr := NewServerCert(config.ServerTLS{Enabled: true}, state)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if again.Fingerprint() != fingerprint {
			t.Errorf("a restart changed the fingerprint (%s → %s): an operator who pinned it is now wrong",
				fingerprint, again.Fingerprint())
		}
	})

	t.Run("without a state directory it stays in memory", func(t *testing.T) {
		a, aerr := NewServerCert(config.ServerTLS{Enabled: true}, "")
		if aerr != nil {
			t.Fatal(aerr)
		}
		b, berr := NewServerCert(config.ServerTLS{Enabled: true}, "")
		if berr != nil {
			t.Fatal(berr)
		}
		if a.Fingerprint() == b.Fingerprint() {
			t.Error("two stateless instances produced the same certificate")
		}
	})

	t.Run("the private key is not world readable", func(t *testing.T) {
		info, serr := os.Stat(filepath.Join(state, selfSignedDir, selfSignedKey))
		if serr != nil {
			t.Fatal(serr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("key mode = %v, want owner-only", info.Mode().Perm())
		}
	})
}

// TestConfiguredHostsReachTheFallbackCertificate locks server.tls.hosts:
// an instance reached by its service name needs that name in the
// generated certificate, since there is no CA to reissue from.
func TestConfiguredHostsReachTheFallbackCertificate(t *testing.T) {
	sc, err := NewServerCert(config.ServerTLS{Enabled: true, Hosts: []string{"tobby.example.com", "192.0.2.10"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	leaf := parseLeaf(t, sc)
	if err := leaf.VerifyHostname("tobby.example.com"); err != nil {
		t.Errorf("configured DNS name missing: %v", err)
	}
	if err := leaf.VerifyHostname("192.0.2.10"); err != nil {
		t.Errorf("configured IP missing: %v", err)
	}
}

// TestCertificateReplacementTakesEffect is the third FR-082 acceptance
// clause: replacing the certificate is possible through configuration.
// The files are the configuration, and rewriting them is picked up on the
// next handshake — an instance an operator cannot restart on demand is
// exactly the one whose certificate is about to expire.
func TestCertificateReplacementTakesEffect(t *testing.T) {
	dir := t.TempDir()
	caPEM, certPath, keyPath, first := writeServerPair(t, dir)

	sc, err := NewServerCert(config.ServerTLS{CertFile: certPath, KeyFile: keyPath}, "")
	if err != nil {
		t.Fatal(err)
	}
	if body := serveAndFetch(t, sc, caPEM); body != "ok" {
		t.Fatalf("initial fetch: %q", body)
	}

	// A different authority entirely: the replacement is not a renewal of
	// the same chain, so a client still trusting the old one must fail.
	newCA, _, _, second := writeServerPair(t, dir)
	if first == second {
		t.Fatal("the replacement certificate is identical to the first")
	}
	if body := serveAndFetch(t, sc, newCA); body != "ok" {
		t.Fatalf("after replacement: %q", body)
	}
	if sc.Fingerprint() != second {
		t.Errorf("Fingerprint() = %s, want the replacement %s", sc.Fingerprint(), second)
	}
}

// TestBrokenReplacementKeepsTheRunningCertificate locks the operational
// safety of that reload: a deployment tool caught mid-write must not take
// the listener down. The previous certificate is still valid; the next
// handshake tries again.
func TestBrokenReplacementKeepsTheRunningCertificate(t *testing.T) {
	dir := t.TempDir()
	caPEM, certPath, keyPath, want := writeServerPair(t, dir)
	sc, err := NewServerCert(config.ServerTLS{CertFile: certPath, KeyFile: keyPath}, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(certPath, []byte("-----BEGIN CERTIFICATE-----\ntruncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body := serveAndFetch(t, sc, caPEM); body != "ok" {
		t.Errorf("a half-written certificate file took the listener down: %q", body)
	}
	if sc.Fingerprint() != want {
		t.Errorf("Fingerprint() = %s, want the last good %s", sc.Fingerprint(), want)
	}
}

// TestUnusableCertificateRefusesToServe locks the startup refusal: a
// supplied pair that cannot be used is an error naming the file, never a
// silent fall back to a self-signed certificate — an operator who handed
// over a certificate must be told it was not used.
func TestUnusableCertificateRefusesToServe(t *testing.T) {
	dir := t.TempDir()
	_, certPath, keyPath, _ := writeServerPair(t, dir)
	otherDir := t.TempDir()
	_, _, otherKey, _ := writeServerPair(t, otherDir)

	cases := []struct {
		name     string
		cert     string
		key      string
		expectIn string
	}{
		{"missing certificate", filepath.Join(dir, "absent.crt"), keyPath, "absent.crt"},
		{"missing key", certPath, filepath.Join(dir, "absent.key"), certPath},
		{"mismatched pair", certPath, otherKey, certPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewServerCert(config.ServerTLS{CertFile: tc.cert, KeyFile: tc.key}, "")
			if err == nil {
				t.Fatal("the pair was accepted")
			}
			var te *taxonomy.Error
			if !errors.As(err, &te) || te.Code() != taxonomy.CodeServerTLS {
				t.Fatalf("error = %v, want %s", err, taxonomy.CodeServerTLS)
			}
			if !strings.Contains(te.Error(), tc.expectIn) {
				t.Errorf("error = %v, want it to name %s", te, tc.expectIn)
			}
		})
	}
}

// TestFingerprintMatchesOpenSSLForm locks the rendering: an operator
// compares Tobby's line against `openssl x509 -fingerprint -sha256`
// without converting anything.
func TestFingerprintMatchesOpenSSLForm(t *testing.T) {
	// SHA-256 of the empty DER input, in the form openssl prints.
	got := Fingerprint(nil)
	const want = "E3:B0:C4:42:98:FC:1C:14:9A:FB:F4:C8:99:6F:B9:24:27:AE:41:E4:64:9B:93:4C:A4:95:99:1B:78:52:B8:55"
	if got != want {
		t.Errorf("Fingerprint(nil) = %s, want %s", got, want)
	}
}

// parseLeaf returns the served certificate's parsed leaf.
func parseLeaf(t *testing.T, sc *ServerCert) *x509.Certificate {
	t.Helper()
	pair, err := sc.getCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// serveAndFetch starts a listener with sc and fetches it with a client
// trusting caPEM and nothing else — the only way to assert what the
// listener actually presented rather than what it holds in memory.
//
// The listener is assembled the way serve.go assembles it (a TCP listener
// wrapped in tls.NewListener with the ServerCert's configuration) rather
// than through httptest's TLS helper, which would install a certificate
// of its own and answer a connection made to an IP — no SNI, no call to
// GetCertificate — with that one instead.
func serveAndFetch(t *testing.T, sc *ServerCert, caPEM []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, sc.TLSConfig())) }()
	defer func() { _ = srv.Close() }()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("the test client trust store is empty")
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+ln.Addr().String(), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close() //nolint:errcheck // test read side
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// writeServerPair issues a fresh authority and a leaf for host, writes the
// pair into dir, and returns the authority PEM, the two paths, and the
// leaf's fingerprint.
func writeServerPair(t *testing.T, dir string) (caPEM []byte, certPath, keyPath, fingerprint string) {
	t.Helper()
	// The pair is always issued for the loopback names: every assertion
	// here fetches the listener over 127.0.0.1.
	const host = "localhost"
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "server pair authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
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
	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
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

	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	// The stamp is modification time and size; two pairs written inside
	// the same filesystem timestamp granularity would look unchanged.
	// A real replacement never lands in the same nanosecond, and the
	// test must not depend on the filesystem's clock resolution.
	touchLater(t, certPath, keyPath)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certPath, keyPath, Fingerprint(leafDER)
}

// touchLater pushes the files' modification time forward so a replacement
// is detectable regardless of the filesystem's timestamp granularity.
func touchLater(t *testing.T, paths ...string) {
	t.Helper()
	when := time.Now().Add(time.Duration(len(paths)) * time.Second)
	for _, p := range paths {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
}
