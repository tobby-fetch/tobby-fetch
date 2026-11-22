// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package engine is the recipe engine (milestone 3): it turns a Retriever
// into verified content in the local store — resolving versions (FR-021),
// verifying signatures and pinned digests (FR-033), transferring the four
// ingredient kinds bit-exactly (FR-020), idempotently (NFR-009), under the
// relocation convention (FR-035) with source substitution (FR-036).
package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/pathglob"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
)

// trustFetchTimeout bounds one configuration-time trust-root download.
const trustFetchTimeout = 30 * time.Second

// TrustPolicy is the loaded trust configuration (FR-033, RECIPE-SPEC
// §12.3): the multi-key trust-root set and the declared scopes. The
// default — no scope matching — is strict: a recipe must verify against at
// least one root of the default set. Relaxation exists only as declared
// scopes, visible on every reporting surface; no undeclared bypass exists.
//
// A root that at least one scope lists is RESERVED to the scopes listing
// it (B-057): it verifies inside them and nowhere else. The default set —
// what applies outside every scope, and inside a scope that lists no
// roots — is therefore the roots no scope lists. Before this rule, the
// default was the union of every root, and an emergency key declared for
// an "emergency" scope admitted any recipe outside that scope.
type TrustPolicy struct {
	roots map[string]*sigverify.Keys
	// order is the configured root names, in configuration order.
	order []string
	// reservedBy maps a reserved root to the scopes listing it, in
	// declaration order. A root absent from it belongs to the default set.
	reservedBy map[string][]string
	// defaults is the default set; nil when every configured root is
	// reserved (or none is configured).
	defaults *sigverify.Keys
	// reserved lists the reserved roots: what the default set excludes.
	reserved []media.ExcludedRoot
	scopes   []config.TrustScope
	// requireIngredients is trust.requireIngredientSignatures (B-062):
	// see DecideIngredient.
	requireIngredients bool
}

// Decision is the trust verdict for one nominal cookbook repository. It
// is the media package's TrustDecision: one verdict type, whichever
// surface asks (synchronization, plan, promotion, media verification,
// integrity check).
type Decision = media.TrustDecision

// LoadTrust resolves the configured trust roots into key material: inline
// PEM, local file, or HTTPS URL fetched now — at configuration time, never
// at verification time (§12.3) — and cached under cacheDir so an
// unreachable key server degrades to the cached copy instead of an outage.
//
// The URL form travels on the instance's shared outbound transport
// (FR-080, FR-081) like every other fetch. It is the least obvious of the
// outbound paths — it runs once, at startup, before anything looks like a
// transfer — and therefore the one most likely to be left dialing
// directly into a blocked egress, where it would not fail but hang the
// whole startup. A nil egress is the unconfigured direct one.
func LoadTrust(cfg config.Trust, cacheDir string, eg *netx.Egress) (*TrustPolicy, error) {
	return LoadTrustLogging(slog.Default(), cfg, cacheDir, eg)
}

// LoadTrustLogging is LoadTrust writing to the caller's logger: the one
// thing worth a line here is a key URL that did not answer and the cached
// copy that stood in for it (B-090), and an operator looks for that in
// the instance's log stream, not on a bare stderr.
func LoadTrustLogging(logger *slog.Logger, cfg config.Trust, cacheDir string, eg *netx.Egress) (*TrustPolicy, error) {
	client := netx.Or(eg).Client(trustFetchTimeout)
	p := &TrustPolicy{
		roots: map[string]*sigverify.Keys{}, reservedBy: map[string][]string{}, scopes: cfg.Scopes,
		requireIngredients: cfg.RequireIngredientSignatures,
	}
	for _, r := range cfg.Roots {
		var pem []byte
		var err error
		switch {
		case r.Key != "":
			pem = []byte(r.Key)
		case r.KeyFile != "":
			pem, err = os.ReadFile(r.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("trust root %s: %w", r.Name, err)
			}
		case r.KeyURL != "":
			pem, err = fetchCachedKey(logger, client, r.KeyURL, filepath.Join(cacheDir, r.Name+".pub"))
			if err != nil {
				return nil, fmt.Errorf("trust root %s: %w", r.Name, err)
			}
		}
		keys, err := sigverify.ParsePublicKeys(pem)
		if err != nil {
			return nil, fmt.Errorf("trust root %s: %w", r.Name, err)
		}
		p.roots[r.Name] = keys
		p.order = append(p.order, r.Name)
	}
	p.reserve()
	return p, nil
}

// reserve partitions the roots (B-057): a root listed by a scope is
// reserved to the scopes listing it; the others form the default set.
//
// The reservation follows the KEY, not only the root's name: a public key
// configured twice — once under a name a scope lists, once under a name
// none does — stays out of the default set. Otherwise one duplicated
// configuration entry would reopen the very leak the rule closes.
func (p *TrustPolicy) reserve() {
	for _, s := range p.scopes {
		for _, name := range s.Roots {
			if _, ok := p.roots[name]; ok && !slices.Contains(p.reservedBy[name], s.Name) {
				p.reservedBy[name] = append(p.reservedBy[name], s.Name)
			}
		}
	}
	var unreserved, reserved []*sigverify.Keys
	for _, name := range p.order {
		if scopes, ok := p.reservedBy[name]; ok {
			reserved = append(reserved, p.roots[name])
			p.reserved = append(p.reserved, media.ExcludedRoot{Name: name, Scopes: scopes, Keys: p.roots[name]})
			continue
		}
		unreserved = append(unreserved, p.roots[name])
	}
	if defaults := sigverify.Union(unreserved...).Without(sigverify.Union(reserved...)); defaults.Len() > 0 {
		p.defaults = defaults
	}
}

// Decide returns the trust verdict for a recipe's nominal cookbook
// repository. Scopes are evaluated in declaration order, first match wins;
// no match is the strict default. Trust follows the nominal ref, never the
// substituted endpoint (FR-036).
//
// The key set is the scope's own roots when it lists some, and the
// default set otherwise — outside every scope, and inside a scope that
// lists no roots (an allowUnsigned-only scope). A root reserved to a scope
// never verifies outside it (B-057). The roots a decision leaves out are
// carried in Excluded, for the refusal message only.
func (p *TrustPolicy) Decide(nominalRepo string) Decision {
	for _, s := range p.scopes {
		for _, pattern := range s.Repositories {
			if matchPattern(pattern, nominalRepo) {
				d := Decision{Scope: s.Name, AllowUnsigned: s.AllowUnsigned, Keys: p.defaults, Excluded: p.reserved}
				if len(s.Roots) > 0 {
					d.Keys, d.Excluded = p.merge(s.Roots), p.excluding(s.Roots)
				}
				return d
			}
		}
	}
	return Decision{Keys: p.defaults, Excluded: p.reserved}
}

// HasRoots reports whether any trust root is configured at all, reserved
// or not. Without one, only recipes admitted by an allowUnsigned scope
// can enter. It says nothing about the default set, which may be empty
// while roots exist (every root reserved to a scope).
func (p *TrustPolicy) HasRoots() bool { return len(p.order) > 0 }

// MediaTrust adapts this instance's trust policy to the media package's
// verification interface (FR-054).
//
// The adapter exists so the two packages stay independent of each other,
// and it only ever wraps the DESTINATION instance's policy: trust roots
// present on a transported medium are ignored, and nothing in the media
// package can read any — it has no way to load key material at all.
type MediaTrust struct {
	Policy *TrustPolicy
}

// Decide implements media.Trust.
func (m MediaTrust) Decide(nominalRepo string) media.TrustDecision {
	if m.Policy == nil {
		return media.TrustDecision{}
	}
	return m.Policy.Decide(nominalRepo)
}

// RelaxedScopes names the declared scopes that admit unsigned recipes —
// the permanent-banner surface (FR-075 principle: a reduced posture is
// always visible).
func (p *TrustPolicy) RelaxedScopes() []string {
	var names []string
	for _, s := range p.scopes {
		if s.AllowUnsigned {
			names = append(names, s.Name)
		}
	}
	return names
}

// excluding lists the configured roots outside names, in configuration
// order: what a scope listing names does not apply. A root of the default
// set carries no scope.
func (p *TrustPolicy) excluding(names []string) []media.ExcludedRoot {
	var out []media.ExcludedRoot
	for _, name := range p.order {
		if slices.Contains(names, name) {
			continue
		}
		out = append(out, media.ExcludedRoot{Name: name, Scopes: p.reservedBy[name], Keys: p.roots[name]})
	}
	return out
}

// merge aggregates the named roots into one verification set.
func (p *TrustPolicy) merge(names []string) *sigverify.Keys {
	var pems []*sigverify.Keys
	for _, n := range names {
		if k, ok := p.roots[n]; ok {
			pems = append(pems, k)
		}
	}
	return sigverify.Union(pems...)
}

// fetchCachedKey downloads the key once and caches it; on a network
// failure the cached copy — from a previous successful configuration-time
// fetch — keeps the instance operative. An empty cachePath disables
// caching (instances without a state directory; the cache never lives in
// the transportable store — FR-054: trust material on the media is
// ignored).
func fetchCachedKey(logger *slog.Logger, client *http.Client, url, cachePath string) ([]byte, error) {
	if cachePath == "" {
		return fetchKey(client, url)
	}
	return fetchCachedKeyAt(logger, client, url, cachePath)
}

// fetchCachedKeyAt is fetchCachedKey with a cache. Falling back to the
// cached copy keeps the instance operative, but it is never silent
// (B-090): a trust root that cannot be refreshed may be one that was
// rotated or revoked upstream, and the operator must be able to see that
// the instance is verifying against a copy — and how old it is.
func fetchCachedKeyAt(logger *slog.Logger, client *http.Client, url, cachePath string) ([]byte, error) {
	pem, err := fetchKey(client, url)
	if err == nil {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err == nil {
			_ = os.WriteFile(cachePath, pem, 0o600)
		}
		return pem, nil
	}
	cached, cerr := os.ReadFile(cachePath) //nolint:gosec // G304: our own cache path
	if cerr != nil {
		return nil, fmt.Errorf("%w (and no cached copy)", err)
	}
	attrs := []slog.Attr{
		slog.String("url", url), slog.String("error", err.Error()),
		slog.String("cache", cachePath),
	}
	if info, serr := os.Stat(cachePath); serr == nil {
		attrs = append(attrs,
			slog.Time("cached_at", info.ModTime()),
			slog.Duration("cache_age", time.Since(info.ModTime()).Round(time.Second)))
	}
	logger.LogAttrs(context.Background(), slog.LevelWarn,
		"trust root key URL unreachable: verifying with the cached copy", attrs...)
	return cached, nil
}

// fetchKey downloads one key file, bounded.
func fetchKey(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url) //nolint:noctx // configuration-time fetch, bounded by the client timeout
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	pem, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	return pem, nil
}

// matchPattern matches a repository path against a scope pattern: "*"
// matches within one path segment, "**" spans segments. The whole path
// must match. The grammar lives in internal/pathglob, shared with the
// repository scope of scan exceptions (R-15).
func matchPattern(pattern, path string) bool { return pathglob.Match(pattern, path) }

// nominalRepoOf joins a nominal reference's canonical form — the pattern
// space of trust scopes ("registry.example.com/cookbook/wordpress").
func nominalRepoOf(cookbook, name string) (string, error) {
	p, err := relocate.Path(cookbook + "/" + name)
	if err != nil {
		return "", err
	}
	return p, nil
}
