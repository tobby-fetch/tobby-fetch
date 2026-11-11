// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Vulnerability scanning at retrieval (FR-031, FR-032 — milestone 6),
// through the engine: real registries on both ends, a scanner that answers
// what the policy needs to see. The real scanner is exercised in
// internal/scan/trivy and, end to end, in scan_e2e_test.go.

// fakeScanner answers by digest; an unlisted digest is clean.
type fakeScanner struct {
	mu      sync.Mutex
	results map[string]*scan.Result
	err     error
	calls   []string
}

func (f *fakeScanner) Scan(_ context.Context, t scan.Target) (*scan.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, t.Repository+"@"+t.Digest)
	if f.err != nil {
		return nil, f.err
	}
	if r, ok := f.results[t.Digest]; ok {
		return r, nil
	}
	return scanResult(), nil
}

func (f *fakeScanner) Status(context.Context) scan.Status {
	return scan.Status{Available: f.err == nil}
}

var scanNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func scanResult(findings ...scan.Finding) *scan.Result {
	return &scan.Result{
		Scanner:   scan.Info{Name: "trivy", Version: "0.76.2", Digest: "sha256:bin"},
		ScannedAt: scanNow,
		Database:  scan.Database{Version: "2", Digest: "sha256:db", BuiltAt: scanNow.Add(-24 * time.Hour)},
		Findings:  findings,
	}
}

func cvss(v float64) *float64 { return &v }

// scanEnv: two signed recipes in one cookbook — "vuln" (a container image
// carrying CVE-2024-0001 at 9.8 and CVE-2024-0002 at 6.1, plus an OCI
// artifact that is not scanned) and "clean" (a container image without
// findings).
type scanEnv struct {
	src      *registry
	dst      *store.Store
	eng      *Engine
	scanner  *fakeScanner
	vulnDig  string
	cleanDig string
	hostRepo string
	retr     string
}

type scanEnvOpts struct {
	policy     *spec.ScanPolicy
	annotation string // recipe.tobby.dev/scan-threshold on "vuln"
	settings   ScanSettings
}

func newScanEnv(t *testing.T, o *scanEnvOpts) *scanEnv {
	t.Helper()
	src := newRegistry(t)
	dst := openStore(t)
	vulnDig := seedImage(t, src, "images/vuln", "1.0")
	cleanDig := seedImage(t, src, "images/clean", "1.0")
	modelDig := seedArtifact(t, src, "models/m", "1.0", "application/vnd.example.model.v1+json")
	kp := newKeyPair(t)
	publish := func(name string, ann map[string]string, ings []spec.Ingredient) {
		r := spec.Recipe{
			APIVersion: spec.APIVersion, Kind: spec.KindRecipe,
			Metadata: spec.Metadata{Name: name, Version: "1.0.0", Annotations: ann},
			Spec:     spec.RecipeSpec{Ingredients: ings},
		}
		raw, err := yaml.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		d := publishRecipe(t, src.st, "cookbook/"+name, "1.0.0", raw)
		signManifest(t, src.st, "cookbook/"+name, d, kp)
	}
	var ann map[string]string
	if o.annotation != "" {
		ann = map[string]string{spec.AnnotationScanThreshold: o.annotation}
	}
	publish("vuln", ann, []spec.Ingredient{
		{Name: "app", Kind: spec.IngredientContainerImage, Ref: src.addr + "/images/vuln", Version: "1.0", Digest: vulnDig},
		{Name: "model", Kind: spec.IngredientOCIArtifact, Ref: src.addr + "/models/m", Version: "1.0", Digest: modelDig,
			ArtifactType: "application/vnd.example.model.v1+json"},
	})
	publish("clean", nil, []spec.Ingredient{
		{Name: "app", Kind: spec.IngredientContainerImage, Ref: src.addr + "/images/clean", Version: "1.0", Digest: cleanDig},
	})
	r := spec.Retriever{
		APIVersion: spec.APIVersion, Kind: spec.KindRetriever,
		Metadata: spec.Metadata{Name: testZone},
		Spec: spec.RetrieverSpec{Cookbook: src.addr + "/cookbook", ScanPolicy: o.policy, Recipes: []spec.RecipeSelector{
			{Name: "vuln", Version: "1.0.0"}, {Name: "clean", Version: "1.0.0"},
		}},
	}
	raw, err := yaml.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	retr := writeTempFile(t, "retriever.yaml", raw)
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())
	fs := &fakeScanner{results: map[string]*scan.Result{
		vulnDig: scanResult(
			scan.Finding{ID: "CVE-2024-0001", Severity: scan.SeverityCritical, CVSS: cvss(9.8), Package: "openssl", InstalledVersion: "1.0", FixedVersion: "1.1"},
			scan.Finding{ID: "CVE-2024-0002", Severity: scan.SeverityMedium, CVSS: cvss(6.1), Package: "zlib", InstalledVersion: "1.2"},
		),
	}}
	s := o.settings
	if s.Mode == "" {
		s.Mode = scan.ModeWarn
	}
	if s.Threshold == 0 {
		s.Threshold = 7
	}
	s.Scanner = fs
	s.Now = func() time.Time { return scanNow }
	if s.Freshness.MaxAge == 0 {
		s.Freshness.MaxAge = 168 * time.Hour
	}
	eng.SetScan(&s)
	return &scanEnv{src: src, dst: dst, eng: eng, scanner: fs, vulnDig: vulnDig, cleanDig: cleanDig,
		hostRepo: strings.ReplaceAll(src.addr, ":", "_"), retr: retr}
}

func reasonCodes(r *scan.Report) []taxonomy.Code {
	var out []taxonomy.Code
	for _, rs := range r.Policy.Reasons {
		out = append(out, rs.Code)
	}
	return out
}

func recorded(t *testing.T, st *store.Store, name string) bool {
	t.Helper()
	recs, err := st.RecipeRecords()
	if err != nil {
		t.Fatal(err)
	}
	for i := range recs {
		if recs[i].Name == name {
			return true
		}
	}
	return false
}

// TestScanAdvisoryDefaultPushesAndReports is the SRS acceptance of the
// default: the violating image is pushed, the finding is in the task
// report, the verdict says advisory.
func TestScanAdvisoryDefaultPushesAndReports(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{})
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatal(err)
	}
	it := itemByName(t, task, "vuln@1.0.0/app")
	if it.Scan == nil || it.Scan.Policy.Verdict != scan.VerdictWarn {
		t.Fatalf("scan report = %+v", it.Scan)
	}
	if !slices.Equal(it.Scan.Policy.Violations, []string{"CVE-2024-0001"}) || it.Scan.Counts.Critical != 1 {
		t.Errorf("violations = %v counts = %+v", it.Scan.Policy.Violations, it.Scan.Counts)
	}
	if it.Scan.Database == nil || it.Scan.Database.AgeSeconds != 86400 {
		t.Errorf("the report states no database age: %+v", it.Scan.Database)
	}
	if !slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanPolicyAdvisory) {
		t.Errorf("reasons = %v", reasonCodes(it.Scan))
	}
	// Not scanned: the OCI artifact carries no scan section.
	if m := itemByName(t, task, "vuln@1.0.0/model"); m.Scan != nil {
		t.Errorf("an OCI artifact was scanned: %+v", m.Scan)
	}
	for _, n := range []string{"push/vuln@1.0.0/app", "push/vuln@1.0.0/recipe", "push/clean@1.0.0/recipe"} {
		if s := itemByName(t, task, n).Status; s != tasks.StatusDone {
			t.Errorf("%s = %s; the advisory policy pushes", n, s)
		}
	}
	if env.eng.ScanPolicyBlocking() {
		t.Error("the default policy reads as blocking")
	}
}

// TestScanBlockRefusesTheRecipeWhole: with block at 7.0 the 9.8 finding
// refuses the image, the recipe is withheld whole — nothing of it pushed,
// not in the local cookbook, not in the recipe graph — and its neighbour
// crosses.
func TestScanBlockRefusesTheRecipeWhole(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Mode: scan.ModeBlock}})
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)
	var refused []string
	env.eng.SetMeters(Meters{PushRefused: func(code string) { refused = append(refused, code) }})
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatal(err)
	}
	it := itemByName(t, task, "vuln@1.0.0/app")
	if it.Scan == nil || it.Scan.Policy.Verdict != scan.VerdictBlock || !it.Scan.Blocks() {
		t.Fatalf("scan report = %+v", it.Scan)
	}
	for _, n := range []string{"vuln@1.0.0/recipe", "push/vuln@1.0.0/app", "push/vuln@1.0.0/recipe"} {
		got := itemByName(t, task, n)
		if got.Status != tasks.StatusFailed || got.Error == nil || got.Error.Code != taxonomy.CodeScanPolicyBlocked {
			t.Errorf("%s = %s %+v; want failed with %s", n, got.Status, got.Error, taxonomy.CodeScanPolicyBlocked)
		}
	}
	for _, it := range task.Items {
		if it.Name == "push/vuln@1.0.0/model" {
			t.Errorf("an ingredient of a withheld recipe was pushed: %+v", it)
		}
	}
	if got := itemByName(t, task, "push/clean@1.0.0/recipe").Status; got != tasks.StatusDone {
		t.Errorf("the clean recipe = %s; R-19: one refusal does not stop its neighbour", got)
	}
	for _, w := range dest.writes() {
		if strings.Contains(w, "images/vuln") || strings.Contains(w, "cookbook/vuln") || strings.Contains(w, "models/m") {
			t.Errorf("the destination received %s", w)
		}
	}
	if recorded(t, env.dst, "vuln") || !recorded(t, env.dst, "clean") {
		t.Error("the recipe graph does not reflect the refusal")
	}
	if _, ok := env.dst.ResolveTag(context.Background(), env.hostRepo+"/cookbook/vuln", "1.0.0"); ok {
		t.Error("the withheld recipe is in the local cookbook: a zone below could resolve it")
	}
	if !slices.Contains(refused, string(taxonomy.CodeScanPolicyBlocked)) {
		t.Errorf("refusal meters = %v", refused)
	}
	if !env.eng.ScanPolicyBlocking() {
		t.Error("the blocking policy reads as advisory")
	}
}

// TestScanRetrieverLevel: a Retriever threshold is blocking for its zone
// even on an advisory instance; a Retriever exception lets the finding
// cross, and the report names it.
func TestScanRetrieverLevel(t *testing.T) {
	seven := 7.0
	t.Run("threshold blocks", func(t *testing.T) {
		env := newScanEnv(t, &scanEnvOpts{policy: &spec.ScanPolicy{Threshold: &seven}})
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		it := itemByName(t, task, "vuln@1.0.0/app")
		p := it.Scan.Policy.Policy
		if it.Scan.Policy.Verdict != scan.VerdictBlock || p.ModeLevel != scan.LevelRetriever || p.ThresholdLevel != scan.LevelRetriever {
			t.Errorf("evaluation = %+v", it.Scan.Policy)
		}
		if !env.eng.ScanPolicyBlocking() {
			t.Error("the zone's threshold does not read as blocking")
		}
	})
	t.Run("exception lets it cross", func(t *testing.T) {
		env := newScanEnv(t, &scanEnvOpts{
			settings: ScanSettings{Mode: scan.ModeBlock},
			policy:   &spec.ScanPolicy{Exceptions: []spec.ScanException{{ID: "CVE-2024-0001", Justification: "not reachable"}}},
		})
		dest := newDestRegistry(t)
		withDestination(t, env.eng, dest, config.Destination{}, nil)
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		it := itemByName(t, task, "vuln@1.0.0/app")
		if it.Scan.Policy.Verdict != scan.VerdictPass || len(it.Scan.Policy.ExceptionsApplied) != 1 ||
			it.Scan.Policy.ExceptionsApplied[0].Level != scan.LevelRetriever || it.Scan.Policy.ExceptionsApplied[0].Justification != "not reachable" {
			t.Errorf("evaluation = %+v", it.Scan.Policy)
		}
		if s := itemByName(t, task, "push/vuln@1.0.0/recipe").Status; s != tasks.StatusDone {
			t.Errorf("the excepted recipe = %s", s)
		}
	})
	t.Run("expired exception does not apply", func(t *testing.T) {
		env := newScanEnv(t, &scanEnvOpts{
			settings: ScanSettings{Mode: scan.ModeBlock},
			policy:   &spec.ScanPolicy{Exceptions: []spec.ScanException{{ID: "CVE-2024-0001", Expires: "2026-01-01T00:00:00Z"}}},
		})
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		it := itemByName(t, task, "vuln@1.0.0/app")
		if it.Scan.Policy.Verdict != scan.VerdictBlock || !slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanExceptionExpired) {
			t.Errorf("evaluation = %+v", it.Scan.Policy)
		}
	})
}

// TestScanRecipeAnnotation is the SRS acceptance of the recipe level: a
// recipe annotated "5.0" blocks the 6.1 finding; one annotated "9.0" keeps
// the zone at 7.0 and the report says the annotation was ignored.
func TestScanRecipeAnnotation(t *testing.T) {
	t.Run("5.0 hardens", func(t *testing.T) {
		env := newScanEnv(t, &scanEnvOpts{annotation: "5.0", settings: ScanSettings{Mode: scan.ModeBlock}})
		env.scanner.results[env.vulnDig] = scanResult(scan.Finding{ID: "CVE-2024-0002", Severity: scan.SeverityMedium, CVSS: cvss(6.1), Package: "zlib"})
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		it := itemByName(t, task, "vuln@1.0.0/app")
		if it.Scan.Policy.Verdict != scan.VerdictBlock || it.Scan.Policy.Policy.ThresholdLevel != scan.LevelRecipe {
			t.Errorf("evaluation = %+v", it.Scan.Policy)
		}
		// The annotation is the recipe's own: its neighbour stays at 7.0.
		if c := itemByName(t, task, "clean@1.0.0/app"); c.Scan.Policy.Policy.Threshold != 7 {
			t.Errorf("the annotation leaked to another recipe: %+v", c.Scan.Policy.Policy)
		}
	})
	t.Run("9.0 is ignored and reported", func(t *testing.T) {
		env := newScanEnv(t, &scanEnvOpts{annotation: "9.0", settings: ScanSettings{Mode: scan.ModeBlock}})
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		it := itemByName(t, task, "vuln@1.0.0/app")
		p := it.Scan.Policy.Policy
		if it.Scan.Policy.Verdict != scan.VerdictBlock || p.Threshold != 7 || p.IgnoredRecipeThreshold == nil || *p.IgnoredRecipeThreshold != 9 {
			t.Errorf("evaluation = %+v", it.Scan.Policy)
		}
		if !slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanRecipeThresholdIgnored) {
			t.Errorf("reasons = %v", reasonCodes(it.Scan))
		}
	})
	t.Run("an annotation makes an advisory instance block that recipe", func(t *testing.T) {
		env := newScanEnv(t, &scanEnvOpts{annotation: "8.0"})
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		if it := itemByName(t, task, "vuln@1.0.0/app"); it.Scan.Policy.Verdict != scan.VerdictBlock || it.Scan.Policy.Policy.ModeLevel != scan.LevelRecipe {
			t.Errorf("evaluation = %+v", it.Scan.Policy)
		}
		if it := itemByName(t, task, "clean@1.0.0/recipe"); it.Status != tasks.StatusDone {
			t.Errorf("the neighbour = %s", it.Status)
		}
	})
}

// TestScanDegradedMode: an unavailable scanner reports every image not
// scanned with the reason; advisory lets it through, block refuses it.
func TestScanDegradedMode(t *testing.T) {
	cause := taxonomy.New(taxonomy.CodeScannerUnavailable, taxonomy.Params{"detail": "no trivy"})
	for _, mode := range []scan.Mode{scan.ModeWarn, scan.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Mode: mode}})
			env.scanner.err = cause
			task, err := runSync(t, env.eng)
			if err != nil {
				t.Fatal(err)
			}
			for _, rid := range []string{"vuln@1.0.0", "clean@1.0.0"} {
				it := itemByName(t, task, rid+"/app")
				if it.Scan == nil || it.Scan.Policy.Verdict != scan.VerdictNotScanned || reasonCodes(it.Scan)[0] != taxonomy.CodeScannerUnavailable {
					t.Fatalf("%s scan = %+v", rid, it.Scan)
				}
				rec := itemByName(t, task, rid+"/recipe")
				if mode == scan.ModeBlock {
					if rec.Status != tasks.StatusFailed || rec.Error.Code != taxonomy.CodeScannerUnavailable {
						t.Errorf("%s recipe = %s %+v; under block nothing unscanned crosses", rid, rec.Status, rec.Error)
					}
				} else if rec.Status != tasks.StatusDone {
					t.Errorf("%s recipe = %s; advisory proceeds", rid, rec.Status)
				}
			}
		})
	}
}

// TestScanStaleDatabase: beyond scan.maxDbAge, warn reports and proceeds;
// block refuses with the dedicated code whatever the policy mode.
func TestScanStaleDatabase(t *testing.T) {
	for _, block := range []bool{false, true} {
		env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Freshness: scan.Freshness{MaxAge: time.Hour, Block: block}}})
		task, err := runSync(t, env.eng)
		if err != nil {
			t.Fatal(err)
		}
		it := itemByName(t, task, "clean@1.0.0/app")
		if !it.Scan.Database.Stale || !slices.Contains(reasonCodes(it.Scan), taxonomy.CodeScanDBStale) {
			t.Errorf("block=%v: stale not reported: %+v", block, it.Scan)
		}
		rec := itemByName(t, task, "clean@1.0.0/recipe")
		switch {
		case block && (rec.Status != tasks.StatusFailed || rec.Error.Code != taxonomy.CodeScanDBStale):
			t.Errorf("block: clean recipe = %s %+v", rec.Status, rec.Error)
		case !block && rec.Status != tasks.StatusDone:
			t.Errorf("warn: clean recipe = %s", rec.Status)
		}
	}
}

// TestScanMirrorWithdrawsAnEarlierRecord: in mirror mode the media
// manifest is built from the recipe graph, so a recipe recorded by an
// earlier run and refused now is withdrawn from it — the medium written
// next does not deliver it.
func TestScanMirrorWithdrawsAnEarlierRecord(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{})
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	if !recorded(t, env.dst, "vuln") {
		t.Fatal("advisory run did not record the recipe")
	}
	env.eng.scan.Mode = scan.ModeBlock
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	if recorded(t, env.dst, "vuln") {
		t.Error("the refused recipe is still in the graph a medium is written from")
	}
	if !recorded(t, env.dst, "clean") {
		t.Error("the clean recipe was withdrawn too")
	}
}

func TestScanRefreshAfterSystemRecipe(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{})
	var refreshed int
	env.eng.scan.Refresh = func(context.Context) { refreshed++ }
	if _, err := runSync(t, env.eng); err != nil {
		t.Fatal(err)
	}
	// "vuln" carries an OCI artifact, "clean" only an image.
	if refreshed != 1 {
		t.Errorf("refreshed %d times; want once, after the recipe carrying an artifact", refreshed)
	}
}

func TestScanSettingsFromConfig(t *testing.T) {
	c := config.Default().Scan
	c.Mode = config.ScanModeBlock
	c.MaxDBAgeAction = config.ScanModeBlock
	c.Exceptions = []config.ScanException{{ID: "CVE-2024-1", Justification: "j"}}
	s := ScanSettingsFrom(&c, nil)
	if s.Mode != scan.ModeBlock || s.Threshold != 7 || !s.Freshness.Block || s.Freshness.MaxAge != 168*time.Hour ||
		len(s.Exceptions) != 1 || s.Exceptions[0].Level != scan.LevelInstance {
		t.Errorf("settings = %+v", s)
	}
}

func TestRecipeThresholdParsing(t *testing.T) {
	r := &spec.Recipe{}
	if v, ok := recipeThreshold(r); v != nil || !ok {
		t.Error("an absent annotation is a threshold")
	}
	r.Metadata.Annotations = map[string]string{spec.AnnotationScanThreshold: " 6.5 "}
	if v, ok := recipeThreshold(r); v == nil || *v != 6.5 || !ok {
		t.Errorf("6.5 parsed as %v %v", v, ok)
	}
	r.Metadata.Annotations[spec.AnnotationScanThreshold] = "eleven"
	if v, ok := recipeThreshold(r); v == nil || *v != 0 || ok {
		t.Errorf("an unparsable annotation must apply as 0 and be reported: %v %v", v, ok)
	}
	if z := zonePolicyOf(&spec.ScanPolicy{Exceptions: []spec.ScanException{{ID: "CVE-1", Expires: "garbage"}}}); !z.exceptions[0].ExpiredAt(scanNow) {
		t.Error("an unparsable expiry does not read as expired")
	}
}
