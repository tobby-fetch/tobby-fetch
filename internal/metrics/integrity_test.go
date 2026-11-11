// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// family finds one gathered family, nil when it is not exposed.
func family(t *testing.T, reg *Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func sample(f *dto.MetricFamily, label, value string) (float64, bool) {
	for _, m := range f.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == label && l.GetValue() == value {
				if m.GetCounter() != nil {
					return m.GetCounter().GetValue(), true
				}
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// TestIntegrityFamilies: both verdicts are exposed at zero from the first
// scrape, the timestamp is absent until a verification has completed (it
// would read 1970 otherwise — a number where the truth is "never"), and
// each completed verification counts its verdict and dates itself.
func TestIntegrityFamilies(t *testing.T) {
	reg := New()
	f := family(t, reg, MetricIntegrityVerifications)
	if f == nil {
		t.Fatal("tobby_integrity_verifications_total is not exposed from the first scrape")
	}
	for _, v := range []string{VerdictClean, VerdictDefects} {
		if n, ok := sample(f, "verdict", v); !ok || n != 0 {
			t.Errorf("verdict %q = %v (present %v), want an explicit 0", v, n, ok)
		}
	}
	if family(t, reg, MetricIntegrityLastVerification) != nil {
		t.Error("the last-verification timestamp is exposed before any verification: it would read as 1970")
	}

	end := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	reg.ObserveIntegrityVerification(VerdictDefects, end)
	reg.ObserveIntegrityVerification(VerdictClean, end.Add(time.Hour))
	reg.ObserveIntegrityVerification(VerdictClean, end.Add(2*time.Hour))

	f = family(t, reg, MetricIntegrityVerifications)
	if n, _ := sample(f, "verdict", VerdictClean); n != 2 {
		t.Errorf("clean = %v, want 2", n)
	}
	if n, _ := sample(f, "verdict", VerdictDefects); n != 1 {
		t.Errorf("defects = %v, want 1", n)
	}
	last := family(t, reg, MetricIntegrityLastVerification)
	if last == nil || last.GetMetric()[0].GetGauge().GetValue() != float64(end.Add(2*time.Hour).Unix()) {
		t.Errorf("last verification = %v, want the most recent end time", last)
	}
}

// TestReducedRecipesIsComputedAtScrape: every declared scope is present at
// zero from the first scrape, the value follows the store (not events),
// and a scope the store still holds recipes for is reported even when the
// configuration no longer declares it.
func TestReducedRecipesIsComputedAtScrape(t *testing.T) {
	reg := New()
	held := map[string]int{}
	reg.RegisterReducedRecipes([]string{"lab", "sandbox"}, func() map[string]int { return held })

	f := family(t, reg, MetricAssuranceReduced)
	if f == nil {
		t.Fatal("tobby_assurance_reduced_recipes is not exposed")
	}
	for _, s := range []string{"lab", "sandbox"} {
		if n, ok := sample(f, "scope", s); !ok || n != 0 {
			t.Errorf("scope %q = %v (present %v), want an explicit 0 from the first scrape", s, n, ok)
		}
	}

	held["lab"], held["retired"] = 3, 1
	f = family(t, reg, MetricAssuranceReduced)
	if n, _ := sample(f, "scope", "lab"); n != 3 {
		t.Errorf("lab = %v, want 3", n)
	}
	if n, ok := sample(f, "scope", "retired"); !ok || n != 1 {
		t.Errorf("an undeclared scope that still holds recipes = %v (present %v), want 1", n, ok)
	}

	delete(held, "lab")
	f = family(t, reg, MetricAssuranceReduced)
	if n, ok := sample(f, "scope", "lab"); !ok || n != 0 {
		t.Errorf("lab after clearing = %v (present %v): clearing must be as visible as raising", n, ok)
	}
}
