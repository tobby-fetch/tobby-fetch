// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package provenance

import (
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

type lister []*tasks.Task

func (l lister) List(tasks.Status, string, string) []*tasks.Task { return l }

type graph []store.RecipeRecord

func (g graph) RecipeRecords() ([]store.RecipeRecord, error) { return g, nil }

func scanned(at time.Time, verdict scan.Verdict) *scan.Report {
	return &scan.Report{
		ScannedAt: at,
		Database:  &scan.Database{BuiltAt: at.Add(-2 * time.Hour), AgeSeconds: 7200, Stale: true},
		Policy:    scan.Evaluation{Verdict: verdict},
	}
}

// TestLastScansPicksTheMostRecentActualScan locks the FR-098 scan history:
// the latest dated scan of each digest wins, wherever its task sits in
// the list; an item tied by the recipe graph to another repository does
// not count; reports without a scan date (out of scope, not scanned) and
// items that are not scanned elements never do.
func TestLastScansPicksTheMostRecentActualScan(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(48 * time.Hour)
	h := &History{
		Graph: graph{{Name: "web", Version: "1.0.0", Ingredients: []store.IngredientRecord{
			{Name: "nginx", Repo: "docker.io/library/nginx"},
			{Name: "nginx-mirror", Repo: "mirror/nginx"},
		}}},
		Tasks: lister{
			{ID: "old", Items: []tasks.Item{{Name: "web@1.0.0/nginx", Digest: "sha256:a", Scan: scanned(t1, scan.VerdictWarn)}}},
			{ID: "new", Items: []tasks.Item{
				{Name: "web@1.0.0/nginx", Digest: "sha256:a", Scan: scanned(t2, scan.VerdictPass)},
				// Same digest, another repository: not this repository's scan.
				{Name: "web@1.0.0/nginx-mirror", Digest: "sha256:b", Scan: scanned(t2, scan.VerdictBlock)},
				// No scan date: out of scope or not scanned.
				{Name: "web@1.0.0/chart", Digest: "sha256:c", Scan: &scan.Report{Policy: scan.Evaluation{Verdict: scan.VerdictOutOfScope}}},
				// Not a scanned element (a unit-import platform item).
				{Name: "linux/amd64", Digest: "sha256:d", Scan: scanned(t2, scan.VerdictPass)},
			}},
			// A recipe no longer recorded: matched by its digest alone.
			{ID: "gone", Items: []tasks.Item{{Name: "old@0.1.0/nginx", Digest: "sha256:e", Scan: scanned(t1, scan.VerdictPass)}}},
		},
	}
	got := h.LastScans("docker.io/library/nginx", []string{"sha256:a", "sha256:b", "sha256:c", "sha256:d", "sha256:e"})
	if s, ok := got["sha256:a"]; !ok || s.Verdict != "pass" || !s.ScannedAt.Equal(t2) || s.Task != "new" ||
		s.DatabaseAgeSeconds != 7200 || !s.Stale || !s.DatabaseBuiltAt.Equal(t2.Add(-2*time.Hour)) {
		t.Errorf("sha256:a = %+v (%v), want the newer pass", s, ok)
	}
	for _, d := range []string{"sha256:b", "sha256:c", "sha256:d"} {
		if s, ok := got[d]; ok {
			t.Errorf("%s counted: %+v", d, s)
		}
	}
	if _, ok := got["sha256:e"]; !ok {
		t.Error("the scan of a recipe no longer recorded was dropped")
	}
	latest, ok := Latest(got)
	if !ok || !latest.ScannedAt.Equal(t2) {
		t.Errorf("Latest = %+v %v", latest, ok)
	}
	var none *History
	if none.LastScans("x", []string{"sha256:a"}) != nil {
		t.Error("a nil history answered")
	}
	if _, ok := Latest(nil); ok {
		t.Error("Latest of nothing found something")
	}
}
