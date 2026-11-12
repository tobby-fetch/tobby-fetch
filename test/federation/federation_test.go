// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation_test

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/federation"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/test/keycloak"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestFederation is the 6.3 / R-20 acceptance against a real identity
// provider: one Keycloak, one Tobby, every scenario a subtest (the
// fixture costs seconds to boot). R-20 runs last: it cuts the provider.
func TestFederation(t *testing.T) {
	e := newEnv(t)
	t.Run("oidc sign-in per role", func(t *testing.T) { oidcPerRole(t, e) })
	t.Run("oidc attacks", func(t *testing.T) { oidcAttacks(t, e) })
	t.Run("saml sign-in per role", func(t *testing.T) { samlPerRole(t, e) })
	t.Run("saml attacks", func(t *testing.T) { samlAttacks(t, e) })
	t.Run("saml clock out of tolerance", func(t *testing.T) { samlClock(t, e) })
	t.Run("the four methods side by side", func(t *testing.T) { fourMethods(t, e) })
	t.Run("identity provider down (R-20)", func(t *testing.T) { idpDown(t, e) })
}

// oidcStart opens the OIDC sign-in in b and returns Keycloak's
// authorization URL.
func (e *env) oidcStart(b *browser, next string) string {
	b.t.Helper()
	resp, body := b.get(e.tobby.URL + federation.PathOIDCLogin + "?next=" + url.QueryEscape(next))
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("starting the OIDC sign-in: %d %s", resp.StatusCode, snippet(body))
	}
	return location(b.t, resp)
}

// oidcFinish brings the provider's redirect back to Tobby.
func (e *env) oidcFinish(b *browser, redirect *url.URL) (resp *http.Response, body string) {
	b.t.Helper()
	return b.get(redirect.String())
}

func (e *env) oidcSignIn(t *testing.T, b *browser, u keycloak.User, next string) (resp *http.Response, body string) {
	t.Helper()
	authURL := e.oidcStart(b, next)
	redirect, err := e.kc.LoginOIDC(context.Background(), authURL, u)
	if err != nil {
		t.Fatalf("keycloak login as %s: %v", u.Username, err)
	}
	return e.oidcFinish(b, redirect)
}

func snippet(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func oidcPerRole(t *testing.T, e *env) {
	for _, u := range []keycloak.User{keycloak.Viewer, keycloak.Operator, keycloak.Admin} {
		t.Run(u.Username, func(t *testing.T) {
			b := newBrowser(t)
			resp, body := e.oidcSignIn(t, b, u, "/tasks")
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/tasks" {
				t.Fatalf("callback: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), snippet(body))
			}
			if got := e.whoAmI(b); string(got) != u.Role {
				t.Errorf("%s signed in as %q, want %q (group %s)", u.Username, got, u.Role, u.Group)
			}
		})
	}
	t.Run("no group", func(t *testing.T) {
		b := newBrowser(t)
		resp, body := e.oidcSignIn(t, b, keycloak.NoGroup, "/")
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "TBY-AUTH-017") {
			t.Errorf("no-group user: %d, want 403 TBY-AUTH-017: %s", resp.StatusCode, snippet(body))
		}
		if e.whoAmI(b) != "" {
			t.Error("a user in no mapped group holds a session")
		}
	})
	recs := e.audit.records("session.login")
	var named bool
	for _, r := range recs {
		actor, _ := r["actor"].(string)
		named = named || (r["actor_name"] == "admin" && strings.HasPrefix(actor, "oidc:") && actor != "oidc:admin" && r["outcome"] == "success")
	}
	if !named {
		t.Error("no session.login success record keyed on the OIDC subject with actor_name admin")
	}
}

func hasRecord(recs []map[string]any, actor, target, outcome string) bool {
	for _, r := range recs {
		if r["actor"] == actor && r["target"] == target && r["outcome"] == outcome {
			return true
		}
	}
	return false
}

func oidcAttacks(t *testing.T, e *env) {
	ctx := context.Background()
	t.Run("without PKCE", func(t *testing.T) {
		b := newBrowser(t)
		authURL := e.oidcStart(b, "/")
		u, _ := url.Parse(authURL)
		q := u.Query()
		q.Del("code_challenge")
		q.Del("code_challenge_method")
		u.RawQuery = q.Encode()
		redirect, err := e.kc.LoginOIDC(ctx, u.String(), keycloak.Admin)
		if err == nil && redirect.Query().Get("code") != "" {
			t.Fatal("Keycloak issued a code without a PKCE challenge")
		}
		if err == nil {
			// Keycloak sent the error back to the callback: Tobby refuses it.
			resp, body := e.oidcFinish(b, redirect)
			if !strings.Contains(body, "TBY-AUTH-014") || resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("callback carrying the PKCE refusal: %d %s", resp.StatusCode, snippet(body))
			}
		}
		if e.whoAmI(b) != "" {
			t.Error("a session exists after a sign-in without PKCE")
		}
	})
	t.Run("forged state", func(t *testing.T) {
		b := newBrowser(t)
		redirect, err := e.kc.LoginOIDC(ctx, e.oidcStart(b, "/"), keycloak.Admin)
		if err != nil {
			t.Fatal(err)
		}
		q := redirect.Query()
		q.Set("state", "forged-state")
		redirect.RawQuery = q.Encode()
		resp, body := e.oidcFinish(b, redirect)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "TBY-AUTH-014") || e.whoAmI(b) != "" {
			t.Errorf("forged state: %d %s", resp.StatusCode, snippet(body))
		}
	})
	t.Run("wrong nonce", func(t *testing.T) {
		b := newBrowser(t)
		u, _ := url.Parse(e.oidcStart(b, "/"))
		q := u.Query()
		q.Set("nonce", "attacker-chosen-nonce")
		u.RawQuery = q.Encode()
		redirect, err := e.kc.LoginOIDC(ctx, u.String(), keycloak.Admin)
		if err != nil {
			t.Fatal(err)
		}
		resp, body := e.oidcFinish(b, redirect)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "TBY-AUTH-014") || e.whoAmI(b) != "" {
			t.Errorf("wrong nonce: %d %s", resp.StatusCode, snippet(body))
		}
		if !e.audit.contains(`"check":"nonce"`) {
			t.Error("the log does not name the nonce check")
		}
	})
	t.Run("code replayed into another browser", func(t *testing.T) {
		b := newBrowser(t)
		redirect, err := e.kc.LoginOIDC(ctx, e.oidcStart(b, "/"), keycloak.Admin)
		if err != nil {
			t.Fatal(err)
		}
		thief := newBrowser(t)
		resp, body := e.oidcFinish(thief, redirect)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "TBY-AUTH-014") || e.whoAmI(thief) != "" {
			t.Errorf("another browser's callback: %d %s", resp.StatusCode, snippet(body))
		}
	})
	t.Run("open redirect", func(t *testing.T) {
		b := newBrowser(t)
		resp, _ := e.oidcSignIn(t, b, keycloak.Viewer, "//evil.test/steal")
		if loc := resp.Header.Get("Location"); loc != "/" {
			t.Errorf("post-login redirect to %q, want / (B-033)", loc)
		}
	})
}

// samlStart opens the SAML sign-in and returns the IdP redirect URL.
func (e *env) samlStart(b *browser, next string) string {
	b.t.Helper()
	resp, body := b.get(e.tobby.URL + federation.PathSAMLLogin + "?next=" + url.QueryEscape(next))
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("starting the SAML sign-in: %d %s", resp.StatusCode, snippet(body))
	}
	return location(b.t, resp)
}

// samlPost posts the IdP's auto-submitted form to the ACS and, when it is
// accepted, follows to the finish step.
func (e *env) samlPost(b *browser, p keycloak.SAMLPost) (resp *http.Response, body string) {
	b.t.Helper()
	resp, body = b.do(http.MethodPost, p.Action, url.Values{"SAMLResponse": {p.SAMLResponse}, "RelayState": {p.RelayState}})
	if resp.StatusCode != http.StatusSeeOther {
		return resp, body
	}
	return b.get(location(b.t, resp))
}

func (e *env) samlLogin(t *testing.T, b *browser, u keycloak.User, next string) keycloak.SAMLPost {
	t.Helper()
	post, err := e.kc.LoginSAML(context.Background(), e.samlStart(b, next), u)
	if err != nil {
		t.Fatalf("keycloak SAML login as %s: %v", u.Username, err)
	}
	if post.Action != e.tobby.URL+federation.PathSAMLACS {
		t.Fatalf("the response is posted to %s, not the ACS", post.Action)
	}
	return post
}

func samlPerRole(t *testing.T, e *env) {
	for _, u := range []keycloak.User{keycloak.Viewer, keycloak.Operator, keycloak.Admin} {
		t.Run(u.Username, func(t *testing.T) {
			b := newBrowser(t)
			resp, body := e.samlPost(b, e.samlLogin(t, b, u, "/recipes"))
			if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/recipes" {
				t.Fatalf("finish: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), snippet(body))
			}
			if got := e.whoAmI(b); string(got) != u.Role {
				t.Errorf("%s signed in as %q, want %q", u.Username, got, u.Role)
			}
		})
	}
	t.Run("no group", func(t *testing.T) {
		b := newBrowser(t)
		resp, body := e.samlPost(b, e.samlLogin(t, b, keycloak.NoGroup, "/"))
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "TBY-AUTH-017") || e.whoAmI(b) != "" {
			t.Errorf("no-group user: %d %s", resp.StatusCode, snippet(body))
		}
	})
	t.Run("metadata", func(t *testing.T) {
		_, body := newBrowser(t).get(e.tobby.URL + federation.PathSAMLMetadata)
		if !strings.Contains(body, e.tobby.URL+federation.PathSAMLACS) || !strings.Contains(body, "X509Certificate") {
			t.Errorf("SP metadata: %s", snippet(body))
		}
	})
}

// stripSignatures removes every XML signature from a response.
func stripSignatures(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		t.Fatal(err)
	}
	for _, sig := range doc.FindElements("//Signature") {
		sig.Parent().RemoveChild(sig)
	}
	out, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out)
}

func samlAttacks(t *testing.T, e *env) {
	refused := func(t *testing.T, b *browser, resp *http.Response, body string) {
		t.Helper()
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "TBY-AUTH-015") {
			t.Errorf("want 401 TBY-AUTH-015, got %d: %s", resp.StatusCode, snippet(body))
		}
		if e.whoAmI(b) != "" {
			t.Error("a session exists after a refused SAML response")
		}
	}
	t.Run("replayed assertion", func(t *testing.T) {
		b := newBrowser(t)
		post := e.samlLogin(t, b, keycloak.Admin, "/")
		if resp, body := e.samlPost(b, post); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("first use: %d %s", resp.StatusCode, snippet(body))
		}
		attacker := newBrowser(t)
		resp, body := e.samlPost(attacker, post)
		refused(t, attacker, resp, body)
	})
	t.Run("idp-initiated (no RelayState)", func(t *testing.T) {
		b := newBrowser(t)
		post := e.samlLogin(t, b, keycloak.Admin, "/")
		post.RelayState = ""
		resp, body := e.samlPost(b, post)
		refused(t, b, resp, body)
	})
	t.Run("signature removed", func(t *testing.T) {
		b := newBrowser(t)
		post := e.samlLogin(t, b, keycloak.Admin, "/")
		post.SAMLResponse = stripSignatures(t, post.SAMLResponse)
		resp, body := e.samlPost(b, post)
		refused(t, b, resp, body)
		if !e.audit.contains(`"check":"signature"`) {
			t.Error("the log does not name the signature check")
		}
	})
	t.Run("response for another service provider", func(t *testing.T) {
		// A second SP identity over the same IdP: Keycloak's response to
		// the first is not an answer to anything the second asked.
		b := newBrowser(t)
		post := e.samlLogin(t, b, keycloak.Admin, "/")
		other := e.otherSP(t, "https://other-sp.test/auth/saml/metadata", 0)
		_, relay, err := other.BeginSAML(context.Background(), "/")
		if err != nil {
			t.Fatal(err)
		}
		if err := other.ConsumeSAML(context.Background(), post.SAMLResponse, relay); err == nil {
			t.Error("another service provider accepted a response issued for this one")
		}
	})
	t.Run("collected by another browser", func(t *testing.T) {
		b := newBrowser(t)
		post := e.samlLogin(t, b, keycloak.Admin, "/")
		thief := newBrowser(t)
		resp, body := e.samlPost(thief, post)
		refused(t, thief, resp, body)
	})
	t.Run("oversized body", func(t *testing.T) {
		b := newBrowser(t)
		resp, body := b.do(http.MethodPost, e.tobby.URL+federation.PathSAMLACS,
			url.Values{"SAMLResponse": {strings.Repeat("A", federation.MaxSAMLBody+1)}})
		refused(t, b, resp, body)
	})
}

// otherSP builds a SAML-only service on the same files as the env's,
// with another entity ID and/or a shifted clock.
func (e *env) otherSP(t *testing.T, entityID string, shift time.Duration) *federation.Service {
	t.Helper()
	cfg := config.SAML{
		Enabled: true, EntityID: entityID, ACSURL: e.tobby.URL + federation.PathSAMLACS,
		IDPMetadataFile: e.dir + "/idp.xml", CertFile: e.dir + "/sp.crt", KeyFile: e.dir + "/sp.key",
		GroupsAttribute: "groups", ClockSkew: config.Duration(90 * time.Second),
		Roles: config.RoleMapping{Admin: []string{keycloak.GroupAdmin}},
	}
	svc, err := federation.New(&federation.Options{SAML: cfg, Client: &http.Client{Timeout: 5 * time.Second},
		Now: func() time.Time { return time.Now().Add(shift) }})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// samlClock: the same instance identity, its clock ten minutes ahead of
// the IdP's — far beyond the 90 s tolerance. Keycloak's genuine response
// is refused on the clock check.
func samlClock(t *testing.T, e *env) {
	skewed := e.otherSP(t, e.tobby.URL+federation.PathSAMLMetadata, 10*time.Minute)
	target, _, err := skewed.BeginSAML(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	post, err := e.kc.LoginSAML(context.Background(), target, keycloak.Admin)
	if err != nil {
		t.Fatal(err)
	}
	relay := post.RelayState
	err = skewed.ConsumeSAML(context.Background(), post.SAMLResponse, relay)
	var f *federation.Failure
	if !errors.As(err, &f) || f.Check != "clock" {
		t.Errorf("a response from a clock 10 min behind: %v, want the clock check", err)
	}
}

// fourMethods: OIDC, SAML, tokens and basic all work on one instance.
func fourMethods(t *testing.T, e *env) {
	if got := e.whoAmIAfter(t, func(b *browser) { e.oidcSignIn(t, b, keycloak.Operator, "/") }); got != auth.RoleOperator {
		t.Errorf("oidc: %q", got)
	}
	if got := e.whoAmIAfter(t, func(b *browser) { e.samlPost(b, e.samlLogin(t, b, keycloak.Viewer, "/")) }); got != auth.RoleViewer {
		t.Errorf("saml: %q", got)
	}
	if got := e.whoAmIAfter(t, func(b *browser) { e.localSignIn(b) }); got != auth.RoleAdmin {
		t.Errorf("basic: %q", got)
	}
	if code := e.tokenCall(t); code != http.StatusOK {
		t.Errorf("token: %d", code)
	}
}

func (e *env) whoAmIAfter(t *testing.T, signIn func(*browser)) auth.Role {
	t.Helper()
	b := newBrowser(t)
	signIn(b)
	return e.whoAmI(b)
}

// tokenCall authenticates one API call with a fresh static token.
func (e *env) tokenCall(t *testing.T) int {
	t.Helper()
	secret, _, err := e.authn.Store.CreateToken("ci-"+time.Now().Format("150405.000000000"), auth.RoleViewer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := e.authn.API(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/ping", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// idpDown is the R-20 acceptance (FR-078): with the provider cut, the
// sign-in page states the outage with its code naming the provider, the
// local administrator signs in — with the dedicated audit event — tokens
// keep authorizing, existing federated sessions survive; once the
// provider is back, federated sign-in works without a restart.
func idpDown(t *testing.T, e *env) {
	ctx := context.Background()
	federated := newBrowser(t)
	e.oidcSignIn(t, federated, keycloak.Admin, "/")
	if e.whoAmI(federated) != auth.RoleAdmin {
		t.Fatal("no federated session before the cut")
	}

	e.proxy.cut.Store(true)
	e.fed.Probe(ctx)

	b := newBrowser(t)
	_, page := b.get(e.tobby.URL + "/login")
	if !strings.Contains(page, "TBY-AUTH-016") || !strings.Contains(page, "Fixture directory") || !strings.Contains(page, `id="password"`) {
		t.Errorf("the sign-in page does not state the outage beside the local form: %s", snippet(page))
	}
	resp, body := b.get(e.tobby.URL + federation.PathOIDCLogin + "?next=%2F")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "TBY-AUTH-016") {
		t.Errorf("federated sign-in while down: %d %s", resp.StatusCode, snippet(body))
	}
	if r := e.localSignIn(b); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("local sign-in while the provider is down: %d", r.StatusCode)
	}
	if e.whoAmI(b) != auth.RoleAdmin {
		t.Error("the local administrator cannot work while the provider is down")
	}
	if !hasRecord(e.audit.records("auth.local_under_federation"), localAdmin, "ui", "success") {
		t.Error("no auth.local_under_federation record for the break-glass sign-in")
	}
	if code := e.tokenCall(t); code != http.StatusOK {
		t.Errorf("a token stopped authorizing while the provider is down: %d", code)
	}
	if e.whoAmI(federated) != auth.RoleAdmin {
		t.Error("the outage ended an existing federated session")
	}

	e.proxy.cut.Store(false)
	e.fed.Probe(ctx)
	_, page = newBrowser(t).get(e.tobby.URL + "/login")
	if strings.Contains(page, "data-idp-unavailable") {
		t.Error("the sign-in page still states the outage after the provider came back")
	}
	back := newBrowser(t)
	if resp, body := e.oidcSignIn(t, back, keycloak.Viewer, "/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("federated sign-in after recovery: %d %s", resp.StatusCode, snippet(body))
	}
	if e.whoAmI(back) != auth.RoleViewer {
		t.Error("federated sign-in does not work again without a restart")
	}
}
