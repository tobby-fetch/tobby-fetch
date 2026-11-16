// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"strings"
	"testing"
)

const sha = "124c00e49a5d361830d8420af191233f8d16bce3"

func TestPickRun(t *testing.T) {
	runs := []ghRun{
		{DatabaseID: 3, Event: "pull_request", Status: "completed"},
		{DatabaseID: 2, Event: "push", Status: "in_progress"},
		{DatabaseID: 1, Event: "push", Status: "completed"},
	}
	if r, _ := pickRun(runs); r.DatabaseID != 1 {
		t.Errorf("want the completed push run, got %d", r.DatabaseID)
	}
	if r, _ := pickRun(runs[:2]); r.DatabaseID != 3 {
		t.Errorf("want the completed run of another event, got %d", r.DatabaseID)
	}
	if r, ok := pickRun(runs[1:2]); !ok || r.DatabaseID != 2 {
		t.Errorf("want the unfinished run reported as such, got %v %v", r, ok)
	}
	if _, ok := pickRun(nil); ok {
		t.Error("no run, no pick")
	}
}

func TestFindJobAndVerdict(t *testing.T) {
	jobs := []ghJob{
		{Name: "Tests (race, anti-flaky, coverage floor) (windows-latest)", Status: "completed", Conclusion: "success"},
		{Name: "Tests (race, anti-flaky, coverage floor) (ubuntu-latest)", Status: "completed", Conclusion: "failure", URL: "u"},
		{Name: "Hermetic topology scenario (linux/arm64)", Status: "completed", Conclusion: "skipped"},
		{Name: "i18n completeness (EN, FR)", Status: "in_progress"},
	}
	if _, ok := findJob(jobs, jobTests, "macos"); ok {
		t.Error("macos job does not exist")
	}
	w, _ := findJob(jobs, jobTests, "windows-latest")
	if st, _ := jobVerdict(&w); st != statusOK {
		t.Error("windows job is green")
	}
	u, _ := findJob(jobs, jobTests, "ubuntu-latest")
	if st, _ := jobVerdict(&u); st != statusKO {
		t.Error("a failed job is KO")
	}
	s, _ := findJob(jobs, jobTopology, "linux/arm64")
	if st, why := jobVerdict(&s); st != statusKO || !strings.Contains(why, "skipped") {
		t.Errorf("a skipped job proves nothing: %v %q", st, why)
	}
	p, _ := findJob(jobs, jobI18n, "")
	if st, _ := jobVerdict(&p); st != statusKO {
		t.Error("an unfinished job is not OK")
	}
}

func ciFake() *fakeCmd {
	f := newFake("gh", "git")
	f.answers["git rev-parse --verify --quiet v1.0.0-rc.1^{commit}"] = okOut(sha + "\n")
	f.answers["gh run list -R tobby-fetch/tobby-fetch --commit "+sha+" --workflow ci.yml --json databaseId,conclusion,status,event,url --limit 30"] =
		okOut(`[{"databaseId":7,"conclusion":"success","status":"completed","event":"push","url":"https://x/7"}]`)
	f.answers["gh run view 7 -R tobby-fetch/tobby-fetch --json jobs"] = okOut(`{"jobs":[
	 {"name":"OCI distribution-spec conformance (linux/amd64)","status":"completed","conclusion":"success"},
	 {"name":"OCI distribution-spec conformance (linux/arm64)","status":"completed","conclusion":"failure","url":"https://x/j"},
	 {"name":"Coverage floor on the core packages (>= 70%)","status":"completed","conclusion":"success"},
	 {"name":"i18n completeness (EN, FR)","status":"completed","conclusion":"success"}]}`)
	return f
}

func TestCIJobs(t *testing.T) {
	f := ciFake()
	c := newChecker(t.Context(), f, "v1.0.0-rc.1", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	c.resolveCommit()
	if c.commit != sha {
		t.Fatalf("commit = %q", c.commit)
	}
	conf := c.conformance()
	if conf.status() != statusKO || conf.details[0].status != statusOK || conf.details[1].status != statusKO {
		t.Errorf("conformance: %+v", conf.details)
	}
	if c.coverage().status() != statusOK || c.i18n().status() != statusOK {
		t.Error("coverage and i18n are green")
	}
	e := c.e2e()
	if e.status() != statusKO || !strings.Contains(e.details[0].text, "no job") {
		t.Errorf("a job absent from the run is a KO: %+v", e.details)
	}
	runViews := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, "gh run view") {
			runViews++
		}
	}
	if runViews != 1 {
		t.Errorf("the run's jobs must be fetched once, got %d", runViews)
	}
}

func TestNoCIRunIsKO(t *testing.T) {
	f := ciFake()
	f.answers["gh run list -R tobby-fetch/tobby-fetch --commit "+sha+" --workflow ci.yml --json databaseId,conclusion,status,event,url --limit 30"] = okOut(`[]`)
	c := newChecker(t.Context(), f, "v1.0.0-rc.1", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	c.resolveCommit()
	if r := c.coverage(); r.status() != statusKO || !strings.Contains(r.details[0].text, "no ci.yml run") {
		t.Errorf("%+v", r.details)
	}
}

func TestImageCVEUsesPushRunOnly(t *testing.T) {
	f := ciFake()
	f.answers["gh run list -R tobby-fetch/tobby-fetch --commit "+sha+" --workflow release.yml --json databaseId,conclusion,status,event,url --limit 30 --event push"] =
		okOut(`[{"databaseId":9,"status":"completed","conclusion":"success","event":"push","url":"https://x/9"}]`)
	f.answers["gh run view 9 -R tobby-fetch/tobby-fetch --json jobs"] = okOut(`{"jobs":[{"name":"Image CVE gate (Trivy)","status":"completed","conclusion":"success",
	 "steps":[{"name":"Set up job","conclusion":"success"},{"name":"Scan the published image — critical and high block the release","conclusion":"success"},{"name":"Scan the embedded Go binaries — x","conclusion":"success"}]}]}`)
	c := newChecker(t.Context(), f, "v1.0.0-rc.1", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	c.resolveCommit()
	r := c.imageCVE()
	if r.status() != statusOK || len(r.details) != 3 {
		t.Errorf("%v %+v", r.status(), r.details)
	}
}

func TestGhAbsentIsUnverifiable(t *testing.T) {
	c := newChecker(t.Context(), newFake("git"), "v1.0.0-rc.1", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	if r := c.i18n(); r.status() != statusUnverifiable {
		t.Errorf("%v", r.details)
	}
}

func TestSpecTag(t *testing.T) {
	answer := func(body string) *checker {
		f := newFake("gh")
		f.answers["gh api repos/tobby-fetch/recipe-spec/git/matching-refs/tags/v1"] = okOut(body)
		return newChecker(t.Context(), f, "v1.0.0-rc.1", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	}
	r := answer(`[{"ref":"refs/tags/v1alpha1"},{"ref":"refs/tags/v10"}]`).specTag()
	if r.status() != statusKO || r.failing() || r.expectedKO != "expected at 1.0" {
		t.Errorf("on a rc the missing v1 is an expected KO: %v %+v", r.status(), r)
	}
	if r := answer(`[{"ref":"refs/tags/v1"}]`).specTag(); r.status() != statusOK {
		t.Errorf("v1 present: %+v", r.details)
	}
	f := newFake("gh")
	f.answers["gh api repos/tobby-fetch/recipe-spec/git/matching-refs/tags/v1"] = okOut(`[]`)
	final := newChecker(t.Context(), f, "v1.0.0", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	if r := final.specTag(); !r.failing() {
		t.Error("on the final release the missing tag is a real KO")
	}
}
