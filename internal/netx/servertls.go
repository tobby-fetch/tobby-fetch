// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package netx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/secretfile"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// selfSignedValidity is how long a generated fallback certificate lives.
// One year: long enough that an operator is not woken by it, short enough
// that it is visibly a stopgap rather than a decision.
const selfSignedValidity = 365 * 24 * time.Hour

// Self-signed material lives beside the rest of the instance identity, in
// the state directory — never on the transportable store, where a private
// key must never travel (R-16, FR-054).
const (
	selfSignedDir  = "tls"
	selfSignedCert = "self-signed.crt"
	selfSignedKey  = "self-signed.key"
	// An adopted pair is written beside the generated one under its own
	// names, never over it: the fallback's fingerprint is what an
	// operator pinned before the replacement, and losing it would make an
	// adoption impossible to audit after the fact.
	adoptedCert = "server.crt"
	adoptedKey  = "server.key"
)

// ServerCert is the certificate the listener presents (FR-082).
//
// One listener carries the UI, the API and the embedded registry, so this
// is the whole of Tobby's server-side TLS. Two sources exist and only two:
// the administrator's PEM pair, and the self-signed certificate generated
// when none was supplied. The generated one is persisted in the state
// directory so that its fingerprint — the thing operators pin, compare,
// and paste into a client's trust store — survives a restart.
type ServerCert struct {
	// current holds the parsed pair. Guarded by mu rather than an
	// atomic pointer because the reload path also writes the stamp, and
	// one lock is easier to reason about than two invariants.
	mu      sync.RWMutex
	current *tls.Certificate
	stamp   fileStamp
	digest  string

	certFile, keyFile string

	// stateRoot is where an adopted pair is written on an instance that
	// started on the generated fallback. Empty when the instance keeps no
	// state, which is the one case adoption cannot serve.
	stateRoot string

	// selfSigned records which of the two sources produced the current
	// certificate, for the startup log and the reported configuration.
	selfSigned bool
}

// fileStamp is the cheap change detector for the certificate pair:
// modification time and size of both files. It is not a security control
// — an attacker who can rewrite the certificate has already won — only a
// way to notice a replacement without re-reading two files per handshake.
type fileStamp struct {
	certMod, keyMod   time.Time
	certSize, keySize int64
}

// NewServerCert resolves the listener's certificate from configuration.
//
// stateRoot is where a generated certificate is persisted; an empty
// stateRoot keeps it in memory only, which means a fresh fingerprint on
// every restart — acceptable for a throwaway instance, never for one an
// operator has pinned.
func NewServerCert(cfg config.ServerTLS, stateRoot string) (*ServerCert, error) {
	if cfg.CertFile != "" {
		s := &ServerCert{certFile: cfg.CertFile, keyFile: cfg.KeyFile, stateRoot: stateRoot}
		if err := s.reload(); err != nil {
			return nil, err
		}
		return s, nil
	}
	return newSelfSigned(cfg.Hosts, stateRoot)
}

// TLSConfig returns the listener configuration. GetCertificate is used
// rather than a fixed Certificates slice: it is what lets the pair be
// replaced on disk and picked up on the next handshake, which is the
// "certificate replacement is possible via configuration" half of FR-082
// without a restart.
func (s *ServerCert) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: s.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// Fingerprint returns the SHA-256 of the served certificate's DER, in the
// colon-separated hex form `openssl x509 -fingerprint -sha256` prints, so
// an operator compares two strings rather than converting one.
func (s *ServerCert) Fingerprint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.digest
}

// SelfSigned reports whether the served certificate was generated rather
// than supplied — the fact the startup log and the UI must state, because
// a self-signed certificate is a posture, not a detail.
func (s *ServerCert) SelfSigned() bool { return s.selfSigned }

// Source names where the certificate came from: the configured path, or
// the generated marker.
func (s *ServerCert) Source() string {
	if s.selfSigned {
		return "self-signed (generated)"
	}
	return s.certFile
}

// getCertificate serves the current pair, reloading it first when the
// files changed on disk.
//
// A failed reload keeps the previous certificate. A certificate file
// caught half-written by a deployment tool must not take the listener
// down: the old certificate is still valid, and the next handshake will
// try again.
func (s *ServerCert) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s.certFile != "" && s.changed() {
		_ = s.reload()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil, errors.New("no server certificate is loaded")
	}
	return s.current, nil
}

// changed reports whether the configured pair differs from what is loaded.
func (s *ServerCert) changed() bool {
	now, err := stampOf(s.certFile, s.keyFile)
	if err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return now != s.stamp
}

// reload reads the configured pair and installs it.
func (s *ServerCert) reload() error {
	stamp, err := stampOf(s.certFile, s.keyFile)
	if err != nil {
		return taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": s.certFile}).WithCause(err)
	}
	pair, err := tls.LoadX509KeyPair(s.certFile, s.keyFile)
	if err != nil {
		return taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": s.certFile}).WithCause(err)
	}
	if len(pair.Certificate) == 0 {
		return taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": s.certFile}).
			WithCause(errors.New("the PEM file holds no certificate"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = &pair
	s.stamp = stamp
	s.digest = Fingerprint(pair.Certificate[0])
	return nil
}

// stampOf reads both files' modification stamps.
func stampOf(certFile, keyFile string) (fileStamp, error) {
	ci, err := os.Stat(certFile)
	if err != nil {
		return fileStamp{}, err
	}
	ki, err := os.Stat(keyFile)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{
		certMod: ci.ModTime(), certSize: ci.Size(),
		keyMod: ki.ModTime(), keySize: ki.Size(),
	}, nil
}

// Fingerprint renders the SHA-256 of a DER-encoded certificate as
// colon-separated uppercase hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	hexed := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(hexed); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hexed[i : i+2])
	}
	return b.String()
}

// newSelfSigned loads the persisted fallback certificate, or generates
// one (FR-082: "generate a self-signed certificate as a fallback when
// none is supplied").
func newSelfSigned(hosts []string, stateRoot string) (*ServerCert, error) {
	dir := ""
	if stateRoot != "" {
		dir = filepath.Join(stateRoot, selfSignedDir)
	}
	if dir != "" {
		if pair, err := loadPersistedSelfSigned(dir); err == nil {
			return newSelfSignedFrom(pair, stateRoot), nil
		}
	}
	certPEM, keyPEM, err := generateSelfSigned(hosts)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": "the generated self-signed certificate"}).WithCause(err)
	}
	if dir != "" {
		if err := persistSelfSigned(dir, certPEM, keyPEM); err != nil {
			return nil, err
		}
	}
	return newSelfSignedFrom(&pair, stateRoot), nil
}

func newSelfSignedFrom(pair *tls.Certificate, stateRoot string) *ServerCert {
	return &ServerCert{
		current:    pair,
		digest:     Fingerprint(pair.Certificate[0]),
		selfSigned: true,
		stateRoot:  stateRoot,
	}
}

// Destination reports where a replacement pair must be written for this
// instance to serve it, and whether replacement is possible at all.
//
// On an instance configured with server.tls.certFile, that is the
// configured path: an administrator replacing the file expects the file
// they named to be the one that changes. On an instance running the
// generated fallback there is no such path, so one is offered inside the
// state directory — which is also the only place a private key may live
// (R-16). An instance with neither a configured pair nor a state
// directory cannot adopt anything, and says so rather than writing a key
// somewhere it would not survive.
func (s *ServerCert) Destination() (certPath, keyPath string, ok bool) {
	if s.certFile != "" {
		return s.certFile, s.keyFile, true
	}
	if s.stateRoot == "" {
		return "", "", false
	}
	dir := filepath.Join(s.stateRoot, selfSignedDir)
	return filepath.Join(dir, adoptedCert), filepath.Join(dir, adoptedKey), true
}

// Adopt makes the pair now at the destination paths the one this listener
// presents, from the next handshake on.
//
// It is what closes the second half of FR-082 on an instance that started
// self-signed: until the reader is pointed at a path it never re-reads
// anything, so a replacement written beside it would sit there unused
// while the fallback kept being served — a silent no-op where an operator
// saw a success. Writing the files is the caller's business; this is the
// step that makes them take effect.
func (s *ServerCert) Adopt() error {
	certPath, keyPath, ok := s.Destination()
	if !ok {
		return taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": "the state directory"}).
			WithCause(errors.New("this instance keeps no state directory, so it has nowhere to hold a replacement pair"))
	}
	s.mu.Lock()
	s.certFile, s.keyFile = certPath, keyPath
	s.mu.Unlock()
	if err := s.reload(); err != nil {
		return err
	}
	s.mu.Lock()
	s.selfSigned = false
	s.mu.Unlock()
	return nil
}

// loadPersistedSelfSigned reuses a previously generated pair, so that a
// restart does not change the fingerprint an operator distributed. An
// expired one is regenerated.
func loadPersistedSelfSigned(dir string) (*tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, selfSignedCert), filepath.Join(dir, selfSignedKey))
	if err != nil {
		return nil, err
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("no certificate in the persisted pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, fmt.Errorf("the persisted self-signed certificate expired on %s", leaf.NotAfter.Format(time.RFC3339))
	}
	return &pair, nil
}

// persistSelfSigned writes the generated pair into the state directory,
// the key readable by its owner only (NFR-020).
func persistSelfSigned(dir string, certPEM, keyPEM []byte) error {
	fail := func(err error) error {
		return taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": dir}).WithCause(err)
	}
	if err := secretfile.MkdirAll(dir); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, selfSignedCert), certPEM, 0o644); err != nil { //nolint:gosec // G306: the certificate is public by definition; the key below is owner-only
		return fail(err)
	}
	// The key goes through secretfile rather than a mode literal: 0600 is
	// the Unix half of NFR-020, and on Windows it would leave the key
	// readable by everything the inherited access list admits.
	if err := secretfile.Write(filepath.Join(dir, selfSignedKey), keyPEM); err != nil {
		return fail(err)
	}
	return nil
}

// generateSelfSigned issues the fallback certificate: ECDSA P-256, valid
// for a year, covering the loopback names every operator reaches an
// instance by first, plus the machine's own hostname and whatever
// server.tls.hosts adds.
func generateSelfSigned(hosts []string) (certPEM, keyPEM []byte, err error) {
	fail := func(err error) error {
		return taxonomy.New(taxonomy.CodeServerTLS, taxonomy.Params{"source": "the generated self-signed certificate"}).WithCause(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fail(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fail(err)
	}
	names, ips := subjectNames(hosts)
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0], Organization: []string{"Tobby self-signed"}},
		// A minute of backdating absorbs the clock skew between an
		// air-gapped instance and the client that just reached it.
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(selfSignedValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              names,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fail(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fail(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		nil
}

// subjectNames builds the SAN set of the fallback certificate: the
// configured hosts, the machine's hostname, and the loopback names — a
// certificate that does not cover localhost fails the very first thing an
// operator does with a fresh instance.
func subjectNames(hosts []string) (dns []string, ips []net.IP) {
	seenDNS := map[string]bool{}
	seenIP := map[string]bool{}
	add := func(h string) {
		if h == "" {
			return
		}
		if ip := net.ParseIP(h); ip != nil {
			if !seenIP[ip.String()] {
				seenIP[ip.String()] = true
				ips = append(ips, ip)
			}
			return
		}
		if !seenDNS[h] {
			seenDNS[h] = true
			dns = append(dns, h)
		}
	}
	for _, h := range hosts {
		add(strings.TrimSpace(h))
	}
	if name, err := os.Hostname(); err == nil {
		add(name)
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	return dns, ips
}
