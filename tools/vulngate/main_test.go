// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"strings"
	"testing"
	"time"
)

const stream = `{"config":{"protocol_version":"v1.0.0"}}
{"finding":{"osv":"GO-2026-6909","trace":[{"module":"github.com/crewjam/saml","version":"v0.6.3","package":"github.com/crewjam/saml","function":"ParseXMLResponse"}]}}
{"finding":{"osv":"GO-2026-0001","trace":[{"module":"example.com/imported","version":"v1.0.0","package":"example.com/imported"}]}}
`

func allow(review, fixed string) []byte {
	return []byte(`entries:
  - id: GO-2026-6909
    module: github.com/crewjam/saml
    fixedIn: ` + fixed + `
    justification: fixed upstream, the database lacks the fixed version
    evidence: [GHSA-mvp6-372g-vf8r]
    reviewBy: "` + review + `"
`)
}

var now = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func TestGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow []byte
		want  string // substring of the single problem; "" = pass
	}{
		{"covered", allow("2027-02-01", "v0.5.2"), ""},
		{"not listed", []byte("entries: []"), "REACHABLE GO-2026-6909"},
		{"version below the declared fix", allow("2027-02-01", "v0.7.0"), "allow-listed only from v0.7.0"},
		{"expired review", allow("2026-11-01", "v0.5.2"), "EXPIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := gate(tc.allow, strings.NewReader(stream), now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(problems) != 0 {
					t.Errorf("problems = %v, want none (the imported-only finding is not reachable)", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Errorf("problems = %v, want one containing %q", problems, tc.want)
			}
		})
	}
	if _, err := gate([]byte("entries:\n  - id: X\n"), strings.NewReader(""), now); err == nil {
		t.Error("an entry without module, fix, evidence or review date was accepted")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v0.6.3", "v0.5.2", 1}, {"v0.5.2", "v0.5.2", 0}, {"v0.5.1", "v0.5.2", -1},
		{"v1.0.0-rc.1", "v1.0.0", -1}, {"v1.27.3", "v1.27.4", -1}, {"garbage", "v0.0.1", -1},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
