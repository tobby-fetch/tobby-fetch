// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"strings"
	"testing"
)

func gitFake(files map[string]string) *fakeCmd {
	f := newFake("git", "gh")
	f.answers["git rev-parse --verify --quiet v1.0.0^{commit}"] = okOut(sha + "\n")
	f.answers["git cat-file -e v1.0.0^{commit}"] = execResult{}
	byDir := map[string][]string{}
	for p := range files {
		for _, d := range []string{"docs/acceptance", "crucible/scenarios/m6", "crucible/scenarios/m7", "website/src/content/docs/docs", "website/src/content/docs/fr/docs"} {
			if strings.HasPrefix(p, d+"/") {
				byDir[d] = append(byDir[d], p)
			}
		}
		f.answers["git show v1.0.0:"+p] = okOut(files[p])
	}
	for d, ps := range byDir {
		f.answers["git ls-tree -r --name-only v1.0.0 -- "+d] = okOut(strings.Join(ps, "\n"))
	}
	return f
}

func newGitChecker(t *testing.T, f *fakeCmd) *checker {
	t.Helper()
	c := newChecker(t.Context(), f, "v1.0.0", "tobby-fetch/tobby-fetch", "tobby-fetch/recipe-spec", ".")
	c.resolveCommit()
	return c
}

func TestCrucibleMilestones(t *testing.T) {
	green := fixture(t, "report-ok.txt")
	files := map[string]string{
		"crucible/scenarios/m7/a-x.sh":          "",
		"crucible/scenarios/m7/z-crown-rke2.sh": "",
		"crucible/scenarios/m6/b-auth.sh":       "",
	}
	for m := 1; m <= 5; m++ {
		files["docs/acceptance/milestone-"+string(rune('0'+m))+"-crucible-report.txt"] = fixture(t, "report-nofooter.txt")
	}
	files["docs/acceptance/milestone-6-crucible-report-b-auth.txt"] = fixture(t, "report-nofooter.txt")
	files["docs/acceptance/milestone-7-crucible-report-a-x.txt"] = green
	files["docs/acceptance/milestone-7-crucible-report-z-crown-rke2.txt"] = green

	r := newGitChecker(t, gitFake(files)).crucible()
	if r.status() != statusOK {
		t.Fatalf("complete set must pass:\n%+v", r.details)
	}
	var crown bool
	for _, d := range r.details {
		crown = crown || strings.Contains(d.text, "crown scenario (z-crown-rke2) green")
	}
	if !crown {
		t.Error("the crown must be named in the proof")
	}

	// no m7 crown report
	delete(files, "docs/acceptance/milestone-7-crucible-report-z-crown-rke2.txt")
	r = newGitChecker(t, gitFake(files)).crucible()
	if r.status() != statusKO || !strings.Contains(joined(r), "scenario z-crown-rke2 has no published report") || !strings.Contains(joined(r), "no report for the crown") {
		t.Errorf("missing crown report:\n%s", joined(r))
	}

	// no report at all for m4, a failing m5
	delete(files, "docs/acceptance/milestone-4-crucible-report.txt")
	files["docs/acceptance/milestone-5-crucible-report.txt"] = fixture(t, "report-fail.txt")
	r = newGitChecker(t, gitFake(files)).crucible()
	if !strings.Contains(joined(r), "m4: no raw report") || !strings.Contains(joined(r), "m5: milestone-5-crucible-report.txt — 1 FAIL") {
		t.Errorf("%s", joined(r))
	}
}

func joined(r *result) string {
	var b strings.Builder
	for _, d := range r.details {
		b.WriteString(d.text + "\n")
	}
	return b.String()
}

func TestCrucibleNeedsTheTag(t *testing.T) {
	f := newFake("git", "gh")
	f.answers["git rev-parse --verify --quiet v1.0.0^{commit}"] = execResult{exitCode: 1}
	f.answers["gh api repos/tobby-fetch/tobby-fetch/commits/v1.0.0 --jq .sha"] = okOut(sha)
	c := newGitChecker(t, f)
	if c.commit != sha {
		t.Errorf("the API fallback must resolve the commit, got %q", c.commit)
	}
	if r := c.crucible(); r.status() != statusUnverifiable {
		t.Errorf("without the tag locally the reports cannot be read: %+v", r.details)
	}
}

func TestDocParity(t *testing.T) {
	files := map[string]string{
		"website/src/content/docs/docs/a.md":              "",
		"website/src/content/docs/docs/sub/b.mdx":         "",
		"website/src/content/docs/docs/sub/c.md":          "",
		"website/src/content/docs/fr/docs/a.md":           "",
		"website/src/content/docs/fr/docs/sub/b.md":       "",
		"website/src/content/docs/fr/docs/only-french.md": "",
	}
	c := newGitChecker(t, gitFake(files))
	c.skipBuild = true
	r := c.documentation()
	text := joined(r)
	if r.status() != statusKO || !strings.Contains(text, "1 English page(s) without a French counterpart: sub/c") {
		t.Errorf("%s", text)
	}
	if !strings.Contains(text, "note: 1 French-only page(s)") {
		t.Errorf("French-only pages are a note: %s", text)
	}

	delete(files, "website/src/content/docs/docs/sub/c.md")
	c = newGitChecker(t, gitFake(files))
	c.skipBuild = true
	r = c.documentation()
	// parity fine; the skipped build keeps it from being a full OK
	if r.status() != statusUnverifiable {
		t.Errorf("parity alone must not read as a complete OK: %s", joined(r))
	}
}

func TestAuthMethodTests(t *testing.T) {
	f := gitFake(nil)
	have := map[string]bool{"TestOIDCHappyPath": true, "TestOIDCAttacks": true, "TestSAMLHappyPath": true, "TestSAMLAttacks": true,
		"TestTokenLifecycle": true, "TestTokenExpiry": true, "TestRegistryMiddleware": true}
	for _, m := range authTests {
		for _, name := range m.tests {
			key := "git grep -q -E ^func " + name + `\( v1.0.0 -- ` + m.dir
			if have[name] {
				f.answers[key] = execResult{}
			} else {
				f.answers[key] = execResult{exitCode: 1}
			}
		}
	}
	f.answers["gh run list -R tobby-fetch/tobby-fetch --commit "+sha+" --workflow ci.yml --json databaseId,conclusion,status,event,url --limit 30"] =
		okOut(`[{"databaseId":7,"status":"completed","conclusion":"success","event":"push"}]`)
	f.answers["gh run view 7 -R tobby-fetch/tobby-fetch --json jobs"] = okOut(`{"jobs":[{"name":"Tests (race, anti-flaky, coverage floor) (ubuntu-latest)","status":"completed","conclusion":"success"}]}`)
	r := newGitChecker(t, f).authMethods()
	if r.status() != statusKO || !strings.Contains(joined(r), "basic auth: test(s) not found in internal/auth: TestMachineAuthAudited") {
		t.Errorf("%s", joined(r))
	}
	have["TestMachineAuthAudited"] = true
	for _, m := range authTests {
		for _, name := range m.tests {
			f.answers["git grep -q -E ^func "+name+`\( v1.0.0 -- `+m.dir] = execResult{}
		}
	}
	if r := newGitChecker(t, f).authMethods(); r.status() != statusOK {
		t.Errorf("%s", joined(r))
	}
}
