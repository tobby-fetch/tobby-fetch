// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name) //nolint:gosec // G304: fixed fixture names of this package's tests
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeCmd answers commands from a table keyed by the full command line; a
// command with no entry fails with exit 99 so a test notices a stray call.
type fakeCmd struct {
	tools   map[string]bool
	answers map[string]execResult
	calls   []string
	envs    map[string][]string
}

func newFake(tools ...string) *fakeCmd {
	f := &fakeCmd{tools: map[string]bool{}, answers: map[string]execResult{}, envs: map[string][]string{}}
	for _, t := range tools {
		f.tools[t] = true
	}
	return f
}

func (f *fakeCmd) has(name string) bool { return f.tools[name] }

func (f *fakeCmd) run(_ context.Context, _ string, env []string, name string, args ...string) execResult {
	key := cmdline(name, args...)
	f.calls = append(f.calls, key)
	f.envs[key] = env
	if r, ok := f.answers[key]; ok {
		return r
	}
	for prefix, r := range f.answers {
		if strings.HasSuffix(prefix, "*") && strings.HasPrefix(key, strings.TrimSuffix(prefix, "*")) {
			return r
		}
	}
	return execResult{exitCode: 99, stderr: "unexpected command: " + key}
}

func okOut(s string) execResult { return execResult{stdout: s} }

func (f *fakeCmd) called(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestParseReport(t *testing.T) {
	s := parseReport(fixture(t, "report-ok.txt"))
	if !s.started || !s.completed || len(s.fails) != 0 || !s.destroyed {
		t.Fatalf("green report misread: %+v", s)
	}
	if s.checks != 4 {
		t.Errorf("checks = %d, want 4 (info lines are not checks)", s.checks)
	}
	if st, _ := s.verdict(true); st != statusOK {
		t.Errorf("strict verdict = %v, want OK", st)
	}

	f := parseReport(fixture(t, "report-fail.txt"))
	if st, why := f.verdict(false); st != statusKO || !strings.Contains(why, "advisory push") {
		t.Errorf("FAIL report: %v %q", st, why)
	}
	i := parseReport(fixture(t, "report-incomplete.txt"))
	if st, why := i.verdict(false); st != statusKO || !strings.Contains(why, "complete") {
		t.Errorf("incomplete report: %v %q", st, why)
	}
	n := parseReport(fixture(t, "report-nofooter.txt"))
	if st, _ := n.verdict(false); st != statusOK {
		t.Errorf("old report without footer must pass when not strict")
	}
	if st, why := n.verdict(true); st != statusKO || !strings.Contains(why, "destroyed") {
		t.Errorf("strict report without footer: %v %q", st, why)
	}
	if st, _ := func() (status, string) { g := parseReport("garbage\n"); return g.verdict(false) }(); st != statusKO {
		t.Errorf("a file that is no report must not pass")
	}
}

func TestClassifyReports(t *testing.T) {
	got := classifyReports([]string{
		"docs/acceptance/milestone-6-crucible-report-a-scanning-first-run-FAILED.txt",
		"docs/acceptance/milestone-6-crucible-report-a-scanning.txt",
		"docs/acceptance/milestone-4-quality-audit.md",
		"docs/acceptance/release-0.5.1-crucible-report.txt",
		"docs/acceptance/milestone-2-topology-report.txt",
		"docs/acceptance/milestone-6-regression-m1-crucible-report.txt",
	})
	var names []string
	for _, r := range got {
		names = append(names, fmt.Sprintf("%d:%s", r.milestone, r.name))
	}
	want := "6:milestone-6-crucible-report-a-scanning.txt 6:milestone-6-regression-m1-crucible-report.txt"
	if strings.Join(names, " ") != want {
		t.Errorf("got %v", names)
	}
}

func TestScenarioNames(t *testing.T) {
	got := scenarioNames([]string{
		"crucible/scenarios/m7/run.sh", "crucible/scenarios/m7/b-lib.sh",
		"crucible/scenarios/m7/z-crown-rke2.sh", "crucible/scenarios/m7/a-x.sh",
		"crucible/scenarios/m7/crown/crown-lib.sh", "crucible/scenarios/m7/README.md",
	})
	if strings.Join(got, ",") != "a-x,crown-lib,z-crown-rke2" && strings.Join(got, ",") != "a-x,z-crown-rke2" {
		t.Errorf("got %v", got)
	}
}

func TestAggregate(t *testing.T) {
	cases := []struct {
		name string
		in   []status
		want status
	}{
		{"empty is not verified", nil, statusUnverifiable},
		{"all ok", []status{statusOK, statusOK}, statusOK},
		{"unverifiable part keeps it unverifiable", []status{statusOK, statusUnverifiable}, statusUnverifiable},
		{"ko wins over unverifiable", []status{statusUnverifiable, statusKO, statusOK}, statusKO},
	}
	for _, tc := range cases {
		var ds []detail
		for _, s := range tc.in {
			ds = append(ds, detail{status: s})
		}
		if got := aggregate(ds); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExitCodeAndExpectedKO(t *testing.T) {
	expected := &result{id: 11, expectedKO: "expected at 1.0"}
	expected.ko("no tag")
	unver := &result{id: 1}
	unver.unverifiable("manual")
	good := &result{id: 2}
	good.ok("fine")
	if expected.failing() || unver.failing() || good.failing() {
		t.Error("none of these may fail the run")
	}
	broken := &result{id: 3}
	broken.ko("broken")
	if !broken.failing() {
		t.Error("an unexpected KO must fail the run")
	}
	s := summarize([]*result{expected, unver, good, broken})
	if s != (summary{ok: 1, ko: 1, expectedKO: 1, unverifiable: 1}) {
		t.Errorf("summary = %+v", s)
	}
	var b bytes.Buffer
	render(&b, "v1.0.0-rc.1", "0123456789abcdef", []*result{expected, unver})
	out := b.String()
	for _, want := range []string{"[KO] 11.", "expected at 1.0", "[NON VÉRIFIABLE ICI] 1.", "0123456789ab"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), newFake(), []string{"--version", "main"}, &out, &errb); code != 2 {
		t.Errorf("bad version: exit %d", code)
	}
	if code := run(context.Background(), newFake(), nil, &out, &errb); code != 2 {
		t.Errorf("no version: exit %d", code)
	}
}

// With no tool at all, nothing may be OK and nothing may be KO: the run is
// a list of "not verifiable" with the commands to run, and exits 0.
func TestNoToolsNoFalseOK(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), newFake(), []string{"--version", "v1.0.0"}, &out, &errb)
	if code != 0 {
		t.Errorf("exit %d, want 0 (nothing verifiable is KO)\n%s", code, out.String())
	}
	s := out.String()
	if strings.Contains(s, "[OK]") {
		t.Errorf("an OK without any tool:\n%s", s)
	}
	if !strings.Contains(s, "gh run list") || !strings.Contains(s, "gh api repos/tobby-fetch/recipe-spec") {
		t.Errorf("the commands to run are missing:\n%s", s)
	}
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
