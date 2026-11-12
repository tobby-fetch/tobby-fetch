// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check/checktest"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// `tobby store verify` (FR-057, R-31). The walk is proved in
// internal/integrity/check; what is checked here is the command: the exit
// classes, the two output formats, the trust configuration it reads, the
// refusal of a directory that is not a store, and — the property the
// requirement ends on — a store left byte-identical.

// storeVerifyConfig writes a configuration whose trust roots are the given
// keys (named a-root, b-root… like the engine's own fixtures) and whose
// scopes are the given YAML.
func storeVerifyConfig(t *testing.T, root, scopes string, roots ...*sigtest.KeyPair) string {
	t.Helper()
	body := "storage:\n  root: " + strconv.Quote(root) + "\n"
	if len(roots) > 0 || scopes != "" {
		body += "trust:\n"
	}
	if len(roots) > 0 {
		body += "  roots:\n"
		for i, kp := range roots {
			pem, err := kp.PublicPEM()
			if err != nil {
				t.Fatal(err)
			}
			body += "    - name: " + string(rune('a'+i)) + "-root\n      key: " + strconv.Quote(string(pem)) + "\n"
		}
	}
	body += scopes
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStoreVerifyIntactStoreExitsZeroAndTouchesNothing: the FR-057
// acceptance's last sentence, through the real command.
func TestStoreVerifyIntactStoreExitsZeroAndTouchesNothing(t *testing.T) {
	f := checktest.New(t)
	f.AddRecipe("alpha", f.KP)
	f.AddRecipe("beta", f.KP)
	cfg := storeVerifyConfig(t, f.Root, "", f.KP)
	before := checktest.Fingerprint(t, f.Root)

	out, err := run(t, "store", "verify", "--config", cfg)
	if err != nil {
		t.Fatalf("verify = %v\n%s", err, out)
	}
	for _, want := range []string{"instance store", "verdict: clean", "recipe alpha@1.0.0", "signature verified"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if after := checktest.Fingerprint(t, f.Root); after != before {
		t.Error("the command changed the store: it must read only")
	}
}

// TestStoreVerifyNamesEveryDefectOfTheAcceptanceStore is the FR-057
// acceptance through the command: one truncated blob, one manifest missing
// a referenced blob, one recipe whose signing key is no longer a trust
// root, one orphan blob — exit 4 (the verification class), each defect
// named with its object, and the JSON document carrying the same.
func TestStoreVerifyNamesEveryDefectOfTheAcceptanceStore(t *testing.T) {
	f := checktest.New(t)
	acc := f.BuildAcceptance()
	cfg := storeVerifyConfig(t, f.Root, "", f.KP)
	before := checktest.Fingerprint(t, f.Root)

	out, err := run(t, "store", "verify", "--config", cfg)
	if err == nil {
		t.Fatalf("a damaged store verified clean:\n%s", out)
	}
	if code := exitCodeFor(err); code != taxonomy.ExitVerification {
		t.Errorf("exit code = %d, want %d (the verification class, FR-066)", code, taxonomy.ExitVerification)
	}
	for _, want := range []string{
		"verdict: defects",
		string(taxonomy.CodeIntegrityBlobDigest) + " " + checktest.BlobPath(acc.Truncated),
		string(taxonomy.CodeIntegrityIncomplete) + " " + acc.MissingRecord.Ingredients[0].Repo + "@" + acc.MissingRecord.Ingredients[0].Digest,
		string(taxonomy.CodeSignature) + " removedkey@1.0.0",
		"orphaned  " + string(taxonomy.CodeIntegrityOrphan) + " " + checktest.BlobPath(acc.Orphan),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not name %q:\n%s", want, out)
		}
	}

	stdout, stderr, jerr := runSplit(t, "store", "verify", "--config", cfg, "--output", "json")
	if jerr == nil {
		t.Fatal("--output json hid the defects from the exit code")
	}
	var rep integrity.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("--output json is not a report: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if rep.Verdict != integrity.VerdictDefects || rep.DefectCount != 3 || rep.FindingCount != 1 || rep.RunID == "" || rep.StartedAt.IsZero() || rep.FinishedAt.IsZero() {
		t.Errorf("report = verdict %q, %d defects, %d findings: want the timestamped, correlated verdict of the acceptance store",
			rep.Verdict, rep.DefectCount, rep.FindingCount)
	}
	if after := checktest.Fingerprint(t, f.Root); after != before {
		t.Error("the command repaired or deleted something")
	}
}

// TestStoreVerifyReportsReducedAssuranceAndTheScopeOfARefusal covers the
// two scope-facing behaviours of the milestone through the command: a
// recipe admitted unsigned by a declared scope is re-reported with its
// marker (FR-037), and a recipe signed by a key that is a valid trust root
// but not one the matching scope lists is refused with that scope named
// (TBY-SIG-001, plan §2.6).
func TestStoreVerifyReportsReducedAssuranceAndTheScopeOfARefusal(t *testing.T) {
	f := checktest.New(t)
	f.AddRecipe("bare", nil) // unsigned
	other := checktest.NewKeyPair(t)
	f.AddRecipe("tenant", other) // signed by a root the scope below does not list

	scopes := "  scopes:\n" +
		"    - name: lab\n      repositories: [\"registry.example.com/cookbook/bare\"]\n      allowUnsigned: true\n" +
		"    - name: tenant-a\n      repositories: [\"registry.example.com/cookbook/tenant\"]\n      roots: [\"a-root\"]\n"
	cfg := storeVerifyConfig(t, f.Root, scopes, f.KP, other)

	stdout, _, err := runSplit(t, "store", "verify", "--config", cfg, "--output", "json")
	if err == nil || exitCodeFor(err) != taxonomy.ExitVerification {
		t.Fatalf("verify = %v, want the verification exit class for the refused recipe", err)
	}
	var rep integrity.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	var bare, tenant *integrity.Recipe
	for i := range rep.Recipes {
		switch rep.Recipes[i].Name {
		case "bare":
			bare = &rep.Recipes[i]
		case "tenant":
			tenant = &rep.Recipes[i]
		}
	}
	if bare == nil || bare.Signature != integrity.SignatureUnsignedAdmitted || !bare.Assurance.Reduced() || bare.Assurance.Scope != "lab" {
		t.Errorf("bare = %+v, want unsigned-admitted with the reduced marker under lab", bare)
	}
	if tenant == nil || tenant.Signature != integrity.SignatureRefused {
		t.Fatalf("tenant = %+v, want refused", tenant)
	}
	var sig *integrity.Defect
	for i := range rep.Defects {
		if rep.Defects[i].Code == taxonomy.CodeSignature {
			sig = &rep.Defects[i]
		}
	}
	if sig == nil || sig.Params["scope"] != "tenant-a" {
		t.Errorf("signature defect = %+v, want the scope tenant-a named", sig)
	}

	out, _ := run(t, "store", "verify", "--config", cfg)
	if !strings.Contains(out, "[reduced assurance: scope lab]") {
		t.Errorf("the text report does not carry the marker:\n%s", out)
	}
}

// TestStoreVerifyRefusesADirectoryThatIsNotAStoreWithoutWritingToIt:
// opening a store stamps a directory that lacks a format version, which is
// a write the command promises not to make.
func TestStoreVerifyRefusesADirectoryThatIsNotAStoreWithoutWritingToIt(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, "store", "verify", "--storage-root", dir)
	if !isTaxonomy(err, taxonomy.CodeConfigInvalid) {
		t.Fatalf("verify on a plain directory = %v (out %q), want %s", err, out, taxonomy.CodeConfigInvalid)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		t.Errorf("the refused directory now holds %d entries: nothing may be written", len(entries))
	}
	if _, err := run(t, "store", "verify"); !isTaxonomy(err, taxonomy.CodeConfigInvalid) && err == nil {
		t.Error("verify without a store root succeeded")
	}
}

// TestStoreVerifyHasNoRecurringSchedule: v1 verifies on demand and nothing
// else (FR-057). The command takes no interval, and the configuration has
// no key for one.
func TestStoreVerifyHasNoRecurringSchedule(t *testing.T) {
	cmd := newStoreVerifyCmd()
	for _, name := range []string{"interval", "every", "schedule", "cron", "repair", "delete", "fix", "prune"} {
		if cmd.Flags().Lookup(name) != nil {
			t.Errorf("`tobby store verify` has a --%s flag: it verifies on demand and reads only", name)
		}
	}
}

// TestStoreVerifyWorksOnATransportableStore: both modes, one verification.
// A store that carries a media manifest — a mirror synchronization's
// delivery unit, or a medium that arrived — is verified the same way, and
// the report says which kind it was.
func TestStoreVerifyWorksOnATransportableStore(t *testing.T) {
	f := checktest.New(t)
	f.AddRecipe("alpha", f.KP)
	if _, err := media.Write(context.Background(), f.St, media.WriteOptions{Zone: "zone-a", RunID: "run_x"}); err != nil {
		t.Fatal(err)
	}
	cfg := storeVerifyConfig(t, f.Root, "", f.KP)
	before := checktest.Fingerprint(t, f.Root)

	stdout, _, err := runSplit(t, "store", "verify", "--config", cfg, "--output", "json")
	if err != nil {
		t.Fatalf("verify = %v\n%s", err, stdout)
	}
	var rep integrity.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Store.Transportable || rep.Store.MediaID == "" || rep.Verdict != integrity.VerdictClean {
		t.Errorf("store = %+v verdict %q, want a clean transportable store with its medium identity", rep.Store, rep.Verdict)
	}
	// The manifest, the ledgers and the registry content are all in the
	// store: verifying it must leave every byte of them alone.
	if after := checktest.Fingerprint(t, f.Root); after != before {
		t.Error("verifying a transportable store changed it")
	}
}
