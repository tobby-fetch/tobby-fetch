// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package tlsadmin is the administration side of an instance's network
// edges (FR-082): what the listener actually presents, what the outbound
// posture actually is, and how an administrator replaces the served pair
// from the interface rather than from the configuration file.
//
// It is its own package because both administration surfaces need it —
// the /admin/network screen and its /api/v1/network mirror (FR-061) — and
// two copies of PEM validation is how two surfaces end up refusing
// different things. internal/netx owns the runtime edges; this package
// owns what an administrator is allowed to see and change about them, and
// deliberately reaches netx only through the small interfaces below.
//
// One rule governs the whole package: a private key is a secret
// (NFR-015). Nothing here returns, renders, logs, or fingerprints key
// material. [Replace] consumes the submitted bytes, writes them, and
// forgets them; what comes back out is the certificate's own public
// identity — the very bytes the listener hands to every client that
// connects, which is why publishing them on an admin screen leaks
// nothing. The key has no accessor at all, on purpose: an accessor that
// exists is an accessor a later surface can call.
package tlsadmin

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// MaxPEMBytes bounds one submitted PEM document. A certificate chain and
// a private key are small; the bound exists so a hostile upload cannot
// make the instance read an arbitrary amount into memory before the
// first check runs.
const MaxPEMBytes = 1 << 20

// ServerCert is the served certificate, as the administration surfaces
// read it. *netx.ServerCert satisfies it; the interface exists so this
// package — and the two surfaces above it — stay testable without a
// listener, and so that nothing here can reach a netx internal.
type ServerCert interface {
	// Fingerprint is the SHA-256 of the served DER, colon-separated hex.
	Fingerprint() string
	// SelfSigned reports the generated fallback rather than an
	// administrator-supplied pair — a posture, not a detail (FR-082).
	SelfSigned() bool
	// Source names where the certificate came from.
	Source() string
	// TLSConfig is the listener configuration; its GetCertificate is the
	// only way to reach the pair currently served.
	TLSConfig() *tls.Config
	// Destination reports where a replacement pair must be written for
	// this listener to serve it, and whether replacement is possible at
	// all. An instance running the generated fallback has no configured
	// path, so it offers one in its state directory; an instance with no
	// state directory can offer nothing.
	Destination() (certPath, keyPath string, ok bool)
	// Adopt makes the pair now at those paths the one presented, from the
	// next handshake on. Without it a replacement written beside a
	// listener that never re-reads anything would be a silent no-op.
	Adopt() error
}

// Egress is the outbound posture. *netx.Egress satisfies it. Every method
// is one of netx's deliberately printable accessors: there is no path
// from a proxy credential to anything this interface exposes (FR-080).
type Egress interface {
	ProxyURL() string
	ProxyAuthenticated() bool
	ExtraRoots() int
	ExclusiveTrust() bool
	Describe() string
}

// Certificate is the public identity of the served certificate.
//
// Every field is something the listener already hands to any client that
// completes a handshake, which is what makes reporting it on an admin
// surface safe. There is no field for the key, and none derived from it.
type Certificate struct {
	// SelfSigned marks the generated fallback: a degraded posture that
	// must never read like a configured one (FR-082, FR-075 spirit).
	SelfSigned bool `json:"self_signed"`
	// Source is the configured path, or the generated marker.
	Source string `json:"source"`
	// Fingerprint is what an operator pins and compares.
	Fingerprint string `json:"fingerprint_sha256"`
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	// DNSNames and IPAddresses are the subject alternative names: the
	// answer to "why does my client say the name does not match".
	DNSNames    []string  `json:"dns_names"`
	IPAddresses []string  `json:"ip_addresses"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
}

// ExpiredAt reports whether the certificate is already past its
// validity. Named for the clock it takes rather than as a bare Expired,
// so a caller that decorates it with a field of its own cannot end up
// shadowing a method that needs one.
func (c *Certificate) ExpiredAt(now time.Time) bool { return now.After(c.NotAfter) }

// Outbound is the printable outbound posture (FR-080, FR-081).
type Outbound struct {
	// Proxy is the configured proxy in the only form it has: without
	// credentials. Empty means direct egress.
	Proxy string `json:"proxy,omitempty"`
	// ProxyAuthenticated is the fact that credentials are configured,
	// never the value (NFR-015).
	ProxyAuthenticated bool `json:"proxy_authenticated"`
	// ExtraRoots counts the authorities added on top of the host store.
	ExtraRoots int `json:"extra_roots"`
	// ExclusiveTrust reports that the public root store was dropped.
	ExclusiveTrust bool `json:"exclusive_trust"`
	// Summary is netx's own one-line description, the same string the
	// startup log carries — so an operator comparing the screen with the
	// logs compares identical text.
	Summary string `json:"summary"`
}

// Describe reads the certificate the listener would present right now.
//
// It goes through GetCertificate rather than around it for two reasons:
// that callback is the only accessor netx exposes onto the parsed pair,
// and it is also the hook that picks up a pair replaced on disk — so
// reading the screen reports what is served now, not what was loaded at
// startup. A nil ClientHello is honest here: one listener carries the UI,
// the API and the embedded registry (ADR-0015), so netx serves one
// certificate and ignores the hello entirely.
//
// A nil ServerCert means the instance serves plain HTTP; the caller gets
// (nil, nil) and says so on screen rather than inventing a certificate.
func Describe(c ServerCert) (*Certificate, error) {
	if c == nil {
		return nil, nil
	}
	pair, err := c.TLSConfig().GetCertificate(nil)
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeServerTLS,
			taxonomy.Params{"source": c.Source()}).WithCause(err)
	}
	if pair == nil || len(pair.Certificate) == 0 {
		return nil, taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": c.Source()}).
			WithCause(errors.New("the listener holds no certificate"))
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeServerTLS,
			taxonomy.Params{"source": c.Source()}).WithCause(err)
	}
	out := describeLeaf(leaf)
	out.SelfSigned = c.SelfSigned()
	out.Source = c.Source()
	out.Fingerprint = c.Fingerprint()
	return out, nil
}

// DescribeEgress renders the outbound posture. A nil Egress means the
// instance was wired without one.
func DescribeEgress(e Egress) *Outbound {
	if e == nil {
		return nil
	}
	return &Outbound{
		Proxy:              e.ProxyURL(),
		ProxyAuthenticated: e.ProxyAuthenticated(),
		ExtraRoots:         e.ExtraRoots(),
		ExclusiveTrust:     e.ExclusiveTrust(),
		Summary:            e.Describe(),
	}
}

// describeLeaf projects an x509 certificate onto its public identity.
func describeLeaf(leaf *x509.Certificate) *Certificate {
	out := &Certificate{
		Subject:     leaf.Subject.String(),
		Issuer:      leaf.Issuer.String(),
		DNSNames:    append([]string(nil), leaf.DNSNames...),
		IPAddresses: make([]string, 0, len(leaf.IPAddresses)),
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
	}
	for _, ip := range leaf.IPAddresses {
		out.IPAddresses = append(out.IPAddresses, ip.String())
	}
	return out
}

// Replace installs an administrator-supplied pair as the instance's
// listener certificate (FR-082: replacement is possible "via
// configuration and the admin UI").
//
// The replacement is the configured pair, which is the only pair the
// listener re-reads: netx watches certFile/keyFile and reloads them on
// the next handshake. An instance running on the self-signed fallback has
// no configured pair, and is refused here rather than accepting bytes
// nothing would ever read.
//
// Nothing touches the disk until every check has passed, and each file is
// installed by rename — so no reader can observe a half-written PEM.
// Between the two renames the pair is briefly mismatched; netx's reload
// fails on that state and KEEPS THE CERTIFICATE IT HAS, which is what
// makes a replacement safe on a serving instance: the worst case is one
// handshake served with the previous certificate.
//
// The returned Certificate is built from the validated bytes, so the
// caller can audit and display the new identity without re-reading the
// files it just wrote.
func Replace(certFile, keyFile string, certPEM, keyPEM []byte, now time.Time) (*Certificate, error) {
	refuse := func(detail string) error {
		return taxonomy.New(taxonomy.CodeServerCertReplace, taxonomy.Params{"detail": detail})
	}
	if certFile == "" || keyFile == "" {
		return nil, refuse("this instance has no configured certificate pair to replace: it serves the self-signed fallback, which lives in the state directory and is regenerated rather than administered")
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, refuse("both a certificate and its private key are required")
	}
	if len(certPEM) > MaxPEMBytes || len(keyPEM) > MaxPEMBytes {
		return nil, refuse(fmt.Sprintf("a submitted document exceeds the %d-byte bound on PEM material", MaxPEMBytes))
	}
	// One call proves three things at once: both documents are PEM, the
	// certificate parses, and the key is the certificate's key. A pair
	// that fails here is refused before any file is opened.
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// err quotes the parser, never the material: crypto/tls reports
		// structural failures ("failed to find any PEM data", "private
		// key does not match public key") without echoing bytes.
		return nil, refuse("the submitted pair does not hold together: " + err.Error())
	}
	if len(pair.Certificate) == 0 {
		return nil, refuse("the submitted PEM holds no certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, refuse("the submitted certificate does not parse: " + err.Error())
	}
	// An expired certificate is refused rather than installed: it would
	// be rejected by every client, and swapping a working certificate for
	// one that cannot work is the one outcome this screen exists to
	// prevent.
	if now.After(leaf.NotAfter) {
		return nil, refuse("the submitted certificate expired on " + leaf.NotAfter.UTC().Format(time.RFC3339))
	}

	if err := writePEM(certFile, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := writePEM(keyFile, keyPEM, 0o600); err != nil {
		return nil, err
	}

	out := describeLeaf(leaf)
	out.Source = certFile
	// netx renders the fingerprint the operator compares; calling its
	// renderer rather than a second one is what keeps the screen and the
	// startup log from ever disagreeing on the same certificate.
	out.Fingerprint = netx.Fingerprint(pair.Certificate[0])
	return out, nil
}

// writePEM installs one document atomically: a temporary file in the
// destination's own directory (so the rename stays on one filesystem),
// flushed before it is renamed over the target. fallback is the mode used
// when the target does not exist yet; an existing file keeps the mode the
// operator gave it, because a deployment that made the certificate
// world-readable on purpose must not silently lose that.
func writePEM(path string, data []byte, fallback os.FileMode) error {
	fail := func(err error) error {
		return taxonomy.New(taxonomy.CodeServerCertReplace,
			taxonomy.Params{"detail": "writing " + path + ": " + err.Error()}).WithCause(err)
	}
	mode := fallback
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tobby-tls-*")
	if err != nil {
		return fail(err)
	}
	name := tmp.Name()
	// Best effort: on any failure below the temporary file must not
	// survive as an unreferenced copy of key material.
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	// Sync before rename: a crash between the two must not leave the
	// configured path pointing at an empty file.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(name, path); err != nil {
		return fail(err)
	}
	return nil
}
