// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type ghRun struct {
	DatabaseID int64  `json:"databaseId"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	Event      string `json:"event"`
	URL        string `json:"url"`
}

type ghStep struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

type ghJob struct {
	Name       string   `json:"name"`
	Conclusion string   `json:"conclusion"`
	Status     string   `json:"status"`
	URL        string   `json:"url"`
	Steps      []ghStep `json:"steps"`
}

// pickRun chooses the run that speaks for a commit: the newest completed
// push run, else the newest completed run of any event, else the newest.
// gh lists newest first.
func pickRun(runs []ghRun) (ghRun, bool) {
	for _, r := range runs {
		if r.Event == "push" && r.Status == "completed" {
			return r, true
		}
	}
	for _, r := range runs {
		if r.Status == "completed" {
			return r, true
		}
	}
	if len(runs) > 0 {
		return runs[0], true
	}
	return ghRun{}, false
}

// findJob matches a job by name prefix and, for matrix jobs, a fragment
// (GitHub appends the matrix values to a job name that has no expression).
func findJob(jobs []ghJob, prefix, contains string) (ghJob, bool) {
	for _, j := range jobs {
		if strings.HasPrefix(j.Name, prefix) && strings.Contains(j.Name, contains) {
			return j, true
		}
	}
	return ghJob{}, false
}

// jobVerdict judges a job: only a completed success is OK. A skipped job
// proved nothing.
func jobVerdict(j *ghJob) (st status, why string) {
	switch {
	case j.Status != "completed":
		return statusKO, fmt.Sprintf("job %q not finished (%s)", j.Name, j.Status)
	case j.Conclusion != "success":
		return statusKO, fmt.Sprintf("job %q concluded %s — %s", j.Name, j.Conclusion, j.URL)
	}
	return statusOK, fmt.Sprintf("job %q succeeded — %s", j.Name, j.URL)
}

// workflowJobs returns the jobs of the push run of a workflow at the
// tagged commit. The error text is meant for a result line, with the
// status to attach to it.
func (c *checker) workflowJobs(workflow string, pushOnly bool) ([]ghJob, ghRun, status, string) {
	if !c.cmd.has("gh") {
		return nil, ghRun{}, statusUnverifiable, "gh is absent: gh run list --commit <sha> --workflow " + workflow
	}
	if c.commit == "" {
		return nil, ghRun{}, statusUnverifiable, "commit of " + c.version + " unknown: " + c.commitNote
	}
	args := []string{"run", "list", "-R", c.repo, "--commit", c.commit, "--workflow", workflow,
		"--json", "databaseId,conclusion,status,event,url", "--limit", "30"}
	if pushOnly {
		args = append(args, "--event", "push")
	}
	lr := c.gh(args...)
	if !lr.success() {
		return nil, ghRun{}, statusUnverifiable, "gh run list failed: " + lastLine(lr.output(), 160)
	}
	var runs []ghRun
	if err := json.Unmarshal([]byte(lr.stdout), &runs); err != nil {
		return nil, ghRun{}, statusUnverifiable, "unreadable gh run list output: " + err.Error()
	}
	run, ok := pickRun(runs)
	if !ok {
		return nil, ghRun{}, statusKO, fmt.Sprintf("no %s run for commit %s", workflow, shortSHA(c.commit))
	}
	if jobs, cached := c.runs[run.DatabaseID]; cached {
		return jobs, run, statusOK, ""
	}
	vr := c.gh("run", "view", strconv.FormatInt(run.DatabaseID, 10), "-R", c.repo, "--json", "jobs")
	if !vr.success() {
		return nil, run, statusUnverifiable, "gh run view failed: " + lastLine(vr.output(), 160)
	}
	var payload struct {
		Jobs []ghJob `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(vr.stdout), &payload); err != nil {
		return nil, run, statusUnverifiable, "unreadable gh run view output: " + err.Error()
	}
	c.runs[run.DatabaseID] = payload.Jobs
	return payload.Jobs, run, statusOK, ""
}

// jobCheck adds to r the verdict of one CI job.
func (c *checker) jobCheck(r *result, label, prefix, contains string) {
	jobs, run, st, msg := c.workflowJobs(ciWorkflow, false)
	if st != statusOK {
		r.add(st, "%s: %s", label, msg)
		return
	}
	j, found := findJob(jobs, prefix, contains)
	if !found {
		r.ko("%s: no job %q%s in %s", label, prefix, ifNotEmpty(" matching "+quoted(contains), contains), run.URL)
		return
	}
	st, why := jobVerdict(&j)
	r.add(st, "%s: %s", label, why)
}

func quoted(s string) string { return strconv.Quote(s) }

func ifNotEmpty(s, cond string) string {
	if cond == "" {
		return ""
	}
	return s
}

const (
	ciWorkflow      = "ci.yml"
	releaseWorkflow = "release.yml"

	jobConformance = "OCI distribution-spec conformance"
	jobCoverage    = "Coverage floor on the core packages"
	jobTopology    = "Hermetic topology scenario"
	jobTests       = "Tests (race, anti-flaky, coverage floor)"
	jobI18n        = "i18n completeness"
	jobImageScan   = "Image CVE gate (Trivy)"
)

func (c *checker) conformance() *result {
	r := &result{id: 3, title: "OCI distribution conformance suite passes against the embedded registry"}
	c.jobCheck(r, "linux/amd64", jobConformance, "linux/amd64")
	c.jobCheck(r, "linux/arm64", jobConformance, "linux/arm64")
	return r
}

func (c *checker) coverage() *result {
	r := &result{id: 6, title: "Unit test coverage >= 70 % on the core packages, enforced in CI"}
	c.jobCheck(r, "core coverage floor", jobCoverage, "")
	return r
}

func (c *checker) e2e() *result {
	r := &result{id: 7, title: "e2e green on linux/amd64 and linux/arm64, plus Windows (mirror and destination paths)"}
	c.jobCheck(r, "e2e linux/amd64", jobTopology, "linux/amd64")
	c.jobCheck(r, "e2e linux/arm64", jobTopology, "linux/arm64")
	c.jobCheck(r, "windows run (NFR-018)", jobTests, "windows-latest")
	return r
}

func (c *checker) i18n() *result {
	r := &result{id: 9, title: "i18n complete for EN and FR (automated completeness check)"}
	c.jobCheck(r, "i18n check", jobI18n, "")
	return r
}

// imageCVE is criterion 5: the release pipeline's Trivy gate, on the push
// run of the tag (a rehearsal is a workflow_dispatch run and never counts).
func (c *checker) imageCVE() *result {
	r := &result{id: 5, title: "No known critical or high CVE in the released image (Trivy scan of the release pipeline)"}
	jobs, run, st, msg := c.workflowJobs(releaseWorkflow, true)
	if st != statusOK {
		r.add(st, "release pipeline: %s", msg)
		return r
	}
	j, found := findJob(jobs, jobImageScan, "")
	if !found {
		r.ko("no job %q in %s", jobImageScan, run.URL)
		return r
	}
	st, why := jobVerdict(&j)
	r.add(st, "%s", why)
	if st == statusOK {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Name, "Scan ") {
				r.add(stepStatus(s), "step %q: %s", s.Name, s.Conclusion)
			}
		}
	}
	return r
}

func stepStatus(s ghStep) status {
	if s.Conclusion == "success" {
		return statusOK
	}
	return statusKO
}
