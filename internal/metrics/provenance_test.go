// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// gauges reads one gauge family by its single label.
func gauges(t *testing.T, reg *Registry, family, label string) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != family {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label {
					out[l.GetValue()] = m.GetGauge().GetValue()
				}
			}
		}
	}
	return out
}

// TestHardeningEnabledReportsBothSettingsAlways locks FR-098's metric half
// of "reported like every security setting": both settings, always, at 1
// or 0 — an absent series cannot tell "off" from "not reporting".
func TestHardeningEnabledReportsBothSettingsAlways(t *testing.T) {
	for _, c := range []struct{ unit, push bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		reg := New()
		reg.RegisterHardening(c.unit, c.push)
		got := gauges(t, reg, MetricHardeningEnabled, "setting")
		want := map[string]float64{SettingDisableUnitImport: boolGauge(c.unit), SettingDisableRegistryPush: boolGauge(c.push)}
		if len(got) != 2 || got[SettingDisableUnitImport] != want[SettingDisableUnitImport] || got[SettingDisableRegistryPush] != want[SettingDisableRegistryPush] {
			t.Errorf("%+v: %s = %v, want %v", c, MetricHardeningEnabled, got, want)
		}
	}
}

// TestUnmanagedRepositoriesCountsBothClasses locks the unmanaged-content
// gauge: both classes from the first scrape, read from the store at scrape
// time, and absent — not zero — when the store cannot be read.
func TestUnmanagedRepositoriesCountsBothClasses(t *testing.T) {
	counts := map[string]int{ClassUnitImport: 2, ClassSeed: 1}
	calls := 0
	reg := New()
	reg.RegisterUnmanaged(func() (map[string]int, error) { return counts, nil })
	got := gauges(t, reg, MetricStoreUnmanaged, "class")
	if got[ClassUnitImport] != 2 || got[ClassSeed] != 1 || len(got) != 2 {
		t.Fatalf("%s = %v", MetricStoreUnmanaged, got)
	}

	empty := New()
	empty.RegisterUnmanaged(func() (map[string]int, error) { return map[string]int{}, nil })
	if got := gauges(t, empty, MetricStoreUnmanaged, "class"); len(got) != 2 || got[ClassSeed] != 0 {
		t.Errorf("empty store: %v, want both classes at zero", got)
	}

	broken := New()
	broken.RegisterUnmanaged(func() (map[string]int, error) { return nil, errors.New("unreadable") })
	if got := gauges(t, broken, MetricStoreUnmanaged, "class"); len(got) != 0 {
		t.Errorf("unreadable store: %v, want the family absent", got)
	}

	// The scrape-time read is bounded, not frozen: past the TTL the next
	// scrape reads the store again.
	start := time.Now()
	c := &unmanagedCollector{
		desc:  prometheus.NewDesc(MetricStoreUnmanaged, "test", []string{"class"}, nil),
		count: func() (map[string]int, error) { calls++; return counts, nil },
		now:   func() time.Time { return start },
	}
	drain := func() {
		ch := make(chan prometheus.Metric, 4)
		c.Collect(ch)
		close(ch)
		for range ch {
		}
	}
	calls = 0
	drain()
	drain()
	if calls != 1 {
		t.Errorf("two scrapes within the TTL read the store %d times, want 1", calls)
	}
	c.now = func() time.Time { return start.Add(unmanagedTTL) }
	drain()
	if calls != 2 {
		t.Errorf("a scrape past the TTL did not read the store again (%d reads)", calls)
	}
}
