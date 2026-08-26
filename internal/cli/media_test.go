// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/medialog"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The destination-side operator's command (FR-052, FR-066). The medium is
// a real store carrying a real manifest; what the engine does with its
// content is locked in the engine's own tests, and what is checked here
// is the CLI contract: the refusals, the exit-code classes, the two
// output formats, and the operation log landing on the medium.

// servedZone is the zone every fixture medium is addressed to. The
// mismatch cases vary the zone the INSTANCE claims to serve, not the one
// the medium was produced for: that is the direction the guard runs in.
const servedZone = "zone-alpha"

// emptyMedium produces a transportable store carrying no delivery: enough
// to be a medium, which is all the CLI contract needs.
func emptyMedium(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	// A store that has never synchronized anything carries no recipe
	// graph at all, and a medium without one is refused (R-19: the graph
	// IS the reachability set). Writing then forgetting one entry leaves
	// the empty graph a real mirror run produces.
	if err := st.PutRecipeRecord(&store.RecipeRecord{Name: "seed", Version: "0", Digest: "sha256:" + strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRecipeRecord("seed", "0"); err != nil {
		t.Fatal(err)
	}
	if _, err := media.Write(context.Background(), st, media.WriteOptions{Zone: servedZone, RunID: "run_seed"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

// mediumConfig writes a configuration file for a destination instance.
func mediumConfig(t *testing.T, root, zone, destination string) string {
	t.Helper()
	// The store root is emitted as a quoted scalar: a Windows path is full
	// of backslashes and a colon after the drive letter, none of which a
	// plain YAML scalar is obliged to carry unchanged (NFR-018).
	body := "mode: mirror\nzone: " + zone + "\nstorage:\n  root: " + strconv.Quote(root) + "\n"
	if destination != "" {
		body += "destination:\n  registry: " + destination + "\nregistries:\n  insecure: [" + destination + "]\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMediaVerifyRefusesADirectoryThatIsNotAMedium is the first thing the
// command does, and it is deliberately the first thing: opening a store
// stamps a directory with a format file and an identity, and FR-054
// forbids any local write before verification. A directory that carries
// no manifest is refused before anything is opened.
func TestMediaVerifyRefusesADirectoryThatIsNotAMedium(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, "media", "verify", "--storage-root", dir, "--zone", "zone-alpha")
	if !isTaxonomy(err, taxonomy.CodeMediaManifestMissing) {
		t.Fatalf("verify on a plain directory = %v (out %q), want %s",
			err, out, taxonomy.CodeMediaManifestMissing)
	}
	if code := exitCodeFor(err); code != taxonomy.ExitVerification {
		t.Errorf("exit code = %d, want %d (verification class)", code, taxonomy.ExitVerification)
	}
	// Nothing was written: the directory is exactly as it was.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the refused directory now holds %v: no local write may precede verification (FR-054)", names)
	}
}

// TestMediaVerifyNeedsAZoneIdentity: an instance that does not know which
// zone it serves cannot decide anything about a medium, and says so
// rather than guessing.
func TestMediaVerifyNeedsAZoneIdentity(t *testing.T) {
	root := emptyMedium(t)
	if _, err := run(t, "media", "verify", "--storage-root", root); !isTaxonomy(err, taxonomy.CodeConfigInvalid) {
		t.Fatalf("verify without a zone = %v, want %s", err, taxonomy.CodeConfigInvalid)
	}
	// And the zone may come from the environment like every other setting.
	t.Setenv("TOBBY_ZONE", "zone-alpha")
	if _, err := run(t, "media", "verify", "--storage-root", root); err != nil {
		t.Fatalf("verify with TOBBY_ZONE set: %v", err)
	}
}

// TestMediaVerifyRefusesAMediumAddressedElsewhere is the zone guard on the
// CLI, with the exit-code class FR-066 assigns it: a policy refusal, not
// a verification failure — the medium is intact, it is simply not ours.
func TestMediaVerifyRefusesAMediumAddressedElsewhere(t *testing.T) {
	root := emptyMedium(t)
	out, err := run(t, "media", "verify", "--storage-root", root, "--zone", "zone-beta")
	if !isTaxonomy(err, taxonomy.CodeMediaZoneMismatch) {
		t.Fatalf("verify = %v (out %q), want %s", err, out, taxonomy.CodeMediaZoneMismatch)
	}
	if code := exitCodeFor(err); code != taxonomy.ExitPolicy {
		t.Errorf("exit code = %d, want %d (policy class)", code, taxonomy.ExitPolicy)
	}
	if !strings.Contains(out, "an administrator may override it") {
		t.Errorf("the report does not say the refusal is waivable:\n%s", out)
	}

	// Waived deliberately, the same medium verifies and the report says
	// so — a lowered barrier is never silent (FR-075).
	out, err = run(t, "media", "verify", "--storage-root", root, "--zone", "zone-beta", "--allow-zone-mismatch")
	if err != nil {
		t.Fatalf("waived verify = %v (out %q)", err, out)
	}
	if !strings.Contains(out, "OVERRIDDEN by an administrator") {
		t.Errorf("the report does not record the waiver:\n%s", out)
	}
}

// TestMediaVerifyOutputsTheReportAsJSON is the FR-066 machine contract:
// the report travels as the same document every other surface reads.
func TestMediaVerifyOutputsTheReportAsJSON(t *testing.T) {
	root := emptyMedium(t)
	out, errOut, err := runSplit(t, "media", "verify", "--storage-root", root, "--zone", "zone-alpha", "--output", "json")
	if err != nil {
		t.Fatalf("verify: %v (out %q)", err, out)
	}
	if errOut == "" {
		t.Error("nothing was narrated on stderr: the operation left no trace for a human")
	}
	var rep media.Report
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("--output json is not a report: %v\n%s", jerr, out)
	}
	if rep.Verdict != media.VerdictPushable {
		t.Errorf("verdict = %q, want %q", rep.Verdict, media.VerdictPushable)
	}
	if rep.Media == nil || rep.Media.Zone != "zone-alpha" {
		t.Errorf("report media = %+v, want the medium's own claims", rep.Media)
	}
	if rep.Zone.Expected != "zone-alpha" || !rep.Zone.Match {
		t.Errorf("zone check = %+v, want a match", rep.Zone)
	}
	// `verify` promises a store left untouched, and FR-054 puts every
	// local write AFTER verification: the command writes nothing at all,
	// not even the medium's own operation log.
	if _, serr := os.Stat(filepath.Join(root, filepath.FromSlash(medialog.DefaultPath))); serr == nil {
		t.Error("verify wrote the medium's operation log: it must leave the store untouched")
	}

	if _, err := run(t, "media", "verify", "--storage-root", root, "--zone", "zone-alpha", "--output", "yaml"); err == nil {
		t.Error("--output yaml was accepted; only text and json are documented")
	} else if code := exitCodeFor(err); code != taxonomy.ExitUsage {
		t.Errorf("bad --output exits %d, want %d (usage)", code, taxonomy.ExitUsage)
	}
}

// TestMediaVerifyBlocksOnAnAlteredRecipeGraph: the graph IS the
// reachability set every per-recipe verdict is computed from, so an
// altered one blocks the medium as a whole, with no waiver offered
// (R-19).
func TestMediaVerifyBlocksOnAnAlteredRecipeGraph(t *testing.T) {
	root := emptyMedium(t)
	graph := filepath.Join(root, "meta", "recipes.json")
	if err := os.WriteFile(graph, []byte(`{"recipes":{"ghost":{"name":"ghost"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "media", "verify", "--storage-root", root, "--zone", "zone-alpha")
	if !isTaxonomy(err, taxonomy.CodeMediaGraphAltered) {
		t.Fatalf("verify = %v (out %q), want %s", err, out, taxonomy.CodeMediaGraphAltered)
	}
	if code := exitCodeFor(err); code != taxonomy.ExitVerification {
		t.Errorf("exit code = %d, want %d", code, taxonomy.ExitVerification)
	}
	// No waiver is offered, by anyone.
	for _, flag := range []string{"--allow-zone-mismatch", "--allow-stale"} {
		if _, werr := run(t, "media", "verify", "--storage-root", root, "--zone", "zone-alpha", flag); werr == nil {
			t.Errorf("%s let an altered recipe graph through: an integrity verdict has no override (R-19)", flag)
		}
	}
}

// TestMediaImportNeedsADestination: verifying needs no destination
// registry, importing cannot proceed without one — and the two refusals
// must not be the same message.
func TestMediaImportNeedsADestination(t *testing.T) {
	root := emptyMedium(t)
	if _, err := run(t, "media", "verify", "--storage-root", root, "--zone", "zone-alpha"); err != nil {
		t.Fatalf("verify without a destination must work: %v", err)
	}
	_, err := run(t, "media", "import", "--storage-root", root, "--zone", "zone-alpha")
	if !isTaxonomy(err, taxonomy.CodeConfigInvalid) {
		t.Fatalf("import without a destination = %v, want %s", err, taxonomy.CodeConfigInvalid)
	}
}

// TestMediaImportJournalsOntoTheMedium is FR-053 seen from the command:
// the operation writes its own trail onto the medium it operated on, and
// that trail lands OUTSIDE the manifest's coverage — otherwise it would
// invalidate the inventory the same command has just verified.
func TestMediaImportJournalsOntoTheMedium(t *testing.T) {
	root := emptyMedium(t)
	registry := httptest.NewServer(nil)
	t.Cleanup(registry.Close)
	cfgPath := mediumConfig(t, root, "zone-alpha", registry.Listener.Addr().String())

	before := manifestBytes(t, root)
	out, err := run(t, "media", "import", "--config", cfgPath)
	if err != nil {
		t.Fatalf("import: %v (out %q)", err, out)
	}

	logPath := filepath.Join(root, filepath.FromSlash(medialog.DefaultPath))
	raw, rerr := os.ReadFile(logPath) //nolint:gosec // G304: a path this test built
	if rerr != nil {
		t.Fatalf("the operation left no trail on the medium (FR-053): %v", rerr)
	}
	if !strings.Contains(string(raw), "media verification complete") {
		t.Errorf("the medium's log does not narrate the verification:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"log_type":"audit"`) {
		t.Errorf("the medium's log carries no audit record of the import (FR-094):\n%s", raw)
	}
	// The trail is outside coverage, so the inventory it accompanies is
	// still exactly the one that was verified.
	if media.Covered(medialog.DefaultPath) {
		t.Fatalf("%s is inside manifest coverage", medialog.DefaultPath)
	}
	if got := manifestBytes(t, root); !bytes.Equal(got, before) {
		t.Error("the import rewrote the media manifest: the medium must come back describing what it holds")
	}
	// Re-verifying the medium after the import still passes: the return
	// channel did not cost the medium its own integrity.
	if _, verr := run(t, "media", "verify", "--storage-root", root, "--zone", "zone-alpha"); verr != nil {
		t.Errorf("the medium no longer verifies after being written to: %v", verr)
	}
}

// manifestBytes reads the medium's manifest verbatim.
func manifestBytes(t *testing.T, root string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(media.ManifestPath))) //nolint:gosec // G304: a path this test built
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// isTaxonomy reports whether err carries this catalog code.
func isTaxonomy(err error, code taxonomy.Code) bool {
	var te *taxonomy.Error
	return errors.As(err, &te) && te.Code() == code
}
