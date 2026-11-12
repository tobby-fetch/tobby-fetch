// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// defaultSigningAlgorithms is the allow-list of ID token signatures when
// auth.oidc.signingAlgorithms is empty: the asymmetric algorithms only.
// "none" and the HS* family can never appear here, nor be configured
// (config refuses them): an unsigned token proves nothing, and an HMAC
// one is keyed with the client secret Tobby itself holds.
var defaultSigningAlgorithms = []string{
	oidc.RS256, oidc.RS384, oidc.RS512,
	oidc.ES256, oidc.ES384, oidc.ES512,
	oidc.PS256, oidc.PS384, oidc.PS512,
	oidc.EdDSA,
}

// oidcProvider is the OpenID Connect relying party (FR-070).
type oidcProvider struct {
	svc      *Service
	cfg      config.OIDC
	name     string
	secret   string
	algs     []string
	roles    config.RoleMapping
	groupsAt string
	nameAt   string

	// mu guards the discovered provider: discovery is lazy and retried
	// (FR-078), and once it succeeded the provider and its key set are
	// kept — the key set refreshes itself on an unknown key ID.
	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
}

func newOIDCProvider(c *config.OIDC, svc *Service) (*oidcProvider, error) {
	raw, err := os.ReadFile(c.ClientSecretFile)
	if err != nil {
		return nil, fmt.Errorf("auth.oidc.clientSecretFile: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return nil, fmt.Errorf("auth.oidc.clientSecretFile: %s is empty", c.ClientSecretFile)
	}
	algs := c.SigningAlgorithms
	if len(algs) == 0 {
		algs = defaultSigningAlgorithms
	}
	for _, a := range algs {
		// Defence in depth: config already refuses these.
		if strings.EqualFold(a, "none") || strings.HasPrefix(strings.ToUpper(a), "HS") {
			return nil, fmt.Errorf("auth.oidc.signingAlgorithms: %q is refused", a)
		}
	}
	name := c.DisplayName
	if name == "" {
		name = "OpenID Connect"
	}
	groupsAt := c.GroupsClaim
	if groupsAt == "" {
		groupsAt = "groups"
	}
	nameAt := c.NameClaim
	if nameAt == "" {
		nameAt = "preferred_username"
	}
	return &oidcProvider{svc: svc, cfg: *c, name: name, secret: secret, algs: algs, roles: c.Roles, groupsAt: groupsAt, nameAt: nameAt}, nil
}

// clientCtx carries the instance's outbound client to go-oidc and oauth2
// — both read it from the same context key. The context is detached from
// any request: go-oidc keeps the one given at discovery for its later key
// set refreshes, and a request context would be cancelled by then.
func (p *oidcProvider) clientCtx() context.Context {
	return oidc.ClientContext(context.Background(), p.svc.client)
}

// discover returns the discovered provider, discovering it now if needed.
// Discovery happens from the configured issuer only (its well-known
// document, whose "issuer" go-oidc requires to match exactly).
func (p *oidcProvider) discover(ctx context.Context) (*oidc.Provider, *oidc.IDTokenVerifier, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, p.verifier, nil
	}
	// The request-bounded context bounds the discovery itself; the client
	// context is what go-oidc keeps.
	dctx, cancel := context.WithCancel(p.clientCtx())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	prov, err := oidc.NewProvider(dctx, p.cfg.Issuer)
	if err != nil {
		return nil, nil, err
	}
	// The provider keeps the client of dctx, not dctx itself: its key set
	// refreshes later on a background context with the same client.
	p.provider = prov
	p.verifier = prov.VerifierContext(p.clientCtx(), &oidc.Config{
		ClientID:             p.cfg.ClientID,
		SupportedSigningAlgs: p.algs,
	})
	return p.provider, p.verifier, nil
}

// probe fetches the discovery document: the provider answers, or not.
func (p *oidcProvider) probe(ctx context.Context) error {
	target := strings.TrimSuffix(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := p.svc.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &statusError{url: target, code: resp.StatusCode}
	}
	return nil
}

func (p *oidcProvider) oauth2Config(prov *oidc.Provider) *oauth2.Config {
	scopes := append([]string{oidc.ScopeOpenID}, p.cfg.Scopes...)
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.secret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  p.cfg.RedirectURL,
		Scopes:       scopes,
	}
}

func (p *oidcProvider) fail(check string, err error) *Failure {
	return &Failure{Code: taxonomy.CodeOIDCFailed, Provider: p.name, Check: check, Cause: err}
}

// BeginOIDC starts an authorization-code round trip: it records the
// state, nonce and PKCE verifier server-side and returns the provider
// URL to redirect the browser to, with the binding secret the caller
// sets as BindingCookie. next must already be sanitized (B-033).
func (s *Service) BeginOIDC(ctx context.Context, next string) (redirect, binding string, err error) {
	p := s.oidc
	if p == nil {
		return "", "", errors.New("federation: OIDC is not enabled")
	}
	origin, err := s.admitStart(ctx, auth.MethodOIDC, p.name)
	if err != nil {
		return "", "", err
	}
	if !s.isReachable(auth.MethodOIDC) {
		// Known down: look again before sending the person to a provider
		// that will not answer — the button is also the retry, so a
		// provider that came back works at once, without waiting for the
		// next probe.
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		err := p.probe(pctx)
		cancel()
		if err != nil {
			return "", "", s.unreachableFailure(auth.MethodOIDC, p.name, err)
		}
	}
	prov, _, err := p.discover(ctx)
	if err != nil {
		var mismatch *oidc.IssuerMismatchError
		if errors.As(err, &mismatch) {
			// The provider answers, with another identity: a
			// configuration error, not an outage.
			return "", "", p.fail("issuer", err)
		}
		return "", "", s.unreachableFailure(auth.MethodOIDC, p.name, err)
	}
	s.setReachable(auth.MethodOIDC, true, nil)
	binding = randomString()
	e := &pending{
		method:   auth.MethodOIDC,
		binding:  bindingOf(binding),
		next:     next,
		nonce:    randomString(),
		verifier: oauth2.GenerateVerifier(),
	}
	state := s.pending.put(e, origin, s.now())
	u := p.oauth2Config(prov).AuthCodeURL(state,
		oauth2.S256ChallengeOption(e.verifier),
		oidc.Nonce(e.nonce))
	return u, binding, nil
}

// CompleteOIDC finishes the round trip at the callback: query is the
// callback's query string, binding the BindingCookie value. Every check
// that fails refuses the sign-in with TBY-AUTH-014, the check named in
// the Failure for the log; a provider that cannot be reached is
// TBY-AUTH-016; a valid identity without a mapped group is TBY-AUTH-017.
func (s *Service) CompleteOIDC(ctx context.Context, query url.Values, binding string) (*Result, error) {
	p := s.oidc
	if p == nil {
		return nil, errors.New("federation: OIDC is not enabled")
	}
	// The state is consumed whatever happens next: one state, one try.
	e, ok := s.pending.take(query.Get("state"), s.now())
	if !ok || e.method != auth.MethodOIDC {
		return nil, p.fail("state", errors.New("unknown, expired or already used state"))
	}
	if !e.bound(binding) {
		return nil, p.fail("binding", errors.New("the callback did not come from the browser that started the sign-in"))
	}
	if msg := query.Get("error"); msg != "" {
		return nil, p.fail("provider-error", fmt.Errorf("the provider answered %q: %s", msg, query.Get("error_description")))
	}
	code := query.Get("code")
	if code == "" {
		return nil, p.fail("code", errors.New("no authorization code"))
	}
	prov, verifier, err := p.discover(ctx)
	if err != nil {
		return nil, s.unreachableFailure(auth.MethodOIDC, p.name, err)
	}
	xctx, cancel := context.WithCancel(p.clientCtx())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	tok, err := p.oauth2Config(prov).Exchange(xctx, code, oauth2.VerifierOption(e.verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if unreachable(err) || (errors.As(err, &re) && re.Response != nil && re.Response.StatusCode >= 500) {
			return nil, s.unreachableFailure(auth.MethodOIDC, p.name, err)
		}
		return nil, p.fail("token-exchange", err)
	}
	s.setReachable(auth.MethodOIDC, true, nil)
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, p.fail("id-token", errors.New("the token response carries no id_token"))
	}
	// Signature (against the discovered key set, algorithm in the
	// allow-list), issuer (exact), audience (the client ID), expiry.
	idt, err := verifier.Verify(xctx, rawID)
	if err != nil {
		if unreachable(err) {
			return nil, s.unreachableFailure(auth.MethodOIDC, p.name, err)
		}
		return nil, p.fail("id-token", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(e.nonce)) != 1 {
		return nil, p.fail("nonce", errors.New("the ID token nonce does not match the sign-in"))
	}
	// Strict audience: a token minted for several clients must name this
	// one as its authorized party (OpenID Connect Core §3.1.3.7).
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, p.fail("claims", err)
	}
	if len(idt.Audience) > 1 {
		if azp, _ := claims["azp"].(string); azp != p.cfg.ClientID {
			return nil, p.fail("audience", fmt.Errorf("multiple audiences and azp %q is not the client", azp))
		}
	}
	// The key is the subject — stable and unique per issuer (OpenID
	// Connect Core §2) — never a name the person or an admin can change.
	if idt.Subject == "" {
		return nil, p.fail("subject", errors.New("the ID token names nobody"))
	}
	key := "oidc:" + idt.Subject
	display, _ := claims[p.nameAt].(string)
	display = displayName(display)
	groups := stringList(claims[p.groupsAt])
	role := mapRoles(&p.roles, groups)
	if role == "" {
		return nil, noRole(p.name, key, display, groups)
	}
	return &Result{Name: key, Display: display, Role: role, Method: auth.MethodOIDC, Next: e.next, Groups: groups}, nil
}

// stringList reads a claim that is a list of strings, or a single string
// (some providers flatten a one-group membership), bounded.
func stringList(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = []string{t}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
			if len(out) == maxGroups {
				break
			}
		}
	}
	return out
}
