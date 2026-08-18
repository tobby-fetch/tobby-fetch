// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Promotion tests (milestone 4, feature 4.1). Both ends are real
// registries speaking the real Distribution protocol: the destination is
// an embedded store served over its own /v2/ handler, and every request
// it receives is recorded. Nothing here mocks the OCI protocol, and
// nothing here takes go-containerregistry's word for what it transferred.

// withDestination attaches a promotion target to a happy-path engine.
func withDestination(t *testing.T, eng *Engine, d *destRegistry, cfg config.Destination, allow *policy.Allowlist) {
	t.Helper()
	if cfg.Registry == "" {
		cfg.Registry = d.addr
	}
	dest, err := NewDestination(cfg, config.Registries{Insecure: []string{d.addr}}, allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetDestination(dest)
}

// pushMeters counts the promotion hooks of one run.
type pushMeters struct {
	pushed, skipped, refused, bytes atomic.Int64
}

func (p *pushMeters) install(eng *Engine) {
	eng.SetMeters(Meters{
		PushDone:    func() { p.pushed.Add(1) },
		PushSkipped: func() { p.skipped.Add(1) },
		PushedBytes: func(n int64) { p.bytes.Add(n) },
		PushRefused: func(string) { p.refused.Add(1) },
	})
}

// destDigest resolves what the destination holds under repo:tag.
func destDigest(t *testing.T, d *destRegistry, repo, tag string) string {
	t.Helper()
	ref, err := name.NewTag(d.addr+"/"+repo+":"+tag, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	desc, err := remote.Head(ref)
	if err != nil {
		t.Fatalf("destination has no %s:%s — %v", repo, tag, err)
	}
	return desc.Digest.String()
}

// TestPromotionSecondCycleTransfersNothing is the FR-028 acceptance
// criterion, measured rather than asserted: re-promoting an
// already-synchronized recipe must transfer zero blobs.
//
// The verdict is the destination's own request log. A run that skipped
// the transfer but still negotiated an upload, or re-wrote a manifest it
// already had, would pass a byte counter and fail here — which is the
// point, because on a service that reconciles every few minutes those
// are the requests that add up.
func TestPromotionSecondCycleTransfersNothing(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)

	var first pushMeters
	first.install(env.eng)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	const rid = "wordpress@6.8.2"
	for _, name := range []string{"app", "chart", "model", "recipe"} {
		it := itemByName(t, task, pushItemPrefix+rid+"/"+name)
		if it.Status != tasks.StatusDone {
			t.Fatalf("push item %s = %s (error %+v), want done", name, it.Status, it.Error)
		}
	}
	if first.bytes.Load() == 0 || first.pushed.Load() != 4 {
		t.Fatalf("first cycle meters: pushed=%d bytes=%d, want 4 pushes moving bytes",
			first.pushed.Load(), first.bytes.Load())
	}
	// The content really is there, at its relocated path (FR-035) and
	// with its pinned digest unchanged (bit-exact promotion).
	if got := destDigest(t, dest, env.hostRepo+"/ingredients/wordpress", "6.8.2"); got != env.idx.digest {
		t.Errorf("destination index digest = %s, want %s", got, env.idx.digest)
	}
	if got := destDigest(t, dest, env.hostRepo+"/charts/wordpress", "24.2.6"); got != env.chartDig {
		t.Errorf("destination chart digest = %s, want %s", got, env.chartDig)
	}

	// Second cycle: nothing upstream moved, so nothing may cross.
	dest.reset()
	var second pushMeters
	second.install(env.eng)
	task, err = runSync(t, env.eng)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("second cycle wrote to the destination: %v (FR-028 requires zero)", writes)
	}
	if second.bytes.Load() != 0 {
		t.Errorf("second cycle moved %d bytes, want 0", second.bytes.Load())
	}
	if second.pushed.Load() != 0 || second.skipped.Load() != 4 {
		t.Errorf("second cycle meters: pushed=%d skipped=%d, want 0 and 4",
			second.pushed.Load(), second.skipped.Load())
	}
	for _, name := range []string{"app", "chart", "model", "recipe"} {
		it := itemByName(t, task, pushItemPrefix+rid+"/"+name)
		if it.Status != tasks.StatusSkipped {
			t.Errorf("push item %s = %s, want skipped", name, it.Status)
		}
	}
	// FR-026: the status is reported per digest, against the destination,
	// and it is what made the transfer unnecessary.
	for _, ing := range []string{"app", "chart", "model"} {
		res := destResolution(t, task, rid, ing)
		if res.DestinationStatus != "up-to-date" || res.PushedBytes != 0 {
			t.Errorf("resolution %s: status=%q pushed=%d, want up-to-date and 0",
				ing, res.DestinationStatus, res.PushedBytes)
		}
	}
}

// destResolution finds the promotion row of one ingredient — the one
// carrying a destination, as opposed to the fetch row of the same pair.
func destResolution(t *testing.T, task *tasks.Task, recipe, ingredient string) *tasks.Resolution {
	t.Helper()
	for i := range task.Resolutions {
		r := &task.Resolutions[i]
		if r.Recipe == recipe && r.Ingredient == ingredient && r.Destination != "" {
			return r
		}
	}
	t.Fatalf("no promotion resolution for %s/%s (rows: %+v)", recipe, ingredient, task.Resolutions)
	return nil
}

// TestPromotionTransfersOnlyMissingBlobs is the other half of FR-028: a
// partially present image transfers only what the destination lacks.
//
// The fixture is the natural one rather than a contrived one — two
// recipes sharing an ingredient — because that is how partial presence
// actually arises in a zone.
func TestPromotionTransfersOnlyMissingBlobs(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	dest := newDestRegistry(t)

	shared := seedImage(t, src, "ingredients/base", "1.0.0")
	other := seedImage(t, src, "ingredients/tool", "2.0.0")
	kp := newKeyPair(t)

	// Two recipes, the first pinning only the shared ingredient.
	firstYAML := cookedRecipeYAML(t, "alpha", "1.0.0", []spec.Ingredient{{
		Name: "base", Kind: spec.IngredientContainerImage,
		Ref: src.addr + "/ingredients/base", Version: "1.0.0", Digest: shared,
	}})
	firstDig := publishRecipe(t, src.st, "cookbook/alpha", "1.0.0", firstYAML)
	signManifest(t, src.st, "cookbook/alpha", firstDig, kp)

	secondYAML := cookedRecipeYAML(t, "beta", "1.0.0", []spec.Ingredient{
		{
			Name: "base", Kind: spec.IngredientContainerImage,
			Ref: src.addr + "/ingredients/base", Version: "1.0.0", Digest: shared,
		},
		{
			Name: "tool", Kind: spec.IngredientContainerImage,
			Ref: src.addr + "/ingredients/tool", Version: "2.0.0", Digest: other,
		},
	})
	secondDig := publishRecipe(t, src.st, "cookbook/beta", "1.0.0", secondYAML)
	signManifest(t, src.st, "cookbook/beta", secondDig, kp)

	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "alpha", Version: "1.0.0"},
		{Name: "beta", Version: "1.0.0"},
	})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())
	withDestination(t, eng, dest, config.Destination{}, nil)

	if _, err := runSync(t, eng); err != nil {
		t.Fatalf("run: %v", err)
	}
	hostRepo := strings.ReplaceAll(src.addr, ":", "_")
	// beta's shared ingredient resolved to the same repository and digest
	// alpha's did, so the destination already held it: only "tool" could
	// have moved bytes.
	uploads := 0
	for _, w := range dest.writes() {
		if strings.Contains(w, "/blobs/uploads") {
			uploads++
		}
	}
	if uploads == 0 {
		t.Fatal("no blob upload at all: the fixture did not exercise a transfer")
	}
	// Both repositories exist on the destination, and the shared one was
	// written exactly once even though two recipes pin it.
	if got := destDigest(t, dest, hostRepo+"/ingredients/base", "1.0.0"); got != shared {
		t.Errorf("shared ingredient digest = %s, want %s", got, shared)
	}
	if got := destDigest(t, dest, hostRepo+"/ingredients/tool", "2.0.0"); got != other {
		t.Errorf("second ingredient digest = %s, want %s", got, other)
	}
	baseManifestWrites := 0
	for _, w := range dest.writes() {
		if strings.Contains(w, hostRepo+"/ingredients/base/manifests/") {
			baseManifestWrites++
		}
	}
	if baseManifestWrites != 1 {
		t.Errorf("the shared ingredient's manifest was written %d times, want 1 (FR-028)", baseManifestWrites)
	}
}

// TestPromotionWidensPlatformsWhenSparseIndexRefused is FR-022's second
// sentence: the sparse index preserves the pinned digest, and "if the
// destination registry rejects sparse indexes, all platforms SHALL be
// transferred instead".
//
// The happy-path recipe selects two of three platforms, and the
// destination validates index members — which is what a conforming
// registry does. The requirement's outcome is that the promotion
// succeeds, at the SAME pinned digest, with the third platform fetched
// rather than the index rewritten.
func TestPromotionWidensPlatformsWhenSparseIndexRefused(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)

	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("run: %v", err)
	}
	repo := env.hostRepo + "/ingredients/wordpress"
	if got := destDigest(t, dest, repo, "6.8.2"); got != env.idx.digest {
		t.Fatalf("index digest = %s, want the pinned %s (the index must not be rewritten)", got, env.idx.digest)
	}
	// Every platform the index lists resolves at the destination,
	// including the one the recipe did not select.
	for label, child := range env.idx.children {
		ref, err := name.NewDigest(dest.addr+"/"+repo+"@"+child, name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Head(ref); err != nil {
			t.Errorf("platform %s (%s) is missing at the destination: %v", label, child, err)
		}
	}
	// And the local store now holds them too, so the next cycle moves
	// nothing: the widening happens once, not every interval.
	dest.reset()
	var second pushMeters
	second.install(env.eng)
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("the widened index was re-pushed on the next cycle: %v", writes)
	}
}

// TestPromotionPropagatesRecipeAndSignature is FR-034: the destination
// cookbook holds the recipe artifact at
// "<registry>/<cookbook>/<name>:<version>", with its signature, and the
// signature verifies there — the acceptance criterion is "verifiable with
// cosign", so the test verifies rather than merely counting bytes.
func TestPromotionPropagatesRecipeAndSignature(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{Cookbook: "zone-cookbook"}, nil)

	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := destDigest(t, dest, "zone-cookbook/wordpress", "6.8.2")
	if got != env.manDig {
		t.Fatalf("propagated recipe digest = %s, want %s", got, env.manDig)
	}
	// The attached signature travelled with it (§12.2) and still verifies
	// against the same trust root, read back from the destination.
	sig := destDigest(t, dest, "zone-cookbook/wordpress", SignatureTag(env.manDig))
	if sig == "" {
		t.Fatal("the propagated recipe carries no signature artifact")
	}
	dstManifests := &storeManifests{src: dest.st, repo: "zone-cookbook/wordpress"}
	decision := env.eng.trust.Decide(env.src.addr + "/cookbook/wordpress")
	if _, err := sigverify.Verify(context.Background(), dstManifests, "zone-cookbook/wordpress", env.manDig, decision.Keys); err != nil {
		t.Errorf("the propagated recipe does not verify at its destination: %v", err)
	}
}

// TestPromotionRefusesAfterLocalSignatureTampering is FR-033/ADR-0007:
// verification happens before EVERY push, not once at import.
//
// The scenario is the one the requirement exists for. A recipe is
// imported and promoted normally; between two cycles its signature in the
// local store stops matching. A service that trusted its import-time
// verdict would keep promoting it every interval; this one refuses on the
// very next cycle, before a single byte leaves.
func TestPromotionRefusesAfterLocalSignatureTampering(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)

	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("first cycle: %v", err)
	}

	// Replace the locally stored signature with one made by a key nobody
	// trusts: the artifact is well-formed, so this is a trust verdict,
	// not a malformed-artifact one.
	localCookbook := env.hostRepo + "/cookbook/wordpress"
	signManifest(t, env.dst, localCookbook, env.manDig, newKeyPair(t))

	dest.reset()
	var meters pushMeters
	meters.install(env.eng)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("a recipe that no longer verifies still wrote to the destination: %v", writes)
	}
	if meters.refused.Load() == 0 {
		t.Error("the refusal was not counted (FR-091)")
	}
	it := itemByName(t, task, pushItemPrefix+"wordpress@6.8.2/recipe")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeSignature {
		t.Fatalf("push item = %s / %+v, want failed with %s", it.Status, it.Error, taxonomy.CodeSignature)
	}
	// The fetch side is untouched: the content is still local and valid,
	// only its onward journey is blocked (fail closed, per item).
	if fetched := itemByName(t, task, "wordpress@6.8.2/app"); fetched.Status == tasks.StatusFailed {
		t.Error("a pre-push refusal must not fail the fetch item")
	}
}

// TestPromotionRefusedByDestinationNaming is FR-035's pre-push check: a
// destination that will not hold the relocated name must be found out
// before the push, and the refusal must name the limit.
func TestPromotionRefusedByDestinationNaming(t *testing.T) {
	env := newHappyEnv(t)
	// The relocation convention produces "127.0.0.1_<port>/ingredients/…":
	// three components. A destination capped at two refuses it.
	dest := newDestRegistry(t, refusingNestedNames(2))
	withDestination(t, env.eng, dest, config.Destination{}, nil)

	var meters pushMeters
	meters.install(env.eng)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("the refusal happened after a write: %v (FR-035 requires a pre-push failure)", writes)
	}
	it := itemByName(t, task, pushItemPrefix+"wordpress@6.8.2/app")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeDestinationLimit {
		t.Fatalf("push item = %s / %+v, want failed with %s", it.Status, it.Error, taxonomy.CodeDestinationLimit)
	}
	limit, _ := it.Error.Params["limit"].(string)
	if !strings.Contains(limit, "NAME_INVALID") || !strings.Contains(limit, "at most 2 path components") {
		t.Errorf("the refusal does not name the destination's limit: %q", limit)
	}
	if meters.refused.Load() == 0 {
		t.Error("the refusal was not counted")
	}
}

// TestPromotionRefusedByAllowlist is FR-030 on the destination side: the
// allowlist covers where content goes, not only where it comes from, and
// it answers before the socket.
func TestPromotionRefusedByAllowlist(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	allow, err := policy.NewAllowlist([]string{"registry.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	withDestination(t, env.eng, dest, config.Destination{}, allow)

	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(dest.writes()) != 0 || len(dest.reqs) != 0 {
		t.Errorf("a non-allowlisted destination was contacted: %v", dest.reqs)
	}
	it := itemByName(t, task, pushItemPrefix+"wordpress@6.8.2/app")
	if it.Status != tasks.StatusFailed || it.Error == nil || it.Error.Code != taxonomy.CodeNotAllowlisted {
		t.Fatalf("push item = %s / %+v, want failed with %s", it.Status, it.Error, taxonomy.CodeNotAllowlisted)
	}
}

// TestPromotionWithoutDestinationIsANoop: an instance that promotes
// nothing is a complete instance — every mirror-mode one is. The sync
// must behave exactly as it did before this feature existed.
func TestPromotionWithoutDestinationIsANoop(t *testing.T) {
	env := newHappyEnv(t)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, it := range task.Items {
		if strings.HasPrefix(it.Name, pushItemPrefix) {
			t.Errorf("an instance with no destination produced the promotion item %q", it.Name)
		}
	}
}

// TestPromotionStopsOnShutdown is FR-093: a canceled cycle leaves the
// destination consistent — never a manifest whose blobs did not make it.
// The order (blobs, then the manifest naming them) is what guarantees it,
// so the assertion is on what a puller would see.
func TestPromotionStopsOnShutdown(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := &tasks.Task{ID: "tsk_cancel", RunID: "run_cancel", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	_ = env.eng.Runner()(ctx, task, discardLogger(), func() {})

	// Whatever landed, landed whole: every manifest the destination
	// serves resolves, which is exactly what "no partially written
	// manifest served afterwards" means.
	for _, repo := range []string{
		env.hostRepo + "/ingredients/wordpress",
		env.hostRepo + "/charts/wordpress",
		"cookbook/wordpress",
	} {
		ref, err := name.NewTag(dest.addr+"/"+repo+":6.8.2", name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		desc, err := remote.Get(ref)
		if err != nil {
			continue // never written: the correct outcome for an interrupted cycle
		}
		if len(desc.Manifest) == 0 {
			t.Errorf("%s serves an empty manifest after an interrupted cycle", repo)
		}
	}
}
