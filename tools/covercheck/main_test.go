// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mod = "example.com/m"

// profile: pkg a has 10 statements, 8 covered (80 %); pkg b has 10, 6
// covered (60 %); the repeated block of a (covered in the second run only)
// must count once and as covered.
const profile = `mode: set
example.com/m/internal/a/x.go:1.1,2.2 5 1
example.com/m/internal/a/x.go:3.1,4.2 3 0
example.com/m/internal/a/y.go:1.1,2.2 2 1
example.com/m/internal/b/x.go:1.1,2.2 6 1
example.com/m/internal/b/x.go:3.1,4.2 4 0
example.com/m/internal/a/x.go:3.1,4.2 3 1
`

func TestParseProfile(t *testing.T) {
	got, err := parseProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	a := got[mod+"/internal/a"]
	if a.total != 10 || a.covered != 10 {
		t.Errorf("a = %+v, want 10/10 (the merged block is covered)", a)
	}
	b := got[mod+"/internal/b"]
	if b.total != 10 || b.covered != 6 || b.percent() != 60 {
		t.Errorf("b = %+v, want 6/10", b)
	}
}

func TestParseProfileRejectsGarbage(t *testing.T) {
	for name, in := range map[string]string{
		"empty":        "",
		"no mode":      "example.com/m/a/x.go:1.1,2.2 5 1\n",
		"short":        "mode: set\nnonsense\n",
		"bad count":    "mode: set\nexample.com/m/a/x.go:1.1,2.2 5 x\n",
		"bad position": "mode: set\nexample.com/m/a/x.go:1.1 5 1\n",
	} {
		if _, err := parseProfile(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParsePackages(t *testing.T) {
	got, err := parsePackages(strings.NewReader("# core\ninternal/a # fetch\n\n  internal/b\n"))
	if err != nil || len(got) != 2 || got[0] != "internal/a" || got[1] != "internal/b" {
		t.Fatalf("got %v, %v", got, err)
	}
	for name, in := range map[string]string{
		"empty":     "# nothing\n",
		"duplicate": "internal/a\ninternal/a\n",
		"two words": "internal/a internal/b\n",
	} {
		if _, err := parsePackages(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheck(t *testing.T) {
	stats, err := parseProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	rows := check(stats, mod, []string{"internal/a", "internal/b", "internal/missing"}, 70)
	want := map[string]struct {
		ok      bool
		problem string
	}{
		"internal/a":       {true, ""},
		"internal/b":       {false, "60.0% < 70% floor"},
		"internal/missing": {false, "not measured"},
	}
	for _, r := range rows {
		w := want[r.pkg]
		if r.ok != w.ok || !strings.Contains(r.problem, w.problem) {
			t.Errorf("%s: ok=%v problem=%q, want ok=%v problem~%q", r.pkg, r.ok, r.problem, w.ok, w.problem)
		}
	}
}

func TestThresholdIsInclusive(t *testing.T) {
	stats := map[string]pkgStats{mod + "/internal/a": {total: 10, covered: 7}}
	if rows := check(stats, mod, []string{"internal/a"}, 70); !rows[0].ok {
		t.Errorf("exactly 70%% must pass: %+v", rows[0])
	}
}

func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	prof := filepath.Join(dir, "cover.out")
	if err := os.WriteFile(prof, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var out, errb bytes.Buffer
	if rc := run([]string{"-profile", prof, "-packages", write("ok.txt", "internal/a\n"), "-module", mod}, &out, &errb); rc != 0 {
		t.Errorf("passing list exited %d: %s%s", rc, out.String(), errb.String())
	}
	out.Reset()
	if rc := run([]string{"-profile", prof, "-packages", write("bad.txt", "internal/a\ninternal/b\n"), "-module", mod}, &out, &errb); rc != 1 {
		t.Errorf("failing list exited %d, want 1: %s", rc, out.String())
	}
	if !strings.Contains(out.String(), "FAIL internal/b") || !strings.Contains(out.String(), "NOT met") {
		t.Errorf("report does not name the failure:\n%s", out.String())
	}
	if rc := run([]string{"-profile", prof}, &out, &errb); rc != 2 {
		t.Errorf("missing flag exited %d, want 2", rc)
	}
	if rc := run([]string{"-profile", filepath.Join(dir, "nope"), "-packages", "x"}, &out, &errb); rc != 2 {
		t.Errorf("missing profile exited %d, want 2", rc)
	}
}
