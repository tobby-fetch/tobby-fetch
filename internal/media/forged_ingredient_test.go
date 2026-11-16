// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"os"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The ingredient list of a delivery is the one its SIGNED recipe document
// pins, never the medium's own, unsigned bookkeeping (B-066, second
// adversarial review of milestone 7). Whoever controls the medium can
// rewrite meta/recipes.json and re-inventory, so every hash agrees; before
// the fix, the verification then called a properly signed recipe pushable
// with a foreign image in the served set, and an open gate handed it out.

// forge rewrites the recipe graph entry of rec after the fact and writes
// a fresh, self-consistent manifest over it.
func (f *fixture) forge(rec *store.RecipeRecord) {
	f.t.Helper()
	if err := f.st.PutRecipeRecord(rec); err != nil {
		f.t.Fatal(err)
	}
	f.write(time.Now().UTC())
}

// assertForgeryBlocked checks a forged delivery is refused whole with
// TBY-MED-016, and that nothing of what the forgery named is served.
func assertForgeryBlocked(t *testing.T, rep *media.Report, recipe, repo, dgst string) {
	t.Helper()
	v := verdictOf(t, rep, recipe)
	if v.Pushable {
		t.Fatalf("%s is pushable although its recipe graph names %s@%s, which its signed document does not pin", recipe, repo, dgst)
	}
	if v.Reason == nil || v.Reason.Code != taxonomy.CodeMediaIngredientsDisagree {
		t.Errorf("%s reason = %+v, want %s", recipe, v.Reason, taxonomy.CodeMediaIngredientsDisagree)
	}
	if rep.Served != nil && rep.Served.Manifest(repo, dgst) {
		t.Errorf("%s@%s is in the served set", repo, dgst)
	}
}

func TestForgedIngredientIsBlocked(t *testing.T) {
	f := newFixture(t)
	rec := f.addRecipe("alpha", "1.0.0", true)

	const evilRepo = "docker.io/library/alpha-backdoor"
	evil := f.putImage(evilRepo, "latest", []byte("attacker payload"))
	rec.Ingredients = append(rec.Ingredients, storeIngredient("backdoor", evilRepo, "latest", evil))
	f.forge(&rec)

	assertForgeryBlocked(t, f.verify(media.VerifyOptions{Zone: zoneA}), "alpha", evilRepo, evil)
}

func TestForgedIngredientDigestIsBlocked(t *testing.T) {
	f := newFixture(t)
	rec := f.addRecipe("alpha", "1.0.0", true)

	repo := rec.Ingredients[0].Repo
	evil := f.putImage(repo, "1.0", []byte("attacker payload, same repository and tag"))
	rec.Ingredients[0].Digest = evil
	f.forge(&rec)

	assertForgeryBlocked(t, f.verify(media.VerifyOptions{Zone: zoneA}), "alpha", repo, evil)
}

func TestForgedIngredientLocationIsBlocked(t *testing.T) {
	f := newFixture(t)
	rec := f.addRecipe("alpha", "1.0.0", true)

	// The same pinned bytes, copied under a repository of the attacker's
	// choosing: the digest agrees, the place it is served from does not.
	const evilRepo = "docker.io/library/elsewhere"
	moved := f.putImage(evilRepo, "1.0", []byte("layer bytes of alpha"))
	if moved != rec.Ingredients[0].Digest {
		t.Fatalf("fixture: the copy is %s, want %s", moved, rec.Ingredients[0].Digest)
	}
	rec.Ingredients[0].Repo = evilRepo
	f.forge(&rec)

	assertForgeryBlocked(t, f.verify(media.VerifyOptions{Zone: zoneA}), "alpha", evilRepo, moved)
}

// TestOmittedIngredientIsWalkedFromTheSignedDocument: a graph that omits
// an ingredient the signed document pins — what a synchronization records
// when that ingredient failed to arrive — is no disagreement: the walk
// follows the document, so the ingredient is required all the same, and
// its absence blocks the recipe as missing content.
func TestOmittedIngredientIsWalkedFromTheSignedDocument(t *testing.T) {
	f := newFixture(t)
	rec := f.addRecipe("alpha", "1.0.0", true)
	omitted := rec.Ingredients[0]
	rec.Ingredients = nil
	f.forge(&rec)

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if v := verdictOf(t, rep, "alpha"); !v.Pushable {
		t.Fatalf("alpha = %+v, want pushable: its signed ingredient is on the medium", v.Reason)
	}
	if !rep.Served.Manifest(omitted.Repo, omitted.Digest) {
		t.Errorf("the ingredient the signed recipe pins is not served")
	}

	// The same omission with the content gone: missing, not altered.
	if err := os.Remove(f.path(blobPathOf(f.layerDigestOf(omitted.Repo)))); err != nil {
		t.Fatal(err)
	}
	rep = f.verify(media.VerifyOptions{Zone: zoneA})
	v := verdictOf(t, rep, "alpha")
	if v.Pushable || v.Reason == nil || v.Reason.Code != taxonomy.CodeMediaFileMissing {
		t.Errorf("alpha: pushable=%v reason=%+v, want blocked with %s", v.Pushable, v.Reason, taxonomy.CodeMediaFileMissing)
	}
}

// TestSwappedRecipeDocumentIsBlocked: the graph entry of one recipe
// pointing at the signed artifact of another is refused — the signature
// verifies, but the document is not the one the entry names (§11.3).
func TestSwappedRecipeDocumentIsBlocked(t *testing.T) {
	f := newFixture(t)
	alpha := f.addRecipe("alpha", "1.0.0", true)
	beta := f.addRecipe("beta", "1.0.0", true)
	beta.ArtifactRepo, beta.CookbookRepo, beta.Digest = alpha.ArtifactRepo, alpha.CookbookRepo, alpha.Digest
	f.forge(&beta)

	v := verdictOf(t, f.verify(media.VerifyOptions{Zone: zoneA}), "beta")
	if v.Pushable || v.Reason == nil || v.Reason.Code != taxonomy.CodeMediaIngredientsDisagree {
		t.Fatalf("beta: pushable=%v reason=%+v, want blocked with %s", v.Pushable, v.Reason, taxonomy.CodeMediaIngredientsDisagree)
	}
}

// TestRecipeArtifactThatIsNoRecipeIsBlocked: an artifact that does not
// carry a recipe document cannot say what the delivery is.
func TestRecipeArtifactThatIsNoRecipeIsBlocked(t *testing.T) {
	f := newFixture(t)
	rec := f.addRecipe("alpha", "1.0.0", true)
	rec.Digest = f.putImage(rec.ArtifactRepo, rec.ArtifactTag, []byte("not a recipe document"))
	f.sign(rec.ArtifactRepo, rec.Digest)
	f.forge(&rec)

	v := verdictOf(t, f.verify(media.VerifyOptions{Zone: zoneA}), "alpha")
	if v.Pushable || v.Reason == nil || v.Reason.Code != taxonomy.CodeMediaContentUnreadable {
		t.Fatalf("alpha: pushable=%v reason=%+v, want blocked with %s", v.Pushable, v.Reason, taxonomy.CodeMediaContentUnreadable)
	}
}
