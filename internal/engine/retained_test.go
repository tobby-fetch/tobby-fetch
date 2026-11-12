// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"slices"
	"testing"
)

// TestRecordStatesTheRetainedPlatforms: the recipe graph records which
// index children a recipe retained (FR-022), so the store verification
// can tell a lost platform from one the recipe never selected (R-31).
// The recipe keeps linux/amd64 and linux/arm64 of a three-platform index.
func TestRecordStatesTheRetainedPlatforms(t *testing.T) {
	env := newHappyEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	records, err := env.dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	want := []string{env.idx.children["linux/amd64"], env.idx.children["linux/arm64"]}
	slices.Sort(want)
	for _, ing := range records[0].Ingredients {
		got := slices.Clone(ing.Children)
		slices.Sort(got)
		switch ing.Name {
		case "app":
			if !slices.Equal(got, want) {
				t.Errorf("app retained %v; want %v (arm/v7 left out)", got, want)
			}
		default:
			if len(got) != 0 {
				t.Errorf("%s is no index, yet records children %v", ing.Name, got)
			}
		}
	}
}
