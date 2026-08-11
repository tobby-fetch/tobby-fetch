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
