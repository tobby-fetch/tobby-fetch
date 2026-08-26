// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FR-054 acceptance, played on a real store: "truncating or
// corrupting any covered file is detected and blocks the push, naming the
// file; a tampered recipe fails signature verification and is blocked;
// extraneous content not referenced by any verified recipe is reported and
// not pushed; a media produced for zone A is refused by an instance
// configured for zone B". Plus the R-19 amendment, which fixes what
// "blocks" means: the medium for the four global conditions, the recipe
// for everything else.

const zoneA = "zone-a"

// twoRecipes builds a medium carrying two independent, signed deliveries —
// the shape R-19 is about.
func twoRecipes(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.addRecipe("beta", "2.0.0", true)
	f.write(time.Now().UTC())
	return f
}

func blockCodes(rep *media.Report) []taxonomy.Code {
	out := make([]taxonomy.Code, 0, len(rep.Blocks))
	for _, b := range rep.Blocks {
		out = append(out, b.Code)
	}
	return out
}

func verdictOf(t *testing.T, rep *media.Report, name string) media.RecipeVerdict {
	t.Helper()
	for i := range rep.Recipes {
		if rep.Recipes[i].Name == name {
			return rep.Recipes[i]
		}
	}
	t.Fatalf("recipe %q absent from the report (%d recipes)", name, len(rep.Recipes))
	return media.RecipeVerdict{}
}

// TestCleanMediumIsPushable is the baseline every corruption below is a
// deviation from: without it, a test that "detects a problem" might just
// be detecting that nothing ever verifies.
func TestCleanMediumIsPushable(t *testing.T) {
	f := twoRecipes(t)
	rep := f.verify(media.VerifyOptions{Zone: zoneA})

	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want %q (blocks: %v)", rep.Verdict, media.VerdictPushable, blockCodes(rep))
	}
	if len(rep.Recipes) != 2 {
		t.Fatalf("report carries %d recipes, want 2", len(rep.Recipes))
	}
	for i := range rep.Recipes {
		v := rep.Recipes[i]
		if !v.Pushable {
			t.Errorf("%s@%s blocked: %+v", v.Name, v.Version, v.Reason)
		}
		if v.KeyFingerprint == "" {
			t.Errorf("%s@%s verified without naming the key that did it", v.Name, v.Version)
		}
		if v.Files == 0 || v.Bytes == 0 {
			t.Errorf("%s@%s reaches %d files / %d bytes, want both non-zero", v.Name, v.Version, v.Files, v.Bytes)
		}
	}
	if len(rep.Findings) != 0 {
		t.Errorf("a clean medium reports findings: %+v", rep.Findings)
	}
	if rep.Checked.Files == 0 || rep.Checked.Bytes == 0 {
		t.Errorf("verification hashed nothing: %+v", rep.Checked)
	}
	if len(rep.Pushable()) != 2 {
		t.Errorf("Pushable() = %d recipes, want 2", len(rep.Pushable()))
	}
}

// TestTruncatedBlobBlocksOneRecipe is the R-19 acceptance itself: one
// truncated ingredient blob blocks its recipe, names the file, and leaves
// the neighbouring delivery pushable.
func TestTruncatedBlobBlocksOneRecipe(t *testing.T) {
	f := twoRecipes(t)
	layer := f.layerDigestOf("docker.io/library/alpha")
	victim := blobPathOf(layer)
	f.truncate(victim)

	rep := f.verify(media.VerifyOptions{Zone: zoneA})

	if rep.Verdict != media.VerdictPartial {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictPartial)
	}
	alpha := verdictOf(t, rep, "alpha")
	if alpha.Pushable {
		t.Fatal("the recipe reaching a truncated blob is pushable")
	}
	if alpha.Reason == nil || alpha.Reason.Path != victim {
		t.Fatalf("alpha blocked without naming the offending file: %+v", alpha.Reason)
	}
	if alpha.Reason.Code != taxonomy.CodeMediaFileSize {
		t.Errorf("alpha reason = %s, want %s", alpha.Reason.Code, taxonomy.CodeMediaFileSize)
	}
	if beta := verdictOf(t, rep, "beta"); !beta.Pushable {
		t.Fatalf("the intact neighbour was blocked too: %+v", beta.Reason)
	}
	if len(rep.Pushable()) != 1 {
		t.Errorf("Pushable() = %d recipes, want 1", len(rep.Pushable()))
	}
}

// TestCorruptedBlobBlocksOnDigest covers the corruption a size check
// cannot see: same length, different bytes.
func TestCorruptedBlobBlocksOnDigest(t *testing.T) {
	f := twoRecipes(t)
	layer := f.layerDigestOf("docker.io/library/beta")
	victim := blobPathOf(layer)
	f.flip(victim)

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	beta := verdictOf(t, rep, "beta")
	if beta.Pushable {
		t.Fatal("a blob whose bytes changed left its recipe pushable")
	}
	if beta.Reason == nil || beta.Reason.Path != victim {
		t.Fatalf("beta blocked without naming the offending file: %+v", beta.Reason)
	}
	// The inventory digest is checked before the content address, so this
	// is the code an operator sees; the content-address check exists for
	// the case where the inventory itself was rewritten to match.
	if beta.Reason.Code != taxonomy.CodeMediaFileDigest {
		t.Errorf("beta reason = %s, want %s", beta.Reason.Code, taxonomy.CodeMediaFileDigest)
	}
	if alpha := verdictOf(t, rep, "alpha"); !alpha.Pushable {
		t.Errorf("the intact neighbour was blocked too: %+v", alpha.Reason)
	}
}

// TestRewrittenInventoryStillCaughtByContentAddress is the case the
// manifest cannot help with: an attacker who corrupts a blob AND rewrites
// its inventory entry to match. The content-addressed store still
// disagrees with itself.
func TestRewrittenInventoryStillCaughtByContentAddress(t *testing.T) {
	f := twoRecipes(t)
	layer := f.layerDigestOf("docker.io/library/alpha")
	victim := blobPathOf(layer)
	f.flip(victim)

	// Re-inventory the corrupted medium: every entry now matches the bytes
	// on disk, which is exactly what a forged manifest looks like.
	f.write(time.Now().UTC())

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	alpha := verdictOf(t, rep, "alpha")
	if alpha.Pushable {
		t.Fatal("a blob stored under the wrong digest was accepted because the manifest agreed with it")
	}
	if alpha.Reason == nil || alpha.Reason.Code != taxonomy.CodeMediaContentAddress {
		t.Fatalf("alpha reason = %+v, want %s", alpha.Reason, taxonomy.CodeMediaContentAddress)
	}
	if alpha.Reason.Path != victim {
		t.Errorf("reason names %q, want %q", alpha.Reason.Path, victim)
	}
}

// TestMissingBlobBlocksItsRecipe covers a partial copy onto the medium.
func TestMissingBlobBlocksItsRecipe(t *testing.T) {
	f := twoRecipes(t)
	layer := f.layerDigestOf("docker.io/library/alpha")
	victim := blobPathOf(layer)
	if err := os.Remove(f.path(victim)); err != nil {
		t.Fatal(err)
	}

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	alpha := verdictOf(t, rep, "alpha")
	if alpha.Pushable {
		t.Fatal("a recipe missing one of its blobs is pushable")
	}
	if alpha.Reason == nil || alpha.Reason.Code != taxonomy.CodeMediaFileMissing || alpha.Reason.Path != victim {
		t.Fatalf("alpha reason = %+v, want %s on %s", alpha.Reason, taxonomy.CodeMediaFileMissing, victim)
	}
	if alpha.Reason.Params["recipe"] != "alpha@1.0.0" {
		t.Errorf("the refusal does not name the delivery: %+v", alpha.Reason.Params)
	}
}

// TestAlteredRecipeGraphBlocksTheWholeMedium is R-19's second global
// condition: without a trustworthy reachability set there is nothing to
// compute a per-recipe verdict from.
func TestAlteredRecipeGraphBlocksTheWholeMedium(t *testing.T) {
	f := twoRecipes(t)
	f.flip("meta/recipes.json")

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
	}
	if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaGraphAltered {
		t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaGraphAltered)
	}
	if rep.Blocks[0].Overridable {
		t.Error("an altered recipe graph must offer no override (R-19)")
	}
	if len(rep.Pushable()) != 0 {
		t.Error("a globally blocked medium still offered content to push")
	}
}

// TestZoneMismatchBlocksAndOverrides is the third global condition and the
// first overridable one (FR-054, FR-094).
func TestZoneMismatchBlocksAndOverrides(t *testing.T) {
	f := twoRecipes(t)

	rep := f.verify(media.VerifyOptions{Zone: "zone-b"})
	if rep.Verdict != media.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
	}
	if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaZoneMismatch {
		t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaZoneMismatch)
	}
	if !rep.Blocks[0].Overridable || rep.Blocks[0].Overridden {
		t.Fatalf("zone block = %+v, want overridable and not yet overridden", rep.Blocks[0])
	}
	if rep.Blocks[0].Params["expected"] != "zone-b" || rep.Blocks[0].Params["found"] != zoneA {
		t.Errorf("the refusal does not name both zones: %+v", rep.Blocks[0].Params)
	}
	if rep.Zone.Match {
		t.Error("ZoneCheck.Match is true on a mismatch")
	}

	overridden := f.verify(media.VerifyOptions{Zone: "zone-b", AllowZoneMismatch: true})
	if overridden.Verdict != media.VerdictPushable {
		t.Fatalf("overridden verdict = %q, want %q (blocks %v)", overridden.Verdict, media.VerdictPushable, blockCodes(overridden))
	}
	if len(overridden.Blocks) != 1 || !overridden.Blocks[0].Overridden {
		t.Errorf("the applied override left no trace in the report: %+v", overridden.Blocks)
	}
}

// TestStaleMediumRefusedNamingBothTimestamps is R-28: the anti-accident
// guard, its two timestamps, and its audited override.
func TestStaleMediumRefusedNamingBothTimestamps(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	old := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	m := f.write(old)

	last := &media.ImportRecord{
		MediaID:    "20260820T090000Z-0011223344556677",
		ResolvedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
		ImportedAt: time.Date(2026, 8, 20, 9, 5, 0, 0, time.UTC),
	}

	rep := f.verify(media.VerifyOptions{Zone: zoneA, LastImport: last})
	if rep.Verdict != media.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
	}
	if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaStale {
		t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaStale)
	}
	params := rep.Blocks[0].Params
	if params["resolved"] != old.Format(time.RFC3339) || params["recorded"] != last.ResolvedAt.Format(time.RFC3339) {
		t.Errorf("the refusal does not name BOTH timestamps: %+v", params)
	}
	if params["media"] != m.MediaID {
		t.Errorf("the refusal does not name the medium: %+v", params)
	}
	if rep.Freshness == nil || !rep.Freshness.Stale {
		t.Fatalf("freshness = %+v, want a stale verdict", rep.Freshness)
	}

	overridden := f.verify(media.VerifyOptions{Zone: zoneA, LastImport: last, AllowStale: true})
	if overridden.Verdict != media.VerdictPushable {
		t.Fatalf("overridden verdict = %q, want %q", overridden.Verdict, media.VerdictPushable)
	}
}

// TestFresherMediumPasses keeps the staleness guard honest: it must let
// the normal case through, or it is just a broken importer.
func TestFresherMediumPasses(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", true)
	f.write(time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC))

	rep := f.verify(media.VerifyOptions{Zone: zoneA, LastImport: &media.ImportRecord{
		ResolvedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC),
	}})
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want %q (blocks %v)", rep.Verdict, media.VerdictPushable, blockCodes(rep))
	}
	if rep.Freshness == nil || rep.Freshness.Stale {
		t.Errorf("freshness = %+v, want a non-stale verdict", rep.Freshness)
	}
}

// TestAbsentManifestBlocksWithNoOverride is R-19's first global condition.
func TestAbsentManifestBlocksWithNoOverride(t *testing.T) {
	f := twoRecipes(t)
	if err := os.Remove(f.path(media.ManifestPath)); err != nil {
		t.Fatal(err)
	}

	rep := f.verify(media.VerifyOptions{Zone: zoneA, AllowZoneMismatch: true, AllowStale: true})
	if rep.Verdict != media.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
	}
	if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaManifestMissing {
		t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaManifestMissing)
	}
	if rep.Blocks[0].Overridable || rep.Blocks[0].Overridden {
		t.Error("a missing manifest must offer no override, and no override may apply to it")
	}
	if rep.Media != nil {
		t.Error("the report describes a medium whose manifest it never read")
	}
}

// TestUnparseableManifestBlocks covers the truncated or garbled manifest.
func TestUnparseableManifestBlocks(t *testing.T) {
	f := twoRecipes(t)
	if err := os.WriteFile(f.path(media.ManifestPath), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaManifestUnreadable {
		t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaManifestUnreadable)
	}
	if rep.Blocks[0].Overridable {
		t.Error("an unreadable manifest must offer no override")
	}
}

// TestHostileInventoryPathBlocks is the NFR-011 guard: a manifest crafted
// to point outside the store is refused as inconsistent, before anything
// is opened.
func TestHostileInventoryPathBlocks(t *testing.T) {
	for _, hostile := range []string{
		"../../etc/passwd",
		"meta/../../etc/passwd",
		"/etc/passwd",
		`meta\..\..\windows\system32\config\sam`,
		"_tobby/tasks/whatever.json",
		"meta/./recipes.json",
	} {
		t.Run(hostile, func(t *testing.T) {
			f := newFixture(t)
			f.addRecipe("alpha", "1.0.0", true)
			f.write(time.Now().UTC())
			raw, err := os.ReadFile(f.path(media.ManifestPath))
			if err != nil {
				t.Fatal(err)
			}
			var m media.Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m.Inventory = append(m.Inventory, media.File{
				Path: hostile, Size: 1,
				Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			})
			out, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.path(media.ManifestPath), out, 0o600); err != nil {
				t.Fatal(err)
			}

			rep := f.verify(media.VerifyOptions{Zone: zoneA})
			if rep.Verdict != media.VerdictBlocked {
				t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
			}
			if len(rep.Blocks) != 1 || rep.Blocks[0].Code != taxonomy.CodeMediaManifestUnreadable {
				t.Fatalf("blocks = %v, want a single %s", blockCodes(rep), taxonomy.CodeMediaManifestUnreadable)
			}
			// The refusal quotes the path with %q, so a Windows-style
			// entry appears escaped; either form names it.
			detail := rep.Blocks[0].Params["detail"]
			if !strings.Contains(detail, hostile) && !strings.Contains(detail, strconv.Quote(hostile)) {
				t.Errorf("the refusal does not name the offending path: %q", detail)
			}
		})
	}
}

// TestUnverifiableSignatureBlocksItsRecipe is the "tampered recipe" half of
// the FR-054 acceptance: content intact, signature not ours.
func TestUnverifiableSignatureBlocksItsRecipe(t *testing.T) {
	f := twoRecipes(t)
	other := newFixture(t) // a different zone's key material

	rep := f.verify(media.VerifyOptions{Zone: zoneA, Trust: trustFor{keys: other.keys}})
	if rep.Verdict != media.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
	}
	for i := range rep.Recipes {
		v := rep.Recipes[i]
		if v.Pushable {
			t.Errorf("%s verified against a foreign key set", v.Name)
		}
		if v.Reason == nil || v.Reason.Code != taxonomy.CodeSignature {
			t.Errorf("%s reason = %+v, want %s", v.Name, v.Reason, taxonomy.CodeSignature)
		}
		if v.Reason != nil && v.Reason.Params["fingerprints"] == "" {
			t.Errorf("%s refused without naming the keys tried (FR-033)", v.Name)
		}
	}
}

// TestUnsignedRecipeBlocksByDefault is FR-075 applied here: no signature,
// no push, unless a declared scope says otherwise — and then it says so.
func TestUnsignedRecipeBlocksByDefault(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", "1.0.0", false) // no signature published
	f.write(time.Now().UTC())

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	alpha := verdictOf(t, rep, "alpha")
	if alpha.Pushable {
		t.Fatal("an unsigned recipe is pushable by default")
	}
	if alpha.Reason == nil || alpha.Reason.Code != taxonomy.CodeSignature {
		t.Fatalf("alpha reason = %+v, want %s", alpha.Reason, taxonomy.CodeSignature)
	}

	relaxed := f.verify(media.VerifyOptions{Zone: zoneA, Trust: trustFor{
		keys: f.keys, allowUnsigned: true, scope: "lab-sources",
	}})
	admitted := verdictOf(t, relaxed, "alpha")
	if !admitted.Pushable || !admitted.Unsigned {
		t.Fatalf("declared scope did not admit the unsigned recipe: %+v", admitted)
	}
	if admitted.TrustScope != "lab-sources" {
		t.Errorf("the relaxation is invisible: trustScope = %q", admitted.TrustScope)
	}
}

// TestNoTrustRootBlocksEverything: an instance with no roots verifies
// nothing, and says so rather than waving content through.
func TestNoTrustRootBlocksEverything(t *testing.T) {
	f := twoRecipes(t)
	rep := f.verify(media.VerifyOptions{Zone: zoneA, Trust: trustFor{}})
	if rep.Verdict != media.VerdictBlocked {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictBlocked)
	}
	alpha := verdictOf(t, rep, "alpha")
	if !strings.Contains(alpha.Reason.Params["fingerprints"], "no trust root") {
		t.Errorf("the refusal does not explain itself: %+v", alpha.Reason.Params)
	}
}

// TestExtraneousContentReportedNotBlocking is the third FR-054 acceptance
// clause: content nothing reaches is reported and never pushed, and it
// stops nothing.
func TestExtraneousContentReportedNotBlocking(t *testing.T) {
	f := twoRecipes(t)

	// A blob nobody references, inventoried: an earlier delivery's
	// leftover, the usual case.
	f.putBlob("docker.io/library/orphan", []byte("nobody delivers this"))
	f.write(time.Now().UTC())

	// A file added AFTER the manifest: outside the inventory entirely.
	uncovered := "docker/registry/v2/repositories/docker.io/library/alpha/_manifests/tags/1.0/planted"
	if err := os.WriteFile(f.path(uncovered), []byte("planted"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want %q — extraneous content blocks nothing", rep.Verdict, media.VerdictPushable)
	}
	var sawUnreachable, sawUncovered bool
	for _, fd := range rep.Findings {
		switch fd.Code {
		case taxonomy.CodeMediaUnreachable:
			sawUnreachable = true
		case taxonomy.CodeMediaUncovered:
			if fd.Path == uncovered {
				sawUncovered = true
			}
		}
	}
	if !sawUnreachable {
		t.Errorf("the orphan blob was not reported as unreachable: %+v", rep.Findings)
	}
	if !sawUncovered {
		t.Errorf("the planted file was not reported as uncovered: %+v", rep.Findings)
	}
}

// TestAlteredBookkeepingReportedNotBlocking: the store's own ledgers other
// than the recipe graph are checked, reported, and block nothing — nothing
// is ever pushed out of them.
func TestAlteredBookkeepingReportedNotBlocking(t *testing.T) {
	f := twoRecipes(t)
	f.flip("meta/provenance.json")

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want %q", rep.Verdict, media.VerdictPushable)
	}
	found := false
	for _, fd := range rep.Findings {
		if fd.Code == taxonomy.CodeMediaMetadataAltered && fd.Path == "meta/provenance.json" {
			found = true
		}
	}
	if !found {
		t.Errorf("altered bookkeeping went unreported: %+v", rep.Findings)
	}
}

// TestSparseIndexChildIsNotMissingContent: platform selection (FR-022)
// keeps an index's pinned digest while carrying only some children, so an
// absent child must not read as a truncated medium.
func TestSparseIndexChildIsNotMissingContent(t *testing.T) {
	f := newFixture(t)
	repo := "docker.io/library/multi"
	present := f.putImage(repo, "child", []byte("the platform we kept"))
	absent := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	indexDigest := f.putIndex(repo, "1.0", []string{present, absent})

	artifactRepo := "registry.example.com/cookbook/multi"
	artifactDigest := f.putImage(artifactRepo, "1.0.0", []byte("recipe document"))
	f.sign(artifactRepo, artifactDigest)
	rec := f.addRecipe("multi-app", "1.0.0", true)
	rec.Ingredients = append(rec.Ingredients, storeIngredient(repo, "1.0", indexDigest))
	if err := f.st.PutRecipeRecord(&rec); err != nil {
		t.Fatal(err)
	}
	f.write(time.Now().UTC())

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	v := verdictOf(t, rep, "multi-app")
	if !v.Pushable {
		t.Fatalf("a sparse index blocked its recipe: %+v", v.Reason)
	}
}

// TestProgressIsReported: FR-054 asks for verification progress to be
// displayed, which means the engine has to emit it.
func TestProgressIsReported(t *testing.T) {
	f := twoRecipes(t)
	var stages []media.Stage
	var lastFiles int
	rep := f.verify(media.VerifyOptions{Zone: zoneA, Progress: func(p media.Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
		if p.Files < lastFiles {
			t.Errorf("progress went backwards: %d after %d", p.Files, lastFiles)
		}
		lastFiles = p.Files
	}})
	if len(stages) < 3 {
		t.Fatalf("stages seen = %v, want at least manifest, recipes and extraneous", stages)
	}
	if stages[0] != media.StageManifest {
		t.Errorf("first stage = %q, want %q", stages[0], media.StageManifest)
	}
	if lastFiles != rep.Checked.Files {
		t.Errorf("last progress reported %d files, report says %d", lastFiles, rep.Checked.Files)
	}
}

// TestCancellationStops: a verification of a large medium must be
// interruptible, or the UI cannot offer a cancel button.
func TestCancellationStops(t *testing.T) {
	f := twoRecipes(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := media.Verify(ctx, f.st, media.VerifyOptions{Zone: zoneA, Trust: trustFor{keys: f.keys}})
	if err != nil {
		t.Fatalf("verification errored instead of reporting: %v", err)
	}
	if rep.Verdict != media.VerdictBlocked {
		t.Errorf("a cancelled verification returned %q; nothing may be pushed on it", rep.Verdict)
	}
}

// TestReportSerializes: the report is the contract other surfaces consume
// (FR-061, FR-066), so it has to survive a round trip through JSON.
func TestReportSerializes(t *testing.T) {
	f := twoRecipes(t)
	rep := f.verify(media.VerifyOptions{Zone: "zone-b"})
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshalling the report: %v", err)
	}
	var back media.Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshalling the report: %v", err)
	}
	if back.Verdict != rep.Verdict || len(back.Blocks) != len(rep.Blocks) {
		t.Fatalf("round trip changed the report: %+v vs %+v", back, rep)
	}
	// Every code in the report must render in both languages (FR-063):
	// these strings are shown to an operator, not logged.
	for _, b := range back.Blocks {
		for _, lang := range taxonomy.Languages() {
			m := taxonomy.Localize(lang, b.Error())
			if m.What == "" || m.Cause == "" || m.Action == "" {
				t.Errorf("%s renders incompletely in %s: %+v", b.Code, lang, m)
			}
		}
	}
}

// TestUnknownCodeRendersInsteadOfPanicking: a report that came back
// through JSON is data, and a rendering surface must not take the process
// down over a code it does not know.
func TestUnknownCodeRendersInsteadOfPanicking(t *testing.T) {
	b := media.Block{Code: "TBY-MED-999", Params: map[string]string{"path": "meta/media.json"}}
	if m := taxonomy.Localize("en", b.Error()); m.What == "" {
		t.Error("an unknown code rendered to nothing")
	}
	r := media.Reason{Code: "not-a-code"}
	if m := taxonomy.Localize("fr", r.Error()); m.Action == "" {
		t.Error("an unknown reason code rendered to nothing")
	}
	fd := media.Finding{Code: "", Path: "x"}
	if m := taxonomy.Localize("en", fd.Error()); m.Cause == "" {
		t.Error("an empty finding code rendered to nothing")
	}
}
