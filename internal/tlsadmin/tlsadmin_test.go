// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tlsadmin

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

var now = time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)

// pair generates a self-contained certificate/key pair for the tests.
func pair(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{cn, "localhost"},
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

// fakeCert is a ServerCert over a fixed pair — the seam that lets these
// tests run without a listener.
type fakeCert struct {
	destCert, destKey string
	adopted           bool
	pair              *tls.Certificate
	err               error
	selfSigned        bool
	source            string
}

func (f *fakeCert) Fingerprint() string { return "AA:BB" }
func (f *fakeCert) SelfSigned() bool    { return f.selfSigned }
func (f *fakeCert) Source() string      { return f.source }
func (f *fakeCert) TLSConfig() *tls.Config {
	return &tls.Config{ //nolint:gosec // G402: a stub for the accessor under test; no handshake happens here
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return f.pair, f.err },
	}
}

func loadPair(t *testing.T, certPEM, keyPEM []byte) *tls.Certificate {
	t.Helper()
	p, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &p
}

// codeOf extracts the stable taxonomy code of a refusal.
func codeOf(t *testing.T, err error) taxonomy.Code {
	t.Helper()
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not taxonomized", err)
	}
	return te.Code()
}

// TestDescribeReportsThePublicIdentity: the screen's whole certificate
// section comes from here, and every field is one the listener already
// hands to any client (NFR-015 reasoning of the package doc).
func TestDescribeReportsThePublicIdentity(t *testing.T) {
	certPEM, keyPEM := pair(t, "tobby.example.com", now.Add(365*24*time.Hour))
	fc := &fakeCert{pair: loadPair(t, certPEM, keyPEM), selfSigned: true, source: "self-signed (generated)"}

	got, err := Describe(fc)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SelfSigned {
		t.Error("SelfSigned did not travel: a generated certificate must never read as a supplied one")
	}
	if got.Source != "self-signed (generated)" || got.Fingerprint != "AA:BB" {
		t.Errorf("source=%q fingerprint=%q", got.Source, got.Fingerprint)
	}
	if !strings.Contains(got.Subject, "tobby.example.com") {
		t.Errorf("subject = %q", got.Subject)
	}
	if len(got.DNSNames) != 2 || got.DNSNames[0] != "tobby.example.com" {
		t.Errorf("dns names = %v", got.DNSNames)
	}
	if len(got.IPAddresses) != 1 || got.IPAddresses[0] != "127.0.0.1" {
		t.Errorf("ip addresses = %v", got.IPAddresses)
	}
	if got.ExpiredAt(now) {
		t.Error("a certificate valid for a year reads as expired")
	}
	if !got.ExpiredAt(now.Add(400 * 24 * time.Hour)) {
		t.Error("an expired certificate does not read as expired")
	}
}

// TestDescribeNilIsPlainHTTP: an instance with no certificate is not an
// error, it is a posture — the caller says so on screen.
func TestDescribeNilIsPlainHTTP(t *testing.T) {
	got, err := Describe(nil)
	if got != nil || err != nil {
		t.Errorf("Describe(nil) = %v, %v; want nil, nil", got, err)
	}
	if DescribeEgress(nil) != nil {
		t.Error("DescribeEgress(nil) must be nil")
	}
}

// TestDescribeFailureIsTaxonomized: an unreadable pair reaches the screen
// as its catalog entry, not as a bare string.
func TestDescribeFailureIsTaxonomized(t *testing.T) {
	for name, fc := range map[string]*fakeCert{
		"callback error": {err: errors.New("boom"), source: "/etc/tobby/tls.crt"},
		"no certificate": {pair: &tls.Certificate{}, source: "/etc/tobby/tls.crt"},
		"unparseable":    {pair: &tls.Certificate{Certificate: [][]byte{{0x01, 0x02}}}, source: "/etc/tobby/tls.crt"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Describe(fc); codeOf(t, err) != taxonomy.CodeServerTLS {
				t.Errorf("code = %s, want %s", codeOf(t, err), taxonomy.CodeServerTLS)
			}
		})
	}
}

// fakeEgress is an Egress over fixed printable state.
type fakeEgress struct{}

func (fakeEgress) ProxyURL() string         { return "http://proxy.example.com:3128" }
func (fakeEgress) ProxyAuthenticated() bool { return true }
func (fakeEgress) ExtraRoots() int          { return 2 }
func (fakeEgress) ExclusiveTrust() bool     { return true }
func (fakeEgress) Describe() string         { return "proxy http://proxy.example.com:3128" }

// TestDescribeEgressCarriesTheFactNotTheValue: the outbound report says
// that credentials exist and never what they are (FR-080, NFR-015).
func TestDescribeEgressCarriesTheFactNotTheValue(t *testing.T) {
	got := DescribeEgress(fakeEgress{})
	if got.Proxy != "http://proxy.example.com:3128" || !got.ProxyAuthenticated {
		t.Fatalf("egress = %+v", got)
	}
	if got.ExtraRoots != 2 || !got.ExclusiveTrust {
		t.Errorf("trust posture lost: %+v", got)
	}
	if strings.Contains(got.Proxy, "@") {
		t.Error("the reported proxy URL carries userinfo")
	}
}

// TestReplaceInstallsTheConfiguredPair is the FR-082 admin-UI half: the
// submitted pair lands on the configured paths, byte for byte, and the
// reported identity is the one that was installed.
func TestReplaceInstallsTheConfiguredPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	oldCert, oldKey := pair(t, "old.example.com", now.Add(24*time.Hour))
	if err := os.WriteFile(certPath, oldCert, 0o644); err != nil { //nolint:gosec // G306: the certificate is public by definition
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, oldKey, 0o600); err != nil {
		t.Fatal(err)
	}

	newCert, newKey := pair(t, "new.example.com", now.Add(365*24*time.Hour))
	got, err := Replace(certPath, keyPath, newCert, newKey, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Subject, "new.example.com") {
		t.Errorf("reported subject = %q", got.Subject)
	}
	if got.Fingerprint == "" || !strings.Contains(got.Fingerprint, ":") {
		t.Errorf("fingerprint = %q, want the colon-separated hex form operators compare", got.Fingerprint)
	}
	onDisk, err := os.ReadFile(certPath) //nolint:gosec // G304: the path is the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, newCert) {
		t.Error("the installed certificate is not the submitted bytes")
	}
	// The key keeps the restrictive mode the previous file carried: a
	// replacement must not widen the permissions of key material.
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestReplaceRefusesWithoutWriting is the property the screen exists to
// keep: every refusal happens before the first byte reaches the disk, so
// the listener keeps serving what it had.
func TestReplaceRefusesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	good, goodKey := pair(t, "good.example.com", now.Add(24*time.Hour))
	if err := os.WriteFile(certPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, goodKey, 0o600); err != nil {
		t.Fatal(err)
	}
	otherCert, otherKey := pair(t, "other.example.com", now.Add(24*time.Hour))
	expiredCert, expiredKey := pair(t, "gone.example.com", now.Add(-time.Hour))

	for name, tc := range map[string]struct{ cert, key []byte }{
		"mismatched pair":   {otherCert, goodKey},
		"not PEM at all":    {[]byte("hello"), otherKey},
		"empty certificate": {nil, otherKey},
		"empty key":         {otherCert, nil},
		"expired":           {expiredCert, expiredKey},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Replace(certPath, keyPath, tc.cert, tc.key, now)
			if err == nil {
				t.Fatal("the pair was accepted")
			}
			if code := codeOf(t, err); code != taxonomy.CodeServerCertReplace {
				t.Errorf("code = %s, want %s", code, taxonomy.CodeServerCertReplace)
			}
			onDisk, rerr := os.ReadFile(certPath) //nolint:gosec // G304: the path is the test's own temporary directory
			if rerr != nil {
				t.Fatal(rerr)
			}
			if !bytes.Equal(onDisk, good) {
				t.Error("a refused replacement touched the served certificate")
			}
		})
	}
}

// TestReplaceRefusesOversizedMaterial: the bound is enforced before the
// crypto parser sees anything.
func TestReplaceRefusesOversizedMaterial(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	huge := make([]byte, MaxPEMBytes+1)
	_, err := Replace(certPath, keyPath, huge, huge, now)
	if err == nil || codeOf(t, err) != taxonomy.CodeServerCertReplace {
		t.Fatalf("oversized material = %v", err)
	}
}

// TestReplaceRefusedWithoutAConfiguredPair: an instance on the
// self-signed fallback has no path netx re-reads, so the replacement is
// refused with an actionable reason rather than writing somewhere nothing
// would look.
func TestReplaceRefusedWithoutAConfiguredPair(t *testing.T) {
	certPEM, keyPEM := pair(t, "x.example.com", now.Add(24*time.Hour))
	_, err := Replace("", "", certPEM, keyPEM, now)
	if err == nil {
		t.Fatal("a replacement was accepted with no configured pair")
	}
	if code := codeOf(t, err); code != taxonomy.CodeServerCertReplace {
		t.Errorf("code = %s, want %s", code, taxonomy.CodeServerCertReplace)
	}
	if !strings.Contains(taxonomy.Localize("en", errAs(t, err)).Cause, "self-signed") {
		t.Error("the refusal does not name the self-signed posture that causes it")
	}
}

// TestReplaceReportsAnUnwritableTarget: a directory that cannot be
// written is a taxonomized refusal, not a panic.
func TestReplaceReportsAnUnwritableTarget(t *testing.T) {
	certPEM, keyPEM := pair(t, "x.example.com", now.Add(24*time.Hour))
	missing := filepath.Join(t.TempDir(), "no-such-dir", "tls.crt")
	_, err := Replace(missing, missing+".key", certPEM, keyPEM, now)
	if err == nil || codeOf(t, err) != taxonomy.CodeServerCertReplace {
		t.Fatalf("unwritable target = %v", err)
	}
}

func errAs(t *testing.T, err error) *taxonomy.Error {
	t.Helper()
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not taxonomized", err)
	}
	return te
}

func (f *fakeCert) Destination() (certPath, keyPath string, ok bool) {
	return f.destCert, f.destKey, f.destCert != ""
}
func (f *fakeCert) Adopt() error { f.adopted = true; return nil }

// TestReplaceKeepsTheKeyOwnerOnly is the NFR-020 regression: a key file an
// operator once loosened must come back owner-only after a replacement.
// writePEM inherits the target's mode — right for the public certificate,
// wrong for the key — so the key goes through its own writer.
func TestReplaceKeepsTheKeyOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not carry the rule on Windows; the access list does")
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	oldCert, oldKey := pair(t, "old.example.com", now.Add(24*time.Hour))
	// Both targets pre-exist world-readable: the state an operator, an
	// installer, or a restored backup can leave behind.
	if err := os.WriteFile(certPath, oldCert, 0o644); err != nil { //nolint:gosec // G306: the certificate is public by definition
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, oldKey, 0o644); err != nil { //nolint:gosec // G306: deliberately loose — the point of the test
		t.Fatal(err)
	}

	newCert, newKey := pair(t, "new.example.com", now.Add(365*24*time.Hour))
	if _, err := Replace(certPath, keyPath, newCert, newKey, now); err != nil {
		t.Fatal(err)
	}

	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := keyInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("private key mode after replacement = %04o, want 0600", got)
	}
	// The certificate keeps the operator's choice: it is public by
	// construction, and silently narrowing it would break a deployment
	// that published it on purpose.
	certInfo, err := os.Stat(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := certInfo.Mode().Perm(); got != 0o644 {
		t.Errorf("certificate mode after replacement = %04o, want the 0644 it had", got)
	}
}
