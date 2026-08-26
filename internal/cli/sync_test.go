// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The command-line half of plan mode (FR-055 amendment R-04, FR-066).
//
// The exit codes are the contract a CI gate depends on, so they are
// tested as the mapping the process actually performs — exitCodeFor over
// the value the command returns — not as a table of constants.

// TestPlanExitCodesAreDistinct is the requirement: "exit codes SHALL
// distinguish nothing to do, changes planned, and refused by policy".
// Two outcomes sharing a code would make the gate unusable, so the test
// asserts distinctness as such rather than three separate equalities.
func TestPlanExitCodesAreDistinct(t *testing.T) {
	cases := map[engine.Outcome]int{
		engine.OutcomeUpToDate:           taxonomy.ExitOK,
		engine.OutcomeChangesPlanned:     taxonomy.ExitChangesPlanned,
		engine.OutcomePolicyRefused:      taxonomy.ExitPolicy,
		engine.OutcomeVerificationFailed: taxonomy.ExitVerification,
		engine.OutcomeFailed:             taxonomy.ExitFailure,
	}
	seen := map[int]engine.Outcome{}
	for outcome, want := range cases {
		plan := &engine.Plan{Outcome: outcome}
		if got := plan.ExitCode(); got != want {
			t.Errorf("%s exits %d, want %d", outcome, got, want)
		}
		// The process-level mapping, through the error the command
		// returns — the path a script actually observes.
		got := taxonomy.ExitOK
		if want != taxonomy.ExitOK {
			got = exitCodeFor(classifyUsage(&exitError{code: plan.ExitCode()}))
		}
		if got != want {
			t.Errorf("%s reaches the process as %d, want %d", outcome, got, want)
		}
		if other, clash := seen[want]; clash {
			t.Errorf("%s and %s share exit code %d: a gate cannot tell them apart", outcome, other, want)
		}
		seen[want] = outcome
	}
	// And "changes planned" must not collide with the usage code either:
	// a mistyped flag and a pending change are different events.
	if taxonomy.ExitChangesPlanned == taxonomy.ExitUsage {
		t.Error("the changes-planned code collides with the usage code")
	}
}

// TestPlanRefusesTheTriggerFlags: --dry-run and the flags of the live
// trigger are two different commands sharing one verb, and mixing them is
// a usage error rather than a silent no-op. A pipeline that wrote
// `tobby sync --dry-run --wait` must be told it waited for nothing, not
// left believing it waited.
func TestPlanRefusesTheTriggerFlags(t *testing.T) {
	for _, flag := range []string{"--wait", "--instance"} {
		args := []string{"sync", "--dry-run", flag}
		if flag == "--instance" {
			args = append(args, "https://tobby.example")
		}
		err := execute(t, args...)
		if err == nil {
			t.Fatalf("`tobby sync --dry-run %s` was accepted", flag)
		}
		if got := exitCodeFor(err); got != taxonomy.ExitUsage {
			t.Errorf("%s: exit code = %d, want %d (usage)", flag, got, taxonomy.ExitUsage)
		}
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: error %T is not a usageError", flag, err)
		}
		if !strings.Contains(ue.Error(), "--dry-run") {
			t.Errorf("%s: the refusal does not say why: %s", flag, ue.Error())
		}
	}
}

// TestSyncRejectsAnUnknownOutputFormat: `--output json` is a published
// contract (R-08), so an unrecognized value is a usage error and not a
// silent fallback to text.
func TestSyncRejectsAnUnknownOutputFormat(t *testing.T) {
	err := execute(t, "sync", "--dry-run", "--output", "yaml")
	if err == nil {
		t.Fatal("an unknown --output value was accepted")
	}
	if got := exitCodeFor(err); got != taxonomy.ExitUsage {
		t.Errorf("exit code = %d, want %d (usage)", got, taxonomy.ExitUsage)
	}
}

// TestPlanJSONIsTheDocumentedReport locks the machine contract: the
// `--output json` document is the plan report itself, with the field
// names the OpenAPI schema and the Media screen (R-02) consume. A
// wrapper, a renaming or a dropped section here is a breaking change for
// both.
func TestPlanJSONIsTheDocumentedReport(t *testing.T) {
	plan := &engine.Plan{
		Mode:    "mirror",
		Source:  "./retriever.yaml",
		Zone:    "zone-a",
		Outcome: engine.OutcomeChangesPlanned,
		Totals: engine.PlanTotals{
			Recipes: 1, Ingredients: 2, New: 1, UpToDate: 1,
			TransferBytes: 4096, TotalBytes: 8192, LargestFileBytes: 4096,
			StoreBytes: 100, ProjectedStoreBytes: 4196,
		},
		Recipes: []engine.PlanRecipe{{
			Name: "wordpress", Requested: "^6.8", Resolved: "6.8.2",
			Signature: engine.SignatureVerified,
			Ingredients: []engine.PlanIngredient{{
				Name: "app", Status: engine.StatusNew, PushStatus: engine.StatusNotProbed,
			}},
		}},
		Checks: []preflight.Check{{
			Target: preflight.TargetStore, Path: "/var/lib/tobby",
			MarginPercent: 10, Warnings: []preflight.Warning{preflight.WarnFilesystemUnidentified},
		}},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("the report is not valid JSON: %v", err)
	}
	for _, key := range []string{"mode", "source", "zone", "outcome", "recipes", "totals", "policy", "prune", "checks", "generated_at"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("the JSON report has no %q member", key)
		}
	}
	totals, _ := doc["totals"].(map[string]any)
	for _, key := range []string{"transfer_bytes", "total_bytes", "largest_file_bytes", "projected_store_bytes", "up_to_date"} {
		if _, ok := totals[key]; !ok {
			t.Errorf("totals has no %q member", key)
		}
	}
	// Byte counts are raw numbers, never rendered sizes: a consumer has
	// to be able to subtract them (ADR-0015 §7).
	if got := totals["transfer_bytes"]; got != float64(4096) {
		t.Errorf("transfer_bytes = %v (%T), want the raw byte count", got, got)
	}
	checks, _ := doc["checks"].([]any)
	if len(checks) != 1 {
		t.Fatalf("checks = %v, want one entry", checks)
	}
	check, _ := checks[0].(map[string]any)
	for _, key := range []string{"target", "path", "filesystem", "space", "margin_percent", "warnings"} {
		if _, ok := check[key]; !ok {
			t.Errorf("a pre-flight check has no %q member", key)
		}
	}
	fs, _ := check["filesystem"].(map[string]any)
	if identified, ok := fs["identified"]; !ok || identified != false {
		t.Errorf("the filesystem block does not carry an explicit identified flag: %v", fs)
	}
}

// TestPlanTextReportStatesTheNumbers: the human form has to carry the
// figures FR-055 asks to be displayed — per-recipe and total volume, free
// space, and the shortfall when there is one.
func TestPlanTextReportStatesTheNumbers(t *testing.T) {
	plan := &engine.Plan{
		Source:  "./retriever.yaml",
		Zone:    "zone-a",
		Outcome: engine.OutcomeChangesPlanned,
		Totals: engine.PlanTotals{
			Recipes: 1, Ingredients: 1, New: 1,
			TransferBytes: 3145728, TotalBytes: 3145728,
			StoreBytes: 1024, ProjectedStoreBytes: 3146752,
		},
		Recipes: []engine.PlanRecipe{{
			Name: "wordpress", Requested: "^6.8", Resolved: "6.8.2",
			Signature: engine.SignatureVerified, TransferBytes: 3145728,
		}},
		Checks: []preflight.Check{{
			Target: preflight.TargetStore, Path: "/var/lib/tobby",
			Filesystem:    preflight.Filesystem{Type: "vfat", Identified: true, MaxFileSize: 1<<32 - 1},
			Space:         preflight.Space{FreeBytes: 1000, TotalBytes: 4000, Known: true},
			MarginPercent: 10, ReservedBytes: 100, UsableBytes: 900,
			ProjectedBytes: 3145728, ShortfallBytes: 3144828,
			RefusalCode: taxonomy.CodeInsufficientSpace,
		}},
		Prune: engine.PrunePlan{Evaluated: true},
	}

	var buf bytes.Buffer
	writePlanText(&buf, plan)
	out := buf.String()
	for _, want := range []string{
		"zone-a", "changes-planned",
		"wordpress", "6.8.2", "verified",
		"3145728",        // the exact transfer, subtractable
		"3 MiB",          // and the readable form beside it
		"vfat",           // the filesystem, named
		"REFUSED",        // the verdict, unmissable
		"3144828",        // the exact shortfall (FR-055)
		"/var/lib/tobby", // the target
		"nothing would be removed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the text report does not state %q:\n%s", want, out)
		}
	}
}
