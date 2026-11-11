// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// fakeOP is a minimal OpenID provider for the attack cases a real one
// will not produce on demand: tokens signed with "none" or HS256, for
// another audience or issuer, with a stale nonce. The happy path and the
// provider-side refusals are proved against Keycloak
// (test/keycloak, federation integration tests); this exists for the
// checks Tobby itself must make.
type fakeOP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu sync.Mutex
	// codes maps an issued code to the authorization request it answers.
	codes map[string]url.Values
	// mint shapes the next ID token: it receives the claims about to be
	// signed and the signing algorithm, and may change both.
	mint func(claims map[string]any) (alg string, out map[string]any)
	// down makes every endpoint answer 503.
	down bool
}

func newFakeOP(t *testing.T) *fakeOP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	op := &fakeOP{key: key, codes: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", op.discovery)
	mux.HandleFunc("GET /jwks", op.jwks)
	mux.HandleFunc("POST /token", op.token)
	op.srv = httptest.NewServer(op.guard(mux))
	t.Cleanup(op.srv.Close)
	return op
}

func (op *fakeOP) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op.mu.Lock()
		down := op.down
		op.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (op *fakeOP) setDown(v bool) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.down = v
}

func (op *fakeOP) discovery(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                op.srv.URL,
		"authorization_endpoint":                op.srv.URL + "/auth",
		"token_endpoint":                        op.srv.URL + "/token",
		"jwks_uri":                              op.srv.URL + "/jwks",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (op *fakeOP) jwks(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &op.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig",
	}}})
}

// authorize plays the provider's authorization endpoint for authURL as
// the given user and groups: it returns the callback query the browser
// would bring back.
func (op *fakeOP) authorize(t *testing.T, authURL string) url.Values {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	code := randomString()
	op.mu.Lock()
	op.codes[code] = q
	op.mu.Unlock()
	return url.Values{"code": {code}, "state": {q.Get("state")}}
}

func (op *fakeOP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	op.mu.Lock()
	req, ok := op.codes[r.PostForm.Get("code")]
	delete(op.codes, r.PostForm.Get("code"))
	mint := op.mint
	op.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	// PKCE, as a conforming provider checks it.
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if req.Get("code_challenge_method") != "S256" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != req.Get("code_challenge") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"PKCE verification failed"}`))
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": op.srv.URL, "aud": "tobby", "sub": "u-1", "preferred_username": "alice",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": req.Get("nonce"), "groups": []string{"ops"},
	}
	alg := "RS256"
	if mint != nil {
		alg, claims = mint(claims)
	}
	idToken := op.sign(alg, claims)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func (op *fakeOP) sign(alg string, claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	switch alg {
	case "none":
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"k1"}`))
		return h + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
	case "HS256":
		// Keyed with the client secret — what an attacker who learned it,
		// or a confused provider, would produce.
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("tobby-secret-tobby-secret-tobby!")},
			(&jose.SignerOptions{}).WithHeader("kid", "k1"))
		if err != nil {
			panic(err)
		}
		jws, _ := signer.Sign(payload)
		s, _ := jws.CompactSerialize()
		return s
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: op.key},
		(&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		panic(err)
	}
	jws, _ := signer.Sign(payload)
	s, _ := jws.CompactSerialize()
	return s
}

// reach records the reachability the service reports.
type reach struct {
	mu   sync.Mutex
	last map[string]bool
}

func (r *reach) IDPReachable(m string, up bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		r.last = map[string]bool{}
	}
	r.last[m] = up
}

func (r *reach) get(m string) (up, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.last[m]
	return v, ok
}

func oidcService(t *testing.T, issuer string, obs Observer) *Service {
	t.Helper()
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("tobby-secret-tobby-secret-tobby!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New(&Options{
		OIDC: config.OIDC{
			Enabled: true, Issuer: issuer, ClientID: "tobby", ClientSecretFile: secret,
			RedirectURL: "https://tobby.test/auth/oidc/callback", GroupsClaim: "groups",
			Roles: config.RoleMapping{Operator: []string{"ops"}, Admin: []string{"root"}},
		},
		Client:   &http.Client{Timeout: 5 * time.Second},
		Observer: obs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func wantCode(t *testing.T, err error, code taxonomy.Code, check string) {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want a %s Failure", err, code)
	}
	if f.Code != code || (check != "" && f.Check != check) {
		t.Fatalf("failure = %s/%s (%v), want %s/%s", f.Code, f.Check, f.Cause, code, check)
	}
}

// TestOIDCHappyPath: PKCE S256, state and nonce on the request; a valid
// token maps to the configured role and lands on the destination.
func TestOIDCHappyPath(t *testing.T) {
	op := newFakeOP(t)
	svc := oidcService(t, op.srv.URL, nil)
	ctx := context.Background()
	authURL, binding, err := svc.BeginOIDC(ctx, "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("authorization request misses PKCE, state or nonce: %s", authURL)
	}
	if !strings.HasPrefix(authURL, op.srv.URL+"/auth?") {
		t.Errorf("authorization request goes to %s, not the discovered endpoint", authURL)
	}
	res, err := svc.CompleteOIDC(ctx, op.authorize(t, authURL), binding)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "oidc:alice" || res.Role != auth.RoleOperator || res.Next != "/tasks" || res.Method != auth.MethodOIDC {
		t.Errorf("result = %+v", res)
	}
}

// TestOIDCAttacks is the refusal catalogue of the relying party.
func TestOIDCAttacks(t *testing.T) {
	op := newFakeOP(t)
	svc := oidcService(t, op.srv.URL, nil)
	ctx := context.Background()

	begin := func() (string, string) {
		a, b, err := svc.BeginOIDC(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	withMint := func(f func(map[string]any) (string, map[string]any)) {
		op.mu.Lock()
		op.mint = f
		op.mu.Unlock()
	}
	defer withMint(nil)

	t.Run("forged state", func(t *testing.T) {
		a, b := begin()
		q := op.authorize(t, a)
		q.Set("state", randomString())
		_, err := svc.CompleteOIDC(ctx, q, b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "state")
	})
	t.Run("state replayed", func(t *testing.T) {
		a, b := begin()
		q := op.authorize(t, a)
		if _, err := svc.CompleteOIDC(ctx, q, b); err != nil {
			t.Fatal(err)
		}
		_, err := svc.CompleteOIDC(ctx, q, b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "state")
	})
	t.Run("another browser", func(t *testing.T) {
		a, _ := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), randomString())
		wantCode(t, err, taxonomy.CodeOIDCFailed, "binding")
	})
	t.Run("no binding cookie", func(t *testing.T) {
		a, _ := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), "")
		wantCode(t, err, taxonomy.CodeOIDCFailed, "binding")
	})
	t.Run("wrong nonce", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) { c["nonce"] = "stale"; return "RS256", c })
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "nonce")
	})
	t.Run("wrong audience", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) { c["aud"] = "another-client"; return "RS256", c })
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "id-token")
	})
	t.Run("several audiences without azp", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) {
			c["aud"] = []string{"tobby", "another-client"}
			c["azp"] = "another-client"
			return "RS256", c
		})
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "audience")
	})
	t.Run("wrong issuer", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) { c["iss"] = "https://evil.test"; return "RS256", c })
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "id-token")
	})
	t.Run("expired token", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) {
			c["exp"] = time.Now().Add(-time.Hour).Unix()
			return "RS256", c
		})
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "id-token")
	})
	for _, alg := range []string{"none", "HS256"} {
		t.Run("alg "+alg, func(t *testing.T) {
			withMint(func(c map[string]any) (string, map[string]any) { return alg, c })
			defer withMint(nil)
			a, b := begin()
			_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
			wantCode(t, err, taxonomy.CodeOIDCFailed, "id-token")
		})
	}
	t.Run("without the PKCE verifier", func(t *testing.T) {
		// The callback is replayed into a second, unrelated sign-in: its
		// verifier does not match the challenge the code was issued for.
		a, _ := begin()
		q := op.authorize(t, a)
		_, b2 := begin()
		e, _ := svc.pending.peek(q.Get("state"), time.Now())
		e.binding = bindingOf(b2)
		e.verifier = "a-verifier-that-never-produced-this-challenge-000000000"
		_, err := svc.CompleteOIDC(ctx, q, b2)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "token-exchange")
	})
	t.Run("provider error", func(t *testing.T) {
		a, b := begin()
		q := op.authorize(t, a)
		q.Del("code")
		q.Set("error", "access_denied")
		_, err := svc.CompleteOIDC(ctx, q, b)
		wantCode(t, err, taxonomy.CodeOIDCFailed, "provider-error")
	})
	t.Run("no mapped group", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) {
			c["groups"] = []string{"marketing"}
			return "RS256", c
		})
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeFederatedNoRole, "role-mapping")
		var f *Failure
		if errors.As(err, &f) && f.Identity != "oidc:alice" {
			t.Errorf("no-role refusal identity = %q, want oidc:alice for the audit actor", f.Identity)
		}
	})
	t.Run("no groups at all", func(t *testing.T) {
		withMint(func(c map[string]any) (string, map[string]any) { delete(c, "groups"); return "RS256", c })
		defer withMint(nil)
		a, b := begin()
		_, err := svc.CompleteOIDC(ctx, op.authorize(t, a), b)
		wantCode(t, err, taxonomy.CodeFederatedNoRole, "")
	})
}

// TestOIDCProviderDown is R-20 at the relying party: an unreachable
// provider is TBY-AUTH-016, flips the reachability the sign-in page
// shows, and recovers without a restart.
func TestOIDCProviderDown(t *testing.T) {
	op := newFakeOP(t)
	var obs reach
	svc := oidcService(t, op.srv.URL, &obs)
	ctx := context.Background()

	op.setDown(true)
	_, _, err := svc.BeginOIDC(ctx, "/")
	wantCode(t, err, taxonomy.CodeIDPUnreachable, "reachability")
	if p := svc.Providers(); len(p) != 1 || p[0].Available {
		t.Errorf("providers while down = %+v, want OIDC unavailable", p)
	}
	if up, ok := obs.get("oidc"); !ok || up {
		t.Errorf("reachability metric = %v/%v, want down", up, ok)
	}

	op.setDown(false)
	svc.Probe(ctx)
	if p := svc.Providers(); !p[0].Available {
		t.Error("the probe did not bring the provider back")
	}
	a, b, err := svc.BeginOIDC(ctx, "/")
	if err != nil {
		t.Fatalf("after recovery: %v", err)
	}
	q := op.authorize(t, a)
	// Down between the redirect and the callback: the exchange is 016.
	op.setDown(true)
	_, err = svc.CompleteOIDC(ctx, q, b)
	wantCode(t, err, taxonomy.CodeIDPUnreachable, "")

	// A closed port is unreachable too — not a refused sign-in.
	op.srv.Close()
	svc2 := oidcService(t, op.srv.URL, nil)
	_, _, err = svc2.BeginOIDC(ctx, "/")
	wantCode(t, err, taxonomy.CodeIDPUnreachable, "")
}

// TestPendingTableBounded: the table that unauthenticated requests fill
// never grows past its cap, and entries expire.
func TestPendingTableBounded(t *testing.T) {
	var tab pendingTable
	now := time.Now()
	first := tab.put(&pending{}, now)
	for i := range pendingCap + 10 {
		tab.put(&pending{}, now.Add(time.Duration(i+1)*time.Millisecond))
	}
	if n := len(tab.m); n > pendingCap {
		t.Errorf("table holds %d entries, cap %d", n, pendingCap)
	}
	if _, ok := tab.peek(first, now); ok {
		t.Error("the oldest entry survived the cap")
	}
	k := tab.put(&pending{}, now)
	if _, ok := tab.take(k, now.Add(pendingTTL+time.Second)); ok {
		t.Error("an expired entry was returned")
	}
}

// TestRoleMappingRefusesByDefault pins FR-074 for federation: highest
// matched role wins, and nothing matched is no role at all.
func TestRoleMappingRefusesByDefault(t *testing.T) {
	m := &config.RoleMapping{Admin: []string{"a"}, Operator: []string{"o"}, Viewer: []string{"v"}}
	for _, tc := range []struct {
		groups []string
		want   auth.Role
	}{
		{[]string{"v", "o"}, auth.RoleOperator},
		{[]string{"v", "a", "o"}, auth.RoleAdmin},
		{[]string{"v"}, auth.RoleViewer},
		{[]string{"x"}, ""},
		{nil, ""},
		{[]string{"A"}, ""}, // exact match only
	} {
		if got := mapRoles(m, tc.groups); got != tc.want {
			t.Errorf("mapRoles(%v) = %q, want %q", tc.groups, got, tc.want)
		}
	}
}
