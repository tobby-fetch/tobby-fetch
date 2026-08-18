// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package policy enforces the instance's transfer policy — today the
// registry allowlist of FR-030.
//
// The allowlist answers one question, before any byte moves: may this
// instance talk to that registry at all? It is checked on the host
// ACTUALLY contacted, which is not always the one written in the recipe:
// under source substitution (FR-036) a reference to `docker.io` may be
// served by a zone registry, and it is that zone registry the policy is
// about. Trust follows the recipe, policy follows the wire (ADR-0013).
package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Allowlist decides which registries this instance may contact (FR-030).
//
// An UNDECLARED allowlist places no restriction, the way a Kubernetes pod
// with no NetworkPolicy selecting it is unrestricted. A DECLARED but empty
// allowlist allows nothing, the way an empty policy does. The distinction
// is deliberate and carried by the configuration file itself: an absent
// key and `allowlist: []` are different statements, and only the second
// one is an operator saying "nothing".
//
// The default is therefore unrestricted, which is not a relaxation of
// FR-075: there is no safe non-empty default — an instance does not know
// which registries its operator uses — and a deny-all default would make
// every fresh install fail policy before it ever worked. What secure-by-
// default buys instead is visibility: an undeclared allowlist is reported
// as such wherever the instance describes itself.
type Allowlist struct {
	declared bool
	patterns []string
	// onRefusal observes refusals, for the metric FR-091 requires and any
	// other counter a caller wants. Nil is skipped: this package never
	// depends on the metrics library, the way the engine's Meters do not.
	onRefusal func(host string)
}

// Observe installs the refusal hook. Called once at wiring time, before
// the allowlist is shared.
func (a *Allowlist) Observe(onRefusal func(host string)) {
	if a != nil {
		a.onRefusal = onRefusal
	}
}

// NewAllowlist builds the allowlist from configuration.
//
// entries is the configuration slice, and its nil-ness carries meaning: a
// nil slice is an absent key (unrestricted), a non-nil empty slice is a
// declared empty policy (nothing allowed). Every pattern is canonicalized
// so that both sides of a comparison agree on what a registry is called.
func NewAllowlist(entries []string) (*Allowlist, error) {
	a := &Allowlist{declared: entries != nil}
	for _, raw := range entries {
		p := strings.TrimSpace(raw)
		if p == "" {
			return nil, fmt.Errorf("registries.allowlist: empty entry; remove it, or write \"[]\" to allow nothing")
		}
		canonical, err := canonicalPattern(p)
		if err != nil {
			return nil, fmt.Errorf("registries.allowlist: %w", err)
		}
		a.patterns = append(a.patterns, canonical)
	}
	sort.Strings(a.patterns)
	return a, nil
}

// Declared reports whether an allowlist was configured at all — what the
// startup log, the admin screens and the API report, so that "no policy"
// is never mistaken for "policy satisfied".
func (a *Allowlist) Declared() bool { return a != nil && a.declared }

// Patterns returns the configured patterns, canonicalized and sorted.
func (a *Allowlist) Patterns() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.patterns...)
}

// Allows reports whether host may be contacted. An undeclared allowlist
// allows everything; a declared one allows only what it lists.
func (a *Allowlist) Allows(host string) bool {
	if !a.Declared() {
		return true
	}
	canonical, err := relocate.Host(host)
	if err != nil {
		// A host that cannot even be canonicalized is not on any list.
		return false
	}
	for _, p := range a.patterns {
		if matchHost(p, canonical) {
			return true
		}
	}
	return false
}

// Check is Allows as a refusal: it returns the dedicated policy error
// (TBY-POL-001) naming the host, or nil. Callers must call it BEFORE
// opening a connection — the point of the allowlist is that a forbidden
// registry is never contacted, not that the transfer is undone.
func (a *Allowlist) Check(host string) error {
	if a.Allows(host) {
		return nil
	}
	if a.onRefusal != nil {
		a.onRefusal(host)
	}
	return taxonomy.New(taxonomy.CodeNotAllowlisted, taxonomy.Params{"host": host})
}

// canonicalPattern canonicalizes one allowlist entry. Wildcards are left
// alone — they are not hostnames and would not survive host validation —
// but everything else goes through the same canonicalization as the hosts
// it will be compared against.
func canonicalPattern(p string) (string, error) {
	// Rejected before anything else: host canonicalization would happily
	// read "registry.example.com/library" as the host alone and silently
	// hand out the whole registry — an entry that grants strictly more
	// than the operator wrote is the one failure mode an allowlist must
	// not have.
	if strings.Contains(p, "/") {
		return "", fmt.Errorf("pattern %q: an allowlist entry is a registry host, not a URL or a repository path; the policy is per registry, not per repository", p)
	}
	if strings.Contains(p, "*") {
		return strings.ToLower(p), nil
	}
	host, err := relocate.Host(p)
	if err != nil {
		return "", fmt.Errorf("pattern %q: %w", p, err)
	}
	return host, nil
}

// matchHost matches a canonicalized host against a canonicalized pattern.
// The glob vocabulary is the one trust scopes already use, one level down:
// "*" matches within a single DNS label, "**" spans labels. The port, when
// the pattern carries one, must match exactly; a pattern without a port
// matches only a host without one, so widening to every port on a machine
// is always something an operator wrote on purpose.
func matchHost(pattern, host string) bool {
	patHost, patPort := splitPort(pattern)
	gotHost, gotPort := splitPort(host)
	if patPort != gotPort {
		return false
	}
	return matchLabels(strings.Split(patHost, "."), strings.Split(gotHost, "."))
}

// splitPort separates a ":port" suffix from a host.
func splitPort(hostport string) (host, port string) {
	if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		return hostport[:i], hostport[i+1:]
	}
	return hostport, ""
}

// matchLabels matches DNS labels, "**" absorbing zero or more of them.
func matchLabels(pat, labels []string) bool {
	if len(pat) == 0 {
		return len(labels) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(labels); skip++ {
			if matchLabels(pat[1:], labels[skip:]) {
				return true
			}
		}
		return false
	}
	if len(labels) == 0 {
		return false
	}
	if !matchLabel(pat[0], labels[0]) {
		return false
	}
	return matchLabels(pat[1:], labels[1:])
}

// matchLabel matches one DNS label, "*" standing for any run of
// characters inside it.
func matchLabel(pat, s string) bool {
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
