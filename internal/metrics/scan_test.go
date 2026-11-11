// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"testing"
)

func families(t *testing.T, reg *Registry) map[string]bool {
	t.Helper()
	fs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range fs {
		out[f.GetName()] = true
	}
	return out
}

// TestScanFamilies: the two counters exist from the first scrape with
// every label of their closed sets; the gauges exist only once the
// scanner's state is wired, and the age and staleness only while a
// database is present — an age of 0 would say "fresh" about nothing.
func TestScanFamilies(t *testing.T) {
	reg := New()
	got := families(t, reg)
	for _, name := range []string{MetricScanItems, MetricScanFindings} {
		if !got[name] {
			t.Errorf("%s missing from the first scrape", name)
		}
	}
	for _, name := range []string{MetricScanPolicyBlocking, MetricScanScannerAvailable, MetricScanDBPresent, MetricScanDBAge} {
		if got[name] {
			t.Errorf("%s exposed before anything measured it", name)
		}
	}
	fs, _ := reg.Gather()
	for _, f := range fs {
		switch f.GetName() {
		case MetricScanItems:
			if len(f.GetMetric()) != len(scanVerdicts) {
				t.Errorf("%s has %d series, want %d", f.GetName(), len(f.GetMetric()), len(scanVerdicts))
			}
		case MetricScanFindings:
			if len(f.GetMetric()) != len(scanSeverities) {
				t.Errorf("%s has %d series, want %d", f.GetName(), len(f.GetMetric()), len(scanSeverities))
			}
		}
	}

	state := ScanState{PolicyBlocking: false, ScannerAvailable: false, DBPresent: false}
	reg.Scan.SetState(func() ScanState { return state })
	got = families(t, reg)
	if !got[MetricScanScannerAvailable] || !got[MetricScanDBPresent] || !got[MetricScanPolicyBlocking] {
		t.Errorf("the degraded mode is not exposed: %v", got)
	}
	if got[MetricScanDBAge] || got[MetricScanDBStale] {
		t.Error("an absent database has an age")
	}
	if v := gaugeValue(t, reg, MetricScanScannerAvailable); v != 0 {
		t.Errorf("available = %v in the degraded mode", v)
	}

	state = ScanState{PolicyBlocking: true, ScannerAvailable: true, DBPresent: true, DBAgeSeconds: 3600, DBStale: true}
	for name, want := range map[string]float64{
		MetricScanPolicyBlocking: 1, MetricScanScannerAvailable: 1, MetricScanDBPresent: 1,
		MetricScanDBAge: 3600, MetricScanDBStale: 1,
	} {
		if v := gaugeValue(t, reg, name); v != want {
			t.Errorf("%s = %v; want %v", name, v, want)
		}
	}
	state.DBStale = false
	if v := gaugeValue(t, reg, MetricScanDBStale); v != 0 {
		t.Errorf("stale did not clear: %v", v)
	}
}
