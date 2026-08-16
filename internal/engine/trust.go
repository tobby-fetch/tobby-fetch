// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package engine is the recipe engine (milestone 3): it turns a Retriever
// into verified content in the local store — resolving versions (FR-021),
// verifying signatures and pinned digests (FR-033), transferring the four
// ingredient kinds bit-exactly (FR-020), idempotently (NFR-009), under the
// relocation convention (FR-035) with source substitution (FR-036).
package engine

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
)

// TrustPolicy is the loaded trust configuration (FR-033, RECIPE-SPEC
// §12.3): the multi-key trust-root set and the declared scopes. The
// default — no scope matching — is strict: a recipe must verify against at
// least one configured root. Relaxation exists only as declared scopes,
// visible on every reporting surface; no undeclared bypass exists.
type TrustPolicy struct {
	roots  map[string]*sigverify.Keys
	all    *sigverify.Keys
	scopes []config.TrustScope
}

// Decision is the trust verdict for one nominal cookbook repository.
type Decision struct {
	// AllowUnsigned admits an unsigned recipe — only ever true inside the
	// named declared scope.
	AllowUnsigned bool
	// Scope names the matching declared scope; empty for the strict
	// default.
	Scope string
	// Keys is the key set to verify against (the full root set by
	// default; a scope may restrict it).
	Keys *sigverify.Keys
}

// LoadTrust resolves the configured trust roots into key material: inline
// PEM, local file, or HTTPS URL fetched now — at configuration time, never
// at verification time (§12.3) — and cached under cacheDir so an
// unreachable key server degrades to the cached copy instead of an outage.
func LoadTrust(cfg config.Trust, cacheDir string, client *http.Client) (*TrustPolicy, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	p := &TrustPolicy{roots: map[string]*sigverify.Keys{}, scopes: cfg.Scopes}
	var allPEM [][]byte
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
			pem, err = fetchCachedKey(client, r.KeyURL, filepath.Join(cacheDir, r.Name+".pub"))
			if err != nil {
				return nil, fmt.Errorf("trust root %s: %w", r.Name, err)
			}
		}
		keys, err := sigverify.ParsePublicKeys(pem)
		if err != nil {
			return nil, fmt.Errorf("trust root %s: %w", r.Name, err)
		}
		p.roots[r.Name] = keys
		allPEM = append(allPEM, pem)
	}
	if len(allPEM) > 0 {
		all, err := sigverify.ParsePublicKeys(allPEM...)
		if err != nil {
			return nil, err
		}
		p.all = all
	}
	return p, nil
}

// Decide returns the trust verdict for a recipe's nominal cookbook
// repository. Scopes are evaluated in declaration order, first match wins;
// no match is the strict default. Trust follows the nominal ref, never the
// substituted endpoint (FR-036).
func (p *TrustPolicy) Decide(nominalRepo string) Decision {
	for _, s := range p.scopes {
		for _, pattern := range s.Repositories {
			if matchPattern(pattern, nominalRepo) {
				d := Decision{Scope: s.Name, AllowUnsigned: s.AllowUnsigned, Keys: p.all}
				if len(s.Roots) > 0 {
					d.Keys = p.merge(s.Roots)
				}
				return d
			}
		}
	}
	return Decision{Keys: p.all}
}

// HasRoots reports whether any trust root is configured at all: without
// one, only recipes admitted by an allowUnsigned scope can enter.
func (p *TrustPolicy) HasRoots() bool { return p.all != nil }

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
func fetchCachedKey(client *http.Client, url, cachePath string) ([]byte, error) {
	if cachePath == "" {
		resp, err := client.Get(url) //nolint:noctx // configuration-time fetch, bounded by the client timeout
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", url, err)
		}
		defer resp.Body.Close() //nolint:errcheck // read side
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	return fetchCachedKeyAt(client, url, cachePath)
}

func fetchCachedKeyAt(client *http.Client, url, cachePath string) ([]byte, error) {
	resp, err := client.Get(url) //nolint:noctx // configuration-time fetch, bounded by the client timeout
	if err == nil {
		defer resp.Body.Close() //nolint:errcheck // read side
		if resp.StatusCode == http.StatusOK {
			pem, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err == nil {
				if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err == nil {
					_ = os.WriteFile(cachePath, pem, 0o600)
				}
				return pem, nil
			}
		}
		err = fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
		if cached, cerr := os.ReadFile(cachePath); cerr == nil { //nolint:gosec // G304: our own cache path
			return cached, nil
		}
		return nil, err
	}
	if cached, cerr := os.ReadFile(cachePath); cerr == nil { //nolint:gosec // G304: our own cache path
		return cached, nil
	}
	return nil, fmt.Errorf("fetching %s: %w (and no cached copy)", url, err)
}

// matchPattern matches a repository path against a scope pattern: "*"
// matches within one path segment, "**" spans segments. The whole path
// must match.
func matchPattern(pattern, path string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegments(pat, parts []string) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		// "**" absorbs zero or more segments.
		for skip := 0; skip <= len(parts); skip++ {
			if matchSegments(pat[1:], parts[skip:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if !matchSegment(pat[0], parts[0]) {
		return false
	}
	return matchSegments(pat[1:], parts[1:])
}

// matchSegment matches one segment with "*" wildcards (any run of
// characters within the segment).
func matchSegment(pat, s string) bool {
	parts := strings.Split(pat, "*")
	if len(parts) == 1 {
		return pat == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// nominalRepoOf joins a nominal reference's canonical form — the pattern
// space of trust scopes ("registry.example.com/cookbook/wordpress").
func nominalRepoOf(cookbook, name string) (string, error) {
	p, err := relocate.Path(cookbook + "/" + name)
	if err != nil {
		return "", err
	}
	return p, nil
}
