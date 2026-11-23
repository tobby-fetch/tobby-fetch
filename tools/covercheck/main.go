// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command covercheck enforces the release criterion "coverage >= 70 % on
// the core packages" (DEVELOPMENT-PLAN §3, NFR-016) from a `go test
// -coverprofile` profile:
//
//	go test -coverprofile=cover.out ./internal/...
//	go run ./tools/covercheck -profile cover.out -packages tools/coverage-core.txt
//
// The core packages are named in a versioned file, not derived: moving a
// package in or out of the gate is a reviewed edit. A listed package that
// the profile does not measure (renamed, deleted, tests missing) fails the
// gate, because a floor that silently stops applying is no floor.
//
// Coverage is the statement coverage `go test -cover` reports for the
// package's own tests: covered statements over all statements, with
// repeated blocks merged (a block is covered if any run covered it).
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// pkgStats counts the statements of one package.
type pkgStats struct{ total, covered int }

func (s pkgStats) percent() float64 {
	if s.total == 0 {
		return 0
	}
	return 100 * float64(s.covered) / float64(s.total)
}

// block identifies one profile block; the same block may appear several
// times when profiles of several runs are concatenated.
type block struct {
	file  string
	start string
	end   string
}

// parseProfile reads a Go coverage profile and returns the statistics per
// package import path.
func parseProfile(r io.Reader) (map[string]pkgStats, error) {
	type val struct {
		stmts   int
		covered bool
	}
	blocks := map[block]*val{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(line, "mode:") {
				continue
			}
			return nil, errors.New("not a coverage profile: first line must be \"mode: ...\"")
		}
		// <file>:<l>.<c>,<l>.<c> <numStmt> <count>
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			return nil, fmt.Errorf("malformed profile line %q", line)
		}
		file := line[:colon]
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed profile line %q", line)
		}
		pos := strings.SplitN(fields[0], ",", 2)
		if len(pos) != 2 {
			return nil, fmt.Errorf("malformed profile line %q", line)
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("malformed statement count in %q: %w", line, err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("malformed hit count in %q: %w", line, err)
		}
		k := block{file: file, start: pos[0], end: pos[1]}
		if v, ok := blocks[k]; ok {
			v.covered = v.covered || count > 0
		} else {
			blocks[k] = &val{stmts: stmts, covered: count > 0}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if first {
		return nil, errors.New("empty coverage profile")
	}
	out := map[string]pkgStats{}
	for k, v := range blocks {
		pkg := path.Dir(k.file)
		s := out[pkg]
		s.total += v.stmts
		if v.covered {
			s.covered += v.stmts
		}
		out[pkg] = s
	}
	return out, nil
}

// parsePackages reads the core-package list: one module-relative path per
// line, "#" starts a comment.
func parsePackages(r io.Reader) ([]string, error) {
	var pkgs []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			return nil, fmt.Errorf("package list: %q: one path per line", line)
		}
		if seen[line] {
			return nil, fmt.Errorf("package list: %q is listed twice", line)
		}
		seen[line] = true
		pkgs = append(pkgs, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, errors.New("package list is empty: the gate would check nothing")
	}
	return pkgs, nil
}

// row is one line of the report.
type row struct {
	pkg     string
	stats   pkgStats
	ok      bool
	problem string
}

// check compares every listed package against the threshold.
func check(stats map[string]pkgStats, module string, pkgs []string, threshold float64) []row {
	rows := make([]row, 0, len(pkgs))
	for _, p := range pkgs {
		s, found := stats[module+"/"+p]
		switch {
		case !found || s.total == 0:
			rows = append(rows, row{pkg: p, problem: "not measured by the profile (missing, renamed, or no tests)"})
		case s.percent() < threshold:
			rows = append(rows, row{pkg: p, stats: s, problem: fmt.Sprintf("%.1f%% < %.0f%% floor", s.percent(), threshold)})
		default:
			rows = append(rows, row{pkg: p, stats: s, ok: true})
		}
	}
	return rows
}

func report(w io.Writer, rows []row, threshold float64) bool {
	sort.Slice(rows, func(i, j int) bool { return rows[i].pkg < rows[j].pkg })
	pass := true
	for _, r := range rows {
		if r.ok {
			_, _ = fmt.Fprintf(w, "ok   %-24s %5.1f%%  (%d/%d statements)\n", r.pkg, r.stats.percent(), r.stats.covered, r.stats.total)
			continue
		}
		pass = false
		_, _ = fmt.Fprintf(w, "FAIL %-24s %s\n", r.pkg, r.problem)
	}
	if pass {
		_, _ = fmt.Fprintf(w, "core-package coverage floor met (>= %.0f%% on %d packages)\n", threshold, len(rows))
	} else {
		_, _ = fmt.Fprintf(w, "core-package coverage floor NOT met (>= %.0f%% required)\n", threshold)
	}
	return pass
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("covercheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "coverage profile written by go test -coverprofile")
	list := fs.String("packages", "", "file naming the core packages, one module-relative path per line")
	module := fs.String("module", "github.com/tobby-fetch/tobby-fetch", "module path the profile's file names start with")
	threshold := fs.Float64("threshold", 70, "minimum statement coverage per core package, in percent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profile == "" || *list == "" {
		_, _ = fmt.Fprintln(stderr, "covercheck: -profile and -packages are required")
		return 2
	}
	pf, err := os.Open(*profile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "covercheck:", err)
		return 2
	}
	defer func() { _ = pf.Close() }()
	stats, err := parseProfile(pf)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "covercheck:", err)
		return 2
	}
	lf, err := os.Open(*list)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "covercheck:", err)
		return 2
	}
	defer func() { _ = lf.Close() }()
	pkgs, err := parsePackages(lf)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "covercheck:", err)
		return 2
	}
	if !report(stdout, check(stats, *module, pkgs, *threshold), *threshold) {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
