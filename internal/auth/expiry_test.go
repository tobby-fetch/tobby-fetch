// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// expiry records what the session hook was told.
type expiry struct {
	account string
	reason  ExpiryReason
	origin  string
}

type expiryLog struct {
	mu  sync.Mutex
	got []expiry
}

func (l *expiryLog) hook(sess *Session, reason ExpiryReason, origin string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, expiry{sess.Account, reason, origin})
}

func (l *expiryLog) all() []expiry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]expiry(nil), l.got...)
}

// TestSessionIdleExpiry is the R-14 acceptance at the table level: a
// session left idle past the timeout is refused as idle and reported once,
// while a session kept active survives the same delay.
func TestSessionIdleExpiry(t *testing.T) {
	ss := NewSessions(12 * time.Hour)
	ss.SetIdleTimeout(30 * time.Minute)
	var log expiryLog
	ss.OnExpire(log.hook)

	idle := ss.Create("idle", RoleViewer, t0)
	active := ss.Create("active", RoleViewer, t0)

	// The active session is used every 20 minutes, the idle one never.
	for i := 1; i <= 3; i++ {
		now := t0.Add(time.Duration(i) * 20 * time.Minute)
		if _, st := ss.Lookup(active.ID, "198.51.100.7", now); st != SessionLive {
			t.Fatalf("active session at +%dm: state %v, want live", i*20, st)
		}
	}
	at := t0.Add(61 * time.Minute)
	if _, st := ss.Lookup(idle.ID, "198.51.100.7", at); st != SessionIdle {
		t.Fatalf("idle session after 61m: state %v, want SessionIdle", st)
	}
	// Found once, removed: the next lookup is an unknown cookie, and the
	// hook is not called a second time.
	if _, st := ss.Lookup(idle.ID, "198.51.100.7", at); st != SessionUnknown {
		t.Errorf("second lookup of an expired session: state %v, want unknown", st)
	}
	if _, st := ss.Lookup(active.ID, "198.51.100.7", at); st != SessionLive {
		t.Errorf("active session after 61m: state %v, want live (activity renews)", st)
	}
	got := log.all()
	if len(got) != 1 || got[0] != (expiry{"idle", ExpiryIdle, "198.51.100.7"}) {
		t.Errorf("expiry hook calls = %+v, want one idle expiry with the request origin", got)
	}
}

// TestSessionLifetimeWinsOverIdle: activity never extends the absolute
// lifetime, and when both bounds have passed the reason is the lifetime.
func TestSessionLifetimeWinsOverIdle(t *testing.T) {
	ss := NewSessions(time.Hour)
	ss.SetIdleTimeout(10 * time.Minute)
	var log expiryLog
	ss.OnExpire(log.hook)
	s := ss.Create("busy", RoleAdmin, t0)
	for m := 5; m <= 60; m += 5 {
		if _, st := ss.Lookup(s.ID, "o", t0.Add(time.Duration(m)*time.Minute)); st != SessionLive {
			t.Fatalf("at +%dm: state %v, want live", m, st)
		}
	}
	if _, st := ss.Lookup(s.ID, "o", t0.Add(61*time.Minute)); st != SessionLifetime {
		t.Errorf("past the lifetime: state %v, want SessionLifetime", st)
	}
	stale := ss.Create("stale", RoleAdmin, t0)
	if _, st := ss.Lookup(stale.ID, "o", t0.Add(2*time.Hour)); st != SessionLifetime {
		t.Errorf("idle AND past the lifetime: state %v, want SessionLifetime", st)
	}
	if got := log.all(); len(got) != 2 || got[0].reason != ExpiryLifetime || got[1].reason != ExpiryLifetime {
		t.Errorf("hook calls = %+v, want two lifetime expiries", got)
	}
}

// TestSessionSweepReportsAbandoned: a session nobody comes back to still
// leaves its expiry record, from the sweep, with the local origin.
func TestSessionSweepReportsAbandoned(t *testing.T) {
	ss := NewSessions(12 * time.Hour)
	ss.SetIdleTimeout(30 * time.Minute)
	var log expiryLog
	ss.OnExpire(log.hook)
	ss.Create("gone", RoleViewer, t0)
	ss.Sweep(t0.Add(10 * time.Minute))
	if got := log.all(); len(got) != 0 {
		t.Fatalf("sweep before the timeout reported %+v", got)
	}
	ss.Sweep(t0.Add(31 * time.Minute))
	if got := log.all(); len(got) != 1 || got[0] != (expiry{"gone", ExpiryIdle, OriginLocalSweep}) {
		t.Errorf("sweep after the timeout reported %+v, want one local idle expiry", got)
	}
}

// TestDeleteOthersSparesFederatedSessions: a local password change closes
// the local sessions of that name, never a federated principal's.
func TestDeleteOthersSparesFederatedSessions(t *testing.T) {
	ss := NewSessions(time.Hour)
	keep := ss.Create("alex", RoleAdmin, t0)
	other := ss.Create("alex", RoleAdmin, t0)
	fed := ss.CreateWith("alex", RoleViewer, MethodOIDC, t0)
	ss.DeleteOthers("alex", keep.ID)
	if _, ok := ss.Get(other.ID, t0); ok {
		t.Error("the other local session survived the password change")
	}
	if s, ok := ss.Get(fed.ID, t0); !ok || s.Method != MethodOIDC {
		t.Error("a federated session was closed by a local password change")
	}
}

// TestTokenExpiry covers FR-072's R-14 amendment in the store: explicit
// and default expiries, the dedicated expired state, and a long-lived
// token behaving exactly as before.
func TestTokenExpiry(t *testing.T) {
	s := newStore(t)

	if _, _, err := s.CreateTokenExpiring("past", RoleViewer, t0, t0.Add(-time.Second)); !errors.Is(err, ErrExpiryPast) {
		t.Errorf("expiry in the past: err = %v, want ErrExpiryPast", err)
	}
	if _, _, err := s.CreateTokenExpiring("now", RoleViewer, t0, t0); !errors.Is(err, ErrExpiryPast) {
		t.Errorf("expiry at creation instant: err = %v, want ErrExpiryPast", err)
	}

	short, tok, err := s.CreateTokenExpiring("short", RoleOperator, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !tok.Expires.Equal(t0.Add(time.Hour)) {
		t.Errorf("explicit expiry = %v, want %v", tok.Expires, t0.Add(time.Hour))
	}
	if _, st := s.VerifyToken(short, t0.Add(59*time.Minute)); st != TokenValid {
		t.Errorf("before expiry: state %v, want valid", st)
	}
	got, st := s.VerifyToken(short, t0.Add(time.Hour))
	if st != TokenExpired || got.Name != "short" {
		t.Errorf("at expiry: state %v name %q, want expired short (the instant itself is refused)", st, got.Name)
	}

	forever, tok, err := s.CreateToken("forever", RoleViewer, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !tok.Expires.IsZero() {
		t.Errorf("no default lifetime: expiry %v, want none", tok.Expires)
	}
	if _, st := s.VerifyToken(forever, t0.Add(100*365*24*time.Hour)); st != TokenValid {
		t.Errorf("long-lived token a century later: state %v, want valid", st)
	}

	s.SetDefaultTokenLifetime(24 * time.Hour)
	_, tok, err = s.CreateToken("daily", RoleViewer, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !tok.Expires.Equal(t0.Add(24 * time.Hour)) {
		t.Errorf("default lifetime: expiry %v, want %v", tok.Expires, t0.Add(24*time.Hour))
	}
	_, tok, err = s.CreateTokenExpiring("weekly", RoleViewer, t0, t0.Add(7*24*time.Hour))
	if err != nil || !tok.Expires.Equal(t0.Add(7*24*time.Hour)) {
		t.Errorf("explicit expiry beside a default: %v %v", tok.Expires, err)
	}

	// Persisted: a reopened store still refuses the expired token.
	again, err := Open(stateDirOf(s))
	if err != nil {
		t.Fatal(err)
	}
	if _, st := again.VerifyToken(short, t0.Add(2*time.Hour)); st != TokenExpired {
		t.Errorf("after reopen: state %v, want expired", st)
	}
	if err := s.RevokeToken("forever"); err != nil {
		t.Fatal(err)
	}
	if _, st := s.VerifyToken(forever, t0); st != TokenRevoked {
		t.Errorf("revoked token: state %v, want revoked", st)
	}
}

func stateDirOf(s *Store) string {
	return s.path[:len(s.path)-len(accountsFile)-1]
}

// countingObserver records metric events.
type countingObserver struct {
	mu       sync.Mutex
	attempts map[string]int
	expired  map[ExpiryReason]int
	local    int
}

func newCountingObserver() *countingObserver {
	return &countingObserver{attempts: map[string]int{}, expired: map[ExpiryReason]int{}}
}

func (o *countingObserver) Attempt(m Method, outcome string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.attempts[string(m)+"/"+outcome]++
}

func (o *countingObserver) SessionExpired(r ExpiryReason) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expired[r]++
}

func (o *countingObserver) LocalUnderFederation() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.local++
}

func (o *countingObserver) count(key string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.attempts[key]
}

// TestExpiredTokenRefusedWithItsOwnCode is the API half of the R-14
// acceptance: the token authorizes until its expiry, then is refused with
// TBY-AUTH-013 — not the generic failure — and every refusal is a
// token.expire audit record naming the token.
func TestExpiredTokenRefusedWithItsOwnCode(t *testing.T) {
	s := newStore(t)
	secret, _, err := s.CreateTokenExpiring("ci", RoleOperator, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	now := t0
	var buf bytes.Buffer
	obs := newCountingObserver()
	a := &Authenticator{Store: s, Sessions: NewSessions(time.Hour), Observer: obs,
		Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Now: func() time.Time { return now }}
	h := a.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(); w.Code != http.StatusOK {
		t.Fatalf("before expiry: %d", w.Code)
	}
	now = t0.Add(time.Hour + time.Second)
	for range 2 {
		w := call()
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("after expiry: %d, want 401", w.Code)
		}
		var p map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p["code"] != "TBY-AUTH-013" {
			t.Errorf("after expiry: code %v, want TBY-AUTH-013 (%s)", p["code"], w.Body.String())
		}
	}
	recs := auditRecords(t, buf.Bytes(), "token.expire")
	if len(recs) != 2 {
		t.Fatalf("token.expire records = %d, want one per refusal (2)", len(recs))
	}
	if recs[0]["actor"] != "ci" || recs[0]["target"] != "ci" || recs[0]["outcome"] != "denied" {
		t.Errorf("token.expire record = %v, want actor and target ci, denied", recs[0])
	}
	if got := obs.count("token/expired"); got != 2 {
		t.Errorf("expired attempts counted = %d, want 2", got)
	}
	if got := obs.count("token/success"); got != 1 {
		t.Errorf("successful attempts counted = %d, want 1", got)
	}

	// The registry surface refuses it too, with a challenge.
	reg := a.Registry(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	r := httptest.NewRequest(http.MethodGet, "/v2/", http.NoBody)
	r.SetBasicAuth("ci", secret)
	w := httptest.NewRecorder()
	reg.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("registry with an expired token: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
}

// TestRevokedTokenRefusalNamesTheToken: the refusal of a revoked token is
// audited under the token's name, not as an anonymous failure.
func TestRevokedTokenRefusalNamesTheToken(t *testing.T) {
	s := newStore(t)
	secret, _, err := s.CreateToken("old", RoleViewer, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeToken("old"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a := &Authenticator{Store: s, Logger: slog.New(slog.NewJSONHandler(&buf, nil)), Now: func() time.Time { return t0 }}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	a.API(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", w.Code)
	}
	recs := auditRecords(t, buf.Bytes(), "auth.authenticate")
	if len(recs) != 1 || recs[0]["actor"] != "old" || recs[0]["outcome"] != "failure" {
		t.Errorf("revoked-token refusal records = %v, want one failure under the token's name", recs)
	}
}

// TestAPIExpiredSessionCookie: a browser-clickable API link followed with
// an idle-expired session answers TBY-AUTH-005, the code the UI uses.
func TestAPIExpiredSessionCookie(t *testing.T) {
	ss := NewSessions(12 * time.Hour)
	ss.SetIdleTimeout(time.Minute)
	sess := ss.Create("op", RoleOperator, t0)
	a := &Authenticator{Store: newStore(t), Sessions: ss, Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return t0.Add(2 * time.Minute) }}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", http.NoBody)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: sess.ID}) //nolint:gosec // G124: a request-side cookie in a test
	w := httptest.NewRecorder()
	a.API(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || !bytes.Contains(w.Body.Bytes(), []byte("TBY-AUTH-005")) {
		t.Errorf("expired session on the API: %d %s, want 401 TBY-AUTH-005", w.Code, w.Body.String())
	}
}

// TestLocalUnderFederationOnMachineSurfaces: with a federated method
// enabled, an account password presented on a machine surface is
// break-glass use and gets its dedicated record — once per window, like
// the success record — while a token (automation) does not.
func TestLocalUnderFederationOnMachineSurfaces(t *testing.T) {
	s := newStore(t)
	if err := s.AddAccount("root", RoleAdmin, "pw", t0); err != nil {
		t.Fatal(err)
	}
	secret, _, err := s.CreateToken("ci", RoleViewer, t0)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	obs := newCountingObserver()
	federated := false
	a := &Authenticator{Store: s, Observer: obs, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
		Now: func() time.Time { return t0 }, Federated: func() bool { return federated }}
	h := a.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	basic := func(origin string) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
		r.RemoteAddr = origin + ":4000"
		r.SetBasicAuth("root", "pw")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("basic: %d", w.Code)
		}
	}
	basic("192.0.2.1")
	if n := len(auditRecords(t, buf.Bytes(), "auth.local_under_federation")); n != 0 {
		t.Fatalf("without federation: %d break-glass records, want 0", n)
	}
	federated = true
	basic("192.0.2.2")
	basic("192.0.2.2")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(httptest.NewRecorder(), r)
	recs := auditRecords(t, buf.Bytes(), "auth.local_under_federation")
	if len(recs) != 1 || recs[0]["actor"] != "root" || recs[0]["target"] != "api" || recs[0]["origin"] != "192.0.2.2" {
		t.Errorf("break-glass records = %v, want one for root on the api from 192.0.2.2", recs)
	}
	if obs.local != 1 {
		t.Errorf("break-glass metric = %d, want 1", obs.local)
	}

	// The UI path calls the same recorder directly.
	a.LocalUnderFederation(context.Background(), "root", "ui", "192.0.2.3")
	if n := len(auditRecords(t, buf.Bytes(), "auth.local_under_federation")); n != 2 {
		t.Errorf("after a UI sign-in: %d break-glass records, want 2", n)
	}
}

// TestFilesSurfaceRefusesExpiredToken: the /files/ gate goes through the
// same audited check, so an expired token is refused and recorded there
// too.
func TestFilesSurfaceRefusesExpiredToken(t *testing.T) {
	s := newStore(t)
	secret, _, err := s.CreateTokenExpiring("apt", RoleViewer, t0, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	a := &Authenticator{Store: s, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
		Now: func() time.Time { return t0.Add(time.Hour) }}
	r := httptest.NewRequest(http.MethodGet, "/files/x/y", http.NoBody)
	r.SetBasicAuth("apt", secret)
	if _, v := a.Check(r, SurfaceFiles); v != VerdictExpired {
		t.Errorf("files check: verdict %v, want expired", v)
	}
	if n := len(auditRecords(t, buf.Bytes(), "token.expire")); n != 1 {
		t.Errorf("token.expire records = %d, want 1", n)
	}
}
