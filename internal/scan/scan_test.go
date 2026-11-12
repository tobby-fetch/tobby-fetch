// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestLowerBoundIsTheSRSTable locks the FR-031 amendment's table: a
// finding without a CVSS score sits at the lower bound of its CVSS v3
// band, and UNKNOWN has no place on the scale.
func TestLowerBoundIsTheSRSTable(t *testing.T) {
	want := map[Severity]float64{
		SeverityLow: 0.1, SeverityMedium: 4.0, SeverityHigh: 7.0, SeverityCritical: 9.0,
	}
	for _, s := range Severities() {
		if !s.Valid() {
			t.Errorf("%s is listed but not valid", s)
		}
		got, ok := s.LowerBound()
		w, scored := want[s]
		if ok != scored || got != w {
			t.Errorf("%s.LowerBound() = %v, %v; want %v, %v", s, got, ok, w, scored)
		}
	}
	if Severity("NEGLIGIBLE").Valid() {
		t.Error("a severity outside the closed set is valid")
	}
	if _, ok := Severity("NEGLIGIBLE").LowerBound(); ok {
		t.Error("a severity outside the closed set has a lower bound")
	}
}

func TestModeAndExceptionAccessors(t *testing.T) {
	if !ModeWarn.Valid() || !ModeBlock.Valid() || Mode("audit").Valid() {
		t.Error("Mode.Valid does not match the closed set {warn, block}")
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if (Exception{ID: "CVE-2024-1"}).ExpiredAt(now) {
		t.Error("an exception without expiry expired")
	}
	if !(Exception{Expires: now.Add(-time.Second)}).ExpiredAt(now) {
		t.Error("an exception past its expiry still applies")
	}
	if (Exception{Expires: now.Add(time.Hour)}).ExpiredAt(now) {
		t.Error("an exception before its expiry no longer applies")
	}
}

func TestCountsAdd(t *testing.T) {
	var c Counts
	for _, s := range []Severity{SeverityCritical, SeverityHigh, SeverityHigh, SeverityMedium, SeverityLow, SeverityUnknown, "BOGUS"} {
		c.Add(s)
	}
	if c != (Counts{Critical: 1, High: 2, Medium: 1, Low: 1, Unknown: 2}) {
		t.Errorf("counts = %+v", c)
	}
}

// TestReportJSONShape pins the field names the API and the persisted task
// schema expose under items[].scan: snake_case like the rest of the task
// document, and an unscanned item carries no scanner or database at all
// rather than zero values that would read as "scanned with nothing".
func TestReportJSONShape(t *testing.T) {
	score := 9.8
	r := Report{
		Scanner:   &Info{Name: "trivy", Version: "0.0.0"},
		ScannedAt: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC),
		Database:  &Database{Version: "2", AgeSeconds: 3600},
		Findings:  []Finding{{ID: "CVE-2024-1", Severity: SeverityCritical, CVSS: &score, Package: "openssl", FixedVersion: "3.0.1"}},
		Policy:    Evaluation{Verdict: VerdictBlock, Policy: Policy{Mode: ModeBlock, Threshold: 7, ThresholdLevel: LevelInstance}, Violations: []string{"CVE-2024-1"}},
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"scanned_at"`, `"age_seconds"`, `"fixed_version"`, `"threshold_level"`, `"violations"`, `"evaluation"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("report JSON lacks %s: %s", key, raw)
		}
	}

	unscanned, err := json.Marshal(Report{Policy: Evaluation{Verdict: VerdictNotScanned}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"scanner"`, `"database"`, `"scanned_at"`} {
		if strings.Contains(string(unscanned), key) {
			t.Errorf("unscanned report carries %s: %s", key, unscanned)
		}
	}
}
