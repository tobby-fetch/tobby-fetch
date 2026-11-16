// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package server

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
)

const testHTMLPolicy = "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// serveVia runs one request through SecurityHeaders around h.
func serveVia(t *testing.T, p HeaderPolicy, h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	SecurityHeaders(h, p).ServeHTTP(w, r)
	return w
}

// authenticated is a handler that attaches an identity the way every
// surface does, then answers with the given type and cache directive.
func authenticated(ctype, cache, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = auth.WithIdentity(r.Context(), auth.Identity{Name: "alexis", Role: auth.RoleAdmin})
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		if cache != "" {
			w.Header().Set("Cache-Control", cache)
		}
		_, _ = io.WriteString(w, body)
	}
}

// TestSecurityHeadersPerClass pins the NFR-022 decision table of
// headers.go, class by class.
func TestSecurityHeadersPerClass(t *testing.T) {
	p := HeaderPolicy{HTML: testHTMLPolicy}
	for _, c := range []struct {
		name, path string
		h          http.HandlerFunc
		wantCSP    string
		wantCache  string
	}{
		{"ui page", "/content", authenticated("text/html; charset=utf-8", "", "<p>x</p>"), testHTMLPolicy, "no-store"},
		{"ui page sniffed", "/content", authenticated("", "", "<!DOCTYPE html><p>x</p>"), testHTMLPolicy, "no-store"},
		{"ui handler cache weakened", "/help/-/assets/x.png", authenticated("image/png", "no-cache", "x"), InertPolicy, "no-store"},
		{"ui static, unauthenticated", "/static/app.css", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/css")
			_, _ = io.WriteString(w, "body{}")
		}, InertPolicy, ""},
		{"ui handler policy kept", "/media/slip", func(w http.ResponseWriter, r *http.Request) {
			_ = auth.WithIdentity(r.Context(), auth.Identity{Name: "a"})
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Security-Policy", "default-src 'none'")
			w.WriteHeader(http.StatusOK)
		}, "default-src 'none'", "no-store"},
		{"api", "/api/v1/tasks", authenticated("application/json", "", "{}"), InertPolicy, "no-store"},
		{"api html is not a page", "/api/v1/x", authenticated("text/html", "", "<p>x</p>"), InertPolicy, "no-store"},
		{"registry keeps its cache contract", "/v2/x/blobs/sha256:00", authenticated("application/octet-stream", "max-age=31536000", "x"), InertPolicy, "max-age=31536000"},
		{"registry without directive", "/v2/", authenticated("application/json", "", "{}"), InertPolicy, "no-store"},
		{"files html is sandboxed", "/files/set/index.html", authenticated("text/html; charset=utf-8", "", "<script>alert(1)</script>"), FilesPolicy, "no-store"},
		{"files anonymous", "/files/set/index.html", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = io.WriteString(w, "<svg/>")
		}, FilesPolicy, ""},
		{"probe", "/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") }, InertPolicy, ""},
		{"nothing written", "/content", func(http.ResponseWriter, *http.Request) {}, InertPolicy, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := serveVia(t, p, c.h, httptest.NewRequest(http.MethodGet, c.path, http.NoBody))
			h := w.Header()
			for k, want := range map[string]string{
				"X-Content-Type-Options":    "nosniff",
				"Referrer-Policy":           "no-referrer",
				"X-Frame-Options":           "DENY",
				"Content-Security-Policy":   c.wantCSP,
				"Cache-Control":             c.wantCache,
				"Strict-Transport-Security": "",
			} {
				if got := h.Get(k); got != want {
					t.Errorf("%s = %q, want %q", k, got, want)
				}
			}
		})
	}
}

// TestSecurityHeadersHSTS: HSTS when the listener serves TLS, or when the
// operator states that TLS is terminated in front of it — never on a
// plain-HTTP instance, where the browser would pin a scheme it cannot
// reach.
func TestSecurityHeadersHSTS(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	plain := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	if got := serveVia(t, HeaderPolicy{}, ok, plain).Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain HTTP: HSTS = %q, want none", got)
	}
	tlsReq := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	tlsReq.TLS = &tls.ConnectionState{}
	if got := serveVia(t, HeaderPolicy{}, ok, tlsReq).Header().Get("Strict-Transport-Security"); got != hstsValue {
		t.Errorf("TLS listener: HSTS = %q, want %q", got, hstsValue)
	}
	proxied := httptest.NewRequest(http.MethodGet, "/v2/", http.NoBody)
	if got := serveVia(t, HeaderPolicy{SecureCookies: true}, ok, proxied).Header().Get("Strict-Transport-Security"); got != hstsValue {
		t.Errorf("TLS terminated in front: HSTS = %q, want %q", got, hstsValue)
	}
}

// TestSecurityHeadersStreaming: the wrapper commits its headers before a
// flushed or copied body, and keeps the writer's optional interfaces —
// a streaming log tail and a sendfile download both still work.
func TestSecurityHeadersStreaming(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if _, ok := w.(io.ReaderFrom); !ok {
			t.Error("the wrapper hides io.ReaderFrom: downloads lose sendfile")
		}
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("the wrapper hides http.Flusher: log tails stop streaming")
		}
		f.Flush()
		_, _ = io.Copy(w, strings.NewReader("line\n"))
	}
	w := serveVia(t, HeaderPolicy{}, h, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/x/logs", http.NoBody))
	if !w.Flushed || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Body.String() != "line\n" {
		t.Errorf("flushed=%v nosniff=%q body=%q", w.Flushed, w.Header().Get("X-Content-Type-Options"), w.Body.String())
	}
}

// TestServerHandlerIsSecured: the listener's own handler is the mux
// behind the middleware — the probes are covered like everything else,
// and a feature mounted later is too.
func TestServerHandlerIsSecured(t *testing.T) {
	s := New("127.0.0.1:0", time.Second, metrics.New(), slog.New(slog.DiscardHandler))
	s.SetHeaderPolicy(HeaderPolicy{HTML: testHTMLPolicy, SecureCookies: true})
	s.Handle("GET /v2/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, path := range []string{"/healthz", "/readyz", "/metrics", "/v2/"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		h := w.Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Strict-Transport-Security") != hstsValue ||
			h.Get("Content-Security-Policy") != InertPolicy {
			t.Errorf("%s: headers %v", path, h)
		}
	}
	got := s.Routes()
	want := []string{"GET /healthz", "GET /readyz", "GET /metrics", "GET /v2/"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Routes() = %v, want %v", got, want)
	}
}
