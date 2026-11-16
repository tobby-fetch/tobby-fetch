// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command release-criteria checks the v1.0.0 release criteria
// (DEVELOPMENT-PLAN §3) of a given version against real evidence and prints
// a checklist: OK, KO or NON VÉRIFIABLE ICI for each criterion, with the
// proof or the reason.
//
//	go run ./tools/release-criteria --version v1.0.0-rc.1
//
// It reads the tagged tree from the local clone (git), the CI and release
// pipeline verdicts and the recipe-spec tags from GitHub (gh), and runs
// slsa-verifier and cosign on the published artifacts with an empty Docker
// configuration. A tool that is absent yields NON VÉRIFIABLE ICI and the
// command to run, never an OK. The exit code is non-zero when a verifiable
// criterion is KO (a KO that is expected for the version, such as the
// recipe-spec v1 tag on a release candidate, does not count).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
)

var versionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

func main() { os.Exit(realMain()) }

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx, osCommander{}, os.Args[1:], os.Stdout, os.Stderr)
}

func run(ctx context.Context, cmd commander, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release-criteria", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "release to check, e.g. v1.0.0-rc.1 (required)")
	repo := fs.String("repo", "tobby-fetch/tobby-fetch", "GitHub repository of the release")
	specRepo := fs.String("spec-repo", "tobby-fetch/recipe-spec", "GitHub repository of the format specification")
	dir := fs.String("dir", ".", "local clone holding the tag")
	quick := fs.Bool("quick", false, "verify the linux/amd64 binary only (no packages, other binaries or kit)")
	build := fs.Bool("website-build", true, "build the website when the working tree is at the tag")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !versionRe.MatchString(*version) {
		_, _ = fmt.Fprintln(stderr, "release-criteria: --version vX.Y.Z[-pre] is required")
		return 2
	}
	c := newChecker(ctx, cmd, *version, *repo, *specRepo, *dir)
	c.quick = *quick
	c.skipBuild = !*build
	results := c.all()
	render(stdout, *version, c.commit, results)
	if s := summarize(results); s.ko > 0 {
		return 1
	}
	return 0
}
