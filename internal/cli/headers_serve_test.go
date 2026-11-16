// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/ui"
)

// The NFR-022 acceptance: walk every route the real serve path registers
// on the listener — the UI, the API (expanded from its OpenAPI document,
// which TestOpenAPICoversEveryRoute keeps complete), /v2/, /files/,
// /help, the probes and /metrics — and assert the headers of its class.
// Driven through the listener's own handler, so it is the production
// middleware stack, not a test reassembly of it.

// startServeForHeaders runs serve and returns its server once ready.
func startServeForHeaders(t *testing.T, cfg *config.Config) *server.Server {
	t.Helper()
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)
	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	t.Cleanup(func() { serveHook = nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runServe returned %v, want nil", err)
		}
	})
	var srv *server.Server
	select {
	case srv = <-srvCh:
	case err := <-done:
		t.Fatalf("runServe exited before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runServe never built its server")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody))
		if w.Code == http.StatusOK {
			return srv
		}
		if time.Now().After(deadline) {
			t.Fatal("instance never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var (
	restWildcard = regexp.MustCompile(`\{[a-z_]+\.{3}\}`)
	oneWildcard  = regexp.MustCompile(`\{[a-z_]+\}`)
)

// probePath turns a registered pattern's path into a concrete one.
func probePath(p string) string {
	p = strings.ReplaceAll(p, "{$}", "")
	p = restWildcard.ReplaceAllString(p, "no/such")
	p = oneWildcard.ReplaceAllString(p, "no-such")
	switch p {
	case "/static/":
		return "/static/app.css"
	case "/":
		return "/no-such-page"
	}
	return p
}

// headerProbe is one request of the walk.
type headerProbe struct {
	method, path string
	// cred is "cookie", "basic" or "".
	cred string
	// authed: the request is expected to be served as an identity.
	authed bool
}

// TestSecurityHeadersOnEveryRoute is the walk.
func TestSecurityHeadersOnEveryRoute(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Server.SecureCookies = true // TLS terminated in front: HSTS on every response
	srv := startServeForHeaders(t, &cfg)
	h := srv.Handler()

	// A session, through the real sign-in form.
	lr := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=test-password&next=%2F"))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, lr)
	var session *http.Cookie
	for _, c := range lw.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("sign-in: %d, no session cookie", lw.Code)
	}

	send := func(p headerProbe) *httptest.ResponseRecorder {
		r := httptest.NewRequest(p.method, p.path, http.NoBody)
		switch p.cred {
		case "cookie":
			r.AddCookie(session)
		case "basic":
			r.SetBasicAuth("admin", "test-password")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	var probes []headerProbe
	sawAPI := false
	for _, pattern := range srv.Routes() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			method, path = "", pattern
		}
		switch {
		case path == "/api/v1/":
			sawAPI = true
		case path == "/v2/":
			probes = append(probes,
				headerProbe{http.MethodGet, "/v2/", "basic", true},
				headerProbe{http.MethodGet, "/v2/no/such/manifests/latest", "basic", true},
				headerProbe{http.MethodHead, "/v2/no/such/blobs/sha256:" + strings.Repeat("0", 64), "basic", true},
				headerProbe{http.MethodGet, "/v2/", "", false},
				headerProbe{http.MethodPost, "/v2/no/such/blobs/uploads/", "", false})
		case path == "/files/":
			probes = append(probes,
				headerProbe{http.MethodGet, "/files/no-such/index.html", "basic", true},
				headerProbe{http.MethodGet, "/files/no-such/index.html", "", false})
		case path == "/healthz" || path == "/readyz" || path == "/metrics":
			probes = append(probes, headerProbe{method, path, "", false})
		case strings.HasPrefix(path, "/auth/"):
			// Federation is not enabled on this instance: never mounted.
			t.Errorf("federated route %s mounted without federation", pattern)
		default:
			if method == "" {
				method = http.MethodGet
			}
			public := pattern == "GET /static/" || pattern == "GET /login"
			cred := "cookie"
			if pattern == "POST /login" {
				cred = "" // no session authenticates the sign-in form
			}
			probes = append(probes, headerProbe{method, probePath(path), cred, auth.SafeMethod(method) && !public})
		}
	}
	if !sawAPI {
		t.Fatal("the API is not mounted on the listener")
	}

	// The API, from its own published contract.
	w := send(headerProbe{http.MethodGet, "/api/v1/openapi.yaml", "basic", true})
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &doc); err != nil || len(doc.Paths) == 0 {
		t.Fatalf("reading the OpenAPI document: %v (%d)", err, w.Code)
	}
	var paths []string
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for m := range doc.Paths[p] {
			method := strings.ToUpper(m)
			switch method {
			case http.MethodGet, http.MethodHead:
				probes = append(probes, headerProbe{method, probePath(p), "basic", true})
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				// Cookie without token: refused before any handler runs,
				// so the walk mutates nothing.
				probes = append(probes, headerProbe{method, probePath(p), "cookie", false})
			}
		}
	}

	html := ui.ContentSecurityPolicy()
	for _, p := range probes {
		w := send(p)
		got := w.Header()
		name := p.method + " " + p.path
		for k, want := range map[string]string{
			"X-Content-Type-Options":    "nosniff",
			"Referrer-Policy":           "same-origin",
			"X-Frame-Options":           "DENY",
			"Strict-Transport-Security": "max-age=31536000",
		} {
			if got.Get(k) != want {
				t.Errorf("%s (%d): %s = %q, want %q", name, w.Code, k, got.Get(k), want)
			}
		}
		csp := got.Get("Content-Security-Policy")
		ctype := got.Get("Content-Type")
		switch {
		case strings.HasPrefix(p.path, "/files/"):
			if csp != server.FilesPolicy {
				t.Errorf("%s: CSP %q, want the sandboxing FileSet policy", name, csp)
			}
		case strings.HasPrefix(ctype, "text/html") && !strings.HasPrefix(p.path, "/api/") && !strings.HasPrefix(p.path, "/v2/"):
			// A UI page: the UI policy, or a stricter one of the handler's.
			if csp != html {
				for _, d := range []string{"frame-ancestors 'none'", "base-uri 'none'"} {
					if !strings.Contains(csp, d) {
						t.Errorf("%s: HTML policy %q misses %s", name, csp, d)
					}
				}
				if !strings.Contains(csp, "object-src 'none'") && !strings.Contains(csp, "default-src 'none'") {
					t.Errorf("%s: HTML policy %q lets objects load", name, csp)
				}
				if !strings.Contains(csp, "form-action 'self'") && !strings.Contains(csp, "form-action 'none'") {
					t.Errorf("%s: HTML policy %q lets forms post elsewhere", name, csp)
				}
			}
		case strings.HasPrefix(p.path, "/api/") || strings.HasPrefix(p.path, "/v2/") || p.path == "/healthz" || p.path == "/readyz" || p.path == "/metrics":
			if csp != server.InertPolicy && !strings.Contains(csp, "default-src 'none'") {
				t.Errorf("%s: CSP %q, want the inert policy", name, csp)
			}
		default:
			if csp == "" {
				t.Errorf("%s: no CSP", name)
			}
		}
		if p.cred == "cookie" && !auth.SafeMethod(p.method) {
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-004") {
				t.Errorf("%s with the session cookie and no token: %d, want 403 TBY-AUTH-004", name, w.Code)
			}
		}
		if p.authed && w.Code != http.StatusUnauthorized {
			cc := got.Get("Cache-Control")
			switch {
			case strings.HasPrefix(p.path, "/v2/") || strings.HasPrefix(p.path, "/files/"):
				if cc == "" {
					t.Errorf("%s (%d): an authenticated response with no cache directive", name, w.Code)
				}
			case cc != "no-store":
				t.Errorf("%s (%d): Cache-Control %q, want no-store on an authenticated response", name, w.Code, cc)
			}
		}
	}
	if len(probes) < 100 {
		t.Fatalf("the walk sent only %d probes: the route table was not enumerated", len(probes))
	}
}
