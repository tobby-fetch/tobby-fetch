// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	docsEN = "website/src/content/docs/docs"
	docsFR = "website/src/content/docs/fr/docs"
)

// docPages lists the pages of a docs tree as paths relative to its root,
// extension dropped (a page may be .md in one language and .mdx in the
// other).
func docPages(files []string, root string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		rel := strings.TrimPrefix(f, root+"/")
		ext := path.Ext(rel)
		if ext != ".md" && ext != ".mdx" {
			continue
		}
		out[strings.TrimSuffix(rel, ext)] = true
	}
	return out
}

// parity returns the English pages without a French counterpart and the
// French pages without an English source, both sorted.
func parity(en, fr map[string]bool) (missing, orphans []string) {
	for p := range en {
		if !fr[p] {
			missing = append(missing, p)
		}
	}
	for p := range fr {
		if !en[p] {
			orphans = append(orphans, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphans)
	return missing, orphans
}

// documentation is criterion 8: no English page without a French one at the
// tag, and the website builds.
func (c *checker) documentation() *result {
	r := &result{id: 8, title: "User documentation complete in English and French (parity, build)"}
	if !c.localTag() {
		r.unverifiable("%s", needTagMsg)
		return r
	}
	enFiles, okEN := c.lsTree(docsEN)
	frFiles, okFR := c.lsTree(docsFR)
	if !okEN || !okFR || len(enFiles) == 0 {
		r.ko("cannot list %s and %s at %s", docsEN, docsFR, c.version)
		return r
	}
	en, fr := docPages(enFiles, docsEN), docPages(frFiles, docsFR)
	missing, orphans := parity(en, fr)
	if len(missing) > 0 {
		r.ko("%d English page(s) without a French counterpart: %s", len(missing), pageList(missing))
	} else {
		r.ok("%d English pages, each with its French counterpart", len(en))
	}
	if len(orphans) > 0 {
		r.ok("note: %d French-only page(s) (not a criterion): %s", len(orphans), pageList(orphans))
	}
	c.websiteBuild(r)
	return r
}

// pageList names the first pages of a list, then counts the rest.
func pageList(pages []string) string {
	const shown = 8
	if len(pages) <= shown {
		return strings.Join(pages, ", ")
	}
	return fmt.Sprintf("%s, … (+%d more)", strings.Join(pages[:shown], ", "), len(pages)-shown)
}

// websiteBuild builds the website of the working tree, which must be the
// tagged commit: building another tree would vouch for the wrong release.
func (c *checker) websiteBuild(r *result) {
	if c.skipBuild {
		r.unverifiable("website build skipped (--website-build=false): run `mise exec -- pnpm build` in website/ at the tag")
		return
	}
	head := c.git("rev-parse", "HEAD")
	if !head.success() || c.commit == "" || strings.TrimSpace(head.stdout) != c.commit {
		r.unverifiable("website build: the working tree is not at %s — check the tag out and run `mise exec -- pnpm build` in website/", c.version)
		return
	}
	site := filepath.Join(c.dir, "website")
	if _, err := os.Stat(filepath.Join(site, "node_modules")); err != nil {
		r.unverifiable("website build: website/node_modules is missing — run `mise exec -- pnpm install --frozen-lockfile` then `mise exec -- pnpm build` in website/")
		return
	}
	name, args := "pnpm", []string{"build"}
	switch {
	case c.cmd.has("mise"):
		name, args = "mise", []string{"exec", "--", "pnpm", "build"}
	case !c.cmd.has("pnpm"):
		r.unverifiable("website build: neither mise nor pnpm is installed — run `mise exec -- pnpm build` in website/")
		return
	}
	ctx, cancel := c.withTimeout(15 * time.Minute)
	defer cancel()
	res := c.cmd.run(ctx, site, nil, name, args...)
	if !res.success() {
		r.ko("website build failed (%s): %s", cmdline(name, args...), lastLine(res.output(), 200))
		return
	}
	r.ok("website build succeeded (%s)", cmdline(name, args...))
}

// authTests names, per method, the tests that cover it. A method is covered
// when every named test exists at the tag, in its package, and the CI Tests
// job is green.
var authTests = []struct {
	method string
	dir    string
	tests  []string
}{
	{"OIDC", "internal/federation", []string{"TestOIDCHappyPath", "TestOIDCAttacks"}},
	{"SAML", "internal/federation", []string{"TestSAMLHappyPath", "TestSAMLAttacks"}},
	{"static tokens", "internal/auth", []string{"TestTokenLifecycle", "TestTokenExpiry"}},
	{"basic auth", "internal/auth", []string{"TestRegistryMiddleware", "TestMachineAuthAudited"}},
}

// authMethods is criterion 10.
func (c *checker) authMethods() *result {
	r := &result{id: 10, title: "All four authentication methods functional, each covered by an automated test"}
	if !c.localTag() {
		r.unverifiable("%s", needTagMsg)
		return r
	}
	for _, m := range authTests {
		var missing []string
		for _, t := range m.tests {
			g := c.git("grep", "-q", "-E", "^func "+t+`\(`, c.version, "--", m.dir)
			if !g.success() {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			r.ko("%s: test(s) not found in %s: %s", m.method, m.dir, strings.Join(missing, ", "))
		} else {
			r.ok("%s: %s in %s", m.method, strings.Join(m.tests, ", "), m.dir)
		}
	}
	c.jobCheck(r, "CI tests (linux/amd64)", jobTests, "ubuntu-latest")
	return r
}

var v1TagRe = regexp.MustCompile(`^refs/tags/v1(\.\d+\.\d+)?$`)

// specTag is criterion 11: the stable v1 format specification is tagged in
// the recipe-spec repository. A release candidate does not have it —
// the tag is set after the acceptance — and says so.
func (c *checker) specTag() *result {
	r := &result{id: 11, title: "Format specification v1 (stable) tagged in " + c.specRepo}
	if c.prerelease() {
		r.expectedKO = "expected at 1.0"
	}
	if !c.cmd.has("gh") {
		r.unverifiable("gh is absent — run: gh api repos/%s/git/matching-refs/tags/v1", c.specRepo)
		return r
	}
	res := c.gh("api", "repos/"+c.specRepo+"/git/matching-refs/tags/v1")
	if !res.success() {
		if strings.Contains(res.output(), "HTTP 404") {
			r.ko("repository %s not found or not readable", c.specRepo)
		} else {
			r.unverifiable("gh api failed: %s", lastLine(res.output(), 160))
		}
		return r
	}
	var refs []struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &refs); err != nil {
		r.unverifiable("unreadable gh api output: %v", err)
		return r
	}
	var all, stable []string
	for _, ref := range refs {
		all = append(all, strings.TrimPrefix(ref.Ref, "refs/tags/"))
		if v1TagRe.MatchString(ref.Ref) {
			stable = append(stable, strings.TrimPrefix(ref.Ref, "refs/tags/"))
		}
	}
	if len(stable) > 0 {
		r.ok("tag %s present in %s", strings.Join(stable, ", "), c.specRepo)
		return r
	}
	seen := "no v1* tag"
	if len(all) > 0 {
		seen = fmt.Sprintf("only %s", strings.Join(all, ", "))
	}
	r.ko("no stable v1 tag in %s (%s)", c.specRepo, seen)
	return r
}
