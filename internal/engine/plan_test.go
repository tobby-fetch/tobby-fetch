// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"os"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// What a plan reports (FR-055 amendment R-04): version resolution
// (FR-021), per-digest statuses (FR-026), volumes (FR-055), projected
// prune (FR-045), and the policy verdicts reachable without a transfer —
// the allow-list (FR-030) and the recipes' signatures (FR-033).

// TestPlanOnAFreshStoreReportsEverythingAsNew locks the resolution report
// and the first volume figure: on an empty store every ingredient is new,
// and the projected transfer is the whole thing.
func TestPlanOnAFreshStoreReportsEverythingAsNew(t *testing.T) {
	env := newPlanEnv(t)

	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Outcome != OutcomeChangesPlanned {
		t.Fatalf("outcome = %q, want %q (problems %+v)", plan.Outcome, OutcomeChangesPlanned, plan.Problems)
	}
	if plan.ExitCode() != taxonomy.ExitChangesPlanned {
		t.Errorf("exit code = %d, want %d", plan.ExitCode(), taxonomy.ExitChangesPlanned)
	}
	if plan.Zone != planZone {
		t.Errorf("zone = %q, want %q", plan.Zone, planZone)
	}
	if len(plan.Recipes) != 1 {
		t.Fatalf("plan reports %d recipes, want 1", len(plan.Recipes))
	}
	r := plan.Recipes[0]
	// FR-021: requested → resolved → digest.
	if r.Requested != "1.0.0" || r.Resolved != "1.0.0" || r.Digest == "" {
		t.Errorf("resolution = %q → %q (%q), want the exact tag resolved with a digest", r.Requested, r.Resolved, r.Digest)
	}
	// FR-033: the signature verdict, reachable without pulling content.
	if r.Signature != SignatureVerified {
		t.Errorf("signature = %q, want %q", r.Signature, SignatureVerified)
	}
	// FR-026: everything is new on an empty store.
	if plan.Totals.New != 1 || plan.Totals.UpToDate != 0 || plan.Totals.Outdated != 0 {
		t.Errorf("statuses = %+v, want exactly one new ingredient", plan.Totals)
	}
	// FR-055: the transfer is the whole content, and the projection is
	// the store plus it.
	if plan.Totals.TransferBytes != plan.Totals.TotalBytes {
		t.Errorf("on an empty store the transfer (%d) must equal the total (%d)",
			plan.Totals.TransferBytes, plan.Totals.TotalBytes)
	}
	if plan.Totals.ProjectedStoreBytes != plan.Totals.StoreBytes+plan.Totals.TransferBytes {
		t.Errorf("projection = %d, want %d + %d",
			plan.Totals.ProjectedStoreBytes, plan.Totals.StoreBytes, plan.Totals.TransferBytes)
	}
	if plan.Totals.LargestFileBytes <= 0 {
		t.Error("the plan reports no largest file, so the FAT32 verdict has nothing to decide on")
	}
	// FR-055: a check per target, with the margin applied.
	if len(plan.Checks) != 1 || plan.Checks[0].MarginPercent != 10 {
		t.Errorf("checks = %+v, want one store check at the 10 %% default margin", plan.Checks)
	}
}

// TestPlanAfterASyncReportsNothingToDo is the "nothing to do" exit code
// FR-055 asks a CI gate to be able to see: a store already holding the
// resolved content plans to zero.
func TestPlanAfterASyncReportsNothingToDo(t *testing.T) {
	env := newPlanEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}

	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Outcome != OutcomeUpToDate {
		t.Fatalf("outcome = %q, want %q (totals %+v, problems %+v)",
			plan.Outcome, OutcomeUpToDate, plan.Totals, plan.Problems)
	}
	if plan.ExitCode() != taxonomy.ExitOK {
		t.Errorf("exit code = %d, want %d", plan.ExitCode(), taxonomy.ExitOK)
	}
	if plan.Totals.TransferBytes != 0 {
		t.Errorf("an up-to-date store still projects %d bytes to transfer", plan.Totals.TransferBytes)
	}
	if plan.Totals.UpToDate != 1 || plan.Totals.New != 0 {
		t.Errorf("statuses = %+v, want one up-to-date ingredient", plan.Totals)
	}
	// And the totals still describe the content: "nothing to transfer" is
	// not "nothing there".
	if plan.Totals.TotalBytes == 0 {
		t.Error("the gross total collapsed to zero along with the transfer")
	}
}

// TestPlanRefusedByPolicyDoesNotContactTheDestination is the FR-055
// acceptance criterion, word for word: "a plan run whose recipes violate
// the allow-list exits with the policy code without contacting the
// destination".
//
// FALLIBILITY (proved 2026-08-26): with the `plan.Outcome !=
// OutcomePolicyRefused` guard removed from Planner.Plan, the test failed
// with "the plan claims to have evaluated the destination". The request
// count stayed at zero even then — Destination.Repository consults the
// allow-list before it resolves a name, so the socket is refused a
// second time on the way out — and that is worth knowing: the guard is
// the requirement, the allow-list is the belt underneath it, and the
// test asserts both so that losing either one is visible.
func TestPlanRefusedByPolicyDoesNotContactTheDestination(t *testing.T) {
	env := newPlanEnv(t)
	dest := newDestRegistry(t)

	// A declared allow-list naming a registry that is neither the source
	// nor the destination: every outbound reference is refused.
	allow, err := policy.NewAllowlist([]string{"registry.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	remotes, err := NewRemotes(config.Registries{}, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDestination(config.Destination{Registry: dest.addr},
		config.Registries{Insecure: []string{dest.addr}}, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	planner := NewPlanner(env.dst, remotes, env.eng.trust, env.retrieverPath, PlanConfig{})
	planner.SetDestination(d)
	dest.reset()

	plan, err := planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Outcome != OutcomePolicyRefused {
		t.Fatalf("outcome = %q, want %q (problems %+v)", plan.Outcome, OutcomePolicyRefused, plan.Problems)
	}
	if plan.ExitCode() != taxonomy.ExitPolicy {
		t.Errorf("exit code = %d, want %d", plan.ExitCode(), taxonomy.ExitPolicy)
	}
	if n := len(dest.requests()); n != 0 {
		t.Errorf("the policy-refused plan contacted the destination %d times: %v", n, dest.requests())
	}
	if plan.Totals.PushEvaluated {
		t.Error("the plan claims to have evaluated the destination")
	}
	// The verdict is reported positively, not merely as an absence.
	if !plan.Policy.AllowlistDeclared || len(plan.Policy.AllowlistPatterns) != 1 {
		t.Errorf("policy report = %+v, want the declared allow-list", plan.Policy)
	}
	found := false
	for _, h := range plan.Policy.Hosts {
		if !h.Allowed {
			found = true
		}
	}
	if !found {
		t.Errorf("no host is reported as refused: %+v", plan.Policy.Hosts)
	}
	if len(plan.Problems) == 0 || plan.Problems[0].Code != taxonomy.CodeNotAllowlisted {
		t.Errorf("problems = %+v, want the FR-030 refusal", plan.Problems)
	}
}

// TestPlanReportsAVerificationFailure locks the fourth exit code: a
// recipe no configured trust root validates is a verification verdict,
// not an operational failure, and a gate must be able to tell them apart.
func TestPlanReportsAVerificationFailure(t *testing.T) {
	env := newPlanEnv(t)
	// A trust policy built on a DIFFERENT key: the recipe is signed, the
	// signature does not verify.
	other := newKeyPair(t)
	planner := NewPlanner(env.dst, newRemotes(t, nil), trustFor(t, nil, other), env.retrieverPath, PlanConfig{})

	plan, err := planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Outcome != OutcomeVerificationFailed {
		t.Fatalf("outcome = %q, want %q (problems %+v)", plan.Outcome, OutcomeVerificationFailed, plan.Problems)
	}
	if plan.ExitCode() != taxonomy.ExitVerification {
		t.Errorf("exit code = %d, want %d", plan.ExitCode(), taxonomy.ExitVerification)
	}
	if len(plan.Recipes) != 1 || plan.Recipes[0].Signature != SignatureRefused {
		t.Errorf("recipe signature verdict = %+v, want %q", plan.Recipes, SignatureRefused)
	}
}

// TestPlanReportsAFailedResolution: a Retriever naming a version no tag
// satisfies is an operational failure (FR-021 never falls back silently),
// and the plan says which recipe.
func TestPlanReportsAFailedResolution(t *testing.T) {
	env := newPlanEnv(t)
	writeRetriever(t, env.retrieverPath, env.cookbook, "9.9.9")

	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want %q (problems %+v)", plan.Outcome, OutcomeFailed, plan.Problems)
	}
	if plan.ExitCode() != taxonomy.ExitFailure {
		t.Errorf("exit code = %d, want %d", plan.ExitCode(), taxonomy.ExitFailure)
	}
	if len(plan.Recipes) != 1 || plan.Recipes[0].Problem == nil {
		t.Fatalf("the failure is not attached to the recipe: %+v", plan.Recipes)
	}
	if plan.Recipes[0].Resolved != "" {
		t.Errorf("a failed resolution reported a resolved tag: %q", plan.Recipes[0].Resolved)
	}
	if plan.Recipes[0].Signature != SignatureNotEvaluated {
		t.Errorf("signature = %q, want %q on a recipe that was never reached",
			plan.Recipes[0].Signature, SignatureNotEvaluated)
	}
}

// TestPlanProjectsThePrune covers FR-045 inside a plan: content the store
// holds on behalf of a recipe the Retriever no longer names is listed,
// with its size and the recipe that brought it.
func TestPlanProjectsThePrune(t *testing.T) {
	env := newPlanEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}

	// A second recipe replaces the first in the desired state.
	seedOtherRecipe(t, env, "other", "2.0.0")
	raw := retrieverYAML(t, planZone, env.cookbook, []spec.RecipeSelector{
		{Name: "other", Version: "2.0.0"},
	})
	if err := os.WriteFile(env.retrieverPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Prune.Evaluated {
		t.Fatalf("the prune was not projected: %s", plan.Prune.Reason)
	}
	if len(plan.Prune.Repositories) == 0 {
		t.Fatal("the dropped recipe's content is not listed for removal (FR-045)")
	}
	if plan.Prune.TotalBytes <= 0 {
		t.Error("the projected prune frees no bytes")
	}
	for _, p := range plan.Prune.Repositories {
		if p.Recipe != planRecipe {
			t.Errorf("prune candidate %s is attributed to %q, want %q", p.Repo, p.Recipe, planRecipe)
		}
		if p.Bytes <= 0 {
			t.Errorf("prune candidate %s has no size", p.Repo)
		}
	}
}

// TestPlanRefusesToProjectAPruneFromAPartialResolution is the safety
// property of the projection, and the reason it exists as a property at
// all: a prune list reads as "this is no longer wanted", and computing
// one from a Retriever whose recipes did not all resolve would say that
// about content whose only problem was an unreachable cookbook.
//
// FALLIBILITY (proved 2026-08-26): with the `!allResolved` guard removed
// from Planner.planPrune, the test failed with "a plan with an
// unresolved recipe projected 2 repositories for removal".
func TestPlanRefusesToProjectAPruneFromAPartialResolution(t *testing.T) {
	env := newPlanEnv(t)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	// A Retriever naming a recipe that does not exist: the resolution
	// fails, and everything currently in the store is suddenly
	// "unreferenced".
	raw := retrieverYAML(t, planZone, env.cookbook, []spec.RecipeSelector{
		{Name: "no-such-recipe", Version: "1.0.0"},
	})
	if err := os.WriteFile(env.retrieverPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Prune.Evaluated {
		t.Errorf("a plan with an unresolved recipe projected %d repositories for removal",
			len(plan.Prune.Repositories))
	}
	if plan.Prune.Reason == "" {
		t.Error("the plan does not say why the prune was not projected")
	}
	if len(plan.Prune.Repositories) != 0 {
		t.Errorf("the plan listed prune candidates anyway: %+v", plan.Prune.Repositories)
	}
}

// TestPlanDeduplicatesSharedContentByDigest is the FR-055 wording put
// under test: "deduplicated by digest". Two recipes sharing an ingredient
// each report it, and the total counts it once.
func TestPlanDeduplicatesSharedContentByDigest(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	kp := newKeyPair(t)
	cookbook := src.addr + "/cookbook"

	// One image, two recipes pointing at exactly the same digest.
	shared := seedImage(t, src, "ingredients/shared", "1.0.0")
	ing := []spec.Ingredient{{
		Name: "shared", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/ingredients/shared", Version: "1.0.0", Digest: shared,
	}}
	for _, name := range []string{"alpha", "beta"} {
		manifestDigest := publishRecipe(t, src.st, "cookbook/"+name, "1.0.0",
			cookedRecipeYAML(t, name, "1.0.0", ing))
		signManifest(t, src.st, "cookbook/"+name, manifestDigest, kp)
	}
	retr := retrieverFile(t, planZone, cookbook, []spec.RecipeSelector{
		{Name: "alpha", Version: "1.0.0"},
		{Name: "beta", Version: "1.0.0"},
	})
	planner := NewPlanner(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, PlanConfig{})

	plan, err := planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Recipes) != 2 {
		t.Fatalf("plan reports %d recipes, want 2 (problems %+v)", len(plan.Recipes), plan.Problems)
	}
	perRecipe := plan.Recipes[0].TransferBytes + plan.Recipes[1].TransferBytes
	if plan.Recipes[0].TransferBytes == 0 {
		t.Fatal("the recipes report no volume at all")
	}
	// Each recipe owns the shared content; the total counts it once. The
	// per-recipe figures deliberately do NOT add up, and that is the
	// behaviour under test.
	if plan.Totals.TransferBytes >= perRecipe {
		t.Errorf("the total (%d) is not smaller than the sum of the per-recipe figures (%d): "+
			"shared content was counted twice", plan.Totals.TransferBytes, perRecipe)
	}
	// The two recipes' ingredient blobs are identical, so the total is
	// exactly one recipe's worth apart from the two distinct recipe
	// artifacts.
	if plan.Totals.TransferBytes <= plan.Recipes[0].TransferBytes/2 {
		t.Errorf("the total (%d) collapsed below one recipe's content (%d)",
			plan.Totals.TransferBytes, plan.Recipes[0].TransferBytes)
	}
}

// TestPlanReportsTheDestinationStatuses covers the promotion side of
// FR-026 inside a plan: with a destination configured and no policy
// refusal, every ingredient carries its destination verdict, and the
// promotion figure is announced as the upper bound it is.
func TestPlanReportsTheDestinationStatuses(t *testing.T) {
	env := newPlanEnv(t)
	dest := newDestRegistry(t)
	d, err := NewDestination(config.Destination{Registry: dest.addr},
		config.Registries{Insecure: []string{dest.addr}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	env.planner.SetDestination(d)

	plan, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Totals.PushEvaluated {
		t.Fatalf("the destination was not probed (problems %+v)", plan.Problems)
	}
	if plan.Destination != dest.addr {
		t.Errorf("plan destination = %q, want %q", plan.Destination, dest.addr)
	}
	ing := plan.Recipes[0].Ingredients[0]
	if ing.PushStatus != StatusNew {
		t.Errorf("push status = %q, want %q against an empty destination", ing.PushStatus, StatusNew)
	}
	if plan.Totals.PushUpperBoundBytes <= 0 {
		t.Error("nothing is projected towards an empty destination")
	}
	// The destination host carries its own allow-list verdict.
	seen := false
	for _, h := range plan.Policy.Hosts {
		if h.Role == "destination" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("the destination host is missing from the policy report: %+v", plan.Policy.Hosts)
	}
}

// TestPlanCachesSizesOnPinnedDigests locks the FR-055 allowance — "the
// computation MAY be cached (pinned digests make sizes stable)" — and its
// boundary: the SOURCE side is cached on an immutable key, the target
// side never is.
func TestPlanCachesSizesOnPinnedDigests(t *testing.T) {
	env := newPlanEnv(t)

	first, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.planner.sizes) == 0 {
		t.Fatal("the size cache stayed empty")
	}
	// A real synchronization changes what the TARGET holds. The cached
	// source-side sizes are still correct, and the transfer figure must
	// nonetheless drop to zero: presence is re-read, never cached.
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	second, err := env.planner.Plan(context.Background(), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Totals.TotalBytes != first.Totals.TotalBytes {
		t.Errorf("the gross total moved between two plans of the same pinned content: %d → %d",
			first.Totals.TotalBytes, second.Totals.TotalBytes)
	}
	if second.Totals.TransferBytes != 0 {
		t.Errorf("after the synchronization the plan still projects %d bytes: "+
			"target presence is being cached", second.Totals.TransferBytes)
	}
}

// seedOtherRecipe publishes a second signed recipe with its own content.
func seedOtherRecipe(t *testing.T, env *planEnv, name, version string) {
	t.Helper()
	imageDigest := seedImage(t, env.src, "ingredients/"+name, version)
	yaml := cookedRecipeYAML(t, name, version, []spec.Ingredient{{
		Name: name, Kind: spec.IngredientContainerImage,
		Ref: env.src.addr + "/ingredients/" + name, Version: version, Digest: imageDigest,
	}})
	manifestDigest := publishRecipe(t, env.src.st, "cookbook/"+name, version, yaml)
	signManifest(t, env.src.st, "cookbook/"+name, manifestDigest, env.key)
}

// requests returns every request the destination recorded, reads
// included: this file cares about whether the destination was contacted
// at all, which writes() deliberately does not answer.
func (d *destRegistry) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.reqs...)
}

// compile-time guard: the fixture's platform type is the one the engine
// uses, so a signature change over there breaks here.
var _ = v1.Platform{}
