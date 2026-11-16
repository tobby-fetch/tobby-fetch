// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import "testing"

// TestBasePrefix recovers the producing instance's base prefix from where
// the recipe artifact sits beside its nominal cookbook path: the prefix
// every ingredient of the signed recipe document is located under on the
// medium (B-066).
func TestBasePrefix(t *testing.T) {
	for _, tc := range []struct {
		name               string
		cookbook, artifact string
		want               string
		ok                 bool
	}{
		{"no base prefix", "registry.example.com/cookbook/app", "registry.example.com/cookbook/app", "", true},
		{"base prefix", "registry.example.com/cookbook/app", "mirror/zone-a/registry.example.com/cookbook/app", "mirror/zone-a/", true},
		{"port folded", "lab.example.com:5000/cookbook/app", "base/lab.example.com_5000/cookbook/app", "base/", true},
		{"alias folded", "index.docker.io/cookbook/app", "base/docker.io/cookbook/app", "base/", true},
		{"inconsistent medium", "registry.example.com/cookbook/app", "elsewhere/cookbook/app", "", false},
		{"suffix that is no path segment", "registry.example.com/cookbook/app", "baseregistry.example.com/cookbook/app", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := basePrefix(&Recipe{CookbookRepo: tc.cookbook, ArtifactRepo: tc.artifact})
			if got != tc.want || ok != tc.ok {
				t.Errorf("basePrefix = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
