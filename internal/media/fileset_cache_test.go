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
//
// Out of coverage is not out of sight, though (A9 of the milestone-7
// review): a carried cache is summarized in ONE finding (TBY-MED-023), and
// whatever under it does not have the shape of an extraction is reported
// as uncovered content.

var (
	digestDirA = "sha256_" + strings.Repeat("0", 64)
	digestDirB = "sha256_" + strings.Repeat("1", 64)
	cachedFile = "meta/fileserve/site-config/" + digestDirA + "/rootfs/etc/marker.conf"
)

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
	plant(t, f, "meta/fileserve/site-config/"+digestDirB+"/rootfs/etc/marker.conf", "extracted here")

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want pushable", rep.Verdict)
	}
	if got := findingsUnder(rep, media.FileSetCache); len(got) != 1 || got[0] != "TBY-MED-023 "+media.FileSetCache {
		t.Errorf("the fileset cache produces %v, want its one summary finding", got)
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
		map[string]any{"path": "meta/fileserve/site-config/" + digestDirA + "/.complete", "size": 0, "digest": dgstOf(nil)},
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
	if got := findingsUnder(rep, media.FileSetCache); len(got) != 1 || got[0] != "TBY-MED-023 "+media.FileSetCache {
		t.Errorf("the fileset cache listed by a v0.6.0 manifest produces %v, want its one summary finding", got)
	}
}

// TestACarriedFileSetCacheLeavesATrace (A9): the cache crosses the gap
// unhashed, so it is never silent. Its extraction-shaped content is
// summarized — files and bytes — and everything else under it is reported
// path by path as uncovered.
//
// Proved fallible: before the fix every file under meta/fileserve passed
// the verification with no finding at all.
func TestACarriedFileSetCacheLeavesATrace(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.write(time.Now().UTC())
	plant(t, f, cachedFile, "12345")
	plant(t, f, "meta/fileserve/site-config/"+digestDirA+"/.complete", "")
	plant(t, f, "meta/fileserve/exfil.tar", "smuggled")
	plant(t, f, "meta/fileserve/site-config/"+digestDirA+"/notes.txt", "smuggled")
	plant(t, f, "meta/fileserve/site-config/not-a-digest/rootfs/x", "smuggled")

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	got := map[string]media.Finding{}
	for _, fd := range rep.Findings {
		if strings.HasPrefix(fd.Path, media.FileSetCache) {
			got[string(fd.Code)+" "+fd.Path] = fd
		}
	}
	sum, ok := got["TBY-MED-023 "+media.FileSetCache]
	if !ok || sum.Params["files"] != "2" || sum.Params["bytes"] != "5" {
		t.Errorf("no summary of the extraction-shaped cache (2 files, 5 bytes): %+v", got)
	}
	for _, p := range []string{
		"meta/fileserve/exfil.tar",
		"meta/fileserve/site-config/" + digestDirA + "/notes.txt",
		"meta/fileserve/site-config/not-a-digest",
	} {
		if _, ok := got["TBY-MED-020 "+p]; !ok {
			t.Errorf("%s is not reported as uncovered: %v", p, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("findings under the cache = %d, want 4: %v", len(got), got)
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
