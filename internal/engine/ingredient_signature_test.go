// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Ingredient signatures (FR-033 as amended for B-062, RECIPE-SPEC §12.2).
//
// The authenticity of a delivery comes from the recipe's signature, which
// pins every ingredient's digest. An ingredient's OWN signature — an
// upstream publisher's, typically — is a consumer policy decision: by
// default it is copied along, verified for information and recorded, and
// never blocks anything; trust.requireIngredientSignatures makes it a
// requirement, applied identically by every path. These tests use real
// signatures, made with real keys, verified by the real verifier: the
// defect they lock (an image signed upstream with a key the zone does not
// know blocked its whole recipe at import) lived precisely in a path a
// mock would have skipped.

const wordpressRID = "wordpress@6.8.2"

// requiringTrust is trustFor with trust.requireIngredientSignatures on.
func requiringTrust(t *testing.T, scopes []config.TrustScope, kps ...*sigtest.KeyPair) *TrustPolicy {
	t.Helper()
	cfg := config.Trust{Scopes: scopes, RequireIngredientSignatures: true}
	for i, kp := range kps {
		pem, err := kp.PublicPEM()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Roots = append(cfg.Roots, config.TrustRoot{Name: rootName(i), Key: string(pem)})
	}
	tp, err := LoadTrust(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

// fingerprintOf is a key pair's fingerprint as the verifier reports it.
func fingerprintOf(t *testing.T, kp *sigtest.KeyPair) string {
	t.Helper()
	fp, err := kp.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// wantSignature asserts one recorded inspection.
func wantSignature(t *testing.T, where string, got *sigverify.Inspection, state sigverify.State, fingerprint string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: no signature verdict recorded", where)
		return
	}
	if got.State != state {
		t.Errorf("%s: signature = %s, want %s", where, got, state)
	}
	switch state {
	case sigverify.StateVerified:
		if got.Fingerprint != fingerprint {
			t.Errorf("%s: verified by %q, want %q", where, got.Fingerprint, fingerprint)
		}
	case sigverify.StateUnverifiable:
		if fingerprint != "" && !strings.Contains(strings.Join(got.Tried, ","), fingerprint) {
			t.Errorf("%s: keys tried %v do not include %s", where, got.Tried, fingerprint)
		}
	}
}

// fetchResolution finds the fetch row of one ingredient (no destination).
func fetchResolution(t *testing.T, task *tasks.Task, recipe, ingredient string) *tasks.Resolution {
	t.Helper()
	for i := range task.Resolutions {
		r := &task.Resolutions[i]
		if r.Recipe == recipe && r.Ingredient == ingredient && r.Destination == "" {
			return r
		}
	}
	t.Fatalf("no fetch resolution for %s/%s (rows: %+v)", recipe, ingredient, task.Resolutions)
	return nil
}

// TestIngredientSignedByAForeignKeyIsPromoted is the passthrough half of
// B-062: an ingredient carrying a signature no zone root verifies is
// fetched, pushed, its signature travels with it, and what its signature
// turned out to be is on the item, the report row and the log.
func TestIngredientSignedByAForeignKeyIsPromoted(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)
	// The model is signed upstream by its publisher, with a key this zone
	// has never heard of — registry.rancher.com's mirrored-pause, in
	// miniature. The chart is signed by the zone's own root, the image
	// not at all: the three answers in one run.
	signManifest(t, env.src.st, "models/embedding", env.modelDig, newKeyPair(t))
	signManifest(t, env.src.st, "charts/wordpress", env.chartDig, env.kp)
	root := fingerprintOf(t, env.kp)

	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, name := range []string{"app", "chart", "model", "recipe"} {
		it := itemByName(t, task, pushItemPrefix+wordpressRID+"/"+name)
		if it.Status != tasks.StatusDone {
			t.Fatalf("push item %s = %s (error %+v), want done: a foreign ingredient signature must not block",
				name, it.Status, it.Error)
		}
	}
	// §12.2: the signatures crossed with their subjects, unchanged.
	if sig := destDigest(t, dest, env.hostRepo+"/models/embedding", SignatureTag(env.modelDig)); sig == "" {
		t.Error("the ingredient's signature did not travel to the destination")
	}
	if sig := destDigest(t, dest, env.hostRepo+"/charts/wordpress", SignatureTag(env.chartDig)); sig == "" {
		t.Error("the ingredient's signature did not travel to the destination")
	}

	for _, tc := range []struct {
		ing   string
		state sigverify.State
		fp    string
	}{
		{"app", sigverify.StateNone, ""},
		{"chart", sigverify.StateVerified, root},
		{"model", sigverify.StateUnverifiable, root},
	} {
		wantSignature(t, "fetch item "+tc.ing, itemByName(t, task, wordpressRID+"/"+tc.ing).Signature, tc.state, tc.fp)
		wantSignature(t, "push item "+tc.ing, itemByName(t, task, pushItemPrefix+wordpressRID+"/"+tc.ing).Signature, tc.state, tc.fp)
		wantSignature(t, "fetch row "+tc.ing, fetchResolution(t, task, wordpressRID, tc.ing).Signature, tc.state, tc.fp)
		wantSignature(t, "push row "+tc.ing, destResolution(t, task, wordpressRID, tc.ing).Signature, tc.state, tc.fp)
	}

	// A second cycle, everything up to date: the verdict is still taken
	// and recorded, never skipped because nothing moved.
	task, err = runSync(t, env.eng)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	wantSignature(t, "second cycle push item model",
		itemByName(t, task, pushItemPrefix+wordpressRID+"/model").Signature, sigverify.StateUnverifiable, root)
}

// TestMediaVerifyAndImportAgreeOnAForeignIngredientSignature is the
// destination half of B-062: `media verify` called the delivery pushable,
// so the import must push it — the two judge on one rule.
func TestMediaVerifyAndImportAgreeOnAForeignIngredientSignature(t *testing.T) {
	kp := newKeyPair(t)
	m := seedMedium(t, kp, mediumRecipe{name: "pause", version: "3.10.0", ingredientKey: newKeyPair(t)})
	dest := newDestRegistry(t)
	eng, _ := destinationFor(t, m, dest, servedZone, kp)
	root := fingerprintOf(t, kp)

	rep, err := eng.VerifyMedia(context.Background(), discardLogger(), MediaOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Verdict != media.VerdictPushable {
		t.Fatalf("verify verdict = %s (recipes %+v), want pushable", rep.Verdict, rep.Recipes)
	}
	if ings := rep.Recipes[0].Ingredients; len(ings) != 1 {
		t.Fatalf("report ingredients = %+v, want the one ingredient", ings)
	} else {
		in := ings[0].Signature
		wantSignature(t, "report", &in, sigverify.StateUnverifiable, root)
		if ings[0].Required {
			t.Error("the report says the signature is required, on an instance that does not require it")
		}
		if ings[0].Repo != "docker.io/library/pause" {
			t.Errorf("report decided on %q, want the canonical nominal repository", ings[0].Repo)
		}
	}

	task, err := runImport(t, eng, MediaOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("media verify said pushable, the import failed: %+v (items %s)", agg, itemNames(task))
	}
	it := itemByName(t, task, pushItemPrefix+"pause@3.10.0/app")
	if it.Status != tasks.StatusDone {
		t.Fatalf("push item app = %s (error %+v), want done", it.Status, it.Error)
	}
	wantSignature(t, "push item", it.Signature, sigverify.StateUnverifiable, root)
	if sig := destDigest(t, dest, "docker.io/library/pause", SignatureTag(m.ingredientDigest["pause@3.10.0"])); sig == "" {
		t.Error("the ingredient's signature did not travel off the medium")
	}
}

// TestRequiredIngredientSignatures is the opt-in on the synchronization:
// an ingredient that is unsigned, or signed by a key the zone does not
// know, withholds its recipe whole — not stored, not recorded, not pushed
// — and the refusal names the ingredient and the keys tried. Inside an
// allowUnsigned scope, the requirement does not apply.
func TestRequiredIngredientSignatures(t *testing.T) {
	for _, tc := range []struct {
		name string
		// signApp signs the app ingredient (nil: unsigned).
		signApp func(env *happyEnv) *sigtest.KeyPair
		scopes  func(env *happyEnv) []config.TrustScope
		blocked bool
	}{
		{name: "unsigned", signApp: func(*happyEnv) *sigtest.KeyPair { return nil }, blocked: true},
		{name: "foreign", signApp: func(*happyEnv) *sigtest.KeyPair { return newKeyPairFor() }, blocked: true},
		{name: "trusted", signApp: func(env *happyEnv) *sigtest.KeyPair { return env.kp }},
		{
			name:    "unsigned_inside_allow_unsigned_scope",
			signApp: func(*happyEnv) *sigtest.KeyPair { return nil },
			scopes: func(env *happyEnv) []config.TrustScope {
				return []config.TrustScope{{
					Name: "lab-images", AllowUnsigned: true,
					Repositories: []string{env.hostRepo + "/ingredients/**"},
				}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newHappyEnv(t)
			var scopes []config.TrustScope
			if tc.scopes != nil {
				scopes = tc.scopes(env)
			}
			env.eng.trust = requiringTrust(t, scopes, env.kp)
			dest := newDestRegistry(t)
			withDestination(t, env.eng, dest, config.Destination{}, nil)
			signManifest(t, env.src.st, "charts/wordpress", env.chartDig, env.kp)
			signManifest(t, env.src.st, "models/embedding", env.modelDig, env.kp)
			if kp := tc.signApp(env); kp != nil {
				signManifest(t, env.src.st, "ingredients/wordpress", env.idx.digest, kp)
			}

			task, err := runSync(t, env.eng)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !tc.blocked {
				if agg := task.Aggregate(); agg.Failed != 0 {
					t.Fatalf("aggregates = %+v (items %s), want nothing failed", agg, itemNames(task))
				}
				if it := itemByName(t, task, pushItemPrefix+wordpressRID+"/recipe"); it.Status != tasks.StatusDone {
					t.Fatalf("push item recipe = %s (%+v), want done", it.Status, it.Error)
				}
				return
			}

			for _, name := range []string{wordpressRID + "/app", wordpressRID + "/recipe",
				pushItemPrefix + wordpressRID + "/app", pushItemPrefix + wordpressRID + "/recipe"} {
				it := itemByName(t, task, name)
				if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
					t.Fatalf("item %s = %s / %+v, want failed with %s", name, it.Status, it.Error, taxonomy.CodeSignature)
				}
				subject, _ := it.Error.Params["recipe"].(string)
				if !strings.Contains(subject, "ingredient app of "+wordpressRID) || !strings.Contains(subject, env.idx.digest) {
					t.Errorf("item %s: the refusal names %q, want the ingredient, its recipe and its digest", name, subject)
				}
			}
			fp, _ := itemByName(t, task, wordpressRID+"/recipe").Error.Params["fingerprints"].(string)
			if tc.name == "foreign" && !strings.Contains(fp, fingerprintOf(t, env.kp)) {
				t.Errorf("fingerprints = %q, want the keys tried", fp)
			}
			if tc.name == "unsigned" && fp != "no signature artifact found" {
				t.Errorf("fingerprints = %q, want the absence stated", fp)
			}
			// R-19: nothing of the recipe crossed, and nothing of it is
			// recorded for a medium to deliver.
			if writes := dest.writes(); len(writes) != 0 {
				t.Errorf("a withheld recipe wrote to the destination: %v", writes)
			}
			recs, err := env.dst.RecipeRecords()
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 0 {
				t.Errorf("a withheld recipe was recorded in the recipe graph: %+v", recs)
			}
		})
	}
}

// newKeyPairFor is a key pair generated outside a test's helper chain.
func newKeyPairFor() *sigtest.KeyPair {
	kp, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
	if err != nil {
		panic(err)
	}
	return kp
}

// TestPromotionWithholdsTheRecipeOnARequiredIngredientSignature is the
// push-side application of the requirement, on its own: content admitted
// by a fetch that did not require ingredient signatures meets an instance
// that does, at promotion. Nothing of the recipe crosses — not even the
// ingredients whose signatures verify (RECIPE-SPEC §12.3: the affected
// item is the recipe).
func TestPromotionWithholdsTheRecipeOnARequiredIngredientSignature(t *testing.T) {
	env := newHappyEnv(t)
	signManifest(t, env.src.st, "ingredients/wordpress", env.idx.digest, env.kp)
	signManifest(t, env.src.st, "charts/wordpress", env.chartDig, env.kp)
	signManifest(t, env.src.st, "models/embedding", env.modelDig, newKeyPair(t))
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	recs, err := env.dst.RecipeRecords()
	if err != nil || len(recs) != 1 {
		t.Fatalf("recipe records = %+v, %v", recs, err)
	}
	f, err := env.eng.recipeOnMedium(context.Background(), &recs[0])
	if err != nil {
		t.Fatalf("reading the stored recipe: %v", err)
	}

	env.eng.trust = requiringTrust(t, nil, env.kp)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)
	task := &tasks.Task{ID: "tsk_push", RunID: "run_push", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if env.eng.promoteRecipe(context.Background(), newTaskSink(task, func() {}), discardLogger(), f, wordpressRID, env.eng.ownContent()) {
		t.Fatal("the recipe landed although an ingredient signature is required and does not verify")
	}
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("the destination was written to: %v", writes)
	}
	for i := range task.Items {
		switch task.Items[i].Name {
		case pushItemPrefix + wordpressRID + "/app", pushItemPrefix + wordpressRID + "/chart":
			t.Errorf("push item %s exists (%s): nothing of a withheld recipe is attempted",
				task.Items[i].Name, task.Items[i].Status)
		}
	}
	for _, name := range []string{"model", "recipe"} {
		it := itemByName(t, task, pushItemPrefix+wordpressRID+"/"+name)
		if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
			t.Fatalf("push item %s = %s / %+v, want failed with %s", name, it.Status, it.Error, taxonomy.CodeSignature)
		}
	}
	wantSignature(t, "push item model", itemByName(t, task, pushItemPrefix+wordpressRID+"/model").Signature,
		sigverify.StateUnverifiable, fingerprintOf(t, env.kp))
}

// TestMediaVerifyAndImportAgreeUnderRequiredIngredientSignatures is the
// opt-in on the destination side: `media verify` blocks a delivery whose
// ingredient signature does not verify — so an operator reading
// "pushable" can rely on it — and the import pushes nothing of it, while
// the sibling delivery whose ingredient is signed by the zone's root goes
// through (R-19).
func TestMediaVerifyAndImportAgreeUnderRequiredIngredientSignatures(t *testing.T) {
	kp := newKeyPair(t)
	m := seedMedium(t, kp,
		mediumRecipe{name: "pause", version: "3.10.0", ingredientKey: newKeyPair(t)},
		mediumRecipe{name: "busybox", version: "1.37.0"},
		mediumRecipe{name: "alpine", version: "3.22.1", ingredientKey: kp},
	)
	dest := newDestRegistry(t)
	eng, _ := destinationFor(t, m, dest, servedZone, kp)
	eng.trust = requiringTrust(t, nil, kp)

	rep, err := eng.VerifyMedia(context.Background(), discardLogger(), MediaOptions{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.Verdict != media.VerdictPartial {
		t.Fatalf("verify verdict = %s, want partial", rep.Verdict)
	}
	verdicts := map[string]*media.RecipeVerdict{}
	for i := range rep.Recipes {
		verdicts[rep.Recipes[i].Name] = &rep.Recipes[i]
	}
	for _, blocked := range []string{"pause", "busybox"} {
		v := verdicts[blocked]
		if v.Pushable || v.Reason == nil || v.Reason.Code != taxonomy.CodeSignature {
			t.Fatalf("%s: pushable=%v reason=%+v, want blocked with %s", blocked, v.Pushable, v.Reason, taxonomy.CodeSignature)
		}
		if !strings.Contains(v.Reason.Params["recipe"], "ingredient app of "+blocked+"@") {
			t.Errorf("%s: the refusal names %q, want the ingredient", blocked, v.Reason.Params["recipe"])
		}
		if len(v.Ingredients) != 1 || !v.Ingredients[0].Required {
			t.Errorf("%s: report ingredients = %+v, want the requirement stated", blocked, v.Ingredients)
		}
	}
	if v := verdicts["alpine"]; !v.Pushable {
		t.Fatalf("alpine: blocked (%+v), want pushable: its ingredient is signed by the zone's root", v.Reason)
	} else {
		in := v.Ingredients[0].Signature
		wantSignature(t, "alpine report", &in, sigverify.StateVerified, fingerprintOf(t, kp))
	}

	task, err := runImport(t, eng, MediaOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	for _, blocked := range []string{"pause@3.10.0", "busybox@1.37.0"} {
		it := itemByName(t, task, mediaItemPrefix+blocked)
		if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
			t.Errorf("media item %s = %s / %+v, want failed with %s", blocked, it.Status, it.Error, taxonomy.CodeSignature)
		}
	}
	for _, w := range dest.writes() {
		if strings.Contains(w, "library/pause") || strings.Contains(w, "library/busybox") ||
			strings.Contains(w, "cookbook/pause") || strings.Contains(w, "cookbook/busybox") {
			t.Errorf("a blocked delivery reached the destination: %s", w)
		}
	}
	if it := itemByName(t, task, pushItemPrefix+"alpine@3.22.1/recipe"); it.Status != tasks.StatusDone {
		t.Errorf("alpine push = %s (%+v), want done", it.Status, it.Error)
	}
}
