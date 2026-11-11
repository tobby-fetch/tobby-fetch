// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package federation implements the two federated sign-in methods of
// ADR-0009: OpenID Connect, authorization code flow with PKCE (FR-070),
// and SAML 2.0 as an SP-initiated service provider (FR-071), each mapping
// the identity provider's groups onto the three roles by explicit
// configuration, refusal by default (FR-074).
//
// What this package decides, and what it leaves to its callers. It turns
// a browser round trip into a verified identity and a role, or into a
// refusal carrying a taxonomy code; it never opens a session, writes a
// cookie or renders a page — the UI does, so a federated session is the
// same object as a local one (the sessions of Tobby, not a library's,
// ADR-0009 arbitration 3). It never logs a token, an assertion or a
// claim value beyond the identity name and the refused check.
//
// Continuity (FR-078, R-20). Nothing here runs at construction that needs
// the identity provider: discovery and metadata fetches are lazy and
// retried, a background probe keeps the reachability the sign-in page
// shows, and an unreachable provider is a TBY-AUTH-016 refusal of the
// federated sign-in — never an error that stops the instance, ends a
// session or touches the local methods.
//
// Every outbound request goes through the instance's single egress
// (internal/netx: proxy, private CAs), to the configured issuer or
// metadata URL and to the endpoints their documents name — never anywhere
// else (NFR-019).
package federation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The routes of the federated round trips, under the "/auth/" prefix the
// listener reserves for them (internal/server.ReservedPrefixes). The
// configured redirect and ACS URLs must name these paths.
const (
	PathOIDCLogin    = "/auth/oidc/login"
	PathOIDCCallback = "/auth/oidc/callback"
	PathSAMLLogin    = "/auth/saml/login"
	PathSAMLACS      = "/auth/saml/acs"
	PathSAMLFinish   = "/auth/saml/finish"
	PathSAMLMetadata = "/auth/saml/metadata"
)

// BindingCookie is the browser binding of a sign-in in flight: HttpOnly,
// SameSite=Lax, scoped to /auth/ and short-lived (see pending.go).
const BindingCookie = "tobby_fed"

// BindingMaxAge is the binding cookie's lifetime, the pending TTL.
const BindingMaxAge = int(pendingTTL / time.Second)

// Result is a verified federated identity with its role.
type Result struct {
	// Name is the identity KEY as Tobby records it: the method, a colon,
	// and the provider's stable identifier for the person — the OIDC
	// "sub", the SAML persistent NameID. Opaque and stable: a display name
	// can be renamed or reused at the provider, a subject cannot. The
	// prefix keeps a federated identity from ever being taken for a local
	// account.
	Name string
	// Display is the readable name (OIDC auth.oidc.nameClaim, SAML
	// auth.saml.nameAttribute), bounded and cleaned. Display only — the
	// screen and the actor_name of logs and audit — never a key and never
	// an authorization input. Empty when the provider sent none.
	Display string
	Role    auth.Role
	Method  auth.Method
	// Next is the sanitized destination the sign-in started from.
	Next string
	// Groups are the provider groups the role was derived from, for the
	// log line of the sign-in.
	Groups []string
}

// Failure is a refused federated sign-in: the taxonomy code shown to the
// person and the precise check, which goes to the logs only (TBY-AUTH-014
// and -015 deliberately do not say which check failed).
type Failure struct {
	Code     taxonomy.Code
	Provider string
	// Check names the refused check for the log ("nonce", "state",
	// "audience"…): a stable, greppable word.
	Check string
	Cause error
	// Identity is the verified identity key of a refusal that came after
	// authentication (TBY-AUTH-017), for the audit actor; empty otherwise.
	Identity string
	// Display is that identity's readable name, when known.
	Display string
}

func (f *Failure) Error() string {
	return fmt.Sprintf("%s (%s): %s: %v", f.Code, f.Provider, f.Check, f.Cause)
}

func (f *Failure) Unwrap() error { return f.Cause }

// Taxonomy returns the user-facing error of the refusal.
func (f *Failure) Taxonomy() *taxonomy.Error {
	return taxonomy.New(f.Code, taxonomy.Params{"provider": f.Provider}).WithCause(f)
}

// Provider is one federated method as the sign-in page shows it.
type Provider struct {
	Method auth.Method
	// Name is the display name (auth.<method>.displayName, else the
	// protocol name).
	Name string
	// LoginPath starts the round trip.
	LoginPath string
	// Available is false while the provider is known unreachable; the
	// page then states it with Code (TBY-AUTH-016) and keeps the button,
	// which retries.
	Available bool
}

// Observer receives the reachability of each provider, for the
// tobby_auth_idp_reachable gauge.
type Observer interface {
	IDPReachable(method string, up bool)
}

// Options builds a Service.
type Options struct {
	OIDC config.OIDC
	SAML config.SAML
	// Client is the outbound HTTP client, built from the instance egress.
	// Required when a method is enabled.
	Client *http.Client
	Logger *slog.Logger
	// Observer is optional.
	Observer Observer
	// Now injects time in tests; nil means time.Now.
	Now func() time.Time
}

// Service is the federated sign-in of one instance. Safe for concurrent
// use.
type Service struct {
	logger   *slog.Logger
	observer Observer
	now      func() time.Time
	client   *http.Client
	pending  pendingTable
	// starts bounds how fast one origin starts sign-ins (B-049).
	starts *auth.OriginLimiter

	oidc *oidcProvider
	saml *samlProvider

	mu        sync.Mutex
	reachable map[auth.Method]bool
}

// New builds the federated sign-in from configuration. It reads the
// local files the configuration names (client secret, SP key pair,
// pinned IdP metadata and certificate) and fails on any of them — those
// are the operator's own files, an error there is a configuration error.
// It contacts no identity provider: that is lazy (FR-078). A Service
// with neither method enabled is valid and offers nothing.
func New(opts *Options) (*Service, error) {
	s := &Service{
		logger:    opts.Logger,
		observer:  opts.Observer,
		now:       opts.Now,
		reachable: map[auth.Method]bool{},
		starts:    auth.NewOriginLimiter(startBurst, startRefill),
	}
	if opts.Client != nil {
		// Same transport, but no redirect is followed: every request goes
		// to the URL the configuration or the provider's own documents
		// name, and a 3xx answer is a refusal, not a detour (NFR-019).
		c := *opts.Client
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		s.client = &c
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if (opts.OIDC.Enabled || opts.SAML.Enabled) && s.client == nil {
		return nil, errors.New("federation: an outbound HTTP client is required")
	}
	if opts.OIDC.Enabled {
		p, err := newOIDCProvider(&opts.OIDC, s)
		if err != nil {
			return nil, err
		}
		s.oidc = p
		s.reachable[auth.MethodOIDC] = true
	}
	if opts.SAML.Enabled {
		p, err := newSAMLProvider(&opts.SAML, s)
		if err != nil {
			return nil, err
		}
		s.saml = p
		s.reachable[auth.MethodSAML] = true
	}
	return s, nil
}

// Enabled reports whether a federated method is enabled — the FR-078
// condition under which a local sign-in is break-glass.
func (s *Service) Enabled() bool { return s != nil && (s.oidc != nil || s.saml != nil) }

// Providers lists the enabled methods for the sign-in page, OIDC first.
func (s *Service) Providers() []Provider {
	if s == nil {
		return nil
	}
	var out []Provider
	if s.oidc != nil {
		out = append(out, Provider{Method: auth.MethodOIDC, Name: s.oidc.name, LoginPath: PathOIDCLogin, Available: s.isReachable(auth.MethodOIDC)})
	}
	if s.saml != nil {
		out = append(out, Provider{Method: auth.MethodSAML, Name: s.saml.name, LoginPath: PathSAMLLogin, Available: s.isReachable(auth.MethodSAML)})
	}
	return out
}

func (s *Service) isReachable(m auth.Method) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reachable[m]
}

// setReachable records a provider's reachability, logging transitions
// once each way (a provider flapping writes two lines per flap, not one
// per probe) and feeding the gauge.
func (s *Service) setReachable(m auth.Method, up bool, cause error) {
	s.mu.Lock()
	was, known := s.reachable[m]
	s.reachable[m] = up
	s.mu.Unlock()
	if s.observer != nil {
		s.observer.IDPReachable(string(m), up)
	}
	switch {
	case !up && (was || !known):
		s.logger.LogAttrs(context.Background(), slog.LevelWarn, "identity provider unreachable: federated sign-in unavailable, local sign-in unaffected",
			slog.String("method", string(m)), slog.String("code", string(taxonomy.CodeIDPUnreachable)),
			slog.String("error", errString(cause)), slog.String("requirement", "FR-078"))
	case up && !was:
		s.logger.LogAttrs(context.Background(), slog.LevelInfo, "identity provider reachable again: federated sign-in available",
			slog.String("method", string(m)), slog.String("requirement", "FR-078"))
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ProbeInterval paces the background reachability probe.
const ProbeInterval = 30 * time.Second

// probeTimeout bounds one probe: the sign-in page must not wait on a
// black-holed provider, and neither does the probe.
const probeTimeout = 10 * time.Second

// Probe checks every enabled provider once and records the result.
func (s *Service) Probe(ctx context.Context) {
	if s.oidc != nil {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		err := s.oidc.probe(pctx)
		cancel()
		s.setReachable(auth.MethodOIDC, err == nil, err)
	}
	if s.saml != nil && s.saml.probes() {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		err := s.saml.probe(pctx)
		cancel()
		s.setReachable(auth.MethodSAML, err == nil, err)
	}
}

// Run probes at ProbeInterval until ctx ends. The first probe runs at
// once, in the caller's goroutine's stead: Run is meant to be started
// with go, and the instance starts serving without waiting for it.
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	t := time.NewTicker(ProbeInterval)
	defer t.Stop()
	for {
		s.Probe(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// unreachable reports whether err is the provider not answering — a
// network failure or a timeout, or a 5xx — rather than the provider
// answering something wrong. The first is TBY-AUTH-016 and flips the
// reachability; the second is a refused sign-in.
func unreachable(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	var st *statusError
	if errors.As(err, &st) {
		return st.code >= 500
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// statusError is a non-2xx answer from a provider endpoint.
type statusError struct {
	url  string
	code int
}

func (e *statusError) Error() string { return fmt.Sprintf("%s answered HTTP %d", e.url, e.code) }

// originKey carries the client network origin of a request into the
// sign-in it starts.
type originKey struct{}

// WithOrigin returns ctx carrying the client network origin of the
// request (auth.ClientOrigin) — what BeginOIDC and BeginSAML bound the
// rate and the in-flight sign-ins of. A context without one shares the
// bucket of every other such context.
func WithOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

func originOf(ctx context.Context) string {
	o, _ := ctx.Value(originKey{}).(string)
	return o
}

// admitStart spends one unit of the origin's start budget, or refuses the
// sign-in with TBY-AUTH-018 — before anything is stored or any provider
// contacted. The origin's passage into throttling is logged once.
func (s *Service) admitStart(ctx context.Context, m auth.Method, provider string) (string, error) {
	origin := originOf(ctx)
	ok, exhausted := s.starts.Take(origin, s.now())
	if exhausted {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "federated sign-in starts throttled",
			slog.String("origin", origin), slog.String("method", string(m)),
			slog.String("retry_after", s.starts.Refill().String()),
			slog.String("code", string(taxonomy.CodeFederatedThrottled)))
	}
	if !ok {
		return origin, &Failure{Code: taxonomy.CodeFederatedThrottled, Provider: provider, Check: "start-rate",
			Cause: errors.New("too many sign-ins started from origin " + origin)}
	}
	return origin, nil
}

// RetryAfter is how long a throttled origin should wait before starting a
// sign-in again, for the Retry-After header.
func (s *Service) RetryAfter() time.Duration { return s.starts.Refill() }

// unreachableFailure builds the TBY-AUTH-016 refusal and records the
// provider down.
func (s *Service) unreachableFailure(m auth.Method, provider string, err error) *Failure {
	s.setReachable(m, false, err)
	return &Failure{Code: taxonomy.CodeIDPUnreachable, Provider: provider, Check: "reachability", Cause: err}
}

// mapRoles applies the configured mapping: the highest role any of the
// groups grants, or the empty role when none matches — refusal by
// default, never viewer (FR-074, TBY-AUTH-017).
func mapRoles(m *config.RoleMapping, groups []string) auth.Role {
	has := func(list []string) bool {
		for _, want := range list {
			for _, g := range groups {
				if g == want {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has(m.Admin):
		return auth.RoleAdmin
	case has(m.Operator):
		return auth.RoleOperator
	case has(m.Viewer):
		return auth.RoleViewer
	}
	return ""
}

// noRole builds the TBY-AUTH-017 refusal.
func noRole(provider, identity, display string, groups []string) *Failure {
	return &Failure{Code: taxonomy.CodeFederatedNoRole, Provider: provider, Check: "role-mapping", Identity: identity, Display: display,
		Cause: fmt.Errorf("none of the %d groups sent is mapped to a role", len(groups))}
}

// maxGroups bounds how many group values a sign-in carries into the role
// mapping: the claim is provider-controlled, and a directory member of
// thousands of groups must cost a bounded scan.
const maxGroups = 1024

// maxDisplay bounds a display name, in runes.
const maxDisplay = 128

// displayName cleans a provider-supplied display name: control characters
// dropped, whitespace trimmed, length bounded. It is shown and logged,
// never trusted — but it must not be able to forge a log line or flood a
// header.
func displayName(v string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(v) {
		if unicode.IsControl(r) {
			continue
		}
		if n == maxDisplay {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
