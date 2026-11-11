// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package federation_test drives Tobby's federated sign-in end to end
// against the Keycloak fixture (test/keycloak): the real UI routes, the
// real session table, a real identity provider over a real socket. The
// browser is played over plain HTTP by the fixture's login helpers; the
// pages the person sees are covered by the browser suite (R-38).
package federation_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/federation"
	"github.com/tobby-fetch/tobby-fetch/internal/ui"
	"github.com/tobby-fetch/tobby-fetch/test/keycloak"
)

// The local administrator every scenario keeps beside federation (FR-078).
const (
	localAdmin  = "root"
	localPhrase = "local-break-glass-phrase"
)

// cutProxy stands between Tobby and Keycloak: it forwards with the Host
// header preserved — so Keycloak names the proxy in its issuer and
// endpoints, and every server-to-server call goes through here — and
// can be cut, which is how R-20 takes the identity provider away while
// the browser side still works.
type cutProxy struct {
	*httptest.Server
	cut atomic.Bool
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	tu, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{}
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(tu)
		pr.Out.Host = pr.In.Host
	}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.cut.Load() {
			// A provider that is gone, not one that answers an error: the
			// connection drops without a response.
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
					return
				}
			}
			http.Error(w, "cut", http.StatusBadGateway)
			return
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Close)
	return p
}

// env is one Keycloak plus one Tobby web front over it.
type env struct {
	kc    *keycloak.Fixture // addressed through the proxy
	proxy *cutProxy
	tobby *httptest.Server
	authn *auth.Authenticator
	fed   *federation.Service
	audit *syncBuffer
	// spCert is the SP certificate PEM, for the SAML tests.
	dir string
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) records(action string) []map[string]any {
	s.mu.Lock()
	raw := append([]byte(nil), s.b.Bytes()...)
	s.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var rec map[string]any
		if json.Unmarshal(line, &rec) == nil && rec["log_type"] == "audit" && rec["action"] == action {
			out = append(out, rec)
		}
	}
	return out
}

func (s *syncBuffer) contains(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.b.Bytes(), []byte(sub))
}

// lateHandler lets the Tobby server exist (and have a URL to register at
// Keycloak) before its handler is built.
type lateHandler struct{ h atomic.Value }

func (l *lateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.h.Load().(http.Handler).ServeHTTP(w, r)
}

// newEnv starts Keycloak with the realm pointed at a fresh Tobby server,
// and builds that server with both federated methods enabled.
func newEnv(t *testing.T) *env {
	t.Helper()
	var late lateHandler
	tobby := httptest.NewServer(&late)
	t.Cleanup(tobby.Close)

	kcDirect := keycloak.Start(t, keycloak.Options{
		OIDCRedirectURI: tobby.URL + federation.PathOIDCCallback,
		SAMLEntityID:    tobby.URL + federation.PathSAMLMetadata,
		SAMLACSURL:      tobby.URL + federation.PathSAMLACS,
	})
	proxy := newCutProxy(t, kcDirect.BaseURL)
	kc := *kcDirect
	kc.BaseURL = proxy.URL
	kc.Issuer = proxy.URL + "/realms/" + keycloak.RealmName
	kc.SAMLMetadataURL = kc.Issuer + "/protocol/saml/descriptor"
	kc.SAMLSSOURL = kc.Issuer + "/protocol/saml"

	e := &env{kc: &kc, proxy: proxy, tobby: tobby, audit: &syncBuffer{}, dir: t.TempDir()}
	logger := slog.New(slog.NewJSONHandler(e.audit, nil))

	secret := e.file(t, "oidc-secret", []byte(keycloak.ClientSecret))
	certFile, keyFile := e.spKeyPair(t)
	metadata := e.file(t, "idp.xml", fetch(t, kc.SAMLMetadataURL))

	roles := config.RoleMapping{
		Viewer:   []string{keycloak.GroupViewer},
		Operator: []string{keycloak.GroupOperator},
		Admin:    []string{keycloak.GroupAdmin},
	}
	fed, err := federation.New(&federation.Options{
		OIDC: config.OIDC{
			Enabled: true, DisplayName: "Fixture directory",
			Issuer: kc.Issuer, ClientID: keycloak.ClientID, ClientSecretFile: secret,
			RedirectURL: tobby.URL + federation.PathOIDCCallback, GroupsClaim: "groups",
			Scopes: []string{"profile"}, Roles: roles,
		},
		SAML: config.SAML{
			Enabled: true, DisplayName: "Fixture SAML",
			EntityID: tobby.URL + federation.PathSAMLMetadata, ACSURL: tobby.URL + federation.PathSAMLACS,
			IDPMetadataFile: metadata, CertFile: certFile, KeyFile: keyFile,
			GroupsAttribute: "groups", ClockSkew: config.Duration(90 * time.Second), Roles: roles,
		},
		Client: &http.Client{Timeout: 5 * time.Second},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("building the federation: %v", err)
	}
	e.fed = fed

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount(localAdmin, auth.RoleAdmin, localPhrase, time.Now()); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(12 * time.Hour)
	sessions.SetIdleTimeout(30 * time.Minute)
	e.authn = &auth.Authenticator{Store: accounts, Sessions: sessions, Logger: logger, Federated: fed.Enabled}

	st := openStore(t)
	u := ui.New(e.authn, logger, &ui.Options{Version: "0.6.0-e2e", Mode: "passthrough", Store: st, Federation: fed})
	mux := http.NewServeMux()
	u.Mount(mux)
	u.MountFederation(mux)
	late.h.Store(http.Handler(mux))
	return e
}

func (e *env) file(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(e.dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// spKeyPair writes a throwaway self-signed SP key pair.
func (e *env) spKeyPair(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tobby-sp-e2e"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = e.file(t, "sp.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyFile = e.file(t, "sp.key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return certFile, keyFile
}

func fetch(t *testing.T, target string) []byte {
	t.Helper()
	resp, err := http.Get(target) //nolint:gosec,noctx // G107: the fixture's own URL, in a test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %v", target, resp.StatusCode, err)
	}
	return b
}

// browser is a cookie-keeping client that never follows redirects.
type browser struct {
	t *testing.T
	c *http.Client
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, c: &http.Client{Jar: jar, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, target string, form url.Values) (resp *http.Response, page string) {
	b.t.Helper()
	var body io.Reader = http.NoBody
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err = b.c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp, string(raw)
}

func (b *browser) get(target string) (resp *http.Response, page string) {
	return b.do(http.MethodGet, target, nil)
}

// location returns the absolute redirect target of resp.
func location(t *testing.T, resp *http.Response) string {
	t.Helper()
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatalf("HTTP %d without a redirect", resp.StatusCode)
	}
	u, err := resp.Request.URL.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.String()
}

// whoAmI reports the role the browser's session holds, by the floors it
// passes: the dashboard for viewer, /import for operator, /admin for
// admin. Empty when it has no session.
func (e *env) whoAmI(b *browser) auth.Role {
	b.t.Helper()
	if resp, _ := b.get(e.tobby.URL + "/"); resp.StatusCode != http.StatusOK {
		return ""
	}
	if resp, _ := b.get(e.tobby.URL + "/admin/accounts"); resp.StatusCode == http.StatusOK {
		return auth.RoleAdmin
	}
	if resp, _ := b.get(e.tobby.URL + "/import"); resp.StatusCode == http.StatusOK {
		return auth.RoleOperator
	}
	return auth.RoleViewer
}

// localSignIn signs in with the local administrator through the form.
func (e *env) localSignIn(b *browser) *http.Response {
	b.t.Helper()
	resp, _ := b.do(http.MethodPost, e.tobby.URL+"/login",
		url.Values{"username": {localAdmin}, "password": {localPhrase}, "next": {"/"}})
	return resp
}
