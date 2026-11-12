// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// TestReducedAssuranceReachesEverySurfaceOfASynchronization is the FR-037
// acceptance on the source side, in one run: an unsigned recipe admitted
// by a declared scope and a signed one side by side. The first carries the
// marker on every item it reaches, on its log records and in the recipe
// graph; the second carries none anywhere.
func TestReducedAssuranceReachesEverySurfaceOfASynchronization(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	trusted := newKeyPair(t)
	publishRecipe(t, src.st, "cookbook/bare", "1.0.0", trustRecipeYAML(t, src, "bare", imgDig))
	goodDig := publishRecipe(t, src.st, "cookbook/good", "1.0.0", trustRecipeYAML(t, src, "good", imgDig))
	signManifest(t, src.st, "cookbook/good", goodDig, trusted)
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{
		{Name: "bare", Version: "1.0.0"}, {Name: "good", Version: "1.0.0"},
	})
	canonicalRepo := strings.ReplaceAll(src.addr, ":", "_") + "/cookbook/bare"
	tp := trustFor(t, []config.TrustScope{
		{Name: "lab", Repositories: []string{canonicalRepo}, AllowUnsigned: true},
	}, trusted)

	dst := openStore(t)
	var buf bytes.Buffer
	task := &tasks.Task{ID: "tsk_test", RunID: "run_test", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	if err := runSyncTask(t, New(dst, newRemotes(t, nil), tp, retr, "", syncCfg()), task,
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))); err != nil {
		t.Fatalf("run: %v", err)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Fatalf("aggregates = %+v (items %s)", agg, itemNames(task))
	}

	// Task items: every item of the unsigned recipe, none of the signed one.
	reducedItems := 0
	for i := range task.Items {
		it := &task.Items[i]
		switch {
		case strings.HasPrefix(it.Name, "bare@1.0.0"):
			reducedItems++
			if !it.Assurance.Reduced() || it.Assurance.Scope != "lab" ||
				len(it.Assurance.Reasons) != 1 || it.Assurance.Reasons[0] != assurance.ReasonUnsigned {
				t.Errorf("item %s carries %+v, want reduced/lab/unsigned", it.Name, it.Assurance)
			}
		case strings.HasPrefix(it.Name, "good@1.0.0"):
			if it.Assurance != nil {
				t.Errorf("item %s of a signed recipe carries %+v", it.Name, it.Assurance)
			}
		}
	}
	if reducedItems < 2 {
		t.Errorf("only %d items of the unsigned recipe were checked: %s", reducedItems, itemNames(task))
	}
	// The marker is part of the task document the API serves.
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"assurance":{"level":"reduced","scope":"lab","reasons":["unsigned"]}`) {
		t.Errorf("the task document does not carry the marker: %s", raw)
	}

	// Log records: the field is present on the unsigned recipe's records
	// and ONLY on them.
	marked, unmarkedOfBare := 0, 0
	sc := bufio.NewScanner(&buf)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		recipe, _ := rec[logging.KeyRecipe].(string)
		level, has := rec[logging.KeyAssurance]
		switch {
		case recipe == "good" && has:
			t.Errorf("a record of the signed recipe carries the field: %v", rec)
		case recipe == "bare" && rec["msg"] == "recipe verified":
			if level != "reduced" || rec[logging.KeyAssuranceScope] != "lab" {
				t.Errorf("recipe verified record = %v, want assurance=reduced assurance_scope=lab", rec)
			}
			marked++
		case recipe == "bare" && !has:
			unmarkedOfBare++
			t.Errorf("a record of the unsigned recipe lacks the field: %v", rec)
		}
	}
	if marked == 0 || unmarkedOfBare != 0 {
		t.Errorf("marked=%d unmarkedOfBare=%d", marked, unmarkedOfBare)
	}

	// The recipe graph, which the API and the media manifest read.
	recs, err := dst.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	for i := range recs {
		switch recs[i].Name {
		case "bare":
			if !recs[i].Assurance.Reduced() || recs[i].Assurance.Scope != "lab" {
				t.Errorf("bare record = %+v, want the reduced marker", recs[i].Assurance)
			}
		case "good":
			if recs[i].Assurance != nil {
				t.Errorf("good record = %+v, want full assurance", recs[i].Assurance)
			}
		}
	}
}

// unsignMedium removes a delivery's signature tags from the medium and
// re-writes the manifest, so the medium genuinely carries an unsigned
// recipe the way a source that admitted it under a scope would have
// produced it.
func unsignMedium(t *testing.T, m *medium, id, admittedUnder string) {
	t.Helper()
	ctx := context.Background()
	name, _, _ := strings.Cut(id, "@")
	repo := "docker.io/cookbook/" + name
	hexPart := strings.TrimPrefix(m.recipeDigest[id], "sha256:")
	if err := m.st.DeleteTag(ctx, repo, "sha256-"+hexPart+".sig"); err != nil {
		t.Fatalf("removing the signature: %v", err)
	}
	if admittedUnder != "" {
		records, err := m.st.RecipeRecords()
		if err != nil {
			t.Fatal(err)
		}
		for i := range records {
			if records[i].Name == name {
				records[i].Verified, records[i].TrustScope = false, admittedUnder
				if err := m.st.PutRecipeRecord(&records[i]); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := media.Write(ctx, m.st, media.WriteOptions{Zone: m.zone, RunID: "run_unsigned", ResolvedAt: m.resolvedAt}); err != nil {
		t.Fatal(err)
	}
}

// TestMediaImportKeepsTheDestinationsOwnMarkerAndTheSourcesClaim is the
// FR-037 destination half, played through the whole journey: the marker
// of an item is THIS instance's own verdict, the source's claim is shown
// beside it, and nothing reaches the destination registry but the content
// itself (bit-exact, RECIPE-SPEC §7.1).
func TestMediaImportKeepsTheDestinationsOwnMarkerAndTheSourcesClaim(t *testing.T) {
	kp := newKeyPair(t)

	t.Run("the source relaxed, this instance verifies against a root", func(t *testing.T) {
		m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})
		recs, err := m.st.RecipeRecords()
		if err != nil || len(recs) != 1 {
			t.Fatalf("records = %v, %v", recs, err)
		}
		recs[0].Verified, recs[0].TrustScope = false, "source-lab"
		if err := m.st.PutRecipeRecord(&recs[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := media.Write(context.Background(), m.st, media.WriteOptions{Zone: m.zone, RunID: "run_x", ResolvedAt: m.resolvedAt}); err != nil {
			t.Fatal(err)
		}
		eng, _ := destinationFor(t, m, nil, "zone-alpha", kp)
		rep, err := eng.VerifyMedia(context.Background(), discardLogger(), MediaOptions{})
		if err != nil {
			t.Fatal(err)
		}
		v := rep.Recipes[0]
		if !v.Pushable || v.Assurance != nil {
			t.Errorf("verdict = pushable %v, own marker %+v: this instance verified the signature itself", v.Pushable, v.Assurance)
		}
		if !v.SourceAssurance.Reduced() || v.SourceAssurance.Scope != "source-lab" {
			t.Errorf("source claim = %+v, want the manifest's marker re-displayed", v.SourceAssurance)
		}
	})

	t.Run("the source claims nothing, this instance relaxes", func(t *testing.T) {
		dest := newDestRegistry(t)
		m := seedMedium(t, kp, mediumRecipe{name: "alpine", version: "3.22.1"})
		unsignMedium(t, m, "alpine@3.22.1", "")

		imports, err := media.OpenImports(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		tp := trustFor(t, []config.TrustScope{
			{Name: "zone-lab", Repositories: []string{"docker.io/cookbook/*"}, AllowUnsigned: true},
		})
		eng := New(m.st, newRemotes(t, nil), tp, "", "", syncCfg())
		eng.SetMediaImport("zone-alpha", imports)
		withDestination(t, eng, dest, config.Destination{}, nil)

		var buf bytes.Buffer
		task := &tasks.Task{ID: "tsk_media", RunID: "run_media", Type: tasks.TypeMediaImport, Status: tasks.StatusRunning}
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		var rep *media.Report
		if err := eng.importMedia(context.Background(), newTaskSink(task, func() {}), logger,
			MediaOptions{Report: func(r *media.Report) { rep = r }}); err != nil {
			t.Fatalf("import: %v", err)
		}
		v := rep.Recipes[0]
		if !v.Assurance.Reduced() || v.Assurance.Scope != "zone-lab" {
			t.Fatalf("own marker = %+v, want reduced under zone-lab: a manifest saying nothing never hides it", v.Assurance)
		}
		if v.SourceAssurance != nil {
			t.Errorf("source claim = %+v, want none", v.SourceAssurance)
		}
		marked := 0
		for i := range task.Items {
			it := &task.Items[i]
			if !strings.Contains(it.Name, "alpine@3.22.1") {
				continue
			}
			marked++
			if !it.Assurance.Reduced() || it.Assurance.Scope != "zone-lab" {
				t.Errorf("item %s carries %+v, want the reduced marker", it.Name, it.Assurance)
			}
		}
		if marked < 2 {
			t.Errorf("only %d items of the delivery were checked: %s", marked, itemNames(task))
		}
		if !strings.Contains(buf.String(), `"msg":"delivery under reduced assurance"`) ||
			!strings.Contains(buf.String(), `"assurance":"reduced"`) {
			t.Errorf("the medium's log does not name the delivery under reduced assurance:\n%s", buf.String())
		}

		// Bit-exact: the content the destination holds is the content the
		// medium carried, and no repository holds any marker artifact —
		// the zone cookbook carries the recipe and its signature tags
		// only, exactly what a promotion without a marker would leave.
		tags := destTags(t, dest, cookbookRepoOf("alpine"))
		for _, tag := range tags {
			if strings.Contains(strings.ToLower(tag), "assur") {
				t.Errorf("the destination registry holds a marker-like tag %q (RECIPE-SPEC §7.1)", tag)
			}
		}
		for _, w := range dest.writes() {
			if strings.Contains(strings.ToLower(w), "assur") {
				t.Errorf("a write to the destination mentions assurance: %s", w)
			}
		}
	})
}

// TestMediaImportAppliesTheDestinationScopeToACookbookWithAPort is the
// regression of B-055 (crucible m6 c-assurance, step 8): a medium produced
// from a cookbook whose host carries a port, carrying a recipe admitted
// unsigned at the source, imported by a destination whose OWN
// allowUnsigned scope covers it. The scope is written in the documented
// pattern space — the canonical nominal repository, port folded to "_"
// (ADR-0013) — which verification matches. The push re-verification
// (FR-033) matched the raw "host:port" spelling instead, found no scope,
// and refused the recipe in TBY-SIG-001 with an empty scope, right after
// the report had called it pushable.
//
// A docker.io medium cannot show it: there the two spellings coincide.
func TestMediaImportAppliesTheDestinationScopeToACookbookWithAPort(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	if !strings.Contains(src.addr, ":") {
		t.Fatalf("the source %q has no port: the two spellings coincide and nothing is proved", src.addr)
	}
	recipeDig := publishRecipe(t, src.st, "cookbook/lab-app", "1.0.0", trustRecipeYAML(t, src, "lab-app", imgDig))
	canonical := strings.ReplaceAll(src.addr, ":", "_") + "/cookbook/lab-*"
	ctx := context.Background()

	// Source side: a mirror synchronization onto the medium, admitting the
	// unsigned recipe under the source's scope, then the manifest.
	retr := retrieverFile(t, servedZone, src.addr+"/cookbook", []spec.RecipeSelector{{Name: "lab-app", Version: "1.0.0"}})
	onMedium := openStore(t)
	source := New(onMedium, newRemotes(t, nil), trustFor(t, []config.TrustScope{
		{Name: "lab", Repositories: []string{canonical}, AllowUnsigned: true},
	}, newKeyPair(t)), retr, "", syncCfg())
	wrote := false
	source.SetMediaManifest(func(ctx context.Context, z, runID string, at time.Time) error {
		wrote = true
		_, err := media.Write(ctx, onMedium, media.WriteOptions{Zone: z, RunID: runID, ResolvedAt: at})
		return err
	})
	if task, err := runSync(t, source); err != nil || task.Aggregate().Failed != 0 {
		t.Fatalf("producing the medium: %v (items %s)", err, itemNames(task))
	}
	if !wrote {
		t.Fatal("the source wrote no media manifest")
	}

	// Destination side: its own root, its own relaxed scope.
	dest := newDestRegistry(t)
	imports, err := media.OpenImports(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := New(onMedium, newRemotes(t, nil), trustFor(t, []config.TrustScope{
		{Name: "dest-lab", Repositories: []string{canonical}, AllowUnsigned: true},
	}, newKeyPair(t)), "", "", syncCfg())
	eng.SetMediaImport(servedZone, imports)
	withDestination(t, eng, dest, config.Destination{}, nil)

	task := &tasks.Task{ID: "tsk_media", RunID: "run_media", Type: tasks.TypeMediaImport, Status: tasks.StatusRunning}
	var rep *media.Report
	if err := eng.importMedia(ctx, newTaskSink(task, func() {}), discardLogger(),
		MediaOptions{Report: func(r *media.Report) { rep = r }}); err != nil {
		t.Fatalf("import: %v (items %s)", err, itemNames(task))
	}
	if rep == nil || len(rep.Recipes) != 1 || !rep.Recipes[0].Pushable {
		t.Fatalf("verification report = %+v, want lab-app pushable", rep)
	}
	marked := 0
	for i := range task.Items {
		it := &task.Items[i]
		if it.Status == tasks.StatusFailed {
			t.Errorf("item %s failed: %+v", it.Name, it.Error)
		}
		if !strings.Contains(it.Name, "lab-app@1.0.0") {
			continue
		}
		marked++
		if !it.Assurance.Reduced() || it.Assurance.Scope != "dest-lab" {
			t.Errorf("item %s carries %+v, want reduced under dest-lab", it.Name, it.Assurance)
		}
	}
	if marked < 2 {
		t.Errorf("only %d items of the delivery were checked: %s", marked, itemNames(task))
	}

	// The recipe landed, bit-exact, in the zone cookbook (RECIPE-SPEC §7.1).
	repo := cookbookRepoOf("lab-app")
	if tags := destTags(t, dest, repo); len(tags) != 1 || tags[0] != "1.0.0" {
		t.Fatalf("zone cookbook tags = %v, want the recipe's version only", tags)
	}
	if _, _, got, err := dest.st.RawManifest(ctx, repo, "1.0.0"); err != nil || got != recipeDig {
		t.Errorf("zone recipe digest = %s (%v), want %s", got, err, recipeDig)
	}
}

// TestPassthroughPromotesAnUnsignedRecipeAdmittedByAScopeOnACookbookWithAPort
// is the other face of B-055, the one a v0.5.x passthrough instance could
// meet: the synchronization admits an unsigned recipe under a scope written
// in the canonical pattern space, and the promotion that follows in the
// same cycle must re-verify it under that same scope, not refuse it.
func TestPassthroughPromotesAnUnsignedRecipeAdmittedByAScopeOnACookbookWithAPort(t *testing.T) {
	src, imgDig := seedTrustCookbook(t)
	if !strings.Contains(src.addr, ":") {
		t.Fatalf("the source %q has no port: the two spellings coincide and nothing is proved", src.addr)
	}
	publishRecipe(t, src.st, "cookbook/bare", "1.0.0", trustRecipeYAML(t, src, "bare", imgDig))
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{{Name: "bare", Version: "1.0.0"}})
	tp := trustFor(t, []config.TrustScope{
		{Name: "lab", Repositories: []string{strings.ReplaceAll(src.addr, ":", "_") + "/cookbook/bare"}, AllowUnsigned: true},
	}, newKeyPair(t))
	eng := New(openStore(t), newRemotes(t, nil), tp, retr, "", syncCfg())
	dest := newDestRegistry(t)
	withDestination(t, eng, dest, config.Destination{}, nil)

	task, err := runSync(t, eng)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	it := itemByName(t, task, pushItemPrefix+"bare@1.0.0/recipe")
	if it.Status != tasks.StatusDone {
		t.Fatalf("push item = %s (error %+v), want done under the lab scope", it.Status, it.Error)
	}
	if agg := task.Aggregate(); agg.Failed != 0 {
		t.Errorf("aggregates = %+v (items %s)", agg, itemNames(task))
	}
}
