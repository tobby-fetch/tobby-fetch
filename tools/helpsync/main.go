// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command helpsync copies the documentation the binary serves offline
// (NFR-003, amendment 2026-08-11 / R-05) from website/src/content/docs
// into internal/help/corpus, where go:embed picks it up.
//
// It is a copy, not a build: no Markdown is parsed, no HTML is produced,
// nothing is fetched (NFR-019), and the same working tree always yields
// the same bytes (NFR-004). That is the whole point — the guides served by
// an air-gapped instance and the guides published on the website are the
// same file, so there is one documentation to write and review, and
// `helpsync -check` fails the moment the two drift apart.
//
//	go run ./tools/helpsync            # refresh the corpus
//	go run ./tools/helpsync -check     # fail if it has drifted (CI gate)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// docsRoot holds the English (canonical) pages, frDocsRoot their
	// French mirror (website/README-docs.md).
	docsRoot   = "website/src/content/docs/docs"
	frDocsRoot = "website/src/content/docs/fr/docs"
	// assetRoot holds the screenshots the pages reference.
	assetRoot = "website/src/assets/docs"
	// statusSource is the single source of truth for feature status; the
	// corpus carries the data file itself rather than a transcription of
	// it, which is the rule website/README-docs.md sets.
	statusSource = "website/src/data/status.yaml"
	// corpusRoot is where the copy lands.
	corpusRoot = "internal/help/corpus"
)

// excluded lists the source pages the corpus deliberately leaves behind.
//
//   - index.mdx is the website's docs landing page, built from Astro
//     components; /help has its own home, built from the navigation of the
//     embedded corpus.
//   - reference/errors.md renders the taxonomy catalog that already lives
//     in this binary. Embedding a copy would ship a table free to drift
//     from the catalog it describes, so internal/help resolves links to it
//     onto /help — the live index, generated from internal/taxonomy at
//     request time (FR-065, R-03).
var excluded = map[string]bool{
	"index.mdx":           true,
	"reference/errors.md": true,
}

// assetRefRe matches the screenshot references of the corpus. Only the
// files actually referenced are embedded: a screenshot nothing points at
// would be weight in every operator's binary for no reader's benefit.
var assetRefRe = regexp.MustCompile(`\]\(([^)]*/assets/docs/[^)]+)\)`)

func main() {
	check := flag.Bool("check", false, "report drift and exit non-zero instead of writing")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	want, err := collect(root)
	if err != nil {
		fail(err)
	}
	have, err := existing(filepath.Join(root, corpusRoot))
	if err != nil {
		fail(err)
	}
	added, changed, removed := diff(want, have)

	if *check {
		if len(added)+len(changed)+len(removed) == 0 {
			fmt.Printf("corpus in sync with %s (%d files)\n", docsRoot, len(want))
			return
		}
		fmt.Fprintf(os.Stderr, "the embedded help corpus has drifted from %s:\n", docsRoot)
		report("missing from the corpus", added)
		report("different in the corpus", changed)
		report("no longer in the source", removed)
		fmt.Fprintln(os.Stderr, "\nrun: go run ./tools/helpsync")
		os.Exit(1)
	}

	if err := apply(filepath.Join(root, corpusRoot), want, removed); err != nil {
		fail(err)
	}
	fmt.Printf("corpus written: %d files (+%d ~%d -%d)\n",
		len(want), len(added), len(changed), len(removed))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "helpsync:", err)
	os.Exit(1)
}

func report(label string, names []string) {
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-24s %s\n", label+":", n)
	}
}

// repoRoot walks up from the working directory to the module root, so the
// tool runs the same from anywhere.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}

// collect builds the corpus the working tree calls for: the two language
// trees, the status data file, and the screenshots the pages reference.
func collect(root string) (map[string][]byte, error) {
	want := map[string][]byte{}
	for lang, dir := range map[string]string{"en": docsRoot, "fr": frDocsRoot} {
		if err := collectPages(root, dir, lang, want); err != nil {
			return nil, err
		}
	}
	status, err := os.ReadFile(filepath.Join(root, statusSource)) //nolint:gosec // G304: a developer tool reading its own repository, at a path built from constants.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", statusSource, err)
	}
	want["status.yaml"] = status

	for _, name := range referencedAssets(want) {
		body, err := os.ReadFile(filepath.Join(root, assetRoot, name)) //nolint:gosec // G304: the name comes from a page of this repository, under a constant root.
		if err != nil {
			return nil, fmt.Errorf("a page references a screenshot that does not exist: %w", err)
		}
		want["assets/"+name] = body
	}
	return want, nil
}

func collectPages(root, dir, lang string, want map[string][]byte) error {
	base := filepath.Join(root, dir)
	if _, err := os.Stat(base); err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := filepath.Ext(p)
		if ext != ".md" && ext != ".mdx" {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excluded[rel] {
			return nil
		}
		body, err := os.ReadFile(p) //nolint:gosec // G304: p is what WalkDir found under a constant root of this repository.
		if err != nil {
			return err
		}
		want[lang+"/"+rel] = body
		return nil
	})
}

// referencedAssets returns the screenshot file names the collected pages
// point at, sorted and deduplicated.
func referencedAssets(pages map[string][]byte) []string {
	seen := map[string]bool{}
	for name, body := range pages {
		if !strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".mdx") {
			continue
		}
		for _, m := range assetRefRe.FindAllSubmatch(body, -1) {
			seen[path.Base(string(m[1]))] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// existing reads the corpus currently committed.
func existing(dir string) (map[string][]byte, error) {
	have := map[string][]byte{}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return have, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p) //nolint:gosec // G304: p is what WalkDir found under the corpus directory of this repository.
		if err != nil {
			return err
		}
		have[filepath.ToSlash(rel)] = body
		return nil
	})
	return have, err
}

func diff(want, have map[string][]byte) (added, changed, removed []string) {
	for name, body := range want {
		old, ok := have[name]
		switch {
		case !ok:
			added = append(added, name)
		case !bytes.Equal(old, body):
			changed = append(changed, name)
		}
	}
	for name := range have {
		if _, ok := want[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	sort.Strings(removed)
	return added, changed, removed
}

// apply writes the wanted files and drops the ones the source no longer
// has, so a page deleted from the website leaves the binary too.
func apply(dir string, want map[string][]byte, removed []string) error {
	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dst, want[name], 0o644); err != nil { //nolint:gosec // G306: documentation copied from the repository, world-readable like its source.
			return err
		}
	}
	for _, name := range removed {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			return err
		}
	}
	return pruneEmpty(dir)
}

// pruneEmpty removes the directories a deletion left behind, so the corpus
// tree never keeps the shape of a section the website dropped.
func pruneEmpty(dir string) error {
	var dirs []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	}); err != nil {
		return err
	}
	// Deepest first, so a directory emptied by this loop is itself removed.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(d); err != nil {
				return err
			}
		}
	}
	return nil
}
