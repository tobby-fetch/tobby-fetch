// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Vulnerability scanning (6.1, 6.2, R-13 — milestone-6 track A). The
// names were reserved at the opening of the milestone (reserved.go) and
// are registered here now that they are fed.
const (
	// MetricScanItems counts scanned items by verdict:
	// counter, label verdict = pass | warn | block | not-scanned.
	MetricScanItems = "tobby_scan_items_total"
	// MetricScanFindings counts findings by severity:
	// counter, label severity = UNKNOWN | LOW | MEDIUM | HIGH | CRITICAL.
	MetricScanFindings = "tobby_scan_findings_total"
	// MetricScanPolicyBlocking is 1 when the policy blocks (scan.mode:
	// block, or a threshold posed by the served Retriever), 0 when it is
	// advisory — the advisory banner, as a metric: gauge.
	MetricScanPolicyBlocking = "tobby_scan_policy_blocking"
	// MetricScanScannerAvailable is 1 when a verified scanner binary is
	// usable, 0 in the degraded mode: gauge.
	MetricScanScannerAvailable = "tobby_scan_scanner_available"
	// MetricScanDBPresent is 1 when a vulnerability database is present,
	// 0 in the absent-database degraded mode: gauge.
	MetricScanDBPresent = "tobby_scan_db_present"
	// MetricScanDBAge is the database age, from the build timestamp it
	// records (R-13): gauge, seconds. Exposed only while a database is
	// present — MetricScanDBPresent tells "absent" from "fresh", and an
	// age of 0 for a database nobody has would say "perfectly fresh".
	MetricScanDBAge = "tobby_scan_db_age_seconds"
	// MetricScanDBStale is 1 while the age exceeds scan.maxDbAge: gauge,
	// so clearing it is as visible as raising it (the R-33 rule). Exposed
	// with MetricScanDBAge.
	MetricScanDBStale = "tobby_scan_db_stale"
)

// The closed label sets of the two counters, created at zero so every
// series exists from the first scrape.
var (
	scanVerdicts   = []string{"pass", "warn", "block", "not-scanned"}
	scanSeverities = []string{"UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL"}
)

// ScanState is the instantaneous state the scan gauges expose.
type ScanState struct {
	PolicyBlocking   bool
	ScannerAvailable bool
	DBPresent        bool
	DBAgeSeconds     float64
	DBStale          bool
}

// ScanMetrics groups the scanning families.
type ScanMetrics struct {
	// Items counts per-item reports by verdict.
	Items *prometheus.CounterVec
	// Findings counts findings by severity, per scanned item.
	Findings *prometheus.CounterVec

	gauges *scanGauges
}

// SetState installs the function the gauges read at every scrape. Until
// one is installed the gauges are not exposed at all: nothing has been
// measured, and a 0 would state a fact.
func (m *ScanMetrics) SetState(f func() ScanState) {
	m.gauges.mu.Lock()
	m.gauges.state = f
	m.gauges.mu.Unlock()
}

func newScanMetrics(reg *prometheus.Registry) *ScanMetrics {
	items := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricScanItems,
		Help: "Items scanned at retrieval, by verdict (FR-031): pass, warn (advisory violation), block (refused), not-scanned (degraded mode or failed scan).",
	}, []string{"verdict"})
	findings := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricScanFindings,
		Help: "Vulnerability findings reported by item scans, by scanner severity (FR-031).",
	}, []string{"severity"})
	for _, v := range scanVerdicts {
		items.WithLabelValues(v)
	}
	for _, s := range scanSeverities {
		findings.WithLabelValues(s)
	}
	g := &scanGauges{
		blocking:  prometheus.NewDesc(MetricScanPolicyBlocking, "1 when the vulnerability policy blocks (scan.mode: block, or a threshold posed by the served Retriever), 0 when it is advisory (FR-031).", nil, nil),
		available: prometheus.NewDesc(MetricScanScannerAvailable, "1 when a verified scanner binary is usable, 0 in the degraded mode (FR-031).", nil, nil),
		present:   prometheus.NewDesc(MetricScanDBPresent, "1 when a vulnerability database is present, 0 in the absent-database degraded mode (FR-032).", nil, nil),
		age:       prometheus.NewDesc(MetricScanDBAge, "Age of the vulnerability database, from the build timestamp it records (R-13); exposed while a database is present.", nil, nil),
		stale:     prometheus.NewDesc(MetricScanDBStale, "1 while the vulnerability database is older than scan.maxDbAge (R-13); exposed while a database is present.", nil, nil),
	}
	reg.MustRegister(items, findings, g)
	return &ScanMetrics{Items: items, Findings: findings, gauges: g}
}

// scanGauges is a collector: the gauges are computed at scrape time from
// the scanner's state, so the age moves by itself between two scans.
type scanGauges struct {
	mu    sync.Mutex
	state func() ScanState

	blocking, available, present, age, stale *prometheus.Desc
}

func (g *scanGauges) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{g.blocking, g.available, g.present, g.age, g.stale} {
		ch <- d
	}
}

func (g *scanGauges) Collect(ch chan<- prometheus.Metric) {
	g.mu.Lock()
	f := g.state
	g.mu.Unlock()
	if f == nil {
		return
	}
	s := f()
	gauge := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	gauge(g.blocking, b2f(s.PolicyBlocking))
	gauge(g.available, b2f(s.ScannerAvailable))
	gauge(g.present, b2f(s.DBPresent))
	if s.DBPresent {
		gauge(g.age, s.DBAgeSeconds)
		gauge(g.stale, b2f(s.DBStale))
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
