// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package netx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestProxySelection locks which destinations go through the proxy: both
// schemes, the https-specific override, and the noProxy exemptions in the
// standard forms (exact host, suffix, CIDR).
func TestProxySelection(t *testing.T) {
	eg, err := New(&config.Network{Proxy: config.Proxy{
		URL:      "http://proxy.example.com:3128",
		HTTPSURL: "http://secure-proxy.example.com:3129",
		NoProxy:  []string{"internal.example.com", ".corp.example.com", "10.0.0.0/8"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		target string
		want   string
	}{
		{"http://registry.example.com/v2/", "http://proxy.example.com:3128"},
		{"https://registry.example.com/v2/", "http://secure-proxy.example.com:3129"},
		{"https://internal.example.com/v2/", ""},
		{"https://sub.corp.example.com/v2/", ""},
		{"https://10.1.2.3/v2/", ""},
		{"https://11.1.2.3/v2/", "http://secure-proxy.example.com:3129"},
	}
	for _, tc := range cases {
		req, rerr := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.target, http.NoBody)
		if rerr != nil {
			t.Fatal(rerr)
		}
		got, perr := eg.proxyFor(req)
		if perr != nil {
			t.Fatalf("%s: %v", tc.target, perr)
		}
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("%s went through %s, want direct", tc.target, got)
		case tc.want != "" && got == nil:
			t.Errorf("%s went direct, want %s", tc.target, tc.want)
		case tc.want != "" && got.Scheme+"://"+got.Host != tc.want:
			t.Errorf("%s went through %s://%s, want %s", tc.target, got.Scheme, got.Host, tc.want)
		}
	}
}

// TestSingleProxyServesBothSchemes locks the ordinary enterprise shape:
// one CONNECT-capable proxy configured once, honored for plain HTTP and
// for TLS alike (FR-080 — "HTTP and HTTPS forward proxies").
func TestSingleProxyServesBothSchemes(t *testing.T) {
	eg, err := New(&config.Network{Proxy: config.Proxy{URL: "http://proxy.example.com:3128"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://a.example.com/x", "https://b.example.com/x"} {
		req, rerr := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
		if rerr != nil {
			t.Fatal(rerr)
		}
		u, perr := eg.proxyFor(req)
		if perr != nil || u == nil {
			t.Fatalf("%s: proxy = %v, err = %v", target, u, perr)
		}
	}
}

// TestProxyCredentialsAreAttachedButNeverPrintable is the FR-080
// acceptance criterion the requirement states in so many words: the proxy
// is authenticated, and the credentials never appear in anything anyone
// can read.
//
// The transport must receive them — otherwise the proxy answers 407 — and
// nothing else must: not Describe, not the proxy URL accessor, not the
// %v/%+v rendering of the configuration, and not the YAML dump.
func TestProxyCredentialsAreAttachedButNeverPrintable(t *testing.T) {
	const password = "correct-horse-battery-staple"
	cfg := &config.Network{Proxy: config.Proxy{
		URL:      "http://proxy.example.com:3128",
		Username: "tobby",
		Password: config.NewSecret(password),
	}}
	eg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://registry.example.com/v2/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	u, err := eg.proxyFor(req)
	if err != nil || u == nil {
		t.Fatalf("proxy = %v, err = %v", u, err)
	}
	if pw, ok := u.User.Password(); !ok || pw != password {
		t.Fatal("the transport was not given the proxy password: an authenticated proxy would answer 407")
	}

	// Everything a human or a log record can reach.
	printable := map[string]string{
		"Describe()":    eg.Describe(),
		"ProxyURL()":    eg.ProxyURL(),
		"%v of config":  strings.TrimSpace(sprintf("%v", cfg)),
		"%+v of config": strings.TrimSpace(sprintf("%+v", cfg)),
		"%#v of config": strings.TrimSpace(sprintf("%#v", cfg)),
		"%s of secret":  sprintf("%s", cfg.Proxy.Password),
		"%v of proxyURL after auth": func() string {
			// url.URL.String() would print the password in the clear;
			// Redacted is the form any code that logs a proxy URL must
			// use, and the reason the plain form is never stored.
			return u.Redacted()
		}(),
	}
	for where, s := range printable {
		if strings.Contains(s, password) {
			t.Errorf("%s leaked the proxy password: %q", where, s)
		}
	}
	if !eg.ProxyAuthenticated() {
		t.Error("ProxyAuthenticated() = false, want true: the fact must be reportable even though the value is not")
	}
	if !strings.Contains(eg.Describe(), config.Redacted) {
		t.Errorf("Describe() = %q, want the credential marked as redacted", eg.Describe())
	}
}

// TestConfigDumpRedactsTheProxyPassword is the same guarantee on the
// surface an operator actually runs (`tobby config dump`), which is where
// the FR-080 acceptance criterion is verified in practice.
func TestConfigDumpRedactsTheProxyPassword(t *testing.T) {
	const password = "do-not-print-me"
	cfg := config.Default()
	cfg.Mode = config.ModePassthrough
	cfg.Network.Proxy = config.Proxy{
		URL:      "http://proxy.example.com:3128",
		Username: "tobby",
		Password: config.NewSecret(password),
	}
	dump, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, password) {
		t.Fatalf("`tobby config dump` leaked the proxy password:\n%s", dump)
	}
	if !strings.Contains(dump, config.Redacted) {
		t.Errorf("the dump does not mark the password as redacted:\n%s", dump)
	}
	if !strings.Contains(dump, "proxy.example.com") {
		t.Errorf("the dump hides the proxy URL, which is what an operator needs to see:\n%s", dump)
	}
}

// TestTrustStoreAcceptsAPrivateAuthority locks FR-081's mechanism: a
// configured authority lands in the pool, from a file and inline alike,
// and adds to the host store rather than replacing it.
func TestTrustStoreAcceptsAPrivateAuthority(t *testing.T) {
	caPEM := selfSignedCAPEM(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("from a file", func(t *testing.T) {
		eg, err := New(&config.Network{TLS: config.ClientTLS{CAFiles: []string{path}}})
		if err != nil {
			t.Fatal(err)
		}
		if eg.ExtraRoots() != 1 {
			t.Errorf("ExtraRoots() = %d, want 1", eg.ExtraRoots())
		}
		if eg.ExclusiveTrust() {
			t.Error("ExclusiveTrust() = true: a configured authority adds to the host store by default")
		}
	})

	t.Run("inline", func(t *testing.T) {
		eg, err := New(&config.Network{TLS: config.ClientTLS{CA: string(caPEM)}})
		if err != nil {
			t.Fatal(err)
		}
		if eg.ExtraRoots() != 1 {
			t.Errorf("ExtraRoots() = %d, want 1", eg.ExtraRoots())
		}
	})

	t.Run("exclusive trust drops the host store", func(t *testing.T) {
		eg, err := New(&config.Network{TLS: config.ClientTLS{CA: string(caPEM), ExclusiveTrust: true}})
		if err != nil {
			t.Fatal(err)
		}
		if !eg.ExclusiveTrust() {
			t.Error("ExclusiveTrust() = false")
		}
		if !strings.Contains(eg.Describe(), "only") {
			t.Errorf("Describe() = %q, want the narrowed trust stated", eg.Describe())
		}
	})
}

// TestTrustStoreRefusesUselessInput locks the loud-failure half: a CA
// bundle that cannot be read or that contributes nothing is a startup
// refusal with a stable code, not a silent no-op that turns into a
// handshake error blaming the peer.
func TestTrustStoreRefusesUselessInput(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	caPEM := selfSignedCAPEM(t)
	twice := filepath.Join(dir, "twice.pem")
	if err := os.WriteFile(twice, append(append([]byte{}, caPEM...), caPEM...), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		cfg  config.ClientTLS
	}{
		{"missing file", config.ClientTLS{CAFiles: []string{filepath.Join(dir, "absent.pem")}}},
		{"no PEM block", config.ClientTLS{CAFiles: []string{empty}}},
		{"inline garbage", config.ClientTLS{CA: "-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(&config.Network{TLS: tc.cfg})
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			var te *taxonomy.Error
			if !asTaxonomy(err, &te) || te.Code() != taxonomy.CodeTrustStore {
				t.Fatalf("error = %v, want %s", err, taxonomy.CodeTrustStore)
			}
		})
	}

	// A bundle listing the same authority twice adds it once; that is not
	// an error, only a smaller contribution than the file suggests.
	eg, err := New(&config.Network{TLS: config.ClientTLS{CAFiles: []string{twice}}})
	if err != nil {
		t.Fatalf("a bundle repeating one authority was refused: %v", err)
	}
	if eg.ExtraRoots() != 1 {
		t.Errorf("ExtraRoots() = %d, want 1", eg.ExtraRoots())
	}
}

// TestDirectEgressIsAReusableObject locks the fallback's shape: it exists,
// it is shared, and Or turns a nil into it without any caller inventing a
// transport of its own.
func TestDirectEgressIsAReusableObject(t *testing.T) {
	first, second := Direct(), Direct()
	if first != second {
		t.Error("Direct() returns a new transport per call: connection pools would multiply")
	}
	if Or(nil) != Direct() {
		t.Error("Or(nil) is not the direct egress")
	}
	eg, err := New(&config.Network{})
	if err != nil {
		t.Fatal(err)
	}
	if Or(eg) != eg {
		t.Error("Or dropped a configured egress")
	}
	if Direct().ProxyURL() != "" || Direct().ProxyAuthenticated() {
		t.Error("the direct egress reports a proxy")
	}
	if got := Direct().Describe(); !strings.Contains(got, "direct egress") {
		t.Errorf("Describe() = %q", got)
	}
	if Direct().Client(time.Second).Transport != Direct().RoundTripper() {
		t.Error("Client does not use the shared transport")
	}
	Direct().CloseIdleConnections()
}

// TestTLSVerificationIsNeverDisabled is the structural half of FR-081's
// acceptance ("there is no global skip TLS verify switch"): whatever the
// network configuration says, the transport verifies.
func TestTLSVerificationIsNeverDisabled(t *testing.T) {
	for _, cfg := range []*config.Network{
		{},
		{Proxy: config.Proxy{URL: "http://proxy.example.com:3128"}},
		{TLS: config.ClientTLS{CA: string(selfSignedCAPEM(t))}},
		{TLS: config.ClientTLS{CA: string(selfSignedCAPEM(t)), ExclusiveTrust: true}},
	} {
		eg, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		tlsCfg := eg.transport.TLSClientConfig
		if tlsCfg.InsecureSkipVerify {
			t.Fatalf("%+v produced a transport that skips certificate verification", cfg)
		}
		if tlsCfg.MinVersion < 0x0303 { // TLS 1.2
			t.Errorf("%+v produced MinVersion %#x", cfg, tlsCfg.MinVersion)
		}
	}
}

// TestProxyURLIsValidatedAtStartup locks the refusal: a proxy that cannot
// be used must stop the instance, because the alternative — falling back
// to direct egress in a zone that drops it — hangs instead of failing.
func TestProxyURLIsValidatedAtStartup(t *testing.T) {
	_, err := New(&config.Network{Proxy: config.Proxy{URL: "http://proxy.example.com:3128/%zz"}})
	if err == nil {
		t.Fatal("an unparseable proxy URL was accepted")
	}
	var te *taxonomy.Error
	if !asTaxonomy(err, &te) || te.Code() != taxonomy.CodeProxyInvalid {
		t.Fatalf("error = %v, want %s", err, taxonomy.CodeProxyInvalid)
	}
}

// sprintf and asTaxonomy keep the assertions above readable; they exist
// so a leak check reads as a list of surfaces rather than a wall of fmt.
func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

func asTaxonomy(err error, target **taxonomy.Error) bool { return errors.As(err, target) }

// selfSignedCAPEM issues a throwaway root authority.
func selfSignedCAPEM(t *testing.T) []byte {
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
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "unit test authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
