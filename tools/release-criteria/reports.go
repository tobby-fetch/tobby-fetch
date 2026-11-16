// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// A crucible report is the raw output of crucible/lib.sh: one line per
// claim, `ok   <claim>`, `info <fact>` or `FAIL <claim>`, a first
// `scenario <name> started` line, a last `scenario <name> complete` line,
// and, when the acceptance host wrote one, `#` comment lines giving the
// host, the commit and the verified destruction of the node.
type reportSummary struct {
	checks    int
	fails     []string
	started   bool
	completed bool
	// comments are the `#` lines; destroyed is true when one of them says
	// the acceptance node was destroyed.
	comments  []string
	destroyed bool
}

var (
	startedRe   = regexp.MustCompile(`^ok\s+scenario\s.*\bstarted\b`)
	completeRe  = regexp.MustCompile(`^ok\s+scenario\s.*\bcomplete\b`)
	failRe      = regexp.MustCompile(`^FAIL\b`)
	okRe        = regexp.MustCompile(`^ok\s`)
	destroyedRe = regexp.MustCompile(`(?i)\bdestroy(ed|ing)?\b`)
)

func parseReport(body string) reportSummary {
	var s reportSummary
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r \t")
		switch {
		case strings.HasPrefix(line, "#"):
			s.comments = append(s.comments, line)
			if destroyedRe.MatchString(line) {
				s.destroyed = true
			}
		case failRe.MatchString(line):
			s.fails = append(s.fails, strings.TrimSpace(strings.TrimPrefix(line, "FAIL")))
		case startedRe.MatchString(line):
			s.started = true
			s.checks++
		case completeRe.MatchString(line):
			s.completed = true
			s.checks++
		case okRe.MatchString(line):
			s.checks++
		}
	}
	return s
}

// verdict judges one parsed report. strict (the final milestone) also
// demands the destruction footer: the cahier de recette requires host, date
// and verified destruction at the foot of the m7 reports.
func (s *reportSummary) verdict(strict bool) (st status, why string) {
	switch {
	case len(s.fails) > 0:
		return statusKO, fmt.Sprintf("%d FAIL line(s), first: %s", len(s.fails), truncate(s.fails[0], 120))
	case !s.started:
		return statusKO, "no `scenario … started` line: not a crucible report"
	case !s.completed:
		return statusKO, "no `scenario … complete` line: the run did not finish"
	case strict && !s.destroyed:
		return statusKO, "no footer stating the acceptance node was destroyed"
	}
	return statusOK, fmt.Sprintf("%d checks, scenario complete", s.checks)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

const (
	acceptanceDir = "docs/acceptance"
	lastMilestone = 7
)

var reportNameRe = regexp.MustCompile(`^milestone-(\d+)-.*crucible-report.*\.txt$`)

// reportFile is one published report: its name under docs/acceptance and
// the milestone it is filed under.
type reportFile struct {
	name      string
	milestone int
}

// classifyReports keeps the reports that count: `milestone-N-…crucible-report….txt`
// (the hermetic-tier topology reports have their own format and are CI
// evidence, not crucible reports), minus the ones whose name says FAILED (a first run kept for the record
// next to its green replay). Order is by name.
func classifyReports(names []string) []reportFile {
	var out []reportFile
	for _, n := range names {
		base := path.Base(n)
		m := reportNameRe.FindStringSubmatch(base)
		if m == nil || strings.Contains(strings.ToUpper(base), "FAILED") {
			continue
		}
		var ms int
		_, _ = fmt.Sscanf(m[1], "%d", &ms)
		out = append(out, reportFile{name: base, milestone: ms})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// scenarioNames lists the scenario scripts of a milestone directory
// (crucible/scenarios/mN): every *.sh except run.sh and the *-lib.sh
// helpers, by base name without extension.
func scenarioNames(files []string) []string {
	var out []string
	for _, f := range files {
		base := path.Base(f)
		if !strings.HasSuffix(base, ".sh") || base == "run.sh" || base == "lib.sh" || strings.HasSuffix(base, "-lib.sh") {
			continue
		}
		out = append(out, strings.TrimSuffix(base, ".sh"))
	}
	sort.Strings(out)
	return out
}
