// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestSyncVersionResolution locks FR-021 end to end: a constraint entry
// resolves to the highest satisfying cookbook tag (signature tags
// excluded from the candidate set), and an unsatisfiable constraint fails
// with TBY-REG-006 — never a silent fallback.
func TestSyncVersionResolution(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	dst := openStore(t)
	kp := newKeyPair(t)

	for _, v := range []string{"6.8.1", "6.8.2"} {
		yaml := cookedRecipeYAML(t, "wordpress", v, []spec.Ingredient{{
			Name: "app", Kind: spec.IngredientContainerImage,
			Ref: src.addr + "/library/app", Version: "1.0.0", Digest: imgDig,
		}})
		d := publishRecipe(t, src.st, "cookbook/wordpress", v, yaml)
		signManifest(t, src.st, "cookbook/wordpress", d, kp)
	}

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "wordpress", Version: "~6.8.0"}, // resolvable constraint
		{Name: "wordpress", Version: "^9.0.0"}, // satisfiable by nothing
	})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The constraint resolved to the highest tag; the report shows
	// requested → resolved → digest.
	row := resolutionFor(t, task, "wordpress@6.8.2", "")
	if row.Requested != "~6.8.0" || row.Resolved != "6.8.2" || !strings.HasPrefix(row.Digest, "sha256:") {
		t.Errorf("resolution row = %+v, want ~6.8.0 → 6.8.2 with a digest", row)
	}
	if it := itemByName(t, task, "wordpress@6.8.2/recipe"); it.Status != tasks.StatusDone {
		t.Errorf("resolved recipe item = %+v, want done", it)
	}

	// The unsatisfiable constraint: TBY-REG-006 on the entry's item.
	it := itemByName(t, task, "wordpress")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeVersionResolve {
		t.Errorf("unsatisfiable entry item = %+v, want failed TBY-REG-006 (FR-021)", it)
	}
}

// TestResolveVersion locks the FR-021 resolution semantics at the unit
// level: literal exact tags (semver or not), constraints picking the
// highest satisfying tag, hard failure when nothing matches.
func TestResolveVersion(t *testing.T) {
	cases := []struct {
		expr string
		tags []string
		want string // empty = expect an error
	}{
		// Exact tags match literally — including non-semver ones.
		{"6.8.2", []string{"6.8.1", "6.8.2"}, "6.8.2"},
		{"stable-2026", []string{"1.0.0", "stable-2026"}, "stable-2026"},
		{"v1.2.3", []string{"1.2.3"}, ""}, // literal comparison: "v1.2.3" ≠ "1.2.3"
		{"6.9.0", []string{"6.8.1", "6.8.2"}, ""},
		// Constraints resolve to the highest satisfying semver tag.
		{"~6.8.0", []string{"6.8.1", "6.8.9", "6.9.0", "not-semver"}, "6.8.9"},
		{"^6.0.0", []string{"5.9.9", "6.1.0", "6.10.2", "7.0.0"}, "6.10.2"},
		{"6.x", []string{"6.1.0", "6.2.5", "7.0.0"}, "6.2.5"},
		{">=6.8.0 <7.0.0", []string{"6.7.0", "6.8.0", "7.0.0"}, "6.8.0"},
		// No satisfying tag: hard failure, never a fallback.
		{"~9.9.0", []string{"6.8.1", "6.8.2"}, ""},
		{"^1.0.0", nil, ""},
		// Invalid expression.
		{"", []string{"1.0.0"}, ""},
	}
	for _, tc := range cases {
		got, err := ResolveVersion(tc.expr, tc.tags)
		if tc.want == "" {
			if err == nil {
				t.Errorf("ResolveVersion(%q, %v) = %q, want an error", tc.expr, tc.tags, got)
			} else if tc.expr != "" && !strings.HasPrefix(err.Error(), "version ") {
				t.Errorf("ResolveVersion(%q) error %q lacks the taxonomy-mapping prefix", tc.expr, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveVersion(%q, %v): %v", tc.expr, tc.tags, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveVersion(%q, %v) = %q, want %q", tc.expr, tc.tags, got, tc.want)
		}
	}
}
