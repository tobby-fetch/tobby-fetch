// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"slices"
	"strings"
	"testing"
	"time"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Scanning by content, whatever the declared kind (ADR-0017, B-054),
// through the engine. The detection itself is tested in scan/detect and,
// against the store, in scan/trivy; here the scanner says what it found
// and the engine must apply the policy to it exactly as to an image.

// disguise makes the "model" OCI artifact of the scan environment an image
// by its content, carrying a 9.8 finding — and the declared image clean,
// so that only the disguised one can explain a refusal.
func disguise(env *scanEnv) {
	env.scanner.contents = map[string]*scan.Content{env.modelDig: {
		Declared: "OCIArtifact",
		Images:   []scan.DetectedImage{{Digest: env.modelDig, Evidence: scan.EvidenceImageConfig}},
	}}
	env.scanner.results[env.vulnDig] = scanResult()
	env.scanner.results[env.modelDig] = scanResult(scan.Finding{ID: "CVE-2024-0009", Severity: scan.SeverityCritical, CVSS: cvss(9.8), Package: "musl"})
}

func TestScanDisguisedArtifactUnderBlock(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Mode: scan.ModeBlock}})
	disguise(env)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatal(err)
	}
	m := itemByName(t, task, "vuln@1.0.0/model")
	if m.Scan == nil || m.Scan.Policy.Verdict != scan.VerdictBlock || !slices.Equal(m.Scan.Policy.Violations, []string{"CVE-2024-0009"}) {
		t.Fatalf("the disguised image's report = %+v", m.Scan)
	}
	if codes := reasonCodes(m.Scan); len(codes) == 0 || codes[0] != taxonomy.CodeScanDetectedImage {
		t.Errorf("the report does not say first that it was detected by content: %v", codes)
	}
	if m.Scan.Content == nil || m.Scan.Content.Declared != "OCIArtifact" || m.Scan.Content.Evidences()[0] != scan.EvidenceImageConfig {
		t.Errorf("content = %+v", m.Scan.Content)
	}
	// The scanner was asked about what Inspect found.
	var asked bool
	for _, tg := range env.scanner.targets {
		asked = asked || tg.Digest == env.modelDig && tg.Content.IsImage()
	}
	if !asked {
		t.Error("the disguised image was not handed to the scanner with its detected content")
	}
	// R-19: the recipe is withheld whole, nothing of it crosses.
	for _, n := range []string{"vuln@1.0.0/recipe", "push/vuln@1.0.0/model", "push/vuln@1.0.0/recipe"} {
		if got := itemByName(t, task, n); got.Status != tasks.StatusFailed || got.Error == nil || got.Error.Code != taxonomy.CodeScanPolicyBlocked {
			t.Errorf("%s = %s %+v", n, got.Status, got.Error)
		}
	}
	for _, w := range dest.writes() {
		if strings.Contains(w, "models/m") {
			t.Errorf("the destination received %s", w)
		}
	}
	if recorded(t, env.dst, "vuln") {
		t.Error("the recipe carrying the disguised image is in the recipe graph")
	}
}

func TestScanDisguisedArtifactAdvisory(t *testing.T) {
	env := newScanEnv(t, &scanEnvOpts{})
	disguise(env)
	dest := newDestRegistry(t)
	withDestination(t, env.eng, dest, config.Destination{}, nil)
	task, err := runSync(t, env.eng)
	if err != nil {
		t.Fatal(err)
	}
	m := itemByName(t, task, "vuln@1.0.0/model")
	if m.Scan == nil || m.Scan.Policy.Verdict != scan.VerdictWarn || !slices.Contains(reasonCodes(m.Scan), taxonomy.CodeScanDetectedImage) {
		t.Fatalf("report = %+v", m.Scan)
	}
	if s := itemByName(t, task, "push/vuln@1.0.0/recipe").Status; s != tasks.StatusDone {
		t.Errorf("the advisory default refused: %s", s)
	}
}

// A detected image that cannot be scanned — or content that cannot be
// judged — is refused under block, like any item that could not be
// scanned, and proceeds, reported, under warn.
func TestScanDetectedButNotScannable(t *testing.T) {
	unavailable := taxonomy.New(taxonomy.CodeScannerUnavailable, taxonomy.Params{"detail": "no trivy"})
	uninspectable := taxonomy.New(taxonomy.CodeScanFailed, taxonomy.Params{"reference": "x", "detail": "its content could not be inspected: truncated"})
	cases := map[string]struct {
		prepare  func(*scanEnv)
		code     taxonomy.Code
		detected bool
		modes    []scan.Mode
	}{
		"scanner unavailable": {func(e *scanEnv) { disguise(e); e.scanner.err = unavailable }, taxonomy.CodeScannerUnavailable, true, []scan.Mode{scan.ModeBlock, scan.ModeWarn}},
		// The advisory side of a not-scanned item is the case above.
		"content unreadable": {func(e *scanEnv) { e.scanner.inspectErr = uninspectable }, taxonomy.CodeScanFailed, false, []scan.Mode{scan.ModeBlock}},
	}
	for name, c := range cases {
		for _, mode := range c.modes {
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				env := newScanEnv(t, &scanEnvOpts{settings: ScanSettings{Mode: mode}})
				c.prepare(env)
				task, err := runSync(t, env.eng)
				if err != nil {
					t.Fatal(err)
				}
				m := itemByName(t, task, "vuln@1.0.0/model")
				if m.Scan == nil || m.Scan.Policy.Verdict != scan.VerdictNotScanned || !slices.Contains(reasonCodes(m.Scan), c.code) {
					t.Fatalf("report = %+v", m.Scan)
				}
				if got := slices.Contains(reasonCodes(m.Scan), taxonomy.CodeScanDetectedImage); got != c.detected {
					t.Errorf("detected note = %v", got)
				}
				if m.Scan.Content == nil || m.Scan.Content.Declared != "OCIArtifact" {
					t.Errorf("content = %+v", m.Scan.Content)
				}
				rec := itemByName(t, task, "vuln@1.0.0/recipe")
				if mode == scan.ModeBlock && rec.Status != tasks.StatusFailed {
					t.Errorf("under block the recipe = %s", rec.Status)
				}
				if mode == scan.ModeWarn && rec.Status != tasks.StatusDone {
					t.Errorf("advisory: the recipe = %s", rec.Status)
				}
			})
		}
	}
}

// The signed tobby-system recipe delivers the scanner itself — its binary
// as a FileSet that is an image by content. Under block, with findings
// and with a stale database, it still crosses: the deliveries that arm the
// scanner are evaluated advisory, and the report says why (TBY-SCN-025).
func TestScanSystemDeliveryIsAdvisory(t *testing.T) {
	src := newRegistry(t)
	dst := openStore(t)
	binDig := seedImage(t, src, "aquasecurity/trivy", "0.76.2")
	kp := newKeyPair(t)
	raw := cookedRecipeYAML(t, scan.SystemRecipe, "1.0.0", []spec.Ingredient{
		{Name: "trivy", Kind: spec.IngredientFileSet, Ref: src.addr + "/aquasecurity/trivy", Version: "0.76.2", Digest: binDig},
	})
	d := publishRecipe(t, src.st, "cookbook/"+scan.SystemRecipe, "1.0.0", raw)
	signManifest(t, src.st, "cookbook/"+scan.SystemRecipe, d, kp)
	retr := retrieverFile(t, testZone, src.addr+"/cookbook", []spec.RecipeSelector{{Name: scan.SystemRecipe, Version: "1.0.0"}})
	eng := New(dst, newRemotes(t, nil), trustFor(t, nil, kp), retr, "", syncCfg())
	fs := &fakeScanner{
		results: map[string]*scan.Result{binDig: scanResult(scan.Finding{ID: "CVE-2024-0010", Severity: scan.SeverityHigh, CVSS: cvss(7.5), Package: "stdlib"})},
		contents: map[string]*scan.Content{binDig: {Declared: "FileSet", Images: []scan.DetectedImage{
			{Digest: binDig, Evidence: scan.EvidenceImageConfig},
		}}},
	}
	eng.SetScan(&ScanSettings{Scanner: fs, Mode: scan.ModeBlock, Threshold: 7,
		Freshness: scan.Freshness{MaxAge: 1, Block: true}, Now: func() time.Time { return scanNow }})
	task, err := runSync(t, eng)
	if err != nil {
		t.Fatal(err)
	}
	it := itemByName(t, task, scan.SystemRecipe+"@1.0.0/trivy")
	if it.Scan == nil || it.Scan.Blocks() || it.Scan.Policy.Verdict != scan.VerdictWarn {
		t.Fatalf("the scanner's own delivery = %+v", it.Scan)
	}
	codes := reasonCodes(it.Scan)
	for _, want := range []taxonomy.Code{taxonomy.CodeScanDetectedImage, taxonomy.CodeScanPolicyAdvisory, taxonomy.CodeScanDBStale, taxonomy.CodeScanSystemAdvisory} {
		if !slices.Contains(codes, want) {
			t.Errorf("reasons %v lack %s", codes, want)
		}
	}
	if s := itemByName(t, task, scan.SystemRecipe+"@1.0.0/recipe").Status; s != tasks.StatusDone {
		t.Errorf("the tobby-system recipe = %s", s)
	}
	if !recorded(t, dst, scan.SystemRecipe) {
		t.Error("the tobby-system recipe was withheld: the scanner could never be armed")
	}
}
