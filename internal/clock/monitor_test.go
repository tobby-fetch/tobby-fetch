// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package clock

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func TestMonitorKeepsTheLatestVerdictPerAnchorAndNotifiesOnlyImplausibleOnes(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m := NewMonitor(time.Hour)
	m.Now = func() time.Time { return now }
	var seen []Record
	m.Observe(func(r *Record) { seen = append(seen, *r) })

	m.CheckMedium("med_1", now.Add(20*time.Minute)) // within tolerance
	if len(seen) != 0 || len(m.Implausible()) != 0 {
		t.Fatalf("a clock within the tolerance raised something: %+v", seen)
	}
	r := m.CheckMedium("med_2", now.Add(72*time.Hour))
	if !r.Implausible || len(seen) != 1 || seen[0].Medium != "med_2" {
		t.Fatalf("beyond the tolerance: %+v / %+v", r, seen)
	}
	te := r.Error()
	if te == nil || te.Code() != taxonomy.CodeClockBeforeMedium {
		t.Fatalf("Error = %v, want TBY-CLK-002", te)
	}
	if !strings.Contains(taxonomy.Text("en", te), "med_2") {
		t.Error("the message does not name the medium")
	}
	// An earlier medium opened next proves nothing new: the later anchor
	// stays, and so does the banner — the clock is still behind it.
	if r := m.CheckMedium("med_3", now); r.Implausible {
		t.Errorf("med_3 judged on its own date: %+v", r)
	}
	if imp := m.Implausible(); len(imp) != 1 || imp[0].Medium != "med_2" {
		t.Errorf("the later anchor was displaced by an earlier one: %+v", imp)
	}
	// The clock put right: the next re-judgment clears the banner by itself.
	now = now.Add(73 * time.Hour)
	m.Recheck()
	if len(m.Implausible()) != 0 {
		t.Error("the banner survived a clock put right")
	}
	if len(m.Records()) != 1 || m.Records()[0].Medium != "med_2" {
		t.Errorf("Records = %+v", m.Records())
	}
	if len(seen) != 1 {
		t.Errorf("observers called %d times, want once", len(seen))
	}
}

func TestReportWritesTheLogRecordAndTheAuditEvent(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(&buf, slog.LevelDebug)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := Record{Verdict: Check(AnchorBuild, now.Add(30*24*time.Hour), now, time.Hour), Tolerance: time.Hour}
	Report(context.Background(), logger, &rec)
	Report(context.Background(), logger, &Record{}) // plausible: nothing
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d records, want the warning and the audit event:\n%s", len(lines), buf.String())
	}
	var warn, aud map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &warn)
	_ = json.Unmarshal([]byte(lines[1]), &aud)
	if warn["code"] != "TBY-CLK-001" || warn["level"] != "WARN" || warn["anchor"] != "build" {
		t.Errorf("warning record = %v", warn)
	}
	if aud["action"] != "clock.implausible" || aud["target"] != "build" || !strings.Contains(aud["detail"].(string), "gap=720h") {
		t.Errorf("audit record = %v", aud)
	}
}

func TestCheckBuildProvesNothingWithoutABuildTime(t *testing.T) {
	m := NewMonitor(time.Hour)
	if _, ok := m.CheckBuild(); ok {
		t.Skip("this test binary carries a build date")
	}
	if len(m.Records()) != 0 {
		t.Error("a development build recorded a verdict it cannot have")
	}
}

// TestAnEarlierOrUndatedMediumDoesNotMaskAVerdict (adversarial review of
// milestone 7, A5): the gap found against medium A stays when medium B —
// older, or carrying no date at all — is opened next.
func TestAnEarlierOrUndatedMediumDoesNotMaskAVerdict(t *testing.T) {
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // a clock set back
	m := NewMonitor(time.Hour)
	m.Now = func() time.Time { return now }
	m.CheckMedium("A", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if len(m.Implausible()) != 1 {
		t.Fatal("setup: medium A raised nothing")
	}
	m.CheckMedium("B-undated", time.Time{})
	m.CheckMedium("C-older", time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC))
	imp := m.Implausible()
	if len(imp) != 1 || imp[0].Medium != "A" {
		t.Errorf("the verdict against A was masked: %+v", m.Records())
	}
	// A later medium becomes the anchor.
	m.CheckMedium("D-later", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if imp := m.Implausible(); len(imp) != 1 || imp[0].Medium != "D-later" {
		t.Errorf("the latest anchor is not kept: %+v", imp)
	}
}

// TestRecheckCatchesAClockSetBackWhileRunning: the build anchor is judged
// again, and a verdict that turns implausible is announced once.
func TestRecheckCatchesAClockSetBackWhileRunning(t *testing.T) {
	anchor := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	now := anchor.Add(24 * time.Hour)
	m := NewMonitor(time.Hour)
	m.Now = func() time.Time { return now }
	var seen int
	m.Observe(func(*Record) { seen++ })
	m.record(&Record{Verdict: Check(AnchorBuild, anchor, now, time.Hour), Tolerance: time.Hour}, true)
	m.Recheck()
	if seen != 0 || len(m.Implausible()) != 0 {
		t.Fatal("a good clock raised something")
	}
	now = anchor.Add(-48 * time.Hour)
	m.Recheck()
	m.Recheck()
	if seen != 1 || len(m.Implausible()) != 1 {
		t.Errorf("clock set back: %d notifications, %d implausible; want 1 and 1", seen, len(m.Implausible()))
	}
}
