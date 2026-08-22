// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Browser-facing security headers, stamped on every UI response by the
// Mount wrapper (v0.4.2 hardening). Middleware rather than the renderer:
// the renderer only sees pages that rendered, and the responses most
// worth hardening — error pages, redirects, static assets — must carry
// the same headers.
//
// The Content-Security-Policy is defense in depth on top of
// html/template's contextual escaping, and it is built to fit what the
// UI actually is (ADR-0015): a self-contained, server-rendered
// application whose only scripts are the vendored htmx + idiomorph
// files and a handful of inline <script> blocks in the templates.
//
//   - script-src carries 'self' plus the SHA-256 of each embedded inline
//     script, computed at startup from the template sources — never
//     'unsafe-inline'. Hashes rather than nonces on purpose: htmx
//     re-inserts the page scripts on boosted navigations and fragment
//     swaps, and a per-response nonce can never match the document's
//     original one, so nonced page scripts would silently die after the
//     first boost. A hash allows the same bytes whenever they are
//     (re-)inserted. 'unsafe-eval' is not needed: the layout pins
//     htmx-config allowEval:false.
//   - style-src needs 'unsafe-inline' for the style="" attributes the
//     templates use for one-off spacing; a compromise consciously taken —
//     attribute styles cannot exfiltrate and cannot execute.
//   - frame-ancestors 'none' (plus the legacy X-Frame-Options for old
//     proxies): no screen of this UI has a reason to be framed, and a
//     framed session UI is a clickjacking target.
//   - form-action 'self' keeps every form posting home — the CSRF token
//     (NFR-012) protects the session, this protects the submission.
const (
	referrerPolicy = "same-origin"
	frameOptions   = "DENY"
	contentTypeNos = "nosniff"
)

// cspHeader is the policy with the inline-script hashes baked in, built
// once at startup: the templates are embedded, so the set of inline
// scripts is fixed for the life of the process.
var cspHeader = buildCSP()

// inlineScriptRe matches attribute-less inline <script> blocks in the
// template sources. The vendored files load via <script src=…> and are
// covered by 'self'; only attribute-less blocks are inline candidates.
var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// buildCSP assembles the Content-Security-Policy value, hashing every
// inline template script. The hash must cover the bytes the BROWSER
// receives, and html/template rewrites script content on the way out (it
// strips the // comments, for one), so each source block is passed
// through html/template itself before hashing — see renderedScript. That
// round trip is deterministic only while the blocks stay free of
// template actions, which TestInlineScriptsAreStatic enforces.
func buildCSP() string {
	hashes := inlineScriptHashes()
	scriptSrc := "'self'"
	if len(hashes) > 0 {
		scriptSrc += " " + strings.Join(hashes, " ")
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + scriptSrc,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// inlineScriptHashes walks the embedded templates and returns the sorted,
// deduplicated CSP hash source of every inline <script> block, as the
// browser will receive it.
func inlineScriptHashes() []string {
	seen := map[string]bool{}
	err := fs.WalkDir(templatesFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := templatesFS.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range inlineScriptRe.FindAllSubmatch(raw, -1) {
			served, err := renderedScript(m[1])
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			sum := sha256.Sum256(served)
			seen["'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'"] = true
		}
		return nil
	})
	if err != nil {
		// The templates are embedded: a walk failure is a build defect,
		// same class as a template parse error (parseTemplates panics too).
		panic(fmt.Sprintf("ui: hashing inline scripts: %v", err))
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// renderedScript runs one source script block through html/template and
// returns the bytes a page actually serves. This is the load-bearing
// subtlety of the hash-based CSP: html/template's JS escaper rewrites
// static script content — it strips the // comments the repository style
// insists on — so hashing the template source would produce hashes no
// browser ever sees, and every inline script would be silently blocked
// (caught live by the browser suite: window.__tobbyWired never set).
// Rendering through the same engine the pages use makes the hash match
// by construction.
func renderedScript(src []byte) ([]byte, error) {
	t, err := template.New("script").Parse("<script>" + string(src) + "</script>")
	if err != nil {
		return nil, fmt.Errorf("parsing inline script: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, nil); err != nil {
		return nil, fmt.Errorf("rendering inline script: %w", err)
	}
	out := bytes.TrimPrefix(buf.Bytes(), []byte("<script>"))
	return bytes.TrimSuffix(out, []byte("</script>")), nil
}

// secureHeaders stamps the security headers on one handler's responses.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", cspHeader)
		h.Set("X-Content-Type-Options", contentTypeNos)
		h.Set("X-Frame-Options", frameOptions)
		h.Set("Referrer-Policy", referrerPolicy)
		next.ServeHTTP(w, r)
	})
}

// securedRouter wraps a Router so every mounted UI handler answers with
// the security headers — the patterns pass through untouched, which is
// what keeps the RBAC matrix test seeing the route table as declared.
type securedRouter struct{ r Router }

func (s securedRouter) Handle(pattern string, h http.Handler) {
	s.r.Handle(pattern, secureHeaders(h))
}

func (s securedRouter) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	s.r.Handle(pattern, secureHeaders(http.HandlerFunc(h)))
}
