// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import "github.com/prometheus/client_golang/prometheus"

// Track C — scoped and bounded exceptions (R-15, FR-031 amendment
// 2026-11-16). The names, types and labels are the ones reserved in
// reserved.go; this file registers them.

// The closed label sets of MetricScanExceptions.
const (
	ExceptionActive  = "active"
	ExceptionExpired = "expired"

	ExceptionSourceInstance  = "instance"
	ExceptionSourceRetriever = "retriever"
	ExceptionSourceVEX       = "vex"
)

var (
	exceptionStates  = []string{ExceptionActive, ExceptionExpired}
	exceptionSources = []string{ExceptionSourceInstance, ExceptionSourceRetriever, ExceptionSourceVEX}
)

// ExceptionCounts counts exceptions by source then state. An unbounded
// Retriever exception is active: it applies (TBY-SCN-028 says it has no
// limit).
type ExceptionCounts map[string]map[string]int

// RegisterScanExceptions registers tobby_scan_exceptions, the exceptions
// in configuration by state and source. count is called at every scrape,
// so an expiry that passes on a running instance moves the gauge without
// a restart and without an evaluation. The six series exist from the
// first scrape, at zero when nothing is declared; a source that yields no
// count (the Retriever before the first run) is reported at zero too —
// "none known" — while the policy view says it is not known yet.
func (r *Registry) RegisterScanExceptions(count func() ExceptionCounts) {
	r.MustRegister(&exceptionsCollector{
		desc: prometheus.NewDesc(MetricScanExceptions,
			"Vulnerability exceptions in configuration (FR-031, R-15) — the instance's scan.exceptions, those born of the scan.vex documents and those of the last served Retriever — by state (active, expired) and source (instance, retriever, vex).",
			[]string{"state", "source"}, nil),
		count: count,
	})
}

type exceptionsCollector struct {
	desc  *prometheus.Desc
	count func() ExceptionCounts
}

func (c *exceptionsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *exceptionsCollector) Collect(ch chan<- prometheus.Metric) {
	counts := c.count()
	for _, source := range exceptionSources {
		for _, state := range exceptionStates {
			ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(counts[source][state]), state, source)
		}
	}
}
