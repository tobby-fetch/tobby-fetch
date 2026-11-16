// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
)

// B-060. The FileSet cache (meta/fileserve) is derived from blobs by the
// instance serving it: the destination discards whatever cache a medium
// carries and re-extracts from the verified blobs (FR-054, B-058). It is
// therefore not content the manifest can vouch for, nor content whose
// absence or presence means anything — covering it only produced false
// "metadata altered" and "uncovered" findings.

const cachedFile = "meta/fileserve/site-config/sha256_0000/rootfs/etc/marker.conf"

func plant(t *testing.T, f *fixture, slash, content string) {
	t.Helper()
	p := f.path(slash)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findingsUnder(rep *media.Report, prefix string) []string {
	var out []string
	for _, fd := range rep.Findings {
		if strings.HasPrefix(fd.Path, prefix) {
			out = append(out, string(fd.Code)+" "+fd.Path)
		}
	}
	return out
}

// TestNewManifestsLeaveTheFileSetCacheOut: a cache present when the
// manifest is written is not inventoried, and one written by the
// destination after it is not reported as uncovered.
//
// Proved fallible: before B-060 the planted file is in the inventory, and
// the destination's file is reported TBY-MED-* uncovered.
func TestNewManifestsLeaveTheFileSetCacheOut(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	plant(t, f, cachedFile, "extracted on the source side")
	m := f.write(time.Now().UTC())
	for _, e := range m.Inventory {
		if strings.HasPrefix(e.Path, media.FileSetCache+"/") {
			t.Errorf("the manifest inventories the fileset cache: %s", e.Path)
		}
	}

	// The destination discards the carried tree and extracts its own.
	if err := os.RemoveAll(f.path(media.FileSetCache)); err != nil {
		t.Fatal(err)
	}
	plant(t, f, "meta/fileserve/site-config/sha256_1111/rootfs/etc/marker.conf", "extracted here")

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want pushable", rep.Verdict)
	}
	if got := findingsUnder(rep, media.FileSetCache); len(got) > 0 {
		t.Errorf("the fileset cache produces findings: %v", got)
	}
}

// TestAV060ManifestListingTheFileSetCacheIsAccepted: a manifest written by
// v0.6.0 may list the cache. It must not be refused — the entries are
// legal paths of that format — and they are ignored by the verification:
// a missing or altered cached file is no finding, since the destination
// never serves it.
//
// Proved fallible: before B-060 the discarded entry reads as altered
// bookkeeping; with the cache simply excluded from coverage and nothing
// else, the manifest is refused as unreadable (path outside coverage).
func TestAV060ManifestListingTheFileSetCacheIsAccepted(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.write(time.Now().UTC())

	// What v0.6.0 wrote when a cache was present: an inventory entry
	// under meta/fileserve, counted in the totals.
	raw, err := os.ReadFile(f.path(media.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	content := []byte("extracted on the source side")
	inv := doc["inventory"].([]any)
	doc["inventory"] = append(inv,
		map[string]any{"path": cachedFile, "size": len(content), "digest": dgstOf(content)},
		map[string]any{"path": "meta/fileserve/site-config/sha256_0000/.complete", "size": 0, "digest": dgstOf(nil)},
	)
	totals := doc["totals"].(map[string]any)
	totals["files"] = totals["files"].(float64) + 2
	totals["bytes"] = totals["bytes"].(float64) + float64(len(content))
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path(media.ManifestPath), out, 0o600); err != nil {
		t.Fatal(err)
	}

	// One listed file altered, the other gone: both are the cache the
	// destination discards and rebuilds.
	plant(t, f, cachedFile, "altered on the shelf")

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if len(rep.Blocks) > 0 {
		t.Fatalf("a v0.6.0 manifest listing the fileset cache is refused: %v", blockCodes(rep))
	}
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want pushable", rep.Verdict)
	}
	if got := findingsUnder(rep, media.FileSetCache); len(got) > 0 {
		t.Errorf("the fileset cache listed by a v0.6.0 manifest produces findings: %v", got)
	}
}

// TestTheFileSetCachePrefixIsExact: only the cache directory is out of
// coverage — a sibling whose name merely starts the same way is
// bookkeeping like any other.
func TestTheFileSetCachePrefixIsExact(t *testing.T) {
	if !media.Covered("meta/fileserve-notes.json") {
		t.Error("meta/fileserve-notes.json fell out of coverage")
	}
	if media.Covered(media.FileSetCache) || media.Covered(cachedFile) {
		t.Error("the fileset cache is still covered")
	}
}
