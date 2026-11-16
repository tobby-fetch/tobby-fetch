// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// TestScanExceptionsGaugeIsReadAtScrape locks tobby_scan_exceptions: six
// series from the first scrape, computed when scraped — so an expiry that
// passes between two scrapes moves the gauge with no evaluation and no
// restart.
func TestScanExceptionsGaugeIsReadAtScrape(t *testing.T) {
	counts := ExceptionCounts{ExceptionSourceInstance: {ExceptionActive: 2}, ExceptionSourceVEX: {ExceptionActive: 1}}
	reg := New()
	reg.RegisterScanExceptions(func() ExceptionCounts { return counts })
	read := func() map[string]float64 {
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, f := range families {
			if f.GetName() != MetricScanExceptions {
				continue
			}
			for _, m := range f.GetMetric() {
				out[label(m, "source")+"/"+label(m, "state")] = m.GetGauge().GetValue()
			}
		}
		return out
	}
	got := read()
	if len(got) != 6 || got["instance/active"] != 2 || got["vex/active"] != 1 || got["retriever/expired"] != 0 {
		t.Errorf("first scrape = %v", got)
	}
	counts = ExceptionCounts{ExceptionSourceInstance: {ExceptionActive: 1, ExceptionExpired: 1}, ExceptionSourceVEX: {ExceptionActive: 1}}
	if got := read(); got["instance/active"] != 1 || got["instance/expired"] != 1 {
		t.Errorf("after an expiry = %v", got)
	}
}

// TestReblockedCounterExistsAtZero locks tobby_scan_reblocked_items_total:
// present from the first scrape, at zero.
func TestReblockedCounterExistsAtZero(t *testing.T) {
	reg := New()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == MetricScanReblocked {
			if v := f.GetMetric()[0].GetCounter().GetValue(); v != 0 {
				t.Errorf("%s = %v at start", MetricScanReblocked, v)
			}
			reg.Scan.Reblocked.Inc()
			return
		}
	}
	t.Errorf("%s is not registered", MetricScanReblocked)
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}
