// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The milestone-6 configuration surface: the vulnerability-scan policy
// (FR-031, FR-032), session and token expiry (FR-072 amendment, R-14), and
// the two federated identity methods (FR-070, FR-071, FR-078). This file
// declares the keys, their safe defaults and their validation; it gives
// them no behaviour. The features that consume them land on their own
// tracks, and until one does, an opt-in that would otherwise be silently
// ignored is REFUSED at load (pendingOptIns) — a configuration that asks
// for blocking, federation or token expiry must not start an instance that
// does none of them (FR-075: nothing relaxes security implicitly, and an
// ignored hardening request is exactly that).

// Scan configures vulnerability scanning and its policy at the instance
// level (FR-031 amendment 2026-11-11, ADR-0008). The Retriever
// (spec.scanPolicy, RECIPE-SPEC §10.4) and the recipe annotation
// recipe.tobby.dev/scan-threshold refine it; the mode is set here only.
//
// There is deliberately no "enabled" switch: the scan always runs and its
// results are always visible. Advisory is the default mode, not an
// absence of scanning.
type Scan struct {
	// Mode is "warn" (advisory, the default) or "block".
	Mode string `yaml:"mode"`
	// Threshold is the CVSS base score from which a finding violates the
	// policy, inclusive, from 0 to 10. Default 7.0. The Retriever may
	// override it in either direction; a recipe may only lower the result.
	Threshold float64 `yaml:"threshold"`
	// Exceptions are CVE identifiers tolerated beyond the threshold at
	// the instance level, added to the Retriever's. List-valued:
	// configuration file only.
	Exceptions []ScanException `yaml:"exceptions,omitempty"`
	// MaxDBAge is the maximum age of the vulnerability database, measured
	// from the build timestamp it records (FR-032 amendment, R-13).
	// Default 168h. Must be positive: an unchecked database age is not an
	// option a value of zero may select.
	MaxDBAge Duration `yaml:"maxDbAge"`
	// MaxDBAgeAction is what happens beyond MaxDBAge: "warn" (default) or
	// "block" — items scanned with a stale database are then not pushed,
	// whatever Mode says.
	MaxDBAgeAction string `yaml:"maxDbAgeAction"`
	// DBRepository is the relocated repository of the offline database in
	// this instance's store (ADR-0008 decision 3, ADR-0013). Empty selects
	// the relocated path of the upstream Trivy database the tobby-system
	// recipe carries.
	DBRepository string `yaml:"dbRepository,omitempty"`
	// Trivy locates and pins the scanner binary (ADR-0008 decision 4).
	Trivy Trivy `yaml:"trivy"`
}

// ScanException tolerates one vulnerability beyond the threshold, as in
// RECIPE-SPEC §10.4.
type ScanException struct {
	// ID is the CVE identifier ("CVE-2024-12345").
	ID string `yaml:"id"`
	// Justification is optional at milestone 6 (R-15 bounds exceptions
	// at milestone 7).
	Justification string `yaml:"justification,omitempty"`
	// Expires is optional at milestone 6. A past expiry means the
	// exception no longer applies.
	Expires time.Time `yaml:"expires,omitempty"`
}

// Trivy locates the scanner binary. With every field empty the platform
// default applies (FR-031 amendment): the binary bundled in the container
// image; on Linux and Windows hosts the binary delivered by the
// tobby-system channel; on macOS the first "trivy" on the PATH. A missing
// or unverifiable binary is the visible degraded mode, never a silent one.
type Trivy struct {
	// Path is an explicit binary location, absolute. Exclusive with Ref.
	Path string `yaml:"path,omitempty"`
	// Digest pins the binary at Path: "sha256:<64 hex>", verified before
	// every execution.
	Digest string `yaml:"digest,omitempty"`
	// Ref is the nominal reference (host and repository, no tag or
	// digest) of the tobby-system ingredient carrying the binary, when it
	// is not the default one. Its digest is the recipe's pin. Exclusive
	// with Path.
	Ref string `yaml:"ref,omitempty"`
}

// The scan modes and stale-database actions.
const (
	ScanModeWarn  = "warn"
	ScanModeBlock = "block"
)

// AuthSession bounds UI sessions beyond their absolute lifetime
// (auth.sessionTTL).
type AuthSession struct {
	// IdleTimeout ends a session after this long without an
	// authenticated request (FR-072 amendment, R-14). Default 30m. Must be
	// positive; auth.sessionTTL still bounds the session's whole life.
	IdleTimeout Duration `yaml:"idleTimeout"`
}

// AuthTokens configures static API tokens (FR-072).
type AuthTokens struct {
	// DefaultLifetime gives tokens created without an explicit expiry
	// one. Zero — the default — keeps the FR-072 long-lived token.
	DefaultLifetime Duration `yaml:"defaultLifetime,omitempty"`
}

// RoleMapping maps identity-provider groups (or SAML attribute values)
// onto the three roles (FR-074). A user matching several entries gets the
// highest role; one matching none is refused, never defaulted to viewer.
type RoleMapping struct {
	Admin    []string `yaml:"admin,omitempty"`
	Operator []string `yaml:"operator,omitempty"`
	Viewer   []string `yaml:"viewer,omitempty"`
}

// empty reports whether no group maps to any role.
func (m *RoleMapping) empty() bool {
	return len(m.Admin) == 0 && len(m.Operator) == 0 && len(m.Viewer) == 0
}

// OIDC configures OpenID Connect sign-in, authorization code flow with
// PKCE (FR-070). Disabled by default. Discovery happens from Issuer only;
// outbound calls go through the instance's network settings (FR-080,
// FR-081) and never anywhere else (NFR-019).
type OIDC struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// DisplayName labels the sign-in button ("Sign in with <name>").
	// Empty shows "OpenID Connect".
	DisplayName string `yaml:"displayName,omitempty"`
	// Issuer is the provider's issuer URL, https only, matched exactly
	// against the tokens' iss claim.
	Issuer string `yaml:"issuer,omitempty"`
	// ClientID is this instance's client identifier, matched against the
	// tokens' audience.
	ClientID string `yaml:"clientID,omitempty"`
	// ClientSecretFile holds the client secret. A path, never the value:
	// secrets do not live in the configuration file, and the file must sit
	// outside the transportable store (NFR-020).
	ClientSecretFile string `yaml:"clientSecretFile,omitempty"`
	// RedirectURL is the callback URL registered with the provider.
	RedirectURL string `yaml:"redirectURL,omitempty"`
	// Scopes requested beyond "openid".
	Scopes []string `yaml:"scopes,omitempty"`
	// GroupsClaim names the claim carrying the user's groups.
	GroupsClaim string `yaml:"groupsClaim,omitempty"`
	// SigningAlgorithms is the allow-list of ID token signature
	// algorithms. "none" and the HS* family are refused outright.
	SigningAlgorithms []string `yaml:"signingAlgorithms,omitempty"`
	// Roles maps groups onto roles.
	Roles RoleMapping `yaml:"roles,omitempty"`
}

// SAML configures SAML 2.0 sign-in as a service provider, SP-initiated
// only (FR-071). Disabled by default. The identity provider's metadata is
// pinned in configuration; a metadata URL is contacted only when set
// explicitly (NFR-019).
type SAML struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// DisplayName labels the sign-in button. Empty shows "SAML".
	DisplayName string `yaml:"displayName,omitempty"`
	// EntityID is this service provider's entity identifier — the
	// audience assertions must name.
	EntityID string `yaml:"entityID,omitempty"`
	// ACSURL is the assertion consumer service URL — the Destination
	// responses must name.
	ACSURL string `yaml:"acsURL,omitempty"`
	// IDPMetadataFile is the pinned identity-provider metadata. Exactly
	// one of IDPMetadataFile and IDPMetadataURL.
	IDPMetadataFile string `yaml:"idpMetadataFile,omitempty"`
	// IDPMetadataURL fetches the metadata over https — an explicit
	// outbound connection the operator chose.
	IDPMetadataURL string `yaml:"idpMetadataURL,omitempty"`
	// IDPCertificateFile pins the identity provider's signing certificate
	// (PEM). Required with IDPMetadataURL: a fetched descriptor that is
	// not itself signed (Keycloak's is not) must not be able to swap the
	// key that assertions are verified with. With IDPMetadataFile it is
	// optional and, when set, replaces the certificates of the file.
	IDPCertificateFile string `yaml:"idpCertificateFile,omitempty"`
	// CertFile and KeyFile are the service provider's key pair. The key
	// file is secret material and must sit outside the store (NFR-020).
	CertFile string `yaml:"certFile,omitempty"`
	KeyFile  string `yaml:"keyFile,omitempty"`
	// GroupsAttribute names the assertion attribute carrying the groups.
	GroupsAttribute string `yaml:"groupsAttribute,omitempty"`
	// ClockSkew bounds the tolerated clock difference with the provider.
	// Default 90s, at most 5m.
	ClockSkew Duration `yaml:"clockSkew,omitempty"`
	// Roles maps groups onto roles.
	Roles RoleMapping `yaml:"roles,omitempty"`
}

// Defaults of the milestone-6 surface, applied by Default.
const (
	DefaultScanThreshold   = 7.0
	DefaultScanMaxDBAge    = 168 * time.Hour
	DefaultSessionIdle     = 30 * time.Minute
	DefaultSAMLClockSkew   = 90 * time.Second
	maxSAMLClockSkew       = 5 * time.Minute
	defaultOIDCGroupsClaim = "groups"
	// The federated round trips live under the "/auth/" prefix reserved
	// for them (internal/server, internal/federation): the configured
	// callback URLs must name exactly these paths.
	oidcCallbackPath = "/auth/oidc/callback"
	samlACSPath      = "/auth/saml/acs"
)

var (
	cvePattern    = regexp.MustCompile(`^CVE-\d{4}-\d{4,}$`)
	sha256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// defaultMilestone6 fills the milestone-6 defaults into cfg.
func defaultMilestone6(cfg *Config) {
	cfg.Scan = Scan{
		Mode:           ScanModeWarn,
		Threshold:      DefaultScanThreshold,
		MaxDBAge:       Duration(DefaultScanMaxDBAge),
		MaxDBAgeAction: ScanModeWarn,
	}
	cfg.Auth.Session = AuthSession{IdleTimeout: Duration(DefaultSessionIdle)}
	cfg.Auth.OIDC.GroupsClaim = defaultOIDCGroupsClaim
	cfg.Auth.SAML.ClockSkew = Duration(DefaultSAMLClockSkew)
}

// validateScan checks the scan section (FR-031, FR-032).
func (c *Config) validateScan() []error {
	var errs []error
	s := &c.Scan
	for _, f := range []struct{ key, value string }{
		{"scan.mode", s.Mode},
		{"scan.maxDbAgeAction", s.MaxDBAgeAction},
	} {
		if f.value != ScanModeWarn && f.value != ScanModeBlock {
			errs = append(errs, fmt.Errorf(`%s: unknown value %q: valid values are "warn" and "block"`, f.key, f.value))
		}
	}
	if s.Threshold < 0 || s.Threshold > 10 {
		errs = append(errs, fmt.Errorf("scan.threshold: %v is not a CVSS score: it must lie between 0 and 10 — a finding scoring at or above it violates the policy", s.Threshold))
	}
	if time.Duration(s.MaxDBAge) <= 0 {
		errs = append(errs, errors.New(`scan.maxDbAge must be positive: the database age is always checked (FR-032); use "scan.maxDbAgeAction: warn" to report staleness without blocking`))
	}
	seen := map[string]int{}
	for i, ex := range s.Exceptions {
		if !cvePattern.MatchString(ex.ID) {
			errs = append(errs, fmt.Errorf("scan.exceptions[%d]: %q is not a CVE identifier (CVE-<year>-<number>)", i, ex.ID))
		}
		if first, dup := seen[ex.ID]; dup {
			errs = append(errs, fmt.Errorf("scan.exceptions[%d]: %s is already listed by scan.exceptions[%d]", i, ex.ID, first))
		} else {
			seen[ex.ID] = i
		}
	}
	if s.DBRepository != "" {
		if err := validRepositoryPath("scan.dbRepository", s.DBRepository); err != nil {
			errs = append(errs, err)
		}
	}
	t := &s.Trivy
	if t.Path != "" && t.Ref != "" {
		errs = append(errs, errors.New("scan.trivy.path and scan.trivy.ref are exclusive: the binary is either on this host or delivered by the tobby-system channel"))
	}
	if t.Path != "" && !filepath.IsAbs(t.Path) {
		errs = append(errs, fmt.Errorf("scan.trivy.path: %q must be absolute", t.Path))
	}
	if t.Digest != "" {
		if t.Path == "" {
			errs = append(errs, errors.New("scan.trivy.digest pins the binary at scan.trivy.path and is set without it: a channel-delivered binary is pinned by its recipe"))
		}
		if !sha256Pattern.MatchString(t.Digest) {
			errs = append(errs, fmt.Errorf("scan.trivy.digest: %q must be sha256:<64 lowercase hex digits>", t.Digest))
		}
	}
	if t.Ref != "" && (strings.Contains(t.Ref, "://") || strings.ContainsAny(t.Ref, "@") || !strings.Contains(t.Ref, "/")) {
		errs = append(errs, fmt.Errorf("scan.trivy.ref: %q must be a nominal reference — registry host and repository, no scheme, tag or digest", t.Ref))
	}
	return errs
}

// validateAuthMilestone6 checks session and token expiry and the two
// federated methods (FR-070, FR-071, FR-072 amendment).
func (c *Config) validateAuthMilestone6() []error {
	var errs []error
	a := &c.Auth
	// An idle timeout longer than auth.sessionTTL is not an error: the
	// absolute lifetime simply fires first, and refusing it would break
	// every configuration that shortened sessionTTL below the default.
	if time.Duration(a.Session.IdleTimeout) <= 0 {
		errs = append(errs, errors.New("auth.session.idleTimeout must be positive: idle sessions always expire (FR-072)"))
	}
	if a.Tokens.DefaultLifetime < 0 {
		errs = append(errs, errors.New("auth.tokens.defaultLifetime must not be negative: use 0 for tokens without expiry"))
	}
	if a.OIDC.Enabled {
		errs = append(errs, a.OIDC.validate()...)
	}
	if a.SAML.Enabled {
		errs = append(errs, a.SAML.validate()...)
	}
	skew := time.Duration(a.SAML.ClockSkew)
	if skew < 0 || skew > maxSAMLClockSkew {
		errs = append(errs, fmt.Errorf("auth.saml.clockSkew must lie between 0 and %s", maxSAMLClockSkew))
	}
	if a.Disabled && (a.OIDC.Enabled || a.SAML.Enabled) {
		errs = append(errs, errors.New("auth.disabled is set together with a federated method: an instance without authentication has nothing to federate"))
	}
	return errs
}

func (o *OIDC) validate() []error {
	var errs []error
	if u, err := url.Parse(o.Issuer); o.Issuer == "" || err != nil || u.Scheme != "https" || u.Host == "" {
		errs = append(errs, fmt.Errorf("auth.oidc.issuer: %q must be an https:// URL — discovery happens from it and from nowhere else", o.Issuer))
	}
	if o.ClientID == "" {
		errs = append(errs, errors.New("auth.oidc.clientID is required"))
	}
	if o.ClientSecretFile == "" {
		errs = append(errs, errors.New("auth.oidc.clientSecretFile is required: the client secret lives in a file, never in the configuration"))
	}
	if u, err := url.Parse(o.RedirectURL); o.RedirectURL == "" || err != nil || u.Host == "" {
		errs = append(errs, fmt.Errorf("auth.oidc.redirectURL: %q must be the absolute callback URL registered with the provider", o.RedirectURL))
	} else if u.Path != oidcCallbackPath || u.RawQuery != "" || u.Fragment != "" {
		errs = append(errs, fmt.Errorf("auth.oidc.redirectURL: %q must name this instance's callback path %s, with no query", o.RedirectURL, oidcCallbackPath))
	}
	for _, alg := range o.SigningAlgorithms {
		if strings.EqualFold(alg, "none") || strings.HasPrefix(strings.ToUpper(alg), "HS") {
			errs = append(errs, fmt.Errorf("auth.oidc.signingAlgorithms: %q is refused — unsigned and shared-secret signatures never authenticate an ID token", alg))
		}
	}
	if o.Roles.empty() {
		errs = append(errs, errors.New("auth.oidc.roles maps no group to any role: nobody could sign in"))
	}
	return errs
}

func (s *SAML) validate() []error {
	var errs []error
	if s.EntityID == "" {
		errs = append(errs, errors.New("auth.saml.entityID is required"))
	}
	if u, err := url.Parse(s.ACSURL); s.ACSURL == "" || err != nil || u.Host == "" {
		errs = append(errs, fmt.Errorf("auth.saml.acsURL: %q must be the absolute assertion consumer service URL", s.ACSURL))
	} else if u.Path != samlACSPath || u.RawQuery != "" || u.Fragment != "" {
		errs = append(errs, fmt.Errorf("auth.saml.acsURL: %q must name this instance's assertion consumer service path %s, with no query", s.ACSURL, samlACSPath))
	}
	switch {
	case (s.IDPMetadataFile == "") == (s.IDPMetadataURL == ""):
		errs = append(errs, errors.New("auth.saml: exactly one of idpMetadataFile and idpMetadataURL is required"))
	case s.IDPMetadataURL != "" && !strings.HasPrefix(s.IDPMetadataURL, "https://"):
		errs = append(errs, fmt.Errorf("auth.saml.idpMetadataURL: %q must be https://", s.IDPMetadataURL))
	case s.IDPMetadataURL != "" && s.IDPCertificateFile == "":
		errs = append(errs, errors.New("auth.saml.idpCertificateFile is required with idpMetadataURL: the signing certificate is pinned in configuration, never taken from a fetched descriptor"))
	}
	if s.CertFile == "" || s.KeyFile == "" {
		errs = append(errs, errors.New("auth.saml.certFile and auth.saml.keyFile are both required: the service provider signs its requests"))
	}
	if s.Roles.empty() {
		errs = append(errs, errors.New("auth.saml.roles maps no group to any role: nobody could sign in"))
	}
	return errs
}

// pendingOptIns refuses the milestone-6 opt-ins whose behaviour has not
// landed on this branch yet. Each line is deleted by the track that
// implements it, together with its test case; none may survive into the
// v0.6.0 tag (milestone 6 — single tag at the end of the milestone).
func (c *Config) pendingOptIns() []error {
	var errs []error
	pending := func(set bool, key, item string) {
		if set {
			errs = append(errs, fmt.Errorf("%s is not implemented by this build yet (%s, milestone 6): refusing to start rather than ignore it", key, item))
		}
	}
	// Track A — 6.1 / R-13.
	pending(c.Scan.Mode == ScanModeBlock, `scan.mode: block`, "6.1")
	pending(c.Scan.MaxDBAgeAction == ScanModeBlock, `scan.maxDbAgeAction: block`, "R-13")
	// Track B — R-14 and 6.3 are implemented: no pending opt-in left.
	return errs
}
