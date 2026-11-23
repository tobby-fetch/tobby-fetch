// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"testing"
	"time"
)

func opsFamily(t *testing.T, reg *Registry, name string) (present bool, values map[string]float64) {
	t.Helper()
	fs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.GetName() != name {
			continue
		}
		values = map[string]float64{}
		for _, m := range f.GetMetric() {
			label := ""
			if len(m.GetLabel()) > 0 {
				label = m.GetLabel()[0].GetValue()
			}
			switch {
			case m.GetGauge() != nil:
				values[label] = m.GetGauge().GetValue()
			case m.GetCounter() != nil:
				values[label] = m.GetCounter().GetValue()
			case m.GetHistogram() != nil:
				values[label] = float64(m.GetHistogram().GetSampleCount())
			}
		}
		return true, values
	}
	return false, nil
}

// TestSyncFamiliesFollowRuns: the counters exist at zero before any run,
// the timestamp and the ingredient gauges are absent until they have
// measured something, a failure moves the failure series only and never
// the last-success timestamp.
func TestSyncFamiliesFollowRuns(t *testing.T) {
	reg := New()
	if ok, v := opsFamily(t, reg, MetricSyncRuns); !ok || v[OutcomeSuccess] != 0 || v[OutcomeFailure] != 0 {
		t.Fatalf("runs before any run = %v (present %v)", v, ok)
	}
	for _, absent := range []string{MetricSyncLastSuccess, MetricSyncIngredients, MetricServerCertificateExpiry, MetricClockImplausible} {
		if ok, _ := opsFamily(t, reg, absent); ok {
			t.Errorf("%s is exposed before it measured anything (a 0 would state a fact)", absent)
		}
	}
	end := time.Unix(1_800_000_000, 0)
	reg.ObserveSync(SyncRun{Success: true, Duration: 90 * time.Second, Finished: end,
		Ingredients: map[string]int{IngredientNew: 2, IngredientUpToDate: 5, "bogus": 9}})
	reg.ObserveSync(SyncRun{Success: false, Duration: time.Second, Finished: end.Add(time.Hour)})

	if _, v := opsFamily(t, reg, MetricSyncRuns); v[OutcomeSuccess] != 1 || v[OutcomeFailure] != 1 {
		t.Errorf("runs = %v", v)
	}
	if _, v := opsFamily(t, reg, MetricSyncDuration); v[OutcomeSuccess] != 1 || v[OutcomeFailure] != 1 {
		t.Errorf("durations = %v", v)
	}
	if ok, v := opsFamily(t, reg, MetricSyncLastSuccess); !ok || v[""] != float64(end.Unix()) {
		t.Errorf("last success = %v, want the success, not the later failure", v)
	}
	// The ingredient gauges describe the LAST run (a failure with none).
	if ok, v := opsFamily(t, reg, MetricSyncIngredients); !ok || v[IngredientNew] != 0 || v[IngredientUpToDate] != 0 || len(v) != 3 {
		t.Errorf("ingredients = %v", v)
	}
}

func TestCertificateExpiryFollowsTheCertificateAtEveryScrape(t *testing.T) {
	reg := New()
	notAfter := time.Unix(1_900_000_000, 0)
	serving := true
	reg.SetServerCertificate(func() (time.Time, bool) { return notAfter, serving })
	if ok, v := opsFamily(t, reg, MetricServerCertificateExpiry); !ok || v[""] != 1_900_000_000 {
		t.Fatalf("expiry = %v", v)
	}
	notAfter = time.Unix(2_000_000_000, 0) // a replacement
	if _, v := opsFamily(t, reg, MetricServerCertificateExpiry); v[""] != 2_000_000_000 {
		t.Errorf("a replacement was not followed: %v", v)
	}
	serving = false // plain HTTP
	if ok, _ := opsFamily(t, reg, MetricServerCertificateExpiry); ok {
		t.Error("exposed while the listener serves plain HTTP")
	}
}

func TestClockAndBundleFamilies(t *testing.T) {
	reg := New()
	reg.SetClock(func() []ClockReading {
		return []ClockReading{{Anchor: "build", Implausible: true}, {Anchor: "medium"}, {Anchor: "elsewhere", Implausible: true}}
	})
	if _, v := opsFamily(t, reg, MetricClockImplausible); v["build"] != 1 || v["medium"] != 0 || len(v) != 2 {
		t.Errorf("clock = %v (an anchor outside the closed set must be dropped)", v)
	}
	reg.ObserveDiagnosticsBundle(BundleRefused)
	reg.ObserveDiagnosticsBundle("nonsense")
	if _, v := opsFamily(t, reg, MetricDiagnosticsBundles); v[BundleRefused] != 1 || v[BundleWritten] != 0 || len(v) != 3 {
		t.Errorf("bundles = %v", v)
	}
	reg.SetSyncParallelism(3)
	if _, v := opsFamily(t, reg, MetricSyncParallelism); v[""] != 3 {
		t.Errorf("parallelism = %v", v)
	}
}
