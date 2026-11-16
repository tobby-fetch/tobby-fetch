// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package server

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
)

// The HTTP security headers of the single listener (NFR-022, amendment
// 2026-11-16). One middleware wraps the whole mux, so a route added later
// — UI screen, API endpoint, probe — cannot ship without them; the
// per-handler habit it replaces (the UI's securedRouter) left every
// machine surface bare.
//
// The headers are decided when the response is committed (WriteHeader,
// or the first Write), not before the handler runs: the CSP depends on
// what the response IS (HTML or not), the Cache-Control on whether the
// request turned out to be authenticated, and both are only known then.
//
// Per response class:
//
//	every response   X-Content-Type-Options: nosniff
//	                 Referrer-Policy: no-referrer
//	                 X-Frame-Options: DENY
//	                 Strict-Transport-Security, when TLS is served here
//	                 or terminated in front (server.secureCookies)
//	UI HTML          the UI's Content-Security-Policy (hash-allowed inline
//	                 scripts, frame-ancestors/object-src/base-uri 'none',
//	                 form-action 'self'), unless the handler set a
//	                 stricter one of its own (the transfer slip does)
//	/files/          a sandboxing policy: a FileSet is third-party content
//	                 served on THIS origin, and an HTML or SVG file in it
//	                 must not run script with the UI's privileges
//	everything else  an inert policy (default-src 'none', sandbox): an API
//	                 document, a blob or a probe is never a page
//	authenticated    Cache-Control: no-store — forced on the UI and the API;
//	                 on /v2/ and /files/ only when the handler set no cache
//	                 directive of its own, because the registry's
//	                 digest-addressed blobs are immutable and their
//	                 Cache-Control is part of the distribution contract
//	                 (shared caches do not store a response to a request
//	                 carrying Authorization anyway, RFC 9111 §3.5)
//
// Every header added to /v2/ and /files/ is one their clients ignore:
// docker, containerd, helm, oras, crane, apt and dnf do not interpret
// nosniff, the referrer policy, the frame options, the CSP or HSTS.
const (
	contentTypeOptions = "nosniff"
	referrerPolicy     = "no-referrer"
	frameOptions       = "DENY"
	// hstsValue: one year, no includeSubDomains and no preload — the
	// instance does not own its parent domain and must not decide for the
	// other services under it.
	hstsValue = "max-age=31536000"

	// InertPolicy is the CSP of every response that is not a UI page: it
	// lets nothing load, run, be framed or submitted.
	InertPolicy = "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; sandbox"
	// FilesPolicy is the CSP of the FileSet surface: a document opened
	// from /files/ is sandboxed into an opaque origin (no script, no form
	// submission, no access to this origin's pages or cookies), may still
	// show its own same-origin images and stylesheets, and cannot be
	// framed.
	FilesPolicy = "sandbox; default-src 'self'; script-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// responseClass is the NFR-022 class of a request path.
type responseClass int

const (
	classUI responseClass = iota
	classAPI
	classRegistry
	classFiles
	classProbe
)

// classify maps a path to its class. The UI owns the root and the
// "/static/" and "/auth/" prefixes it mounts; everything else reserved is
// a machine surface (ADR-0015).
func classify(path string) responseClass {
	switch {
	case strings.HasPrefix(path, "/v2/") || path == "/v2":
		return classRegistry
	case strings.HasPrefix(path, "/files/") || path == "/files":
		return classFiles
	case strings.HasPrefix(path, "/api/") || path == "/api":
		return classAPI
	case path == "/metrics" || path == "/healthz" || path == "/readyz" ||
		strings.HasPrefix(path, "/metrics/") || strings.HasPrefix(path, "/healthz/") || strings.HasPrefix(path, "/readyz/"):
		return classProbe
	}
	return classUI
}

// HeaderPolicy configures SecurityHeaders.
type HeaderPolicy struct {
	// HTML is the Content-Security-Policy of the UI's HTML responses
	// (ui.ContentSecurityPolicy). Empty falls back to InertPolicy, which
	// is safe and breaks every page — the failure an instance wired
	// without a UI policy should show.
	HTML string
	// SecureCookies states that TLS is terminated in front of the
	// listener (server.secureCookies): HSTS is then sent on plain-HTTP
	// responses too, since the browser saw HTTPS.
	SecureCookies bool
}

// SecurityHeaders wraps next with the NFR-022 headers.
func SecurityHeaders(next http.Handler, p HeaderPolicy) http.Handler {
	html := p.HTML
	if html == "" {
		html = InertPolicy
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, mark := auth.WithAuthMark(r.Context())
		sw := &secureWriter{
			ResponseWriter: w,
			class:          classify(r.URL.Path),
			html:           html,
			hsts:           r.TLS != nil || p.SecureCookies,
			authenticated:  mark.Load,
		}
		next.ServeHTTP(sw, r.WithContext(ctx))
		// A handler that wrote nothing still answers 200: commit the
		// headers on its behalf, exactly as net/http would.
		sw.commit(nil)
	})
}

// secureWriter stamps the headers once, at commit time.
type secureWriter struct {
	http.ResponseWriter
	class         responseClass
	html          string
	hsts          bool
	authenticated func() bool
	committed     bool
}

// commit stamps the headers if they are not yet. first is the first
// chunk of the body, used to sniff the type of a response that set none
// — the same sniffing net/http itself would do, done first so the CSP
// matches what the browser will see.
func (w *secureWriter) commit(first []byte) {
	if w.committed {
		return
	}
	w.committed = true
	h := w.Header()
	h.Set("X-Content-Type-Options", contentTypeOptions)
	h.Set("Referrer-Policy", referrerPolicy)
	h.Set("X-Frame-Options", frameOptions)
	if w.hsts {
		h.Set("Strict-Transport-Security", hstsValue)
	}

	ctype := h.Get("Content-Type")
	_, typed := h["Content-Type"] // present but nil: the handler opted out of sniffing
	if !typed && first != nil && h.Get("Content-Encoding") == "" {
		ctype = http.DetectContentType(first)
		h.Set("Content-Type", ctype)
	}
	if h.Get("Content-Security-Policy") == "" {
		h.Set("Content-Security-Policy", w.policy(ctype))
	}

	if w.authenticated() {
		switch w.class {
		case classRegistry, classFiles:
			if h.Get("Cache-Control") == "" {
				h.Set("Cache-Control", "no-store")
			}
		default:
			h.Set("Cache-Control", "no-store")
		}
	}
}

// policy picks the CSP of an uncommitted response by class and type.
func (w *secureWriter) policy(ctype string) string {
	switch w.class {
	case classFiles:
		return FilesPolicy
	case classUI:
		if isHTML(ctype) {
			return w.html
		}
	}
	return InertPolicy
}

// isHTML reports whether a Content-Type is an HTML document.
func isHTML(ctype string) bool {
	mt, _, _ := strings.Cut(ctype, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	return mt == "text/html" || mt == "application/xhtml+xml"
}

func (w *secureWriter) WriteHeader(code int) {
	// 1xx informational responses carry no final headers; 103 Early Hints
	// would otherwise freeze the set before the handler decided its type.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.commit(nil)
	w.ResponseWriter.WriteHeader(code)
}

func (w *secureWriter) Write(p []byte) (int, error) {
	w.commit(p)
	return w.ResponseWriter.Write(p)
}

// ReadFrom keeps the sendfile path of the blob and file downloads: a
// wrapper without it would force every multi-gigabyte transfer through
// userspace copies.
func (w *secureWriter) ReadFrom(src io.Reader) (int64, error) {
	w.commit(nil)
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

// Flush keeps streaming responses (log tails) streaming.
func (w *secureWriter) Flush() {
	w.commit(nil)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack is passed through for completeness; no route hijacks today.
func (w *secureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("server: the response writer does not support hijacking")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *secureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
