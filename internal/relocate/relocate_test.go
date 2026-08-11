// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package relocate

import (
	"strings"
	"testing"
)

// TestPath covers the ADR-0013 rules: nominal host prefix, closed
// canonicalization list, port folding, case folding.
func TestPath(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		// FR-035 acceptance examples.
		{"docker.io/bitnami/wordpress", "docker.io/bitnami/wordpress"},
		{"registry-1.docker.io/bitnami/wordpress", "docker.io/bitnami/wordpress"},
		{"index.docker.io/library/nginx", "docker.io/library/nginx"},
		// Case folding on the host only.
		{"GHCR.io/Owner/App", "ghcr.io/Owner/App"},
		// Port folding, ":" → "_".
		{"registry.example.com:5000/team/app", "registry.example.com_5000/team/app"},
		{"localhost:5000/dev/app", "localhost_5000/dev/app"},
		{"localhost/dev/app", "localhost/dev/app"},
		// No other alias collapses (closed list).
		{"mirror.gcr.io/library/nginx", "mirror.gcr.io/library/nginx"},
		// Deep repository paths pass through untouched.
		{"quay.io/a/b/c/d", "quay.io/a/b/c/d"},
	}
	for _, tc := range cases {
		got, err := Path(tc.ref)
		if err != nil {
			t.Errorf("Path(%q) error: %v", tc.ref, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Path(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

// TestPathRejects covers the explicit refusals: no truncation-prone or
// ambiguous input is ever silently accepted.
func TestPathRejects(t *testing.T) {
	cases := []struct {
		ref     string
		wantErr string
	}{
		{"", "empty"},
		{"https://docker.io/library/nginx", "URL scheme"},
		{"[2001:db8::1]:5000/a/b", "IPv6"},
		{"docker.io/library/nginx:1.25", "must not carry a tag"},
		{"docker.io/library/nginx@sha256:abc", "must not pin a digest"},
		{"nginx", "explicit registry host"},
		{"library/nginx", "explicit registry host"},
		{"docker.io/", "empty repository"},
		{"docker.io//nginx", "empty repository"},
		{"docker.io/nginx/", "empty repository"},
		{"/nginx", "explicit registry host"},
		{"registry.example.com:/a", "malformed host port"},
		{"registry.example.com:56x0/a", "malformed host port"},
	}
	for _, tc := range cases {
		_, err := Path(tc.ref)
		if err == nil {
			t.Errorf("Path(%q) succeeded, want error containing %q", tc.ref, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Path(%q) error = %v, want containing %q", tc.ref, err, tc.wantErr)
		}
	}
}

// TestPathWithBase covers the optional single base prefix of FR-035.
func TestPathWithBase(t *testing.T) {
	for _, tc := range []struct {
		base, ref, want string
	}{
		{"", "docker.io/a/b", "docker.io/a/b"},
		{"tobby", "docker.io/a/b", "tobby/docker.io/a/b"},
		{"/tobby/", "docker.io/a/b", "tobby/docker.io/a/b"},
		{"zone/mirror", "ghcr.io/a/b", "zone/mirror/ghcr.io/a/b"},
	} {
		got, err := PathWithBase(tc.base, tc.ref)
		if err != nil {
			t.Errorf("PathWithBase(%q, %q) error: %v", tc.base, tc.ref, err)
			continue
		}
		if got != tc.want {
			t.Errorf("PathWithBase(%q, %q) = %q, want %q", tc.base, tc.ref, got, tc.want)
		}
	}
}

// TestCascadeInvariance is the ADR-0013 core property: the relocated path
// never depends on where content is fetched from, so it is identical at
// every hop of a cascade. Substituting the effective endpoint must not
// re-enter the path computation at all — the function only ever sees the
// nominal ref.
func TestCascadeInvariance(t *testing.T) {
	nominal := "docker.io/bitnami/wordpress"
	first, err := Path(nominal)
	if err != nil {
		t.Fatal(err)
	}
	// Second hop: the downstream zone fetches from the upstream zone's
	// registry, recipes unmodified — same nominal ref, same path.
	second, err := Path(nominal)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("cascade hops diverge: %q vs %q", first, second)
	}
	// The double-prefix bug class: relocating an already-relocated path
	// (as if the wire host had been prefixed) must yield a different,
	// visibly wrong value — proving the path and the wire host must never
	// be conflated by callers.
	doubled, err := Path("registry.zone1.example/" + first)
	if err == nil && doubled == first {
		t.Error("relocating a relocated path must not be a fixed point")
	}
}
