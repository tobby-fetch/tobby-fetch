// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// listTree names every file and directory under root, so a test can prove
// a command left a store exactly as it found it.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		out = append(out, path+"|"+info.ModTime().String()+"|"+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestMediaSlipDrawsTheSlipOnEitherSide: the destination side verifies and
// carries the verdict, the source side summarizes, and a directory with no
// manifest still gets a slip that says so. All three carry the unsigned
// notice, in the language asked for (R-07).
func TestMediaSlipDrawsTheSlipOnEitherSide(t *testing.T) {
	root := emptyMedium(t)

	out, err := run(t, "media", "slip", "--storage-root", root, "--zone", servedZone, "--lang", "en")
	if err != nil {
		t.Fatalf("media slip (destination) = %v", err)
	}
	for _, want := range []string{
		"TRANSFER SLIP", "UNSIGNED AID DOCUMENT — NOT PROOF OF AUTHENTICITY",
		"Destination side — zone " + servedZone, "VERIFICATION REPORT", "run_seed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("destination slip lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, "media", "slip", "--storage-root", root, "--lang", "fr")
	if err != nil {
		t.Fatalf("media slip (source) = %v", err)
	}
	for _, want := range []string{"BORDEREAU DE TRANSFERT", "DOCUMENT D'AIDE NON SIGNÉ — NE VAUT PAS PREUVE D'AUTHENTICITÉ", "Côté source"} {
		if !strings.Contains(out, want) {
			t.Errorf("source slip lacks %q:\n%s", want, out)
		}
	}

	out, err = run(t, "media", "slip", "--storage-root", t.TempDir(), "--zone", servedZone, "--output", "html", "--lang", "en")
	if err != nil {
		t.Fatalf("media slip on a directory without a manifest = %v", err)
	}
	if !strings.HasPrefix(out, "<!doctype html>") || !strings.Contains(out, "TBY-MED-001") {
		t.Errorf("a store without a manifest should still get a slip that names the refusal:\n%s", out)
	}
}

// TestMediaSlipWritesNothing: it is a read of the medium, like verify.
func TestMediaSlipWritesNothing(t *testing.T) {
	root := emptyMedium(t)
	before := listTree(t, root)
	if _, err := run(t, "media", "slip", "--storage-root", root, "--zone", servedZone); err != nil {
		t.Fatal(err)
	}
	after := listTree(t, root)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("media slip changed the store:\nbefore %v\nafter  %v", before, after)
	}
}

// TestMediaSlipRefusesAnUnknownLanguage is a usage error, like an unknown
// --output.
func TestMediaSlipRefusesAnUnknownLanguage(t *testing.T) {
	_, err := run(t, "media", "slip", "--storage-root", t.TempDir(), "--lang", "de")
	if err == nil || exitCodeFor(err) != 2 {
		t.Errorf("--lang de = %v (exit %d), want a usage error", err, exitCodeFor(err))
	}
}
