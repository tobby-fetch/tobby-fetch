// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The published error reference is hand-written prose, in two languages,
// beside a catalog that is code. /help renders the catalog live, so the
// binary never drifts — but the website pages are what an operator reads
// while deciding whether to buy the medium a refusal is asking for, and
// nothing kept them in step.
//
// Milestone 5 added seven codes across three lots and none of them
// reached those pages: three lots, three scopes, one page nobody owned.
// This test gives it an owner.
func TestPublishedReferenceCoversEveryCode(t *testing.T) {
	root := repoRoot(t)
	for _, page := range []string{
		"website/src/content/docs/docs/reference/errors.md",
		"website/src/content/docs/fr/docs/reference/errors.md",
	} {
		//nolint:gosec // G304: page comes from the fixed list above, not from input
		raw, err := os.ReadFile(filepath.Join(root, page))
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		// The headings are searched for with "\n", so the page is normalized
		// rather than trusting a checkout to have kept it that way (NFR-018).
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		var missing []string
		for _, entry := range taxonomy.All() {
			if !strings.Contains(text, "### "+string(entry.Code)+"\n") {
				missing = append(missing, string(entry.Code))
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s documents no entry for %s\n"+
				"the catalog is the source of truth: add a section per code, "+
				"in both languages, with what happened / probable cause / "+
				"corrective action / what it blocks",
				page, strings.Join(missing, ", "))
		}
	}
}

// repoRoot walks up to the module root, the way tools/helpsync does: a
// test that reads repository files cannot assume the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal(fmt.Errorf("no go.mod above %s", dir))
		}
		dir = parent
	}
}
