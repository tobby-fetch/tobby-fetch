// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"fmt"
	"strings"
)

// crucible is criterion 2: the raw acceptance reports of milestones 1 to 7
// exist at the tag, are green, and (m6, m7) cover every scenario script of
// the milestone — the m7 set including the z-crown-rke2 crown.
func (c *checker) crucible() *result {
	r := &result{id: 2, title: "Crucible suite green, milestones 1-7, crown included; raw reports published"}
	if !c.cmd.has("git") || !c.localTag() {
		r.unverifiable("%s", needTagMsg)
		return r
	}
	names, ok := c.lsTree(acceptanceDir)
	if !ok {
		r.ko("cannot list %s at %s", acceptanceDir, c.version)
		return r
	}
	reports := classifyReports(names)
	for m := 1; m <= lastMilestone; m++ {
		c.milestoneReports(r, m, reports)
	}
	return r
}

func (c *checker) milestoneReports(r *result, m int, all []reportFile) {
	var mine []reportFile
	for _, rf := range all {
		if rf.milestone == m {
			mine = append(mine, rf)
		}
	}
	if len(mine) == 0 {
		r.ko("m%d: no raw report under %s at %s", m, acceptanceDir, c.version)
		return
	}
	strict := m == lastMilestone
	read := map[string]*reportSummary{}
	for _, rf := range mine {
		body, ok := c.show(acceptanceDir + "/" + rf.name)
		if !ok {
			r.ko("m%d: cannot read %s", m, rf.name)
			continue
		}
		s := parseReport(body)
		read[rf.name] = &s
		st, why := s.verdict(strict)
		r.add(st, "m%d: %s — %s", m, rf.name, why)
	}
	// Milestones 6 and 7 are several scenarios, each with its own report: a
	// scenario script without a green report is a hole in the suite.
	if m < 6 {
		return
	}
	files, ok := c.lsTree(fmt.Sprintf("crucible/scenarios/m%d", m))
	if !ok {
		return
	}
	for _, sc := range scenarioNames(files) {
		found := false
		for _, rf := range mine {
			if strings.Contains(rf.name, sc) {
				found = true
				break
			}
		}
		if !found {
			r.ko("m%d: scenario %s has no published report", m, sc)
		}
	}
	if m == lastMilestone {
		crown := false
		for _, rf := range mine {
			if strings.Contains(rf.name, "crown") {
				crown = true
				if s := read[rf.name]; s != nil {
					if st, _ := s.verdict(strict); st == statusOK {
						r.ok("m%d: crown scenario (z-crown-rke2) green", m)
					}
				}
			}
		}
		if !crown {
			r.ko("m%d: no report for the crown scenario (z-crown-rke2)", m)
		}
	}
}
