// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Tests for `tobby fileset pack` (FR-048) through the real command: the
// stdout/stderr split the R-08 contract promises, the exit-code classes of
// FR-066, and the reproducible digest, all against a real store.

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// runSplit executes the CLI with args and returns stdout and stderr apart,
// which is the whole point on this command: the report is on stdout and
// nothing else may join it (B-010, R-08).
func runSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := New()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// packTree writes a small file tree and returns its path.
func packTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pool"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.html":   "<h1>hi</h1>\n",
		"pool/one.deb": "package-bytes",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestFileSetPackReportsOnStdoutAndStderr: the digest alone on stdout so
// the command composes into a script, everything human on stderr — plus
// the two things the operator must not miss, that the FileSet is unsigned
// and that serving it takes a configuration change (FR-047, ADR-0007).
func TestFileSetPackReportsOnStdoutAndStderr(t *testing.T) {
	store := t.TempDir()
	stdout, stderr, err := runSplit(t, "fileset", "pack", packTree(t), "docs:1.0.0", "--storage-root", store)
	if err != nil {
		t.Fatalf("pack: %v (stderr %s)", err, stderr)
	}
	digest := strings.TrimSpace(stdout)
	if !strings.HasPrefix(digest, "sha256:") || strings.Contains(digest, "\n") {
		t.Fatalf("stdout = %q, want the digest alone", stdout)
	}
	for _, want := range []string{
		"localhost/filesets/docs", "unsigned", "files:", "filesets:", "name: docs",
		`"action":"fileset.pack"`, `"outcome":"success"`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr misses %q:\n%s", want, stderr)
		}
	}
	// FR-048: the packed repository is recorded as a manual import of
	// local origin, which is what tells it apart from Recipe-delivered
	// content in every listing.
	ledger, err := os.ReadFile(filepath.Join(store, "meta", "provenance.json")) //nolint:gosec // G304: the store this test just created
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"class": "unit-import"`, `"origin": "local-pack"`} {
		if !strings.Contains(string(ledger), want) {
			t.Errorf("provenance ledger misses %s:\n%s", want, ledger)
		}
	}
}

// TestFileSetPackJSONOutputIsAloneOnStdout locks the R-08 machine
// contract: --output json puts a parseable document on stdout and nothing
// beside it — not the digest line, not the audit record.
func TestFileSetPackJSONOutputIsAloneOnStdout(t *testing.T) {
	stdout, _, err := runSplit(t, "fileset", "pack", packTree(t), "docs:1.0.0",
		"--storage-root", t.TempDir(), "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Reference string `json:"reference"`
		Digest    string `json:"digest"`
		Files     int    `json:"files"`
		Signed    bool   `json:"signed"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, stdout)
	}
	if res.Reference != "localhost/filesets/docs" || res.Files != 2 || !strings.HasPrefix(res.Digest, "sha256:") {
		t.Fatalf("report = %+v", res)
	}
	if res.Signed {
		t.Fatal(`the report claims "signed": true; Tobby holds no key (ADR-0007)`)
	}
}

// TestFileSetPackTwiceYieldsTheSameDigest is the reproducibility promise
// through the real command and a real store: a second run over an
// unchanged tree reports the same digest and writes nothing new.
func TestFileSetPackTwiceYieldsTheSameDigest(t *testing.T) {
	src, store := packTree(t), t.TempDir()
	first, _, err := runSplit(t, "fileset", "pack", src, "docs:1.0.0", "--storage-root", store)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := runSplit(t, "fileset", "pack", src, "docs:1.0.0", "--storage-root", store)
	if err != nil {
		t.Fatalf("second pack: %v", err)
	}
	if strings.TrimSpace(first) != strings.TrimSpace(second) {
		t.Fatalf("digests differ across two runs:\n%s\n%s", first, second)
	}
}

// TestFileSetPackExitCodes walks the FR-066 classes: an unsafe tree is a
// policy refusal (3), a request Tobby cannot act on is an operational
// failure (1), and a malformed command line is a usage error (2).
func TestFileSetPackExitCodes(t *testing.T) {
	escaping := t.TempDir()
	if err := os.WriteFile(filepath.Join(escaping, "ok.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(escaping, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"an escaping symlink is a policy refusal", []string{"fileset", "pack", escaping, "docs:1.0.0"}, taxonomy.ExitPolicy},
		{"a missing directory is an operational failure", []string{"fileset", "pack", filepath.Join(escaping, "nope"), "docs:1.0.0"}, taxonomy.ExitFailure},
		{"a target with no version is an operational failure", []string{"fileset", "pack", escaping, "docs"}, taxonomy.ExitFailure},
		{"an unknown output format is a usage error", []string{"fileset", "pack", escaping, "docs:1.0.0", "--output", "xml"}, taxonomy.ExitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "--storage-root", t.TempDir())
			_, _, err := runSplit(t, args...)
			if err == nil {
				t.Fatal("the command succeeded")
			}
			if got := exitCodeFor(classifyUsage(err)); got != tc.want {
				t.Fatalf("exit code = %d, want %d (error %v)", got, tc.want, err)
			}
		})
	}
}

// TestFileSetPackNeedsOnlyTheStoreRoot is the R-34/B-006 rule applied to
// this command: packing a directory into the local store says nothing
// about how the host serves, so it must not demand a mode.
func TestFileSetPackNeedsOnlyTheStoreRoot(t *testing.T) {
	if _, _, err := runSplit(t, "fileset", "pack", packTree(t), "docs:1.0.0",
		"--storage-root", t.TempDir()); err != nil {
		t.Fatalf("pack without --mode: %v", err)
	}
	// And the store root itself is required, named — a configuration that
	// sets nothing must not silently pack into the working directory.
	empty := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(empty, []byte("logging:\n  level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runSplit(t, "fileset", "pack", packTree(t), "docs:1.0.0", "--config", empty)
	if err == nil || !strings.Contains(err.Error(), "storage.root") {
		t.Fatalf("error = %v, want it to name storage.root", err)
	}
}
