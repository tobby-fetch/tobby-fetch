// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Ingredient signatures on the medium (FR-033 as amended for B-062): the
// verification reports what each ingredient's own signature is, blocks
// nothing on it by default, and applies trust.requireIngredientSignatures
// exactly as the import's promotion does.

// ingredientMedium carries three deliveries whose ingredient is signed by
// the destination's root, by a foreign key, and not at all.
func ingredientMedium(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	foreign, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	trusted := f.addRecipe("trusted", "1.0.0", true)
	f.sign(trusted.Ingredients[0].Repo, trusted.Ingredients[0].Digest)
	upstream := f.addRecipe("upstream", "1.0.0", true)
	own := f.kp
	f.kp = foreign
	f.sign(upstream.Ingredients[0].Repo, upstream.Ingredients[0].Digest)
	f.kp = own
	f.addRecipe("bare", "1.0.0", true)
	f.write(time.Now().UTC())
	return f
}

func TestIngredientSignaturesAreRecordedAndNeverBlockByDefault(t *testing.T) {
	f := ingredientMedium(t)
	rep := f.verify(media.VerifyOptions{Zone: zoneA})
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verdict = %q, want pushable: an ingredient signature blocks nothing by default", rep.Verdict)
	}
	root := f.keys.Fingerprints()[0]
	for name, want := range map[string]sigverify.State{
		"trusted": sigverify.StateVerified, "upstream": sigverify.StateUnverifiable, "bare": sigverify.StateNone,
	} {
		v := verdictOf(t, rep, name)
		if len(v.Ingredients) != 1 {
			t.Fatalf("%s: report ingredients = %+v, want one", name, v.Ingredients)
		}
		got := v.Ingredients[0]
		if got.Signature.State != want || got.Required {
			t.Errorf("%s: signature %s (required %v), want %s, not required", name, got.Signature, got.Required, want)
		}
		if want == sigverify.StateVerified && got.Signature.Fingerprint != root {
			t.Errorf("%s: verified by %q, want %q", name, got.Signature.Fingerprint, root)
		}
		if want == sigverify.StateUnverifiable && strings.Join(got.Signature.Tried, ",") != root {
			t.Errorf("%s: keys tried %v, want %s", name, got.Signature.Tried, root)
		}
	}
}

func TestRequiredIngredientSignaturesBlockTheDeliveryWhole(t *testing.T) {
	f := ingredientMedium(t)
	rep := f.verify(media.VerifyOptions{Zone: zoneA, Trust: trustFor{keys: f.keys, requireIngredients: true}})
	if rep.Verdict != media.VerdictPartial {
		t.Fatalf("verdict = %q, want partial (R-19)", rep.Verdict)
	}
	if v := verdictOf(t, rep, "trusted"); !v.Pushable || !v.Ingredients[0].Required {
		t.Errorf("trusted: pushable=%v ingredients=%+v, want pushable under the requirement", v.Pushable, v.Ingredients)
	}
	for name, fingerprints := range map[string]string{
		"upstream": f.keys.Fingerprints()[0], "bare": "no signature artifact found",
	} {
		v := verdictOf(t, rep, name)
		if v.Pushable || v.Reason == nil || v.Reason.Code != taxonomy.CodeSignature {
			t.Fatalf("%s: pushable=%v reason=%+v, want blocked with %s", name, v.Pushable, v.Reason, taxonomy.CodeSignature)
		}
		if subject := v.Reason.Params["recipe"]; !strings.HasPrefix(subject, "ingredient "+name+" of "+name+"@1.0.0 (docker.io/library/"+name+"@") {
			t.Errorf("%s: the refusal names %q, want the ingredient, its recipe and its digest", name, subject)
		}
		if got := v.Reason.Params["fingerprints"]; got != fingerprints {
			t.Errorf("%s: fingerprints = %q, want %q", name, got, fingerprints)
		}
	}
}

func TestAllowUnsignedScopeLiftsTheIngredientRequirement(t *testing.T) {
	f := ingredientMedium(t)
	rep := f.verify(media.VerifyOptions{Zone: zoneA, Trust: trustFor{
		keys: f.keys, requireIngredients: true, allowUnsigned: true, scope: "lab",
	}})
	if v := verdictOf(t, rep, "bare"); !v.Pushable || v.Ingredients[0].Required {
		t.Errorf("bare: pushable=%v reason=%+v ingredients=%+v, want pushable: the scope admits unsigned content",
			v.Pushable, v.Reason, v.Ingredients)
	}
	// The scope admits the unsigned; it does not make a foreign signature
	// verify — but it lifts the requirement, so that one passes too, and
	// says what it is.
	if v := verdictOf(t, rep, "upstream"); !v.Pushable || v.Ingredients[0].Signature.State != sigverify.StateUnverifiable {
		t.Errorf("upstream: pushable=%v ingredients=%+v", v.Pushable, v.Ingredients)
	}
}
