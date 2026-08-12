// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package server

import "testing"

// uiRoutes lists every UI route root of the current milestone plus the ones
// reserved for later milestones (UI-SPEC §3). Grow it with each screen: the
// test is the ADR-0015 collision lock.
var uiRoutes = []string{
	"/",
	"/login",
	"/content",
	"/import",
	"/tasks",
	"/help",
	"/about",
	"/api-docs",
	"/admin/accounts",
	"/admin/retriever",    // milestone 3
	"/admin/registries",   // milestone 4
	"/admin/certificates", // milestone 4
	"/admin/maintenance",  // milestone 4
	"/recipes",            // milestone 3
	"/media",              // milestone 5
}

func TestUIRoutesAvoidReservedPrefixes(t *testing.T) {
	for _, r := range uiRoutes {
		if r == "/" {
			continue // the root mounts the UI shell itself
		}
		if Reserved(r) {
			t.Errorf("UI route %s collides with a reserved prefix (ADR-0015)", r)
		}
	}
}

func TestReservedMatching(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v2/", true},
		{"/v2", true},
		{"/v2/docker.io/library/redis/manifests/7.2", true},
		{"/api/v1/tasks", true},
		{"/metrics", true},
		{"/metrics/sub", true},
		{"/healthz", true},
		{"/readyz", true},
		{"/auth/oidc/callback", true},
		{"/static/tokens.css", true},
		{"/content", false},
		{"/content/docker.io/bitnami/wordpress/-/tags/6.4.2", false},
		{"/metricsish", false}, // exact-path entries do not swallow lookalikes
		{"/apix", false},
		{"/", false},
	}
	for _, tc := range cases {
		if got := Reserved(tc.path); got != tc.want {
			t.Errorf("Reserved(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
