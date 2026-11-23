// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// checker carries what every criterion needs: the tool runner, the
// repositories, the version and the commit it resolves to.
type checker struct {
	cmd commander
	ctx context.Context

	version  string // v1.0.0-rc.1
	repo     string // tobby-fetch/tobby-fetch
	specRepo string // tobby-fetch/recipe-spec
	dir      string // local clone to read the tagged tree from

	quick      bool // verify one binary instead of all of them
	skipBuild  bool // do not build the website
	commit     string
	commitNote string // why commit is empty, when it is

	runs map[int64][]ghJob // jobs of a run, fetched once
}

func newChecker(ctx context.Context, cmd commander, version, repo, specRepo, dir string) *checker {
	return &checker{
		cmd: cmd, ctx: ctx, version: version, repo: repo, specRepo: specRepo, dir: dir,
		runs: map[int64][]ghJob{},
	}
}

// beforeV1 reports a version that is not 1.0.0 or later: a 0.x release,
// or a pre-release of any version (v1.0.0-rc.1).
func (c *checker) beforeV1() bool {
	return strings.Contains(c.version, "-") || strings.HasPrefix(c.version, "v0.")
}

// git runs git in the local clone.
func (c *checker) git(args ...string) execResult {
	return c.cmd.run(c.ctx, c.dir, nil, "git", args...)
}

// gh runs the GitHub CLI.
func (c *checker) gh(args ...string) execResult {
	return c.cmd.run(c.ctx, c.dir, nil, "gh", args...)
}

// resolveCommit finds the commit the tag points to: the local clone first
// (no network), then the GitHub API.
func (c *checker) resolveCommit() {
	if c.cmd.has("git") {
		if r := c.git("rev-parse", "--verify", "--quiet", c.version+"^{commit}"); r.success() {
			c.commit = strings.TrimSpace(r.stdout)
			return
		}
	}
	if c.cmd.has("gh") {
		r := c.gh("api", "repos/"+c.repo+"/commits/"+c.version, "--jq", ".sha")
		if r.success() && strings.TrimSpace(r.stdout) != "" {
			c.commit = strings.TrimSpace(r.stdout)
			return
		}
		c.commitNote = fmt.Sprintf("tag %s not found in %s (%s)", c.version, c.repo, lastLine(r.output(), 100))
		return
	}
	c.commitNote = fmt.Sprintf("tag %s unknown locally and gh is absent", c.version)
}

// localTag reports whether the tagged tree can be read from the clone.
func (c *checker) localTag() bool {
	if !c.cmd.has("git") {
		return false
	}
	return c.git("cat-file", "-e", c.version+"^{commit}").success()
}

// lsTree lists the files of a directory at the tag.
func (c *checker) lsTree(dir string) ([]string, bool) {
	r := c.git("ls-tree", "-r", "--name-only", c.version, "--", dir)
	if !r.success() {
		return nil, false
	}
	var out []string
	for _, l := range strings.Split(r.stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, true
}

// show reads one file at the tag.
func (c *checker) show(file string) (string, bool) {
	r := c.git("show", c.version+":"+file)
	return r.stdout, r.success()
}

// withTimeout bounds one external verification.
func (c *checker) withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.ctx, d)
}

const needTagMsg = "the tag is not in the local clone: git fetch --tags, then run again"

// all returns the checklist, in the order of DEVELOPMENT-PLAN §3 with the
// §2.7 exit criteria folded into the criterion they belong to.
func (c *checker) all() []*result {
	c.resolveCommit()
	return []*result{
		c.modes(),
		c.crucible(),
		c.conformance(),
		c.supplyChain(),
		c.imageCVE(),
		c.coverage(),
		c.e2e(),
		c.documentation(),
		c.i18n(),
		c.authMethods(),
		c.specTag(),
	}
}

// modes is criterion 1. A signed acceptance record is a human act; the
// script says so rather than approximating it.
func (c *checker) modes() *result {
	r := &result{id: 1, title: "Both operating modes validated end to end and formally accepted"}
	r.unverifiable("not verifiable by a script: the proof is the signed acceptance record (procès-verbal de recette, RCT-U1 and RCT-U2) of the release candidate")
	return r
}
