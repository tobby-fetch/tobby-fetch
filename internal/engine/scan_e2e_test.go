// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/trivy"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/trivytest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The vulnerability database crosses the air gap through Tobby's own
// transport (DEVELOPMENT-PLAN §2.6 exit criterion, FR-032), end to end:
//
//  1. the connected zone's production registry carries the tobby-system
//     recipe — the Trivy database as an ordinary OCIArtifact ingredient —
//     and an application recipe whose image is known-vulnerable;
//  2. a mirror synchronization writes both onto a medium (the policy is
//     advisory there, so the vulnerable image is reported and written);
//  3. the medium is carried to another host and imported into the
//     isolated zone's registry, after the FR-054 verification — which
//     does not need the database (the destination instance has none
//     until it is there);
//  4. a passthrough instance INSIDE the isolated zone synchronizes from
//     that registry, tobby-system first: its scanner finds the database
//     the medium carried — same artifact digest, same build timestamp,
//     its age in every report — and, with a blocking policy, refuses the
//     known-vulnerable image with the finding in the task report, while
//     the advisory policy pushes it with the finding reported.
//
// Without a real Trivy (TOBBY_TEST_TRIVY, TOBBY_TEST_TRIVY_DB), the same
// journey runs with a synthetic database and stops at what does not need
// the binary: the database's arrival, unpacking and age on the far side.
// CI provides the real binary and database and makes the skip a failure.
func TestScanDatabaseCrossesTheAirGap(t *testing.T) {
	live := trivytestEnv(t)
	builtAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	var dbLayer []byte
	if live != nil {
		dbLayer = trivytest.DBLayer(t, live.DB)
	} else {
		dbLayer = trivytest.SyntheticDBLayer(t, fmt.Sprintf(`{"Version":2,"UpdatedAt":%q}`, builtAt.Format(time.RFC3339)))
	}

	// 1. The connected zone.
	kp := newKeyPair(t)
	src := newRegistry(t)
	dbDig := pushImage(t, src, trivy.DefaultDBRef, trivy.DBTag, trivytest.DBArtifact(t, dbLayer))
	vulnDig := pushImage(t, src, "docker.io/example/vulnerable", "1.0", trivytest.VulnerableImage(t))
	publishSigned := func(name string, ings []spec.Ingredient) {
		raw := cookedRecipeYAML(t, name, "1.0.0", ings)
		d := publishRecipe(t, src.st, "docker.io/cookbook/"+name, "1.0.0", raw)
		signManifest(t, src.st, "docker.io/cookbook/"+name, d, kp)
	}
	publishSigned("tobby-system", []spec.Ingredient{{
		Name: "trivy-db", Kind: spec.IngredientOCIArtifact, Ref: trivy.DefaultDBRef, Version: trivy.DBTag,
		Digest: dbDig, ArtifactType: trivytest.DBConfigType,
	}})
	publishSigned("vulnerable", []spec.Ingredient{{
		Name: "app", Kind: spec.IngredientContainerImage, Ref: "docker.io/example/vulnerable", Version: "1.0", Digest: vulnDig,
	}})
	entries := []spec.RecipeSelector{{Name: "tobby-system", Version: "1.0.0"}, {Name: "vulnerable", Version: "1.0.0"}}
	subs := map[string]string{"docker.io": src.addr, "ghcr.io": src.addr}

	// 2. The medium, written by a mirror instance with an advisory policy.
	produced := openStore(t)
	source := New(produced, newRemotes(t, subs), trustFor(t, nil, kp),
		retrieverFile(t, servedZone, "docker.io/cookbook", entries), "", syncCfg())
	source.SetMediaManifest(func(ctx context.Context, z, runID string, at time.Time) error {
		_, err := media.Write(ctx, produced, media.WriteOptions{Zone: z, RunID: runID, ResolvedAt: at})
		return err
	})
	sourceScanner := scannerFor(t, produced, live)
	source.SetScan(&ScanSettings{Scanner: sourceScanner, Mode: scan.ModeWarn, Threshold: 7,
		Freshness: scan.Freshness{MaxAge: 168 * time.Hour}, Refresh: func(ctx context.Context) { sourceScanner.Refresh(ctx) }})
	task, err := runSync(t, source)
	if err != nil {
		t.Fatalf("writing the medium: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("writing the medium: %s", itemNames(task))
	}
	if live != nil {
		it := itemByName(t, task, "vulnerable@1.0.0/app")
		if it.Scan == nil || it.Scan.Policy.Verdict != scan.VerdictWarn || !slices.Contains(it.Scan.Policy.Violations, trivytest.KnownCVE) {
			t.Fatalf("the source-side scan = %+v", it.Scan)
		}
	}
	if err := produced.Close(); err != nil {
		t.Fatal(err)
	}

	// 3. The other side of the gap.
	carried := carryToAnotherHost(t, produced.Root())
	arrived, err := store.Open(context.Background(), carried, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = arrived.Close() })
	// The destination instance has its own scanner, and before the import
	// that scanner already reads the database the medium carries: it is
	// only a reader of the store it was started on.
	if st := scannerFor(t, arrived, nil).Refresh(context.Background()); st.Database == nil || st.Database.Digest != dbDig {
		t.Fatalf("the scanner on the medium does not see the database that crossed: %+v", st)
	}
	zoneRegistry := newDestRegistry(t)
	destination, _ := destinationFor(t, &medium{st: arrived, zone: servedZone}, zoneRegistry, servedZone, kp)
	imp, err := runImport(t, destination, MediaOptions{})
	if err != nil {
		t.Fatalf("importing the medium: %v", err)
	}
	for _, id := range []string{"tobby-system@1.0.0", "vulnerable@1.0.0"} {
		if it := itemByName(t, imp, "media/"+id); it.Status != tasks.StatusDone {
			t.Fatalf("%s = %s %+v", id, it.Status, it.Error)
		}
	}

	// 4. A passthrough instance inside the isolated zone. Its only
	// upstream is the zone registry; nothing else is reachable.
	zoneSubs := map[string]string{"docker.io": zoneRegistry.addr, "ghcr.io": zoneRegistry.addr}
	zoneEntries := []spec.RecipeSelector{{Name: "tobby-system", Version: "1.0.0"}, {Name: "vulnerable", Version: "1.0.0"}}
	for _, mode := range []scan.Mode{scan.ModeBlock, scan.ModeWarn} {
		t.Run(string(mode), func(t *testing.T) {
			zoneStore := openStore(t)
			inside := New(zoneStore, newRemotes(t, zoneSubs), trustFor(t, nil, kp),
				retrieverFile(t, servedZone, zoneRegistry.addr+"/"+config.DefaultCookbook, zoneEntries), "", syncCfg())
			downstream := newDestRegistry(t)
			withDestination(t, inside, downstream, config.Destination{}, nil)
			zoneScanner := scannerFor(t, zoneStore, live)
			inside.SetScan(&ScanSettings{Scanner: zoneScanner, Mode: mode, Threshold: 7,
				Freshness: scan.Freshness{MaxAge: 168 * time.Hour}, Refresh: func(ctx context.Context) { zoneScanner.Refresh(ctx) }})
			task, err := runSync(t, inside)
			if err != nil {
				t.Fatal(err)
			}
			st := zoneScanner.Status(context.Background())
			if st.Database == nil || st.Database.Digest != dbDig {
				t.Fatalf("the zone's scanner does not use the database that crossed: %+v", st)
			}
			if live == nil {
				if !st.Database.BuiltAt.Equal(builtAt) {
					t.Errorf("the database's build timestamp changed on the way: %v, want %v", st.Database.BuiltAt, builtAt)
				}
				// Without a binary every image is reported not scanned —
				// visibly — and under block it does not cross.
				it := itemByName(t, task, "vulnerable@1.0.0/app")
				if it.Scan == nil || it.Scan.Policy.Verdict != scan.VerdictNotScanned {
					t.Errorf("scan = %+v", it.Scan)
				}
				return
			}
			it := itemByName(t, task, "vulnerable@1.0.0/app")
			if it.Scan == nil || it.Scan.Database == nil || it.Scan.Database.Digest != dbDig || it.Scan.Database.AgeSeconds <= 0 {
				t.Fatalf("the report does not name the database that crossed, with its age: %+v", it.Scan)
			}
			if !slices.Contains(it.Scan.Policy.Violations, trivytest.KnownCVE) {
				t.Fatalf("%s not found: %+v", trivytest.KnownCVE, it.Scan.Policy)
			}
			// The finding is what the API serves: the task document, items[].scan.
			raw, err := json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"id":"`+trivytest.KnownCVE+`"`) {
				t.Error("the task document does not carry the finding")
			}
			push := itemByName(t, task, "push/vulnerable@1.0.0/recipe")
			_, _, _, pushedErr := downstream.st.RawManifest(context.Background(), "docker.io/example/vulnerable", "1.0")
			switch mode {
			case scan.ModeBlock:
				if push.Status != tasks.StatusFailed || push.Error.Code != taxonomy.CodeScanPolicyBlocked {
					t.Errorf("under block the recipe = %s %+v", push.Status, push.Error)
				}
				if pushedErr == nil {
					t.Error("the known-vulnerable image reached the registry under a blocking policy")
				}
			case scan.ModeWarn:
				if push.Status != tasks.StatusDone || pushedErr != nil {
					t.Errorf("advisory: the recipe = %s, image pushed: %v", push.Status, pushedErr)
				}
				if it.Scan.Policy.Verdict != scan.VerdictWarn {
					t.Errorf("verdict = %s", it.Scan.Policy.Verdict)
				}
			}
			if s := itemByName(t, task, "push/tobby-system@1.0.0/recipe").Status; s != tasks.StatusDone {
				t.Errorf("the database did not cross further down: %s", s)
			}
		})
	}
}

// scannerFor builds the adapter over st: with the real binary when
// the environment provides it, without one otherwise (the database side
// still works, the binary side is the visible degraded mode).
func scannerFor(t *testing.T, st *store.Store, live *trivytest.Env) *trivy.Scanner {
	t.Helper()
	opts := &trivy.Options{Store: st, WorkDir: t.TempDir(), Timeout: 5 * time.Minute,
		// No binary unless the real one is given: an empty PATH lookup and
		// a nonexistent image path, whatever host runs the test.
		LookPath:  func(string) (string, error) { return "", fmt.Errorf("no PATH in this test") },
		ImagePath: t.TempDir() + "/none",
		GOOS:      "darwin",
	}
	if live != nil {
		opts.Path, opts.Digest, opts.GOOS = live.Binary, trivytest.FileDigest(t, live.Binary), ""
	}
	s := trivy.New(opts)
	s.Refresh(context.Background())
	return s
}

// trivytestEnv returns the real-Trivy environment when there is one; a
// test that can also run without it gets nil instead of a skip — unless
// the environment requires it, in which case a missing binary fails.
func trivytestEnv(t *testing.T) *trivytest.Env {
	t.Helper()
	var env *trivytest.Env
	ok := t.Run("real-trivy-available", func(t *testing.T) {
		e := trivytest.Require(t)
		env = &e
	})
	if !ok {
		t.FailNow()
	}
	return env
}
