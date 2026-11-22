// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The NFR-012 acceptance (amendment 2026-11-16), over the same route
// table as the RBAC matrix: every state-changing route of the UI and of
// the API, authenticated by the session cookie, is refused with
// TBY-AUTH-004 without a token, with a forged one, and from another
// origin; the same request with its token from this origin — or, on the
// API, with a bearer token — passes the gate. And no GET route writes.

package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// csrfExempt reports whether a route is outside NFR-012's scope: safe
// methods, the federated callbacks (state / InResponseTo, FR-070/071),
// and the sign-in form, which no session authenticates (it is covered by
// the origin check alone, TestLoginRefusesCrossOrigin).
func csrfExempt(row *rbacRoute) bool {
	return auth.SafeMethod(row.Method) || strings.HasPrefix(row.Path, "/auth/") || row.Pattern == "POST /login"
}

// csrfProbe sends one cookie-authenticated request as an administrator
// (so that the role gate never decides) with the given token and origin.
func (e *rbacEnv) csrfProbe(t *testing.T, row *rbacRoute, api bool, token, origin string) *httptest.ResponseRecorder {
	t.Helper()
	sess := e.u.authn.Sessions.Create("probe-admin", auth.RoleAdmin, t0)
	if token == "valid" {
		token = sess.CSRF
	}
	var r *http.Request
	if api {
		r = httptest.NewRequest(row.Method, row.Path, strings.NewReader(row.Body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set(auth.CSRFHeader, token)
		}
	} else {
		body := row.Form
		if token != "" {
			if body != "" {
				body += "&"
			}
			body += "csrf=" + token
		}
		r = httptest.NewRequest(row.Method, row.Path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: sess.ID}) //nolint:gosec // G124: a request-side cookie in a test
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func csrfRefusal(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "TBY-AUTH-004")
}

// TestCSRFOnEveryStateChangingRoute is the NFR-012 acceptance.
func TestCSRFOnEveryStateChangingRoute(t *testing.T) {
	env := newRBACEnv(t)
	const self = "http://example.com" // httptest's default Host
	checked := 0
	for _, surface := range []struct {
		api  bool
		rows []rbacRoute
	}{{false, uiMatrix}, {true, apiMatrix}} {
		for i := range surface.rows {
			row := &surface.rows[i]
			if csrfExempt(row) {
				continue
			}
			checked++
			name := row.Pattern
			for _, c := range []struct{ what, token, origin string }{
				{"without a token", "", ""},
				{"with a forged token", "forged-" + strings.Repeat("0", 32), ""},
				{"with its token from another origin", "valid", "https://evil.example"},
				{"with its token from a sibling site", "valid", "http://evil.example.com"},
			} {
				if w := env.csrfProbe(t, row, surface.api, c.token, c.origin); !csrfRefusal(w) {
					t.Errorf("%s %s: %d, want 403 TBY-AUTH-004", name, c.what, w.Code)
				}
			}
			// Admission: the token from this origin passes the gate (the
			// probe then fails in the handler, on purpose — rbacRoute).
			w := env.csrfProbe(t, row, surface.api, "valid", self)
			if csrfRefusal(w) || w.Code == http.StatusUnauthorized {
				t.Errorf("%s with its token from this origin: %d %s, want the gate to open", name, w.Code, w.Body.String())
			}
			if surface.api {
				// Bearer carries no ambient authority: accepted without any
				// token, whatever the origin.
				r := httptest.NewRequest(row.Method, row.Path, strings.NewReader(row.Body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+env.tokens[auth.RoleAdmin])
				r.Header.Set("Origin", "https://evil.example")
				w := httptest.NewRecorder()
				env.mux.ServeHTTP(w, r)
				if csrfRefusal(w) || w.Code == http.StatusUnauthorized {
					t.Errorf("%s with a bearer token: %d, want the gate to open", name, w.Code)
				}
			}
		}
	}
	if checked < 40 {
		t.Fatalf("only %d state-changing routes checked: the enumeration lost its rows", checked)
	}
}

// TestLoginRefusesCrossOrigin: the sign-in form has no session to carry a
// token, so login CSRF — signing the victim's browser in as the
// attacker — is stopped by the origin check alone.
func TestLoginRefusesCrossOrigin(t *testing.T) {
	mux := mount(newTestUI(t, false))
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alexis&password=pw-admin&next=%2F"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if !csrfRefusal(w) || len(w.Result().Cookies()) != 0 {
		t.Errorf("cross-site sign-in: %d cookies=%v, want 403 TBY-AUTH-004 and no session", w.Code, w.Result().Cookies())
	}
}

// TestOverrideRefusesCrossOriginUI: under the FR-075 override there is no
// session and no token, so a cross-origin state change is refused on the
// origin alone — the UI half of the API's TestAPIOverrideRefusesCrossOrigin.
func TestOverrideRefusesCrossOriginUI(t *testing.T) {
	mux := mount(newTestUI(t, true))
	for _, path := range []string{"/admin/store/reset", "/lang"} {
		// "Origin: null" is what a page served with no-referrer, or a
		// sandboxed iframe, makes the browser send (B-069): over plain
		// HTTP there is no Sec-Fetch-Site to tell it apart, so it is
		// refused like a named foreign origin.
		for _, origin := range []string{"https://evil.example", "null"} {
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("confirmation=x&lang=fr"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if !csrfRefusal(w) {
				t.Errorf("override, cross-site POST %s from Origin %s: %d, want 403 TBY-AUTH-004", path, origin, w.Code)
			}
		}
	}
}

// stateDigest fingerprints everything a request could durably change: the
// store tree, the task queue, the accounts and the tokens.
func (e *rbacEnv) stateDigest(t *testing.T) string {
	t.Helper()
	h := sha256.New()
	_, _ = io.WriteString(h, strings.Join(e.stateLines(t), "\n"))
	return hex.EncodeToString(h.Sum(nil))
}

// stateLines is the state stateDigest fingerprints, one line per entry, so
// that a failure can name what changed.
func (e *rbacEnv) stateLines(t *testing.T) []string {
	t.Helper()
	var out []string
	root := e.u.store.Root()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, fmt.Sprintf("%s %d %d", rel, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range e.u.queue.List(tasks.Status(""), "", "") {
		out = append(out, fmt.Sprintf("task %s %s", task.ID, task.Status))
	}
	var lines []string
	for _, a := range e.u.authn.Store.Accounts() {
		lines = append(lines, fmt.Sprintf("account %s %s", a.Name, a.Role))
	}
	for _, tok := range e.u.authn.Store.Tokens() {
		lines = append(lines, fmt.Sprintf("token %s %s %v", tok.Name, tok.Role, tok.Revoked))
	}
	sort.Strings(lines)
	return append(out, lines...)
}

// stateDiff names the entries present in one state listing and not the other.
func stateDiff(before, after []string) string {
	seen := map[string]int{}
	for _, l := range before {
		seen[l]--
	}
	for _, l := range after {
		seen[l]++
	}
	var d []string
	for l, n := range seen {
		switch {
		case n < 0:
			d = append(d, "- "+l)
		case n > 0:
			d = append(d, "+ "+l)
		}
	}
	sort.Strings(d)
	return strings.Join(d, "; ")
}

// TestNoSafeRouteWrites is the last clause of the NFR-012 acceptance: the
// enumeration finds no GET handler that writes. Every GET row of both
// matrices is sent as an administrator, and the instance's durable state
// is the same afterwards.
func TestNoSafeRouteWrites(t *testing.T) {
	env := newRBACEnv(t)
	before := env.stateDigest(t)
	beforeLines := env.stateLines(t)
	for _, surface := range []struct {
		api  bool
		rows []rbacRoute
	}{{false, uiMatrix}, {true, apiMatrix}} {
		for i := range surface.rows {
			row := &surface.rows[i]
			if !auth.SafeMethod(row.Method) || strings.HasPrefix(row.Path, "/auth/") {
				continue
			}
			if surface.api {
				env.probeAPI(t, auth.RoleAdmin, row)
			} else {
				env.probeUI(t, auth.RoleAdmin, row)
			}
			if after := env.stateDigest(t); after != before {
				afterLines := env.stateLines(t)
				t.Errorf("%s changed the instance's state: %s", row.Pattern, stateDiff(beforeLines, afterLines))
				before, beforeLines = after, afterLines
			}
		}
	}
	// Control: the digest does see a write, or the loop above proves
	// nothing.
	env.probeAPI(t, auth.RoleAdmin, &rbacRoute{Method: http.MethodPost, Path: "/api/v1/tokens",
		Body: `{"name":"digest-control","role":"viewer"}`})
	if env.stateDigest(t) == before {
		t.Fatal("creating a token left the state digest unchanged: the digest is blind")
	}
}

// TestLoginNameBounded (NFR-023): the username of a failed sign-in is the
// audit actor; one outside the name grammar is recorded as
// auth.InvalidActor — not the up-to-16-MiB payload an unauthenticated
// client chose — and still costs the origin's failure budget.
func TestLoginNameBounded(t *testing.T) {
	var logs strings.Builder
	u := newTestUIWithOptions(t, &Options{}, &logs)
	mux := mount(u)
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("username="+strings.Repeat("A", 64<<10)+"&password=x&next=%2F"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "TBY-AUTH-002") {
		t.Errorf("over-long username: %d, want 401 TBY-AUTH-002", w.Code)
	}
	if strings.Contains(logs.String(), strings.Repeat("A", 300)) || !strings.Contains(logs.String(), auth.InvalidActor) {
		t.Errorf("the claimed name reached the trail as given (%d log bytes)", logs.Len())
	}
}
