// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// B-063: what a verification vouches for is the reach of its pushable
// recipes, and nothing else.

// TestTheServedSetIsTheReachOfThePushableRecipes: the set holds the recipe
// artifact, its signature, the ingredient, the ingredient's own signature
// (B-062: a consumer in the zone verifies an upstream-signed image), their
// blobs and their verified tags — and none of a blocked recipe's content,
// nor a tag added on the medium after the manifest was written.
func TestTheServedSetIsTheReachOfThePushableRecipes(t *testing.T) {
	f := newFixture(t)
	alpha := f.addRecipe("alpha", "1.0.0", true)
	f.sign(alpha.Ingredients[0].Repo, alpha.Ingredients[0].Digest)
	beta := f.addRecipe("beta", "1.0.0", false) // unsigned: blocked
	f.write(time.Now().UTC())
	smuggled := f.putImage(alpha.Ingredients[0].Repo, "9.9.9", []byte("smuggled after the manifest"))

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPartial {
		t.Fatalf("verdict = %q, want partial", rep.Verdict)
	}
	s := rep.Served
	if s.Empty() || !strings.HasPrefix(s.Digest, "sha256:") {
		t.Fatalf("no sealed served set: %+v", s)
	}
	ing := alpha.Ingredients[0]
	for _, c := range []struct {
		what string
		ok   bool
	}{
		{"the recipe artifact", s.Manifest(alpha.ArtifactRepo, alpha.Digest)},
		{"the ingredient", s.Manifest(ing.Repo, ing.Digest)},
		{"the ingredient's layer", s.Blob(ing.Repo, f.layerDigestOf(ing.Repo))},
		{"the recipe's signature tag", hasTag(s, alpha.ArtifactRepo, "sha256-"+alpha.Digest[7:]+".sig")},
		{"the ingredient's signature tag", hasTag(s, ing.Repo, "sha256-"+ing.Digest[7:]+".sig")},
		{"the ingredient's verified tag", hasTag(s, ing.Repo, "1.0")},
	} {
		if !c.ok {
			t.Errorf("the served set misses %s", c.what)
		}
	}
	if d, _ := s.Tag(ing.Repo, "1.0"); d != ing.Digest {
		t.Errorf("tag 1.0 resolves to %q, want the pinned %s", d, ing.Digest)
	}
	for _, c := range []struct {
		what string
		in   bool
	}{
		{"the blocked recipe's artifact", s.Manifest(beta.ArtifactRepo, beta.Digest)},
		{"the blocked recipe's ingredient", s.Manifest(beta.Ingredients[0].Repo, beta.Ingredients[0].Digest)},
		{"the smuggled manifest", s.Manifest(ing.Repo, smuggled)},
		{"the smuggled tag", hasTag(s, ing.Repo, "9.9.9")},
	} {
		if c.in {
			t.Errorf("the served set includes %s", c.what)
		}
	}

	// The report carries a summary, never the listing.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Served map[string]any `json:"served"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Served["digest"] != s.Digest || doc.Served["manifests"] == nil || len(doc.Served) != 5 {
		t.Errorf("the report does not carry the served set's summary: %v", doc.Served)
	}
}

// TestAMediumWithoutRecipeVouchesForNothing: zero blocked out of zero
// reads "pushable" — there is nothing to push — but the served set is
// empty, so a guarded destination serves nothing (it used to serve the
// whole store).
func TestAMediumWithoutRecipeVouchesForNothing(t *testing.T) {
	f := newFixture(t)
	f.putImage("docker.io/library/loose", "1.0", []byte("no recipe reaches this"))
	if err := f.st.PutRecipeRecord(&storeRecordFixture); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteRecipeRecord(storeRecordFixture.Name, storeRecordFixture.Version); err != nil {
		t.Fatal(err)
	}
	f.write(time.Now().UTC())

	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPushable || len(rep.Blocks) != 0 {
		t.Fatalf("verdict = %q, blocks %+v: want pushable, nothing to push", rep.Verdict, rep.Blocks)
	}
	if !rep.Served.Empty() {
		t.Error("a medium without recipe vouches for content")
	}
}

// storeRecordFixture is written then deleted, leaving the empty recipe
// graph a mirror synchronization leaves behind.
var storeRecordFixture = store.RecipeRecord{Name: "fixture", Version: "0.0.0"}

func hasTag(s *media.Served, repo, tag string) bool {
	_, ok := s.Tag(repo, tag)
	return ok
}
