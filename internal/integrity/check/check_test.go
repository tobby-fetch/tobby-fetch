// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package check_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check/checktest"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FR-057 verification played on a real store: built with the real
// store API, signed with real key material, and corrupted on disk the way
// a failing disk or a careless copy does. The defects have to bite on the
// layout the product actually writes, not on a hand-drawn imitation.

type fixture struct {
	*checktest.Fixture
	t    *testing.T
	root string
	st   *store.Store
	kp   *sigtest.KeyPair
	keys *sigverify.Keys
}

// trust is the instance's trust policy under test.
type trust struct {
	keys          *sigverify.Keys
	allowUnsigned bool
	scope         string
}

func (t trust) Decide(string) media.TrustDecision {
	return media.TrustDecision{Keys: t.keys, AllowUnsigned: t.allowUnsigned, Scope: t.scope}
}

func keysOf(t *testing.T, kp *sigtest.KeyPair) *sigverify.Keys { return checktest.KeysOf(t, kp) }
func newKeyPair(t *testing.T) *sigtest.KeyPair                 { return checktest.NewKeyPair(t) }
func blobPathOf(d string) string                               { return checktest.BlobPath(d) }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := checktest.New(t)
	return &fixture{Fixture: f, t: t, root: f.Root, st: f.St, kp: f.KP, keys: f.Keys}
}

func (f *fixture) putBlob(repo string, b []byte) string { return f.PutBlob(repo, b) }
func (f *fixture) putImage(repo, tag string, layer []byte) string {
	return f.PutImage(repo, tag, layer)
}
func (f *fixture) putIndex(repo, tag string, children ...string) string {
	return f.PutIndex(repo, tag, children...)
}
func (f *fixture) sign(kp *sigtest.KeyPair, repo, subject string) { f.Sign(kp, repo, subject) }
func (f *fixture) addRecipe(name string, signer *sigtest.KeyPair) (rec store.RecipeRecord, layerDigest string) {
	return f.AddRecipe(name, signer)
}

func fingerprint(t *testing.T, root string) string { return checktest.Fingerprint(t, root) }

const cookbook = checktest.Cookbook

func (f *fixture) verify(opts check.Options) *integrity.Report {
	f.t.Helper()
	if opts.Trust == nil {
		opts.Trust = trust{keys: f.keys}
	}
	rep, err := check.Verify(context.Background(), f.st, opts)
	if err != nil {
		f.t.Fatalf("verify: %v", err)
	}
	return rep
}

func codesOf(l []integrity.Defect) []taxonomy.Code {
	out := make([]taxonomy.Code, 0, len(l))
	for i := range l {
		out = append(out, l[i].Code)
	}
	return out
}

func find(l []integrity.Defect, code taxonomy.Code) *integrity.Defect {
	for i := range l {
		if l[i].Code == code {
			return &l[i]
		}
	}
	return nil
}

// TestIntactStoreVerifiesClean is the FR-057 acceptance's last sentence:
// an intact store verifies clean, and the store is byte-identical
// afterwards — the verification wrote nothing, not even a timestamp.
func TestIntactStoreVerifiesClean(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)
	f.addRecipe("beta", f.kp)
	before := fingerprint(t, f.root)

	rep := f.verify(check.Options{RunID: "run_x"})
	if rep.Verdict != integrity.VerdictClean || rep.DefectCount != 0 || rep.FindingCount != 0 {
		t.Fatalf("report = verdict %q, defects %v, findings %v; want a clean store", rep.Verdict, rep.Defects, rep.Findings)
	}
	if rep.Principal() != nil {
		t.Errorf("a clean report has a principal defect: %v", rep.Principal())
	}
	if rep.Checked.Blobs == 0 || rep.Checked.Bytes == 0 || rep.Checked.Manifests == 0 || rep.Checked.Recipes != 2 {
		t.Errorf("checked = %+v: the walk covered nothing, a clean verdict proves nothing", rep.Checked)
	}
	if rep.RunID != "run_x" || rep.StartedAt.IsZero() || rep.FinishedAt.Before(rep.StartedAt) {
		t.Errorf("the report is not timestamped and correlated: %+v", rep)
	}
	for _, r := range rep.Recipes {
		if r.Signature != integrity.SignatureVerified || !r.Complete || r.KeyFingerprint == "" || r.Assurance != nil {
			t.Errorf("recipe row %+v, want complete, verified by a named key, full assurance", r)
		}
	}
	if after := fingerprint(t, f.root); after != before {
		t.Error("the store changed during a verification: it must be read-only")
	}
}

// TestEveryDefectOfTheAcceptanceIsNamed is the FR-057 acceptance clause:
// a store holding one truncated blob, one manifest missing a referenced
// blob, one recipe whose signing key is no longer a trust root and one
// orphan blob — each defect is reported with its object, the verdict is
// "defects", and the store is untouched.
func TestEveryDefectOfTheAcceptanceIsNamed(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp) // intact: the control
	_, truncatedLayer := f.addRecipe("truncated", f.kp)
	rec, missingLayer := f.addRecipe("missing", f.kp)
	removedKey := newKeyPair(t)
	f.addRecipe("removedkey", removedKey) // signed by a key this instance does not trust
	orphan := f.putBlob("docker.io/library/orphaned", []byte("a blob nothing references"))

	// 1. a truncated blob
	truncPath := filepath.Join(f.root, filepath.FromSlash(blobPathOf(truncatedLayer)))
	info, err := os.Stat(truncPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(truncPath, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	// 2. a manifest whose referenced blob is gone
	if err := os.RemoveAll(filepath.Dir(filepath.Join(f.root, filepath.FromSlash(blobPathOf(missingLayer))))); err != nil {
		t.Fatal(err)
	}
	before := fingerprint(t, f.root)

	rep := f.verify(check.Options{})
	if rep.Verdict != integrity.VerdictDefects {
		t.Fatalf("verdict = %q, want %q (defects %v)", rep.Verdict, integrity.VerdictDefects, rep.Defects)
	}

	blob := find(rep.Defects, taxonomy.CodeIntegrityBlobDigest)
	if blob == nil || blob.Path != blobPathOf(truncatedLayer) || blob.Params["expected"] != truncatedLayer || blob.Params["actual"] == truncatedLayer {
		t.Errorf("truncated blob defect = %+v, want %s naming %s with both digests", blob, taxonomy.CodeIntegrityBlobDigest, blobPathOf(truncatedLayer))
	}
	incomplete := find(rep.Defects, taxonomy.CodeIntegrityIncomplete)
	wantRef := rec.Ingredients[0].Repo + "@" + rec.Ingredients[0].Digest
	if incomplete == nil || incomplete.Path != wantRef || !strings.Contains(incomplete.Params["missing"], missingLayer) {
		t.Errorf("incomplete manifest defect = %+v, want %s naming %s and the missing %s",
			incomplete, taxonomy.CodeIntegrityIncomplete, wantRef, missingLayer)
	}
	sig := find(rep.Defects, taxonomy.CodeSignature)
	if sig == nil || sig.Path != "removedkey@1.0.0" || sig.Params["fingerprints"] == "" {
		t.Errorf("signature defect = %+v, want %s naming removedkey@1.0.0 and the keys tried", sig, taxonomy.CodeSignature)
	}
	orphaned := find(rep.Findings, taxonomy.CodeIntegrityOrphan)
	if orphaned == nil || orphaned.Path != blobPathOf(orphan) {
		t.Errorf("orphan finding = %+v, want %s naming %s", orphaned, taxonomy.CodeIntegrityOrphan, blobPathOf(orphan))
	}
	if find(rep.Defects, taxonomy.CodeIntegrityOrphan) != nil {
		t.Error("an orphan is listed as a blocking defect: it is a finding, never a reason to fail")
	}
	// Only what is wrong is reported: the control recipe stays clean.
	for _, d := range rep.Defects {
		if strings.Contains(d.Path, "alpha") {
			t.Errorf("the intact recipe is named in a defect: %+v", d)
		}
	}
	if p := rep.Principal(); p == nil || p.Entry().Class != taxonomy.ClassVerification || p.Entry().Class.ExitCode() != taxonomy.ExitVerification {
		t.Errorf("principal = %v, want a verification-class code (exit 4)", p)
	}
	rows := map[string]integrity.Recipe{}
	for _, r := range rep.Recipes {
		rows[r.Name] = r
	}
	if rows["missing"].Complete || !rows["alpha"].Complete {
		t.Errorf("completeness rows wrong: missing=%v alpha=%v", rows["missing"].Complete, rows["alpha"].Complete)
	}
	if rows["removedkey"].Signature != integrity.SignatureRefused || rows["alpha"].Signature != integrity.SignatureVerified {
		t.Errorf("signature rows wrong: removedkey=%q alpha=%q", rows["removedkey"].Signature, rows["alpha"].Signature)
	}
	if after := fingerprint(t, f.root); after != before {
		t.Error("the verification repaired or deleted something: it must report and touch nothing")
	}
}

// TestFlippedBlobIsCaughtByItsDigest: same length, different bytes — the
// corruption a size check cannot see.
func TestFlippedBlobIsCaughtByItsDigest(t *testing.T) {
	f := newFixture(t)
	_, layer := f.addRecipe("alpha", f.kp)
	p := filepath.Join(f.root, filepath.FromSlash(blobPathOf(layer)))
	raw, err := os.ReadFile(p) //nolint:gosec // G304: the fixture's own file
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xff
	if err := os.WriteFile(p, raw, 0o600); err != nil { //nolint:gosec // G703: the fixture's own tree
		t.Fatal(err)
	}
	rep := f.verify(check.Options{})
	if d := find(rep.Defects, taxonomy.CodeIntegrityBlobDigest); d == nil || d.Path != blobPathOf(layer) {
		t.Fatalf("defects = %v, want the flipped blob named", rep.Defects)
	}
}

// TestUnreadableManifestIsReportedNotSkipped: a manifest the walk cannot
// parse is a defect of its own (TBY-INT-005), because what it references
// could not be checked — silence would read as completeness.
func TestUnreadableManifestIsReportedNotSkipped(t *testing.T) {
	f := newFixture(t)
	rec, _ := f.addRecipe("alpha", f.kp)
	// Overwrite the ingredient manifest's data with bytes that are not a
	// manifest. The blob no longer hashes to its digest either, and both
	// defects are reported: the second is the consequence of the first.
	p := filepath.Join(f.root, filepath.FromSlash(blobPathOf(rec.Ingredients[0].Digest)))
	if err := os.WriteFile(p, []byte("{ this is not a manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := f.verify(check.Options{})
	if find(rep.Defects, taxonomy.CodeIntegrityUnreadable) == nil {
		t.Errorf("defects = %v, want %s for the unparseable manifest", codesOf(rep.Defects), taxonomy.CodeIntegrityUnreadable)
	}
	if find(rep.Defects, taxonomy.CodeIntegrityBlobDigest) == nil {
		t.Errorf("defects = %v, want the blob digest defect too", codesOf(rep.Defects))
	}
}

// TestSparseIndexChildIsNotMissingContent: a platform-filtered index keeps
// its pinned digest while carrying only some of its children (FR-022). The
// walk must not call that incomplete.
func TestSparseIndexChildIsNotMissingContent(t *testing.T) {
	f := newFixture(t)
	repo := "docker.io/library/multi"
	child := f.putImage(repo, "amd64-only", []byte("the one platform that was mirrored"))
	absent := "sha256:" + strings.Repeat("ab", 32) // never fetched: filtered out
	idx := f.putIndex(repo, "1.0", child, absent)
	rec := store.RecipeRecord{
		Name: "multi", Version: "1.0.0", CookbookRepo: cookbook + "/multi", ArtifactRepo: cookbook + "/multi", ArtifactTag: "1.0.0",
		Ingredients: []store.IngredientRecord{{Name: "multi", Kind: "Image", Repo: repo, Tag: "1.0", Digest: idx}},
	}
	artifact := f.putImage(rec.ArtifactRepo, "1.0.0", []byte("recipe document"))
	rec.Digest = artifact
	f.sign(f.kp, rec.ArtifactRepo, artifact)
	rec.Verified = true
	if err := f.st.PutRecipeRecord(&rec); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetProvenance(repo, &store.Provenance{Class: store.ProvenanceRecipe}); err != nil {
		t.Fatal(err)
	}
	if rep := f.verify(check.Options{}); rep.Verdict != integrity.VerdictClean {
		t.Fatalf("a sparse index was reported as defective: %v", rep.Defects)
	}
}

// TestProtectedContentIsNeverAnOrphan pins FR-057's "nor from a protected
// root (FR-045)": content seeded through /v2/ and unit imports have no
// recipe and are not orphans; a tag a removed recipe left in a
// recipe-managed repository is; and the signature artifact of content a
// recipe holds is attached to it, not stray.
func TestProtectedContentIsNeverAnOrphan(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)

	f.putImage("docker.io/library/seeded", "1.0", []byte("pushed by a standard client")) // no ledger entry: a seed
	f.putImage("docker.io/library/imported", "2.0", []byte("a unit import"))
	if err := f.st.SetProvenance("docker.io/library/imported", &store.Provenance{Class: store.ProvenanceUnitImport}); err != nil {
		t.Fatal(err)
	}
	// A recipe-managed repository holding a tag no recipe reaches.
	f.putImage("docker.io/library/alpha", "0.9-stale", []byte("an old version a removed recipe brought"))

	rep := f.verify(check.Options{})
	if rep.Verdict != integrity.VerdictClean {
		t.Fatalf("defects = %v", rep.Defects)
	}
	var paths []string
	for _, o := range rep.Findings {
		paths = append(paths, o.Path)
	}
	if len(paths) == 0 || !contains(paths, "docker.io/library/alpha:0.9-stale") {
		t.Errorf("findings = %v, want the stale tag docker.io/library/alpha:0.9-stale", paths)
	}
	for _, p := range paths {
		for _, protected := range []string{"seeded", "imported", ".sig", "cookbook/alpha:1.0.0", "library/alpha:1.0"} {
			if strings.Contains(p, protected) && !strings.Contains(p, "0.9-stale") {
				t.Errorf("finding %q concerns protected or reachable content (%s)", p, protected)
			}
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// TestScopesAreAppliedAndAssuranceIsReReported: signatures are verified
// under THIS instance's trust configuration now (FR-033), and the
// reduced-assurance marker is re-reported (FR-037) — present while a
// declared scope admits the unsigned recipe, replaced by a defect when none
// does any more.
func TestScopesAreAppliedAndAssuranceIsReReported(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("bare", nil) // unsigned

	rep := f.verify(check.Options{Trust: trust{keys: f.keys, allowUnsigned: true, scope: "lab"}})
	row := rep.Recipes[0]
	if rep.Verdict != integrity.VerdictClean || row.Signature != integrity.SignatureUnsignedAdmitted || !row.Assurance.Reduced() || row.Assurance.Scope != "lab" || row.TrustScope != "lab" {
		t.Errorf("under an allowUnsigned scope: verdict %q, row %+v — want clean, unsigned-admitted, reduced under lab", rep.Verdict, row)
	}

	rep = f.verify(check.Options{Trust: trust{keys: f.keys}})
	sig := find(rep.Defects, taxonomy.CodeSignature)
	if rep.Verdict != integrity.VerdictDefects || sig == nil || rep.Recipes[0].Assurance != nil {
		t.Errorf("without the scope: verdict %q, defects %v — want the unsigned recipe refused (TBY-SIG-001)", rep.Verdict, rep.Defects)
	}

	// A scope that restricts the roots names itself in the refusal.
	other := newKeyPair(t)
	rep = f.verify(check.Options{Trust: trust{keys: keysOf(t, other), scope: "tenant-a"}})
	sig = find(rep.Defects, taxonomy.CodeSignature)
	if sig == nil || sig.Params["scope"] != "tenant-a" {
		t.Errorf("signature defect = %+v, want the scope tenant-a named", sig)
	}

	// No trust root at all: nothing verifies, and nothing is waved through.
	rep = f.verify(check.Options{Trust: trust{}})
	if rep.Verdict != integrity.VerdictDefects {
		t.Errorf("verdict with no trust root = %q, want defects", rep.Verdict)
	}
}

// TestRecordWithoutAnArtifactLocationIsNamed: a graph entry written before
// the artifact location existed cannot be walked, and the report says so
// instead of skipping it.
func TestRecordWithoutAnArtifactLocationIsNamed(t *testing.T) {
	f := newFixture(t)
	rec := store.RecipeRecord{Name: "old", Version: "1.0.0", CookbookRepo: cookbook + "/old", Digest: "sha256:" + strings.Repeat("0", 64)}
	if err := f.st.PutRecipeRecord(&rec); err != nil {
		t.Fatal(err)
	}
	rep := f.verify(check.Options{})
	d := find(rep.Defects, taxonomy.CodeIntegrityUnreadable)
	if d == nil || !strings.Contains(d.Params["detail"], "old@1.0.0") {
		t.Fatalf("defects = %v, want %s naming old@1.0.0", rep.Defects, taxonomy.CodeIntegrityUnreadable)
	}
}

// TestListsAreBoundedAndCountsAreExact: a store with a thousand orphans has
// one problem, not a thousand lines — the first entries are named and the
// exact count is kept.
func TestListsAreBoundedAndCountsAreExact(t *testing.T) {
	f := newFixture(t)
	const total, limit = 12, 5
	for i := range total {
		f.putBlob("docker.io/library/orphaned", []byte("orphan number "+strings.Repeat("x", i)))
	}
	rep := f.verify(check.Options{Limit: limit})
	if rep.FindingCount != total || len(rep.Findings) != limit || rep.FindingsOmitted != total-limit {
		t.Errorf("findings: count %d listed %d omitted %d, want %d / %d / %d",
			rep.FindingCount, len(rep.Findings), rep.FindingsOmitted, total, limit, total-limit)
	}
	if rep.Verdict != integrity.VerdictClean {
		t.Errorf("orphans alone made the verdict %q", rep.Verdict)
	}
	if def := f.verify(check.Options{}); len(def.Findings) != total {
		t.Errorf("the default bound listed %d of %d findings", len(def.Findings), total)
	}
}

// TestProgressAndCancellation: progress is published per stage with exact
// totals, and a cancelled context stops the walk with the context's error
// instead of a report that claims to have covered the store.
func TestProgressAndCancellation(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)

	var stages []integrity.Stage
	var last integrity.Progress
	f.verify(check.Options{Progress: func(p integrity.Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
		last = p
	}})
	if len(stages) < 3 || stages[0] != integrity.StageBlobs {
		t.Errorf("stages = %v, want blobs first and every stage reported", stages)
	}
	if last.Stage != integrity.StageOrphans {
		t.Errorf("last progress = %+v, want the orphan stage", last)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := check.Verify(ctx, f.st, check.Options{Trust: trust{keys: f.keys}}); err == nil {
		t.Error("a cancelled verification returned a report")
	}
}

// TestTheWalkHoldsTheContentLock: while a verification runs, a garbage
// collection waits — a blob swept under the walk would read as a defect
// that was never there. Content stays put until the verification is done.
func TestTheWalkHoldsTheContentLock(t *testing.T) {
	f := newFixture(t)
	f.addRecipe("alpha", f.kp)

	var started, swept atomic.Bool
	var observed atomic.Bool
	f.verify(check.Options{Progress: func(p integrity.Progress) {
		if p.Stage == integrity.StageOrphans && !started.Swap(true) {
			go func() {
				_ = f.st.Sweep(context.Background(), slog.New(slog.DiscardHandler))
				swept.Store(true)
			}()
			time.Sleep(300 * time.Millisecond)
			observed.Store(!swept.Load())
		}
	}})
	if !started.Load() {
		t.Fatal("the orphan stage never reported progress")
	}
	if !observed.Load() {
		t.Error("a sweep completed while the verification was running: the walk does not hold the content lock")
	}
	deadline := time.After(5 * time.Second)
	for !swept.Load() {
		select {
		case <-deadline:
			t.Fatal("the sweep never ran after the verification released the lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
