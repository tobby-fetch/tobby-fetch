// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"strings"
	"testing"
)

// TestSanitizeNameSurvivesWindowsFilesystemSemantics locks the three ways
// a FileSet name could have shared a cache directory with another one on
// Windows, where the volume is case-insensitive and trailing dots are
// stripped (NFR-018).
//
// Sharing a directory is not a cosmetic clash: the second extraction
// os.RemoveAll's the tree the first one is serving from, and purge then
// reclaims the survivor's directory because the name it reads back from
// disk is not the key it was told to keep.
//
// Two directory names are compared as Windows would resolve them, on
// every platform this test runs on: what matters is whether Windows would
// see one directory, not whether the Linux machine running the suite
// happens to see two.
func TestSanitizeNameSurvivesWindowsFilesystemSemantics(t *testing.T) {
	// asWindowsResolves normalizes a segment the way the Win32 layer does
	// before it reaches the volume: case is folded away and trailing dots
	// are stripped.
	asWindowsResolves := func(s string) string {
		return strings.TrimRight(strings.ToLower(s), ".")
	}

	for _, pair := range [][2]string{
		{"Docs", "docs"},   // case-insensitive collision
		{"docs.", "docs"},  // Windows strips the trailing dot
		{"DOCS", "docs"},   // ditto, in the other case
		{"docs..", "docs"}, // and it strips more than one
	} {
		a, b := sanitizeName(pair[0]), sanitizeName(pair[1])
		if asWindowsResolves(a) == asWindowsResolves(b) {
			t.Errorf("sanitizeName(%q) = %q and sanitizeName(%q) = %q resolve to one cache directory on Windows",
				pair[0], a, pair[1], b)
		}
	}

	// Every DOS device name must leave the verbatim branch: Windows
	// resolves them ahead of any file of that name and os.MkdirAll fails
	// outright, so the fileset would never extract at all.
	devices := []string{"con", "prn", "aux", "nul"}
	for i := 1; i <= 9; i++ {
		devices = append(devices, "com"+string(rune('0'+i)), "lpt"+string(rune('0'+i)))
	}
	for _, dev := range devices {
		for _, name := range []string{dev, strings.ToUpper(dev), dev + ".txt", dev + ".tar.gz"} {
			if got := sanitizeName(name); !strings.HasPrefix(got, "_") {
				t.Errorf("sanitizeName(%q) = %q, want the hashed form: it is a reserved device name on Windows", name, got)
			}
		}
	}

	// The whole corpus must remain mutually distinct as Windows resolves
	// it: routing a name to the hashed form fixes nothing if two names
	// land on the same directory anyway, and an ordinary name must keep
	// its own directory rather than be swept up by the new refusals.
	corpus := append([]string{
		"Docs", "docs", "DOCS", "docs.", "docs..", "doc-s", "docs2",
		"con", "CON", "con.txt", "console", "com1", "com10", "lpt1", "lpt9",
	}, devices...)
	seen := map[string]string{}
	for _, name := range corpus {
		key := asWindowsResolves(sanitizeName(name))
		if other, dup := seen[key]; dup && other != name {
			t.Errorf("sanitizeName(%q) and sanitizeName(%q) both give %q", other, name, key)
			continue
		}
		seen[key] = name
	}

	// And the mapping stays stable: an ordinary lower-case name is still
	// used verbatim, so an existing store copied onto a Windows host does
	// not re-extract everything it already carries.
	if got := sanitizeName("docs"); got != "docs" {
		t.Errorf("sanitizeName(%q) = %q, want it verbatim", "docs", got)
	}
}
