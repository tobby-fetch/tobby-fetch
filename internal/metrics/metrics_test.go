// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"testing"
)

func TestRegistryExposesSocleFamilies(t *testing.T) {
	reg := New()

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range families {
		found[f.GetName()] = true
		if f.GetName() == "tobby_build_info" {
			metrics := f.GetMetric()
			if len(metrics) != 1 || metrics[0].GetGauge().GetValue() != 1 {
				t.Errorf("tobby_build_info = %v, want single gauge at 1", metrics)
			}
			labels := map[string]bool{}
			for _, l := range metrics[0].GetLabel() {
				labels[l.GetName()] = true
			}
			if !labels["version"] || !labels["commit"] {
				t.Errorf("tobby_build_info labels = %v, want version and commit", labels)
			}
		}
	}
	for _, want := range []string{"go_goroutines", "tobby_build_info"} {
		if !found[want] {
			t.Errorf("family %q missing from registry", want)
		}
	}
}

// TestStoreOccupancyFamiliesExistFromTheFirstScrape locks the R-33 metric
// surface. All three gauges are registered eagerly, so an alert can tell
// "the store is within its threshold" from "this instance is not
// reporting" — a distinction a family that only appears once it fires
// cannot make. The names are documented API surface.
func TestStoreOccupancyFamiliesExistFromTheFirstScrape(t *testing.T) {
	reg := New()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range families {
		found[f.GetName()] = true
	}
	for _, want := range []string{
		"tobby_store_occupancy_bytes",
		"tobby_store_occupancy_threshold_bytes",
		"tobby_store_occupancy_exceeded",
	} {
		if !found[want] {
			t.Errorf("family %q missing from the first scrape", want)
		}
	}

	// The threshold gauge moves in both directions: clearing the
	// condition is as visible as raising it (R-33).
	reg.StoreOccupancyExceeded.Set(1)
	if got := gaugeValue(t, reg, "tobby_store_occupancy_exceeded"); got != 1 {
		t.Errorf("exceeded gauge = %v after raising, want 1", got)
	}
	reg.StoreOccupancyExceeded.Set(0)
	if got := gaugeValue(t, reg, "tobby_store_occupancy_exceeded"); got != 0 {
		t.Errorf("exceeded gauge = %v after clearing, want 0", got)
	}
}

func gaugeValue(t *testing.T, reg *Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		if len(f.GetMetric()) != 1 {
			t.Fatalf("%s has %d metrics, want one", name, len(f.GetMetric()))
		}
		return f.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatalf("family %q not found", name)
	return 0
}
