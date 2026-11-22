// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package netx_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/tlsadmin"
)

// selfIssuedPair returns a PEM pair for the loopback names valid over
// [notBefore, notAfter], and its fingerprint.
func selfIssuedPair(t *testing.T, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, fingerprint string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		netx.Fingerprint(der)
}

// TestAdoptedPairSurvivesARestart is B-083: a pair the Network screen
// adopted on a self-signed instance (tlsadmin.Replace, then Adopt) is the
// one the next start serves — not the fallback the screen said it had
// replaced.
func TestAdoptedPairSurvivesARestart(t *testing.T) {
	state := t.TempDir()
	tlsCfg := config.ServerTLS{Enabled: true}
	first, err := netx.NewServerCert(tlsCfg, state)
	if err != nil {
		t.Fatal(err)
	}
	if !first.SelfSigned() {
		t.Fatal("want the generated fallback as the starting point")
	}
	fallback := first.Fingerprint()

	certPEM, keyPEM, want := selfIssuedPair(t, time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour))
	certPath, keyPath, ok := first.Destination()
	if !ok {
		t.Fatal("no destination on an instance with a state directory")
	}
	if _, err := tlsadmin.Replace(certPath, keyPath, certPEM, keyPEM, time.Now()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := first.Adopt(); err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	// The restart.
	again, err := netx.NewServerCert(tlsCfg, state)
	if err != nil {
		t.Fatal(err)
	}
	if again.SelfSigned() {
		t.Error("the restarted instance reports the self-signed fallback")
	}
	if got := again.Fingerprint(); got != want || got == fallback {
		t.Errorf("fingerprint after restart = %s, want the adopted %s (fallback %s)", got, want, fallback)
	}
	if !strings.HasPrefix(again.Source(), "adopted (") {
		t.Errorf("Source = %q, want it to name the adopted pair", again.Source())
	}
	// Re-read on change, like a configured pair: the destination is
	// still the adopted path, so the next replacement lands there.
	if c, k, ok := again.Destination(); !ok || c != certPath || k != keyPath {
		t.Errorf("Destination after restart = %s, %s, %v; want %s, %s", c, k, ok, certPath, keyPath)
	}
	served, err := again.TLSConfig().GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if netx.Fingerprint(served.Certificate[0]) != want {
		t.Error("the listener does not present the adopted pair")
	}
}

// TestExpiredAdoptedPairFallsBack: an adopted pair that has expired since
// is not served at the next start — the fallback is, and the source says
// why the adopted pair was passed over.
func TestExpiredAdoptedPairFallsBack(t *testing.T) {
	state := t.TempDir()
	tlsCfg := config.ServerTLS{Enabled: true}
	first, err := netx.NewServerCert(tlsCfg, state)
	if err != nil {
		t.Fatal(err)
	}
	fallback := first.Fingerprint()
	certPath, keyPath, _ := first.Destination()
	certPEM, keyPEM, expired := selfIssuedPair(t, time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour))
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	again, err := netx.NewServerCert(tlsCfg, state)
	if err != nil {
		t.Fatal(err)
	}
	if !again.SelfSigned() {
		t.Error("an expired adopted pair was served")
	}
	if got := again.Fingerprint(); got == expired || got != fallback {
		t.Errorf("fingerprint = %s, want the persisted fallback %s", got, fallback)
	}
	if !strings.Contains(again.Source(), "expired") {
		t.Errorf("Source = %q, want the reason the adopted pair was skipped", again.Source())
	}
}
