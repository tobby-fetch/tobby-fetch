// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package policy

import (
	"errors"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func mustAllowlist(t *testing.T, entries []string) *Allowlist {
	t.Helper()
	a, err := NewAllowlist(entries)
	if err != nil {
		t.Fatalf("NewAllowlist(%v): %v", entries, err)
	}
	return a
}

// The nil/empty distinction is the whole default policy, and Go makes it
// easy to lose. It is pinned here in both directions.
func TestUndeclaredAllowsEverythingDeclaredEmptyAllowsNothing(t *testing.T) {
	undeclared := mustAllowlist(t, nil)
	if undeclared.Declared() {
		// Declared drives what every status surface reports.
		t.Error("a nil configuration slice must read as undeclared")
	}
	for _, host := range []string{"docker.io", "registry.example.com", "127.0.0.1:5000"} {
		if !undeclared.Allows(host) {
			t.Errorf("undeclared allowlist refused %q; absent means no restriction", host)
		}
		if err := undeclared.Check(host); err != nil {
			t.Errorf("undeclared allowlist Check(%q) = %v, want nil", host, err)
		}
	}

	declaredEmpty := mustAllowlist(t, []string{})
	if !declaredEmpty.Declared() {
		t.Error("an empty but present configuration list must read as declared")
	}
	for _, host := range []string{"docker.io", "registry.example.com"} {
		if declaredEmpty.Allows(host) {
			t.Errorf("declared empty allowlist admitted %q; it must allow nothing", host)
		}
	}
}

func TestAllowsMatching(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
		host    string
		want    bool
	}{
		{"exact host", []string{"registry.example.com"}, "registry.example.com", true},
		{"another host on the list", []string{"a.example.com", "b.example.com"}, "b.example.com", true},
		{"host absent from the list", []string{"a.example.com"}, "b.example.com", false},
		{"case is not significant", []string{"Registry.Example.COM"}, "registry.example.com", true},

		// Docker Hub answers to three names for one registry. An operator
		// writing either must not be surprised by the other.
		{"docker.io written, index.docker.io contacted", []string{"docker.io"}, "index.docker.io", true},
		{"docker.io written, registry-1 contacted", []string{"docker.io"}, "registry-1.docker.io", true},
		{"index.docker.io written, docker.io contacted", []string{"index.docker.io"}, "docker.io", true},

		// A port is part of the identity of an endpoint.
		{"port matches", []string{"registry.example.com:5000"}, "registry.example.com:5000", true},
		{"a different port is a different endpoint", []string{"registry.example.com:5000"}, "registry.example.com:5001", false},
		{"no port on the pattern does not mean any port", []string{"registry.example.com"}, "registry.example.com:5000", false},
		{"no port on the host does not match a ported pattern", []string{"registry.example.com:5000"}, "registry.example.com", false},

		// "*" inside one label, "**" across labels — the vocabulary trust
		// scopes already use.
		{"* spans one label", []string{"*.example.com"}, "registry.example.com", true},
		{"* does not span a dot", []string{"*.example.com"}, "a.b.example.com", false},
		{"** spans any depth", []string{"**.example.com"}, "a.b.example.com", true},
		{"** absorbs zero labels", []string{"**.example.com"}, "example.com", true},
		{"* inside a label", []string{"registry-*.example.com"}, "registry-eu.example.com", true},
		{"a wildcard does not cross the suffix", []string{"*.example.com"}, "registry.example.org", false},
		{"a wildcard pattern still respects the port rule", []string{"*.example.com"}, "a.example.com:5000", false},

		{"localhost", []string{"localhost:5000"}, "localhost:5000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustAllowlist(t, tc.entries).Allows(tc.host); got != tc.want {
				t.Errorf("Allows(%q) with %v = %v, want %v", tc.host, tc.entries, got, tc.want)
			}
		})
	}
}

// A refusal must be the dedicated policy class, naming the host: FR-030
// requires a distinct error class, logged and counted.
func TestCheckReturnsThePolicyCode(t *testing.T) {
	err := mustAllowlist(t, []string{"a.example.com"}).Check("b.example.com")
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("Check returned %v, want a taxonomy error", err)
	}
	if te.Code() != taxonomy.CodeNotAllowlisted {
		t.Errorf("code = %s, want %s", te.Code(), taxonomy.CodeNotAllowlisted)
	}
	if te.ParamsMap()["host"] != "b.example.com" {
		t.Errorf("params = %v, want the refused host named", te.ParamsMap())
	}
}

// A host that cannot be canonicalized is not on any list. Failing closed
// here matters: the alternative is a malformed host slipping past a
// declared policy.
func TestUnparseableHostIsRefusedByADeclaredList(t *testing.T) {
	a := mustAllowlist(t, []string{"**"})
	for _, host := range []string{"", "http://registry.example.com", "[::1]:5000"} {
		if a.Allows(host) {
			t.Errorf("Allows(%q) = true; an unusable host must never pass a declared policy", host)
		}
	}
	// ...while an undeclared policy still restricts nothing.
	if !mustAllowlist(t, nil).Allows("[::1]:5000") {
		t.Error("an undeclared allowlist must not start refusing hosts")
	}
}

func TestPatternsAreReportedCanonicalAndSorted(t *testing.T) {
	a := mustAllowlist(t, []string{"index.docker.io", "B.example.com", "a.example.com"})
	got := a.Patterns()
	want := []string{"a.example.com", "b.example.com", "docker.io"}
	if len(got) != len(want) {
		t.Fatalf("Patterns() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Patterns() = %v, want %v", got, want)
		}
	}
	// The reported slice is a copy: a caller cannot edit the policy.
	got[0] = "mutated"
	if a.Patterns()[0] == "mutated" {
		t.Error("Patterns() exposes the internal slice")
	}
}

func TestRejectedConfigurations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []string
	}{
		{"an empty entry says nothing", []string{""}},
		{"whitespace is an empty entry", []string{"   "}},
		{"a URL is not a host", []string{"https://registry.example.com"}},
		{"a repository path is not a host", []string{"registry.example.com/library"}},
		{"a wildcard repository path is not a host", []string{"*.example.com/library"}},
		{"a bare name is not a host", []string{"registry"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewAllowlist(tc.entries); err == nil {
				t.Errorf("NewAllowlist(%v) accepted an unusable entry", tc.entries)
			}
		})
	}
}

// A nil *Allowlist must behave like an undeclared one rather than panic:
// it is what a zero-valued component holds before configuration lands.
func TestNilAllowlistIsUnrestricted(t *testing.T) {
	var a *Allowlist
	if a.Declared() {
		t.Error("a nil allowlist must not read as declared")
	}
	if !a.Allows("registry.example.com") {
		t.Error("a nil allowlist must not restrict")
	}
	if err := a.Check("registry.example.com"); err != nil {
		t.Errorf("Check on a nil allowlist = %v, want nil", err)
	}
	if a.Patterns() != nil {
		t.Error("a nil allowlist must report no patterns")
	}
}
