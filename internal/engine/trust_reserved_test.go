// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// B-057. A trust root that a scope lists is RESERVED to that scope: it
// verifies nothing outside it. Before the fix, the strict default verified
// against the union of every configured root, so an emergency key declared
// for an "emergency" scope admitted any recipe outside that scope — the
// partitioning content-trust promises existed only inside the scope.
//
// The fixture is the documented emergency path: a normal key ("a-root",
// index 0) for everyday content, an emergency key ("b-root", index 1)
// listed by the scope "emergency", which covers a repository the recipes
// below are NOT in.
const emergencyScope = "emergency"

func emergencyScopes() []config.TrustScope {
	return []config.TrustScope{{
		Name:         emergencyScope,
		Repositories: []string{"emergency.example.com/**"},
		Roots:        []string{rootName(1)},
	}}
}

// TestReservedRootDoesNotVerifyOutsideItsScope is the sync face of B-057:
// a recipe outside every scope, signed by the emergency key, is refused in
// TBY-SIG-001 — and the refusal names the root that does verify it and the
// scope that root is reserved to, so the operator knows what happened.
func TestReservedRootDoesNotVerifyOutsideItsScope(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	normal, emergency := newKeyPair(t), newKeyPair(t)
	dig := publishRecipe(t, src.st, "cookbook/app", "1.0.0", trustRecipeYAML(t, src, "app", imgDig))
	signManifest(t, src.st, "cookbook/app", dig, emergency)
	okDig := publishRecipe(t, src.st, "cookbook/ok", "1.0.0", trustRecipeYAML(t, src, "ok", imgDig))
	signManifest(t, src.st, "cookbook/ok", okDig, normal)
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "app", Version: "1.0.0"},
		{Name: "ok", Version: "1.0.0"},
	})

	dst := openStore(t)
	eng := New(dst, newRemotes(t, nil), trustFor(t, emergencyScopes(), normal, emergency), retr, "", syncCfg())
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	it := itemByName(t, task, "app@1.0.0")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
		t.Fatalf("app item = %s / %+v, want failed TBY-SIG-001: a key reserved to the scope %q verified a recipe outside it (B-057)",
			it.Status, it.Error, emergencyScope)
	}
	emergencyFP := fpOf(t, emergency)
	assertReservedNamed(t, it.Error.Params, emergencyFP)
	// The keys tried are the default set's — the emergency key is not one.
	if fps, _ := it.Error.Params["fingerprints"].(string); strings.Contains(fps, emergencyFP) ||
		!strings.Contains(fps, fpOf(t, normal)) {
		t.Errorf("keys tried = %q, want the normal key only", fps)
	}
	// The normal key still verifies outside the scope: the default set is
	// the roots no scope lists, not nothing.
	if ok := itemByName(t, task, "ok@1.0.0/recipe"); ok.Status != tasks.StatusDone {
		t.Errorf("ok item = %+v, want admitted by the unreserved root", ok.Error)
	}
	recs, err := dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Name == "app" {
			t.Errorf("the refused recipe was recorded: %+v", r)
		}
	}
}

// TestReservedRootStillVerifiesInsideItsScope: the reservation narrows
// where a root applies, it does not switch it off.
func TestReservedRootStillVerifiesInsideItsScope(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	normal, emergency := newKeyPair(t), newKeyPair(t)
	dig := publishRecipe(t, src.st, "cookbook/app", "1.0.0", trustRecipeYAML(t, src, "app", imgDig))
	signManifest(t, src.st, "cookbook/app", dig, emergency)
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{{Name: "app", Version: "1.0.0"}})
	scopes := []config.TrustScope{{
		Name:         emergencyScope,
		Repositories: []string{strings.ReplaceAll(src.addr, ":", "_") + "/cookbook/*"},
		Roots:        []string{rootName(1)},
	}}
	task, err := runSync(t, New(openStore(t), newRemotes(t, nil), trustFor(t, scopes, normal, emergency), retr, "", syncCfg()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("aggregates = %+v (items %s), want the recipe admitted inside its scope", agg, itemNames(task))
	}
}

// TestReservedRootRefusalOnPromotion is the stored-verification face of
// B-057: the re-verification before every push applies the same rule.
func TestReservedRootRefusalOnPromotion(t *testing.T) {
	env := newHappyEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	// The local copy is now signed by the emergency key only, and the
	// instance reserves that key to the emergency scope.
	normal, emergency := newKeyPair(t), newKeyPair(t)
	localCookbook := env.hostRepo + "/cookbook/wordpress"
	signManifest(t, env.dst, localCookbook, env.manDig, emergency)
	env.eng.trust = trustFor(t, emergencyScopes(), normal, emergency)

	err := env.eng.verifyStored(context.Background(), localCookbook, env.src.addr+"/cookbook/wordpress", env.manDig, "wordpress@6.8.2")
	if err == nil {
		t.Fatal("verifyStored admitted a recipe outside the emergency scope signed by the emergency key (B-057)")
	}
	te := env.eng.mapError(err, "wordpress@6.8.2")
	if te.Code() != taxonomy.CodeSignature {
		t.Fatalf("code = %s, want %s", te.Code(), taxonomy.CodeSignature)
	}
	assertReservedNamed(t, te.ParamsMap(), fpOf(t, emergency))
}

// TestDecideReservesScopedRoots locks the rule at the policy level, for
// every caller of Decide at once (sync, plan, promotion, media
// verification, integrity check).
func TestDecideReservesScopedRoots(t *testing.T) {
	normal, emergency, lab := newKeyPair(t), newKeyPair(t), newKeyPair(t)
	normalFP, emergencyFP := fpOf(t, normal), fpOf(t, emergency)
	tp := trustFor(t, append(emergencyScopes(), config.TrustScope{
		// A roots-less scope gets the default set, never another scope's
		// reserved roots: the same leak would otherwise reopen there.
		Name: "lab", Repositories: []string{"lab.example.com/**"}, AllowUnsigned: true,
	}), normal, emergency, lab)

	for _, repo := range []string{"other.example.com/cookbook/x", "lab.example.com/cookbook/x"} {
		d := tp.Decide(repo)
		fps := fingerprintsOf(d)
		if strings.Contains(strings.Join(fps, ","), emergencyFP) || len(fps) != 2 {
			t.Errorf("Decide(%s) keys = %v, want the two unreserved roots only", repo, fps)
		}
		if len(d.Excluded) != 1 || d.Excluded[0].Name != rootName(1) ||
			len(d.Excluded[0].Scopes) != 1 || d.Excluded[0].Scopes[0] != emergencyScope {
			t.Errorf("Decide(%s) excluded = %+v, want b-root reserved to %s", repo, d.Excluded, emergencyScope)
		}
	}
	d := tp.Decide("emergency.example.com/cookbook/x")
	if fps := fingerprintsOf(d); len(fps) != 1 || fps[0] != emergencyFP {
		t.Errorf("inside the scope keys = %v, want the emergency key only", fps)
	}
	// Inside a scope that lists its roots, every other root is excluded,
	// and a root of the default set says so by naming no scope.
	if len(d.Excluded) != 2 || len(d.Excluded[0].Scopes) != 0 || len(d.Excluded[1].Scopes) != 0 {
		t.Errorf("inside the scope excluded = %+v, want a-root and c-root of the default set", d.Excluded)
	}

	t.Run("every root reserved", func(t *testing.T) {
		tp := trustFor(t, []config.TrustScope{
			{Name: "a", Repositories: []string{"a.example.com/**"}, Roots: []string{rootName(0)}},
			{Name: "b", Repositories: []string{"b.example.com/**"}, Roots: []string{rootName(1)}},
		}, newKeyPair(t), newKeyPair(t))
		d := tp.Decide("other.example.com/cookbook/x")
		if d.Keys != nil {
			t.Errorf("default keys = %v, want none: every root is reserved", fingerprintsOf(d))
		}
		if !tp.HasRoots() {
			t.Error("HasRoots() = false with two configured roots")
		}
	})

	t.Run("a key reserved under one name is not default under another", func(t *testing.T) {
		// The same public key configured twice — once listed by a scope,
		// once not — stays reserved: the reservation follows the key, so
		// a duplicate root entry cannot reopen the leak.
		kp := newKeyPair(t)
		tp := trustFor(t, []config.TrustScope{
			{Name: emergencyScope, Repositories: []string{"emergency.example.com/**"}, Roots: []string{rootName(1)}},
		}, normal, kp, kp)
		d := tp.Decide("other.example.com/cookbook/x")
		if fps := fingerprintsOf(d); len(fps) != 1 || fps[0] != normalFP {
			t.Errorf("default keys = %v, want the normal key only", fps)
		}
	})
}

// TestEveryRootReservedRefusalExplainsItself: with no unreserved root,
// the default set is empty, and the refusal must not claim that no trust
// root is configured — it says every configured root is reserved, and
// names the one that verifies.
func TestEveryRootReservedRefusalExplainsItself(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	emergency := newKeyPair(t)
	dig := publishRecipe(t, src.st, "cookbook/app", "1.0.0", trustRecipeYAML(t, src, "app", imgDig))
	signManifest(t, src.st, "cookbook/app", dig, emergency)
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{{Name: "app", Version: "1.0.0"}})
	scopes := []config.TrustScope{{Name: emergencyScope, Repositories: []string{"emergency.example.com/**"}, Roots: []string{rootName(0)}}}
	task, err := runSync(t, New(openStore(t), newRemotes(t, nil), trustFor(t, scopes, emergency), retr, "", syncCfg()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	it := itemByName(t, task, "app@1.0.0")
	if it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
		t.Fatalf("item = %+v, want TBY-SIG-001", it)
	}
	fps, _ := it.Error.Params["fingerprints"].(string)
	if strings.Contains(fps, "no trust root is configured") || !strings.Contains(fps, "reserved") {
		t.Errorf("fingerprints = %q, want the every-root-reserved explanation", fps)
	}
	if reserved, _ := it.Error.Params["reserved"].(string); !strings.Contains(reserved, rootName(0)) {
		t.Errorf("reserved = %q, want %s named", reserved, rootName(0))
	}
}

// assertReservedNamed checks the actionable half of the B-057 refusal:
// the root that verifies (name and fingerprint) and the scope it is
// reserved to, rendered in both languages.
func assertReservedNamed(t *testing.T, params taxonomy.Params, fingerprint string) {
	t.Helper()
	reserved, _ := params["reserved"].(string)
	if !strings.Contains(reserved, rootName(1)) || !strings.Contains(reserved, fingerprint) {
		t.Errorf("reserved = %q, want the emergency root named with its fingerprint %s", reserved, fingerprint)
	}
	if to, _ := params["reservedto"].(string); to != emergencyScope {
		t.Errorf("reservedto = %q, want %q", to, emergencyScope)
	}
	for _, lang := range taxonomy.Languages() {
		m := taxonomy.Localize(lang, taxonomy.New(taxonomy.CodeSignature, params))
		if !strings.Contains(m.Cause, rootName(1)) || !strings.Contains(m.Cause, emergencyScope) {
			t.Errorf("%s cause does not name the reserved root and its scope: %s", lang, m.Cause)
		}
		if !strings.Contains(m.Action, emergencyScope) {
			t.Errorf("%s action does not name the scope: %s", lang, m.Action)
		}
	}
}

func fpOf(t *testing.T, kp interface{ Fingerprint() (string, error) }) string {
	t.Helper()
	fp, err := kp.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// labWithoutKeys is a policy whose only root is reserved to the emergency
// scope, plus an allowUnsigned "lab" scope listing no root and covering
// the cookbook at addr: inside "lab" no key applies at all (B-068).
func labWithoutKeys(t *testing.T, addr string, emergency interface {
	PublicPEM() ([]byte, error)
}) *TrustPolicy {
	t.Helper()
	pem, err := emergency.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	tp, err := LoadTrust(config.Trust{
		Roots: []config.TrustRoot{{Name: rootName(1), Key: string(pem)}},
		Scopes: append(emergencyScopes(), config.TrustScope{
			Name: "lab", Repositories: []string{strings.ReplaceAll(addr, ":", "_") + "/cookbook/*"}, AllowUnsigned: true,
		}),
	}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

// TestAllowUnsignedWithoutKeysRefusesASignedRecipe is the sync (and plan:
// same verifyRecipe) face of B-068: inside an allowUnsigned scope no root
// applies to, a recipe carrying a signature no applicable root verifies
// is refused in TBY-SIG-001 — as it is where keys apply — naming the
// reserved root that verifies it. It used to be admitted as unsigned.
// A recipe that carries no signature at all is still admitted.
func TestAllowUnsignedWithoutKeysRefusesASignedRecipe(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	emergency := newKeyPair(t)
	dig := publishRecipe(t, src.st, "cookbook/app", "1.0.0", trustRecipeYAML(t, src, "app", imgDig))
	signManifest(t, src.st, "cookbook/app", dig, emergency)
	publishRecipe(t, src.st, "cookbook/bare", "1.0.0", trustRecipeYAML(t, src, "bare", imgDig))
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "app", Version: "1.0.0"},
		{Name: "bare", Version: "1.0.0"},
	})

	task, err := runSync(t, New(openStore(t), newRemotes(t, nil), labWithoutKeys(t, src.addr, emergency), retr, "", syncCfg()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	it := itemByName(t, task, "app@1.0.0")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
		t.Fatalf("app item = %s / %+v, want failed TBY-SIG-001: a signed recipe was admitted as unsigned (B-068)", it.Status, it.Error)
	}
	assertReservedNamed(t, it.Error.Params, fpOf(t, emergency))
	if bare := itemByName(t, task, "bare@1.0.0/recipe"); bare.Status != tasks.StatusDone {
		t.Errorf("bare item = %s / %+v, want the unsigned recipe admitted by the lab scope", bare.Status, bare.Error)
	}
}

// TestAllowUnsignedWithoutKeysRefusesOnPromotion is the pre-push
// re-verification face of B-068.
func TestAllowUnsignedWithoutKeysRefusesOnPromotion(t *testing.T) {
	env := newHappyEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	emergency := newKeyPair(t)
	localCookbook := env.hostRepo + "/cookbook/wordpress"
	signManifest(t, env.dst, localCookbook, env.manDig, emergency)
	env.eng.trust = labWithoutKeys(t, env.src.addr, emergency)

	err := env.eng.verifyStored(context.Background(), localCookbook, env.src.addr+"/cookbook/wordpress", env.manDig, "wordpress@6.8.2")
	if err == nil {
		t.Fatal("verifyStored admitted a signed recipe as unsigned inside a scope no root applies to (B-068)")
	}
	te := env.eng.mapError(err, "wordpress@6.8.2")
	if te.Code() != taxonomy.CodeSignature {
		t.Fatalf("code = %s, want %s", te.Code(), taxonomy.CodeSignature)
	}
	assertReservedNamed(t, te.ParamsMap(), fpOf(t, emergency))
}
