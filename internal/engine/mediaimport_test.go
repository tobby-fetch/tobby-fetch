// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Destination-side operation (FR-052, feature 5.4). Both ends are real:
// the medium is produced by a real mirror synchronization, the
// destination is a real registry speaking the real Distribution protocol
// and recording every request it receives.

// cookbookRepoOf is where a promoted recipe lands in the zone's own
// cookbook (FR-034).
func cookbookRepoOf(name string) string { return config.DefaultCookbook + "/" + name }

// TestMediaImportPushesNothingBeforeVerificationReports is the central
// guarantee of this feature, and the reason it is written as an ORDER
// assertion rather than an outcome one.
//
// FR-054 does not say "verify and push", it says verification "SHALL
// precede any push, any serving, and any local write". An outcome test —
// a corrupted medium delivers nothing — would pass on an implementation
// that pushed first and rolled back afterwards, and rolling back a
// registry write is not something anyone can do. So the destination is
// watched: it fails the test the instant it is contacted while the report
// has not been produced yet.
func TestMediaImportPushesNothingBeforeVerificationReports(t *testing.T) {
	var reported atomic.Bool
	var early atomic.Int64
	dest := newDestRegistry(t, watching(func(string, string) {
		if !reported.Load() {
			early.Add(1)
		}
	}))

	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})
	eng, _ := destinationFor(t, m, dest, "zone-alpha", kp)

	task, err := runImport(t, eng, MediaOptions{
		Report: func(*media.Report) { reported.Store(true) },
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := early.Load(); n != 0 {
		t.Errorf("the destination was contacted %d times before verification reported: "+
			"FR-054 makes that order normative", n)
	}
	// Without this the assertion above is vacuous: an import that pushed
	// nothing at all would satisfy it trivially.
	if len(dest.writes()) == 0 {
		t.Fatalf("nothing was pushed, so the ordering assertion proves nothing (items %s)", itemNames(task))
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("import aggregates = %+v (items %s)", agg, itemNames(task))
	}
	if !reported.Load() {
		t.Error("the report hook never fired: the guided journey has no Verify → Report → Push seam")
	}
}

// TestMediaImportPushesNothingFromABlockedMedium is the other half of the
// order: a medium whose only delivery did not survive the trip must leave
// the destination untouched AND the medium unwritten.
//
// The second assertion is the one a request log cannot make. "No local
// write" is checked by fingerprinting every covered file before and after:
// a run that stamped the store, re-tagged something, or wrote its own
// bookkeeping inside coverage would show up here — and would have
// invalidated the very inventory it had just verified.
func TestMediaImportPushesNothingFromABlockedMedium(t *testing.T) {
	dest := newDestRegistry(t)
	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})

	id := "alpine@3.22.1"
	broken := corruptBlob(t, m.root(), m.ingredientDigest[id])
	before := coveredFingerprint(t, m.root())

	eng, imports := destinationFor(t, m, dest, "zone-alpha", kp)
	task, err := runImport(t, eng, MediaOptions{})

	if !isCode(err, taxonomy.CodeMediaFileDigest) {
		t.Fatalf("import error = %v, want %s (the corrupted blob)", err, taxonomy.CodeMediaFileDigest)
	}
	if w := dest.writes(); len(w) != 0 {
		t.Errorf("the destination received %d writes from a blocked medium: %v", len(w), w)
	}
	if changed := diffFingerprints(before, coveredFingerprint(t, m.root())); len(changed) != 0 {
		t.Errorf("the import wrote inside the manifest's coverage: %v", changed)
	}
	if _, ok := imports.Last("zone-alpha"); ok {
		t.Error("a blocked medium advanced the freshness register (R-28: only a COMPLETED import does)")
	}

	// The refusal names the file (FR-054 acceptance).
	item := itemByName(t, task, "media/"+id)
	if item.Status != tasks.StatusFailed || item.Error == nil {
		t.Fatalf("item %s = %+v, want a failure", item.Name, item)
	}
	got, _ := item.Error.Params["path"].(string)
	if got == "" || !strings.HasSuffix(filepath.ToSlash(broken), got) {
		t.Errorf("the refusal names %q, but the file corrupted was %q", got, broken)
	}
}

// TestMediaImportDeliversTheIntactRecipeAndBlocksTheDamagedOne is R-19,
// which is the whole reason a medium carries several deliveries: one
// damaged recipe must not cost the operator the others.
//
// The damage is chosen so that the GATE is what stops the push, and not
// the damage itself. One inventory entry is removed: the file is still
// there, still correct, still perfectly pushable — the only thing
// withholding it is FR-054's rule that a recipe reaching content the
// manifest does not vouch for is blocked whole. A corrupted blob would
// have failed the push on its own, and the test would then pass on an
// implementation with no gate at all.
func TestMediaImportDeliversTheIntactRecipeAndBlocksTheDamagedOne(t *testing.T) {
	dest := newDestRegistry(t)
	kp := newKeyPair(t)
	m := seedMedium(t, kp,
		mediumRecipe{name: "alpine", version: "3.22.1"},
		mediumRecipe{name: "nginx", version: "1.25.0"},
	)
	dropInventoryEntry(t, m.root(), blobInventoryPath(m.ingredientDigest["alpine@3.22.1"]))

	eng, imports := destinationFor(t, m, dest, "zone-alpha", kp)
	task, err := runImport(t, eng, MediaOptions{})
	if err != nil {
		t.Fatalf("a partially damaged medium must not fail the whole task: %v", err)
	}

	if item := itemByName(t, task, "media/alpine@3.22.1"); item.Status != tasks.StatusFailed {
		t.Errorf("the damaged recipe is %s, want failed", item.Status)
	}
	if item := itemByName(t, task, "media/nginx@1.25.0"); item.Status != tasks.StatusDone {
		t.Errorf("the intact recipe is %s, want done", item.Status)
	}
	// The intact delivery really crossed: content and cookbook entry.
	if tags := destTags(t, dest, cookbookRepoOf("nginx")); !containsString(tags, "1.25.0") {
		t.Errorf("the zone cookbook holds %v, want the intact recipe (FR-034)", tags)
	}
	// The damaged one did not, at any level.
	for _, repo := range []string{cookbookRepoOf("alpine"), "docker.io/library/alpine"} {
		if tags, terr := dest.st.Tags(context.Background(), repo); terr == nil && len(tags) > 0 {
			t.Errorf("the destination holds %s %v, from a blocked recipe", repo, tags)
		}
	}
	// Every recipe the report cleared was delivered, so the import
	// completed in the R-28 sense even though the medium was partial.
	if _, ok := imports.Last("zone-alpha"); !ok {
		t.Error("no freshness record: every cleared delivery landed, which is a completed import")
	}
}

// TestMediaImportDoesNotRecordAnImportThatDidNotLand is the other half of
// R-28's "the record advances only on a completed import".
//
// The medium is intact and verification clears it; the DESTINATION is
// what refuses — a registry that will not hold nested repository names
// (FR-035). Nothing is wrong with the medium, so the freshness guard must
// not remember it as delivered: the next attempt, once the destination is
// fixed, would otherwise be judged against a record of an import that
// never happened.
func TestMediaImportDoesNotRecordAnImportThatDidNotLand(t *testing.T) {
	dest := newDestRegistry(t, refusingNestedNames(2))
	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})

	eng, imports := destinationFor(t, m, dest, "zone-alpha", kp)
	task, err := runImport(t, eng, MediaOptions{})
	if err != nil {
		t.Fatalf("a destination refusing a name is a per-item failure, not a task one: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed == 0 {
		t.Fatalf("the push was expected to fail: aggregates %+v (items %s)", agg, itemNames(task))
	}
	if rec, ok := imports.Last("zone-alpha"); ok {
		t.Errorf("the freshness register advanced to %+v although the delivery never landed (R-28)", rec)
	}
	// The delivery's own verdict item must not claim success either:
	// verification clearing a recipe is not the zone holding it, and the
	// item carries up the reason the push gave.
	item := itemByName(t, task, "media/alpine@3.22.1")
	if item.Status != tasks.StatusFailed {
		t.Errorf("the delivery item is %s although the push failed: %s", item.Status, itemNames(task))
	}
	if item.Error == nil || item.Error.Code != taxonomy.CodeDestinationLimit {
		t.Errorf("delivery item error = %+v, want the destination's own refusal (%s)",
			item.Error, taxonomy.CodeDestinationLimit)
	}
}

// TestMediaImportIgnoresTrustRootsOnTheMedium locks FR-054's last
// sentence: "Trust roots present on the media SHALL be ignored."
//
// The medium's recipes are signed by a key whose public half is planted
// ON the medium, in the two places an implementation would plausibly look
// — beside the store's own bookkeeping and in Tobby's own area. The
// destination is configured with a DIFFERENT root, and must refuse.
//
// The positive control in the same test is what makes the negative one
// mean something: the identical medium, verified against the signer's key
// as a configured root, imports. So the refusal is about where the key
// came from and not about the medium being unimportable.
func TestMediaImportIgnoresTrustRootsOnTheMedium(t *testing.T) {
	signer, stranger := newKeyPair(t), newKeyPair(t)
	m := seedMedium(t, signer, mediumRecipe{name: "alpine", version: "3.22.1"})

	pem, err := signer.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	plantOnMedium(t, m.root(), "meta/trust/signer.pub", pem)
	plantOnMedium(t, m.root(), "_tobby/trust/signer.pub", pem)

	t.Run("the key on the medium buys nothing", func(t *testing.T) {
		dest := newDestRegistry(t)
		eng, _ := destinationFor(t, m, dest, "zone-alpha", stranger)
		task, err := runImport(t, eng, MediaOptions{})
		if !isCode(err, taxonomy.CodeSignature) {
			t.Fatalf("import error = %v, want %s: the medium's own key must not verify anything",
				err, taxonomy.CodeSignature)
		}
		if w := dest.writes(); len(w) != 0 {
			t.Errorf("the destination received %d writes although no configured root verified: %v", len(w), w)
		}
		if item := itemByName(t, task, "media/alpine@3.22.1"); item.Status != tasks.StatusFailed {
			t.Errorf("recipe item = %s, want failed", item.Status)
		}
	})

	t.Run("the same key as a CONFIGURED root does", func(t *testing.T) {
		dest := newDestRegistry(t)
		eng, _ := destinationFor(t, m, dest, "zone-alpha", signer)
		if _, err := runImport(t, eng, MediaOptions{}); err != nil {
			t.Fatalf("import: %v", err)
		}
		if tags := destTags(t, dest, cookbookRepoOf("alpine")); !containsString(tags, "3.22.1") {
			t.Errorf("the zone cookbook holds %v, want the recipe", tags)
		}
	})

	// The planted key files are extraneous content: reported, never
	// pushed (FR-054). The one under meta/ is inside coverage and shows
	// up as an uncovered file; the one under _tobby/ is outside coverage
	// entirely and is not the manifest's business at all.
	eng, _ := destinationFor(t, m, nil, "zone-alpha", signer)
	rep, err := eng.VerifyMedia(context.Background(), discardLogger(), MediaOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var reportedPlant bool
	for _, f := range rep.Findings {
		if f.Path == "meta/trust/signer.pub" && f.Code == taxonomy.CodeMediaUncovered {
			reportedPlant = true
		}
		if strings.HasPrefix(f.Path, "_tobby/") {
			t.Errorf("finding about %s: everything under _tobby/ is outside coverage by construction", f.Path)
		}
	}
	if !reportedPlant {
		t.Errorf("the planted key under meta/ is not reported as extraneous: findings = %+v", rep.Findings)
	}
}

// TestMediaImportCarriesBothSignatureLayouts is B-015 checked one hop
// further down than the bug was found.
//
// A signature verified on the zone that produced the medium and then
// disappeared one hop below, because the copy knew only the classic
// attached tag. Here the two layouts travel on the SAME medium: whatever
// the destination ends up holding, it must be verifiable with cosign
// there (FR-034 acceptance), which means both the ".sig" tag of the one
// and the referring artifact plus its fallback index of the other.
func TestMediaImportCarriesBothSignatureLayouts(t *testing.T) {
	ctx := context.Background()
	dest := newDestRegistry(t)
	kp := newKeyPair(t)
	m := seedMedium(t, kp,
		mediumRecipe{name: "alpine", version: "3.22.1"},
		mediumRecipe{name: "nginx", version: "1.25.0", bundleSig: true},
	)
	eng, _ := destinationFor(t, m, dest, "zone-alpha", kp)
	if _, err := runImport(t, eng, MediaOptions{}); err != nil {
		t.Fatalf("import: %v", err)
	}

	for _, c := range []struct {
		recipe string
		bundle bool
	}{
		{recipe: "alpine@3.22.1"},
		{recipe: "nginx@1.25.0", bundle: true},
	} {
		name, _, _ := strings.Cut(c.recipe, "@")
		repo := cookbookRepoOf(name)
		dgst := m.recipeDigest[c.recipe]
		tags := destTags(t, dest, repo)

		if c.bundle {
			// The bundle layout is reachable only through the OCI
			// referrers fallback tag: the embedded registry serves no
			// Referrers API, so without it the artifact exists at the
			// destination and nobody can find it.
			fallback := "sha256-" + strings.TrimPrefix(dgst, "sha256:")
			if !containsString(tags, fallback) {
				t.Errorf("%s at the destination has tags %v, want the referrers fallback %s (§12.2, B-015)",
					repo, tags, fallback)
			}
			raw, _, _, err := dest.st.RawManifest(ctx, repo, fallback)
			if err != nil {
				t.Fatalf("%s: the fallback index did not cross: %v", repo, err)
			}
			if !strings.Contains(string(raw), "sha256:") {
				t.Errorf("%s: the fallback index lists nothing", repo)
			}
			continue
		}
		if want := SignatureTag(dgst); !containsString(tags, want) {
			t.Errorf("%s at the destination has tags %v, want the attached signature %s (§12.2)",
				repo, tags, want)
		}
	}
}

// TestMediaImportRefusesAMediumAddressedElsewhere is the zone guard of
// FR-054 and its audited administrator waiver: blocking by default,
// overridable deliberately, and never silently.
func TestMediaImportRefusesAMediumAddressedElsewhere(t *testing.T) {
	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})

	dest := newDestRegistry(t)
	eng, _ := destinationFor(t, m, dest, "zone-beta", kp)
	if _, err := runImport(t, eng, MediaOptions{}); !isCode(err, taxonomy.CodeMediaZoneMismatch) {
		t.Fatalf("import into the wrong zone = %v, want %s", err, taxonomy.CodeMediaZoneMismatch)
	}
	if w := dest.writes(); len(w) != 0 {
		t.Errorf("a medium addressed to another zone pushed %d writes: %v", len(w), w)
	}

	// The waiver is applied AND the block stays in the report, marked
	// overridden: the report is also the evidence of what was waved
	// through (FR-094).
	dest2 := newDestRegistry(t)
	eng2, _ := destinationFor(t, m, dest2, "zone-beta", kp)
	var rep *media.Report
	if _, err := runImport(t, eng2, MediaOptions{
		AllowZoneMismatch: true,
		Report:            func(r *media.Report) { rep = r },
	}); err != nil {
		t.Fatalf("waived import: %v", err)
	}
	if len(dest2.writes()) == 0 {
		t.Error("the waiver did not let the import through")
	}
	if rep == nil || len(rep.Blocks) != 1 || !rep.Blocks[0].Overridden {
		t.Errorf("report blocks = %+v, want one block recorded as overridden", rep.Blocks)
	}
}

// TestMediaImportRefusesAMediumOlderThanTheLastImport is R-28: the
// anti-accident guard against re-importing last month's medium, and the
// rule that the register advances only on a completed import.
func TestMediaImportRefusesAMediumOlderThanTheLastImport(t *testing.T) {
	kp := newKeyPair(t)
	old := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})
	recent := seedMedium(t, kp, mediumRecipe{name: "nginx", version: "1.25.0"})

	// Both media were produced within the same instant of wall-clock
	// time; the guard compares resolution timestamps, so the older one is
	// dated back explicitly rather than left to a race with the clock.
	rewriteResolvedAt(t, old, recent.resolvedAt.Add(-24*time.Hour))

	dest := newDestRegistry(t)
	eng, imports := destinationFor(t, recent, dest, "zone-alpha", kp)
	if _, err := runImport(t, eng, MediaOptions{}); err != nil {
		t.Fatalf("importing the recent medium: %v", err)
	}
	rec, ok := imports.Last("zone-alpha")
	if !ok {
		t.Fatal("the completed import was not recorded (R-28)")
	}
	if rec.MediaID != recent.st.MediaID() {
		t.Errorf("recorded media id = %q, want the medium that was imported %q", rec.MediaID, recent.st.MediaID())
	}

	// The older medium, on an instance holding that record: refused, and
	// the refusal names both timestamps (they are the block's parameters).
	oldEng := New(old.st, newRemotes(t, nil), trustFor(t, nil, kp), "", "", syncCfg())
	oldEng.SetMediaImport("zone-alpha", imports)
	withDestination(t, oldEng, dest, config.Destination{}, nil)
	dest.reset()
	if _, err := runImport(t, oldEng, MediaOptions{}); !isCode(err, taxonomy.CodeMediaStale) {
		t.Fatalf("importing an older medium = %v, want %s", err, taxonomy.CodeMediaStale)
	}
	if w := dest.writes(); len(w) != 0 {
		t.Errorf("a stale medium pushed %d writes: %v", len(w), w)
	}

	// Waived deliberately — restoring an older delivery on purpose — it
	// goes through, and the register does NOT go backwards.
	if _, err := runImport(t, oldEng, MediaOptions{AllowStale: true}); err != nil {
		t.Fatalf("waived stale import: %v", err)
	}
	after, _ := imports.Last("zone-alpha")
	if after.ResolvedAt.Before(rec.ResolvedAt) {
		t.Errorf("the register rolled back to %s from %s: a deliberate restore does not erase "+
			"the fact that a newer medium was imported", after.ResolvedAt, rec.ResolvedAt)
	}
}

// TestMediaImportNeedsAZoneIdentity: an instance that does not know which
// zone it serves cannot decide anything about a medium, so it refuses
// rather than guessing (FR-052, secure by default).
func TestMediaImportNeedsAZoneIdentity(t *testing.T) {
	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})
	dest := newDestRegistry(t)
	eng, _ := destinationFor(t, m, dest, "", kp)
	if _, err := runImport(t, eng, MediaOptions{}); !isCode(err, taxonomy.CodeConfigInvalid) {
		t.Fatalf("import without a zone = %v, want %s", err, taxonomy.CodeConfigInvalid)
	}
	if w := dest.writes(); len(w) != 0 {
		t.Errorf("an instance with no zone identity pushed %d writes: %v", len(w), w)
	}
}
