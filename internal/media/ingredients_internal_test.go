// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import "testing"

// TestIngredientNominal recovers the canonical nominal repository an
// ingredient's trust decision is taken on, from where the producing
// instance placed it: under its base prefix, which the recipe artifact
// shares with its nominal cookbook path.
func TestIngredientNominal(t *testing.T) {
	for _, tc := range []struct {
		name               string
		cookbook, artifact string
		repo, want         string
	}{
		{"no base prefix", "registry.example.com/cookbook/app", "registry.example.com/cookbook/app",
			"docker.io/library/nginx", "docker.io/library/nginx"},
		{"base prefix", "registry.example.com/cookbook/app", "mirror/zone-a/registry.example.com/cookbook/app",
			"mirror/zone-a/docker.io/library/nginx", "docker.io/library/nginx"},
		{"port folded", "lab.example.com:5000/cookbook/app", "base/lab.example.com_5000/cookbook/app",
			"base/lab.example.com_5000/images/app", "lab.example.com_5000/images/app"},
		{"alias folded", "index.docker.io/cookbook/app", "base/docker.io/cookbook/app",
			"base/docker.io/library/app", "docker.io/library/app"},
		{"inconsistent medium kept as is", "registry.example.com/cookbook/app", "elsewhere/cookbook/app",
			"base/docker.io/library/nginx", "base/docker.io/library/nginx"},
		{"ingredient outside the prefix kept as is", "registry.example.com/cookbook/app", "base/registry.example.com/cookbook/app",
			"docker.io/library/nginx", "docker.io/library/nginx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Recipe{CookbookRepo: tc.cookbook, ArtifactRepo: tc.artifact}
			if got := ingredientNominal(r, tc.repo); got != tc.want {
				t.Errorf("ingredientNominal = %q, want %q", got, tc.want)
			}
		})
	}
}
