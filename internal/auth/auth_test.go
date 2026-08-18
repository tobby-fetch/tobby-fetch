// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoleLadder(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleOperator) || !RoleOperator.AtLeast(RoleViewer) || !RoleViewer.AtLeast(RoleViewer) {
		t.Error("role ladder broken upward")
	}
	if RoleViewer.AtLeast(RoleOperator) || RoleOperator.AtLeast(RoleAdmin) {
		t.Error("role ladder broken downward")
	}
	if Role("intruder").AtLeast(RoleViewer) {
		t.Error("unknown roles must rank below viewer")
	}
	if _, err := ParseRole("root"); err == nil {
		t.Error("ParseRole must reject unknown roles")
	}
}

func TestAccountLifecycle(t *testing.T) {
	s := newStore(t)
	if s.HasAccounts() {
		t.Fatal("fresh store must have no accounts (R-01 gate)")
	}
	if err := s.AddAccount("alexis", RoleAdmin, "correct horse", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAccount("alexis", RoleViewer, "x", t0); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate account: err = %v, want ErrExists", err)
	}

	if _, ok := s.VerifyPassword("alexis", "wrong", t0); ok {
		t.Error("wrong password accepted")
	}
	if _, ok := s.VerifyPassword("ghost", "correct horse", t0); ok {
		t.Error("unknown account accepted")
	}
	acct, ok := s.VerifyPassword("alexis", "correct horse", t0.Add(time.Hour))
	if !ok || acct.Role != RoleAdmin {
		t.Fatalf("valid login refused: ok=%v acct=%+v", ok, acct)
	}

	if err := s.SetPassword("alexis", "new phrase", t0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyPassword("alexis", "correct horse", t0); ok {
		t.Error("old password still accepted after passwd")
	}
	if _, ok := s.VerifyPassword("alexis", "new phrase", t0); !ok {
		t.Error("new password refused")
	}
	if err := s.SetPassword("ghost", "x", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("passwd on unknown account: err = %v, want ErrNotFound", err)
	}
}

// TestPersistenceAndPermissions locks the on-disk contract: reload sees the
// same data, the hash is argon2id PHC (never a bare password), and the file
// is 0600 (NFR-018).
func TestPersistenceAndPermissions(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAccount("op", RoleOperator, "s3cret", t0); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, accountsFile)) //nolint:gosec // G304: test-owned temp path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("clear password on disk")
	}
	if !strings.Contains(string(raw), "$argon2id$") {
		t.Error("hash is not argon2id PHC")
	}
	info, err := os.Stat(filepath.Join(dir, accountsFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("accounts file mode = %o, want 0600", perm)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.VerifyPassword("op", "s3cret", t0); !ok {
		t.Error("reloaded store refuses the password")
	}
	accts := s2.Accounts()
	if len(accts) != 1 || accts[0].Role != RoleOperator {
		t.Errorf("reloaded accounts = %+v", accts)
	}
}

func TestTokenLifecycle(t *testing.T) {
	s := newStore(t)
	secret, tok, err := s.CreateToken("ci-pull", RoleViewer, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "tby_") {
		t.Errorf("token secret %q misses the tby_ prefix", secret)
	}
	if strings.Contains(tok.Hash, secret) {
		t.Error("token stored in clear")
	}
	if _, _, err := s.CreateToken("ci-pull", RoleViewer, t0); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate live token: err = %v, want ErrExists", err)
	}

	got, ok := s.VerifyToken(secret)
	if !ok || got.Name != "ci-pull" || got.Role != RoleViewer {
		t.Fatalf("valid token refused: %+v ok=%v", got, ok)
	}
	if _, ok := s.VerifyToken("tby_forged"); ok {
		t.Error("forged token accepted")
	}

	if err := s.RevokeToken("ci-pull"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(secret); ok {
		t.Error("revoked token still accepted (FR-072: revocation is immediate)")
	}
	// The name is reusable after revocation; history keeps the revoked row.
	if _, _, err := s.CreateToken("ci-pull", RoleOperator, t0); err != nil {
		t.Errorf("recreating a revoked token name: %v", err)
	}
	if n := len(s.Tokens()); n != 2 {
		t.Errorf("token history rows = %d, want 2 (revoked + live)", n)
	}
}

func TestSessions(t *testing.T) {
	ss := NewSessions(time.Hour)
	sess := ss.Create("alexis", RoleAdmin, t0)
	if sess.ID == "" || sess.CSRF == "" || sess.ID == sess.CSRF {
		t.Fatal("session ids and CSRF tokens must be distinct random values")
	}
	if got, ok := ss.Get(sess.ID, t0.Add(30*time.Minute)); !ok || got.Account != "alexis" {
		t.Error("live session not found")
	}
	if _, ok := ss.Get(sess.ID, t0.Add(2*time.Hour)); ok {
		t.Error("expired session still served")
	}
	sess2 := ss.Create("op", RoleOperator, t0)
	ss.Delete(sess2.ID)
	if _, ok := ss.Get(sess2.ID, t0); ok {
		t.Error("deleted session still served")
	}
	if !sess.CheckCSRF(sess.CSRF) || sess.CheckCSRF("") || sess.CheckCSRF("forged") {
		t.Error("CSRF check broken")
	}
}

// TestSessionsDeleteOthers covers the R-34 password-change invalidation:
// every session of the account dies except the kept one; other accounts
// are untouched.
func TestSessionsDeleteOthers(t *testing.T) {
	ss := NewSessions(time.Hour)
	kept := ss.Create("alexis", RoleAdmin, t0)
	other := ss.Create("alexis", RoleAdmin, t0)
	foreign := ss.Create("op", RoleOperator, t0)

	ss.DeleteOthers("alexis", kept.ID)
	if _, ok := ss.Get(kept.ID, t0); !ok {
		t.Error("kept session died with the others")
	}
	if _, ok := ss.Get(other.ID, t0); ok {
		t.Error("other session of the account survived DeleteOthers")
	}
	if _, ok := ss.Get(foreign.ID, t0); !ok {
		t.Error("session of another account was collateral damage")
	}

	// keep "" (API path): every session of the account dies.
	ss.DeleteOthers("alexis", "")
	if _, ok := ss.Get(kept.ID, t0); ok {
		t.Error("session survived DeleteOthers with no kept id")
	}
}

// TestRegistryMiddleware locks the /v2/ contract: 401 + Basic challenge
// without credentials, viewer can pull, viewer cannot push, operator can
// push, token-as-Basic-password works (FR-076), and the FR-075 override
// lets everything through as "anonymous".
func TestRegistryMiddleware(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("viewer", RoleViewer, "pw-v", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAccount("op", RoleOperator, "pw-o", t0); err != nil {
		t.Fatal(err)
	}
	pushSecret, _, err := s.CreateToken("pusher", RoleOperator, t0)
	if err != nil {
		t.Fatal(err)
	}

	var lastIdentity Identity
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastIdentity, _ = IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	logger := slog.New(slog.DiscardHandler)
	h := (&Authenticator{Store: s, Logger: logger, Now: func() time.Time { return t0 }}).Registry(backend)

	req := func(method, user, pass, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v2/docker.io/library/redis/manifests/7.2", http.NoBody)
		if user != "" {
			r.SetBasicAuth(user, pass)
		}
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := req(http.MethodGet, "", "", ""); w.Code != http.StatusUnauthorized ||
		!strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("anonymous pull: code=%d challenge=%q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	if w := req(http.MethodGet, "viewer", "pw-v", ""); w.Code != http.StatusOK {
		t.Errorf("viewer pull refused: %d", w.Code)
	}
	if w := req(http.MethodPut, "viewer", "pw-v", ""); w.Code != http.StatusForbidden {
		t.Errorf("viewer push allowed: %d", w.Code)
	}
	if w := req(http.MethodPut, "op", "pw-o", ""); w.Code != http.StatusOK {
		t.Errorf("operator push refused: %d", w.Code)
	}
	if lastIdentity.Name != "op" || lastIdentity.Role != RoleOperator {
		t.Errorf("identity in context = %+v", lastIdentity)
	}
	// docker login with a token as the Basic password (FR-076).
	if w := req(http.MethodPut, "whatever", pushSecret, ""); w.Code != http.StatusOK {
		t.Errorf("token as Basic password refused: %d", w.Code)
	}
	if lastIdentity.Name != "pusher" || !lastIdentity.Token {
		t.Errorf("token identity = %+v", lastIdentity)
	}
	if w := req(http.MethodGet, "", "", pushSecret); w.Code != http.StatusOK {
		t.Errorf("bearer refused: %d", w.Code)
	}
	if w := req(http.MethodGet, "viewer", "wrong", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", w.Code)
	}

	// FR-075 override: open, but explicitly anonymous.
	open := (&Authenticator{Store: s, Disabled: true, Logger: logger}).Registry(backend)
	r := httptest.NewRequest(http.MethodPut, "/v2/x/blobs/uploads/", http.NoBody)
	w := httptest.NewRecorder()
	open.ServeHTTP(w, r)
	if w.Code != http.StatusOK || lastIdentity.Name != "anonymous" || !lastIdentity.Anonymous {
		t.Errorf("override: code=%d identity=%+v", w.Code, lastIdentity)
	}
}

// TestAPISessionReadOnly locks the browser-clickable API contract: a valid
// UI session authenticates GET on /api/v1 (log download, OpenAPI), but
// never a mutation — those require Basic or Bearer.
func TestAPISessionReadOnly(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("op", RoleOperator, "pw", t0); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessions(time.Hour)
	sess := sessions.Create("op", RoleOperator, t0)
	a := &Authenticator{Store: s, Sessions: sessions,
		Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return t0 }}
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := a.API(backend)

	req := func(method string, cookie bool) int {
		r := httptest.NewRequest(method, "/api/v1/tasks/tsk_x/logs", http.NoBody)
		if cookie {
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: sess.ID})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	if got := req(http.MethodGet, true); got != http.StatusOK {
		t.Errorf("GET with session = %d, want 200", got)
	}
	if got := req(http.MethodGet, false); got != http.StatusUnauthorized {
		t.Errorf("GET without credentials = %d, want 401", got)
	}
	if got := req(http.MethodPost, true); got != http.StatusUnauthorized {
		t.Errorf("POST with session only = %d, want 401 (mutations need Basic/Bearer)", got)
	}
}

// TestAccountDeleteAndRole covers the FR-073/FR-074 lifecycle the admin
// surfaces expose: removal, role change, and their unknown-account paths.
func TestAccountDeleteAndRole(t *testing.T) {
	s := newStore(t)
	for _, a := range []struct {
		name string
		role Role
	}{{"alexis", RoleAdmin}, {"claire", RoleAdmin}, {"lecteur", RoleViewer}} {
		if err := s.AddAccount(a.name, a.role, "pw-"+a.name, t0); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.SetRole("lecteur", RoleOperator); err != nil {
		t.Fatal(err)
	}
	acct, ok := s.Account("lecteur")
	if !ok || acct.Role != RoleOperator {
		t.Fatalf("role change not applied: %+v ok=%v", acct, ok)
	}
	// Idempotent: setting the role an account already holds succeeds.
	if err := s.SetRole("lecteur", RoleOperator); err != nil {
		t.Errorf("no-op role change failed: %v", err)
	}

	if err := s.DeleteAccount("lecteur"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Account("lecteur"); ok {
		t.Error("deleted account still resolves")
	}
	if _, ok := s.VerifyPassword("lecteur", "pw-lecteur", t0); ok {
		t.Error("deleted account still authenticates")
	}

	if err := s.DeleteAccount("ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete unknown: err = %v, want ErrNotFound", err)
	}
	if err := s.SetRole("ghost", RoleViewer); !errors.Is(err, ErrNotFound) {
		t.Errorf("role change on unknown: err = %v, want ErrNotFound", err)
	}

	// The change survives a reopen: the invariant is on the file, not on
	// the in-memory copy.
	reopened, err := Open(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.Accounts()); got != 2 {
		t.Errorf("reopened store holds %d accounts, want 2", got)
	}
}

// TestLastAdminProtected is the FR-005 safety invariant: an instance can
// never be driven into a state with no administrator — not by deleting the
// last one, and not by an administrator demoting itself.
func TestLastAdminProtected(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("alexis", RoleAdmin, "pw", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAccount("lecteur", RoleViewer, "pw", t0); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAccount("alexis"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the last admin: err = %v, want ErrLastAdmin", err)
	}
	if err := s.SetRole("alexis", RoleOperator); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demoting the last admin: err = %v, want ErrLastAdmin", err)
	}
	if acct, _ := s.Account("alexis"); acct.Role != RoleAdmin {
		t.Errorf("refused demotion still changed the role: %s", acct.Role)
	}

	// Promoting a second administrator unlocks both operations.
	if err := s.SetRole("lecteur", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole("alexis", RoleOperator); err != nil {
		t.Errorf("demotion with a second admin present: %v", err)
	}
	// And the invariant now protects the remaining one.
	if err := s.DeleteAccount("lecteur"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the new last admin: err = %v, want ErrLastAdmin", err)
	}
	if !s.HasAccounts() {
		t.Error("the store must never end up empty through this path (FR-005)")
	}
}

// auditRecords decodes the audit lines of a captured JSON-Lines log,
// keeping only the requested action.
func auditRecords(t *testing.T, raw []byte, action string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("audit log is not JSON Lines: %v", err)
		}
		if rec["log_type"] != "audit" || rec["action"] != action {
			continue
		}
		for _, field := range []string{"actor", "action", "target", "outcome", "origin", "time"} {
			if _, ok := rec[field]; !ok {
				t.Errorf("audit record misses the %q field of the FR-094 schema: %v", field, rec)
			}
		}
		out = append(out, rec)
	}
	return out
}

// TestMachineAuthAudited is the FR-094 authentication coverage of the
// machine surfaces: a wrong credential is recorded every time with the
// name that was tried, a valid one is recorded once per window, and a
// request carrying no credential at all records an authorization denial
// rather than a phantom authentication failure.
func TestMachineAuthAudited(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("op", RoleOperator, "pw-o", t0); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a := &Authenticator{
		Store:  s,
		Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
		Now:    func() time.Time { return t0 },
	}
	h := a.Registry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	pull := func(user, pass string) {
		r := httptest.NewRequest(http.MethodGet, "/v2/lib/redis/manifests/7", http.NoBody)
		if user != "" {
			r.SetBasicAuth(user, pass)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	pull("", "")           // bare probe: the challenge exchange, not an event
	pull("op", "wrong")    // failure
	pull("ghost", "wrong") // failure, unknown name
	for range 5 {
		pull("op", "pw-o") // one event, four requests of traffic
	}

	failures := auditRecords(t, buf.Bytes(), "auth.authenticate")
	var names, outcomes []string
	for _, rec := range failures {
		names = append(names, rec["actor"].(string))
		outcomes = append(outcomes, rec["outcome"].(string))
		if rec["target"] != "registry" {
			t.Errorf("authentication record targets %v, want the surface", rec["target"])
		}
		if rec["origin"] != "192.0.2.1" {
			t.Errorf("authentication record origin = %v, want the peer address", rec["origin"])
		}
	}
	if len(failures) != 3 {
		t.Fatalf("auth.authenticate records = %v (%d), want 2 failures and 1 success", outcomes, len(failures))
	}
	if outcomes[0] != "failure" || outcomes[1] != "failure" || outcomes[2] != "success" {
		t.Errorf("outcomes = %v, want [failure failure success]", outcomes)
	}
	if names[0] != "op" || names[1] != "ghost" || names[2] != "op" {
		t.Errorf("actors = %v, want the presented names", names)
	}
	// The bare probe recorded a denial, not an authentication failure.
	if got := len(auditRecords(t, buf.Bytes(), "registry.access")); got != 1 {
		t.Errorf("registry.access denials = %d, want 1 (the credential-less probe)", got)
	}
}

// TestAuthSuccessWindowExpires: past the window the same credential is an
// event again — a token still in use tomorrow shows up in tomorrow's trail.
func TestAuthSuccessWindowExpires(t *testing.T) {
	s := newStore(t)
	secret, _, err := s.CreateToken("ci", RoleViewer, t0)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	now := t0
	a := &Authenticator{
		Store:      s,
		Logger:     slog.New(slog.NewJSONHandler(&buf, nil)),
		Now:        func() time.Time { return now },
		AuthWindow: time.Hour,
	}
	h := a.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func() {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
		r.Header.Set("Authorization", "Bearer "+secret)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	call()
	call()
	now = now.Add(30 * time.Minute)
	call()
	now = now.Add(31 * time.Minute)
	call()

	records := auditRecords(t, buf.Bytes(), "auth.authenticate")
	if len(records) != 2 {
		t.Fatalf("auth.authenticate records = %d, want 2 (one per window)", len(records))
	}
	for _, rec := range records {
		if rec["actor"] != "ci" || rec["target"] != "api" || rec["outcome"] != "success" {
			t.Errorf("record off schema: %v", rec)
		}
	}
}

// TestAuthOverrideEmitsNoPerRequestAudit: with authentication disabled
// (FR-075) there is no credential to verify, and the override is already
// audited once at startup — the request path must stay silent.
func TestAuthOverrideEmitsNoPerRequestAudit(t *testing.T) {
	var buf bytes.Buffer
	a := &Authenticator{Disabled: true, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	h := a.Registry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/v2/x/blobs/uploads/", http.NoBody))
	if buf.Len() != 0 {
		t.Errorf("the FR-075 override audits per request: %s", buf.String())
	}
}
