// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestKeyRotationReachesTheStoreAndTheDestination is B-081: a recipe and
// its ingredients are synchronized and promoted under key A; upstream
// then re-signs the SAME digests with key B and the instance's trust root
// rotates to B alone. The digests did not move, so every item is up to
// date — and yet the new signatures must reach the local store (or the
// push-side re-verification refuses what the fetch side just accepted,
// TBY-SIG-001) and the destination (or the zone below refuses it).
//
// The recipe is re-signed in the classic tag layout, the ingredients in
// the cosign 3.x bundle layout, so both copies of §12.2 are exercised;
// trust.requireIngredientSignatures makes the ingredients' signatures
// load-bearing on the push side too.
func TestKeyRotationReachesTheStoreAndTheDestination(t *testing.T) {
	env := newHappyEnv(t)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{Cookbook: "zone-cookbook"}, nil)

	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatalf("first cycle: %v", err)
	}
	if it := itemByName(t, task, pushItemPrefix+wordpressRID+"/recipe"); it.Status != tasks.StatusDone {
		t.Fatalf("first cycle: recipe push = %s (%+v), want done", it.Status, it.Error)
	}

	// Upstream rotation: the same digests, signed again with key B.
	kpB := newKeyPair(t)
	signManifest(t, env.src.st, "cookbook/wordpress", env.manDig, kpB)
	ingredients := map[string]string{ // source repository → pinned digest
		"ingredients/wordpress": env.idx.digest,
		"charts/wordpress":      env.chartDig,
		"models/embedding":      env.modelDig,
	}
	for repo, dgst := range ingredients {
		signManifestBundle(t, env.src.st, repo, dgst, kpB)
	}
	env.eng.trust = requiringTrust(t, nil, kpB)

	task, err = runSync(t, env.eng)
	if err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	for _, name := range []string{"app", "chart", "model", "recipe"} {
		it := itemByName(t, task, pushItemPrefix+wordpressRID+"/"+name)
		if it.Status != tasks.StatusSkipped && it.Status != tasks.StatusDone {
			t.Errorf("second cycle: push item %s = %s (%+v), want it promoted under the rotated key", name, it.Status, it.Error)
		}
	}

	ctx := context.Background()
	keysB := trustFor(t, nil, kpB).Decide("").Keys
	verifyAt := func(where string, st StoreReader, repo, dgst string, keys *sigverify.Keys) {
		t.Helper()
		if _, err := sigverify.Verify(ctx, &storeManifests{src: st, repo: repo}, repo, dgst, keys); err != nil {
			t.Errorf("%s: %s@%s does not verify with key B alone: %v", where, repo, dgst, err)
		}
	}

	// The stored copies verify with B alone.
	cookbookRepo := env.hostRepo + "/cookbook/wordpress"
	verifyAt("local store", env.dst, cookbookRepo, env.manDig, keysB)
	for repo, dgst := range ingredients {
		verifyAt("local store", env.dst, env.hostRepo+"/"+repo, dgst, keysB)
	}
	// And so do the promoted ones, at the destination.
	verifyAt("destination", dest.st, "zone-cookbook/wordpress", env.manDig, keysB)
	for repo, dgst := range ingredients {
		verifyAt("destination", dest.st, env.hostRepo+"/"+repo, dgst, keysB)
	}

	// Nothing moved since: the third cycle re-reads, and writes nothing
	// (FR-028 holds over signatures too).
	dest.reset()
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatalf("third cycle: %v", err)
	}
	if writes := dest.writes(); len(writes) != 0 {
		t.Errorf("third cycle wrote to the destination: %v", writes)
	}
}
