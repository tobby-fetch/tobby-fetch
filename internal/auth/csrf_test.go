// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSameOrigin is the provenance decision table of SameOrigin, reverse
// proxies included (NFR-012 amendment: the defense-in-depth half).
func TestSameOrigin(t *testing.T) {
	for _, c := range []struct {
		name    string
		host    string
		headers map[string]string
		want    bool
	}{
		{"no provenance at all (curl, old browser)", "tobby.example", nil, true},
		{"same origin", "tobby.example:8080", map[string]string{"Origin": "http://tobby.example:8080"}, true},
		{"https behind a TLS-terminating proxy", "tobby.example", map[string]string{"Origin": "https://tobby.example"}, true},
		{"default port spelled out", "tobby.example:443", map[string]string{"Origin": "https://tobby.example"}, true},
		{"host case", "Tobby.Example", map[string]string{"Origin": "https://tobby.example"}, true},
		{"proxy rewrote Host, kept X-Forwarded-Host", "127.0.0.1:8080", map[string]string{"Origin": "https://tobby.example", "X-Forwarded-Host": "tobby.example"}, true},
		{"proxy chain", "127.0.0.1:8080", map[string]string{"Origin": "https://tobby.example", "X-Forwarded-Host": "edge.example, tobby.example"}, true},
		{"cross-site Origin", "tobby.example", map[string]string{"Origin": "https://evil.example"}, false},
		{"other port is another origin", "tobby.example:8080", map[string]string{"Origin": "http://tobby.example:9090"}, false},
		{"sibling subdomain", "tobby.example.org", map[string]string{"Origin": "https://evil.example.org"}, false},
		{"unparseable Origin", "tobby.example", map[string]string{"Origin": "::"}, false},
		{"Origin null (no-referrer form post)", "tobby.example", map[string]string{"Origin": "null"}, true},
		{"Origin null, browser says cross-site", "tobby.example", map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}, false},
		{"Sec-Fetch-Site same-origin wins over a rewritten Host", "127.0.0.1:8080", map[string]string{"Origin": "https://tobby.example", "Sec-Fetch-Site": "same-origin"}, true},
		{"Sec-Fetch-Site none (typed address)", "tobby.example", map[string]string{"Sec-Fetch-Site": "none"}, true},
		{"Sec-Fetch-Site same-site", "tobby.example", map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"Sec-Fetch-Site cross-site", "tobby.example", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://tobby.example"}, false},
		{"Sec-Fetch-Site unknown value", "tobby.example", map[string]string{"Sec-Fetch-Site": "bogus"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
			r.Host = c.host
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := SameOrigin(r); got != c.want {
				t.Errorf("SameOrigin = %v, want %v", got, c.want)
			}
		})
	}
}

// csrfEnv is an API surface with one live operator session and one
// operator token.
type csrfEnv struct {
	a      *Authenticator
	sess   *Session
	bearer string
	now    time.Time
	served []string
}

func newCSRFEnv(t *testing.T) *csrfEnv {
	t.Helper()
	s := newStore(t)
	secret, _, err := s.CreateToken("ci", RoleOperator, t0)
	if err != nil {
		t.Fatal(err)
	}
	ss := NewSessions(12 * time.Hour)
	ss.SetIdleTimeout(time.Minute)
	sess := ss.Create("op", RoleOperator, t0)
	e := &csrfEnv{sess: &sess, bearer: secret, now: t0}
	e.a = &Authenticator{Store: s, Sessions: ss, Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return e.now }}
	return e
}

// do sends one API request; mod decorates it.
func (e *csrfEnv) do(method string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1/sync", strings.NewReader(`{}`))
	r.Host = "tobby.example"
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	e.a.API(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := IdentityFrom(r.Context())
		e.served = append(e.served, id.Name)
		w.WriteHeader(http.StatusAccepted)
	})).ServeHTTP(w, r)
	return w
}

func (e *csrfEnv) cookie(r *http.Request) {
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: e.sess.ID}) //nolint:gosec // G124: a request-side cookie in a test
}

func refusedCSRF(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "TBY-AUTH-004")
}

// TestAPICookieMutationNeedsToken is the API half of the NFR-012
// amendment: a mutating call authenticated by the session cookie is
// refused with TBY-AUTH-004 without its token, with a forged one, and
// from another origin even with the right one; with the token and from
// this origin it is served as the session's identity. Bearer is exempt.
func TestAPICookieMutationNeedsToken(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			e := newCSRFEnv(t)
			for _, c := range []struct {
				name string
				mod  func(*http.Request)
			}{
				{"cookie, no token", func(r *http.Request) { e.cookie(r) }},
				{"cookie, forged token", func(r *http.Request) { e.cookie(r); r.Header.Set(CSRFHeader, "forged") }},
				{"cookie, token in a form field is not read on the API", func(r *http.Request) {
					e.cookie(r)
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}},
				{"cookie, token, cross-site Origin", func(r *http.Request) {
					e.cookie(r)
					r.Header.Set(CSRFHeader, e.sess.CSRF)
					r.Header.Set("Origin", "https://evil.example")
				}},
				{"cookie, token, Sec-Fetch-Site cross-site", func(r *http.Request) {
					e.cookie(r)
					r.Header.Set(CSRFHeader, e.sess.CSRF)
					r.Header.Set("Sec-Fetch-Site", "cross-site")
				}},
			} {
				if w := e.do(method, c.mod); !refusedCSRF(w) {
					t.Errorf("%s: %d %s, want 403 TBY-AUTH-004", c.name, w.Code, w.Body.String())
				}
			}
			if len(e.served) != 0 {
				t.Fatalf("a refused request reached the handler: %v", e.served)
			}

			w := e.do(method, func(r *http.Request) {
				e.cookie(r)
				r.Header.Set(CSRFHeader, e.sess.CSRF)
				r.Header.Set("Origin", "https://tobby.example")
			})
			if w.Code != http.StatusAccepted || len(e.served) != 1 || e.served[0] != "op" {
				t.Errorf("cookie + token + same origin: %d served=%v, want 202 as op", w.Code, e.served)
			}
			w = e.do(method, func(r *http.Request) {
				e.cookie(r)
				r.Header.Set("Authorization", "Bearer "+e.bearer)
				r.Header.Set("Origin", "https://evil.example")
			})
			if w.Code != http.StatusAccepted || e.served[len(e.served)-1] != "ci" {
				t.Errorf("bearer: %d served=%v, want 202 as the token (bearer carries no ambient authority)", w.Code, e.served)
			}
		})
	}
}

// TestAPIForgedMutationDoesNotRenewSession: a refused forgery must not
// keep the victim's session alive — the check peeks, it does not look up.
func TestAPIForgedMutationDoesNotRenewSession(t *testing.T) {
	e := newCSRFEnv(t)
	e.now = t0.Add(50 * time.Second)
	if w := e.do(http.MethodPost, func(r *http.Request) { e.cookie(r) }); !refusedCSRF(w) {
		t.Fatalf("forged: %d", w.Code)
	}
	// Idle timeout is one minute from t0: had the forgery renewed the
	// session, it would still be live 70 s after t0.
	e.now = t0.Add(70 * time.Second)
	w := e.do(http.MethodPost, func(r *http.Request) { e.cookie(r); r.Header.Set(CSRFHeader, e.sess.CSRF) })
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "TBY-AUTH-005") {
		t.Errorf("after a forgery, the idle session answered %d %s, want 401 TBY-AUTH-005", w.Code, w.Body.String())
	}
}

// TestAPIOverrideRefusesCrossOrigin: under the FR-075 override every
// request is the anonymous administrator, so a mutation another origin's
// page makes the browser send is refused; a client without browser
// provenance, or with an explicit credential, is not.
func TestAPIOverrideRefusesCrossOrigin(t *testing.T) {
	e := newCSRFEnv(t)
	e.a.Disabled = true
	if w := e.do(http.MethodPost, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); !refusedCSRF(w) {
		t.Errorf("override, cross-site: %d, want 403 TBY-AUTH-004", w.Code)
	}
	if w := e.do(http.MethodPost, nil); w.Code != http.StatusAccepted {
		t.Errorf("override, no provenance: %d, want 202", w.Code)
	}
	if w := e.do(http.MethodGet, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); w.Code != http.StatusAccepted {
		t.Errorf("override, cross-site GET: %d, want 202 (no GET changes state)", w.Code)
	}
}

// TestPresentedNameBounded (NFR-023): the name a failed Basic attempt
// claims becomes the audit actor; an over-long or control-character one
// is recorded as InvalidActor, never as given, and is not verified.
func TestPresentedNameBounded(t *testing.T) {
	var buf bytes.Buffer
	a := &Authenticator{Store: newStore(t), Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
		Now: func() time.Time { return t0 }}
	h := a.API(http.NotFoundHandler())
	for _, name := range []string{strings.Repeat("A", 4096), "root\nforged"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
		r.SetBasicAuth(name, "pw")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%d", w.Code)
		}
	}
	recs := auditRecords(t, buf.Bytes(), "auth.authenticate")
	if len(recs) != 2 {
		t.Fatalf("%d records, want 2", len(recs))
	}
	for _, rec := range recs {
		if rec["actor"] != InvalidActor {
			t.Errorf("actor = %.40q, want %q", rec["actor"], InvalidActor)
		}
	}
	if !ValidPresentedName("alexis") || !ValidPresentedName("Zoé.d@example.org") || ValidPresentedName(strings.Repeat("a", 257)) {
		t.Error("ValidPresentedName misjudges an ordinary name or the bound")
	}
}
