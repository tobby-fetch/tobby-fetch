// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package provenance answers the second half of FR-098 (R-17): for each
// tag of a repository, the result and the date of its last vulnerability
// scan, with the age of the database it was scanned with (FR-031).
//
// There is no scan ledger in the store, on purpose: the task reports
// already persist every report (tasks.Item.Scan), set whole by the engine
// at retrieval, and a second record of the same fact is a second place
// for it to drift. The history is therefore read from the tasks this
// instance still holds — bounded by tasks.keepFinished — and an item is
// tied to a repository through the recipe graph: an item is named
// "name@version/ingredient", and the recorded recipe says where that
// ingredient landed. An item whose recipe is no longer recorded still
// counts by its pinned digest alone: a digest names the same bytes in
// any repository.
//
// Only an actual scan counts. An item reported out of scope (not an
// image, ADR-0017) or not scanned (degraded mode) carries no scan date,
// and "last scanned" never points at something that was not.
package provenance

import (
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// Scan is the last vulnerability scan of one tag.
type Scan struct {
	// Verdict is the evaluated policy verdict (pass, warn, block).
	Verdict string
	// ScannedAt is when the scan ran.
	ScannedAt time.Time
	// DatabaseBuiltAt is the build time of the vulnerability database the
	// scan used; zero when the report does not name one.
	DatabaseBuiltAt time.Time
	// DatabaseAgeSeconds is the database age at scan time, as the report
	// stated it (R-13: stated, not re-derived — "now" has moved since).
	DatabaseAgeSeconds int64
	// Stale reports a database beyond scan.maxDbAge at scan time.
	Stale bool
	// Task is the task whose report this is.
	Task string
}

// TaskLister is the read side of the task queue the history needs.
type TaskLister interface {
	List(status tasks.Status, taskType, query string) []*tasks.Task
}

// RecipeGraph is the read side of the store the history needs.
type RecipeGraph interface {
	RecipeRecords() ([]store.RecipeRecord, error)
}

// History looks up the last scans of a repository's tags.
type History struct {
	Tasks TaskLister
	Graph RecipeGraph
}

// LastScans returns, for each of the given digests of repo, its most
// recent scan; a digest never scanned is absent from the map. A nil
// History answers nothing.
func (h *History) LastScans(repo string, digests []string) map[string]Scan {
	if h == nil || h.Tasks == nil || len(digests) == 0 {
		return nil
	}
	wanted := map[string]bool{}
	for _, d := range digests {
		wanted[d] = true
	}
	// Where each recorded ingredient landed: "name@version/ingredient" →
	// repository.
	landed := map[string]string{}
	if h.Graph != nil {
		if records, err := h.Graph.RecipeRecords(); err == nil {
			for i := range records {
				r := &records[i]
				for _, ing := range r.Ingredients {
					landed[r.Name+"@"+r.Version+"/"+ing.Name] = ing.Repo
				}
			}
		}
	}
	out := map[string]Scan{}
	for _, t := range h.Tasks.List("", "", "") {
		for i := range t.Items {
			it := &t.Items[i]
			rep := it.Scan
			if rep == nil || rep.ScannedAt.IsZero() || !wanted[it.Digest] {
				continue
			}
			if where, known := landed[it.Name]; known && where != repo {
				continue
			}
			if _, isElement := elementOf(it.Name); !isElement {
				continue
			}
			if prev, ok := out[it.Digest]; ok && !rep.ScannedAt.After(prev.ScannedAt) {
				continue
			}
			s := Scan{Verdict: string(rep.Policy.Verdict), ScannedAt: rep.ScannedAt, Task: t.ID}
			if db := rep.Database; db != nil {
				s.DatabaseBuiltAt, s.DatabaseAgeSeconds, s.Stale = db.BuiltAt, db.AgeSeconds, db.Stale
			}
			out[it.Digest] = s
		}
	}
	return out
}

// Latest returns the most recent of scans, and false when there is none.
func Latest(scans map[string]Scan) (Scan, bool) {
	var best Scan
	found := false
	for _, s := range scans {
		if !found || s.ScannedAt.After(best.ScannedAt) {
			best, found = s, true
		}
	}
	return best, found
}

// elementOf splits a scanned item name "name@version/ingredient"
// (internal/engine/scan.go); anything else is not a scanned element.
func elementOf(name string) (recipe string, ok bool) {
	rid, ingredient, found := strings.Cut(name, "/")
	if !found || ingredient == "" || !strings.Contains(rid, "@") {
		return "", false
	}
	return rid, true
}
