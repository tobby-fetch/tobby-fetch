// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Track C — provenance and hardening (R-17, FR-098). The names, types and
// labels are the ones reserved in reserved.go; this file registers them.

// The closed label sets of the two families.
const (
	// ClassUnitImport and ClassSeed are the class labels of
	// MetricStoreUnmanaged: a unit import or a local packing, and content
	// pushed through /v2/. Recipe-managed content is the managed side and
	// is not counted.
	ClassUnitImport = "unit-import"
	ClassSeed       = "seed"
	// SettingDisableUnitImport and SettingDisableRegistryPush are the
	// setting labels of MetricHardeningEnabled — the configuration keys
	// without their "hardening." section.
	SettingDisableUnitImport   = "disableUnitImport"
	SettingDisableRegistryPush = "disableRegistryPush"
)

// RegisterHardening registers tobby_hardening_enabled: 1 for each
// hardening switch set, 0 otherwise. Both settings exist from the first
// scrape — the switches are read once, at startup, and only change with a
// restart — so "off" is a value an alert can read, never an absence.
func (r *Registry) RegisterHardening(disableUnitImport, disableRegistryPush bool) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: MetricHardeningEnabled,
		Help: "1 for each hardening switch set (FR-098: hardening.disableUnitImport, hardening.disableRegistryPush), 0 otherwise.",
	}, []string{"setting"})
	g.WithLabelValues(SettingDisableUnitImport).Set(boolGauge(disableUnitImport))
	g.WithLabelValues(SettingDisableRegistryPush).Set(boolGauge(disableRegistryPush))
	r.MustRegister(g)
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// UnmanagedCount feeds MetricStoreUnmanaged: the number of repositories
// per class among ClassUnitImport and ClassSeed. An error leaves the
// family out of that scrape rather than reporting a zero nobody measured.
type UnmanagedCount func() (map[string]int, error)

// unmanagedTTL bounds how often a scrape walks the store. The count is
// read from the store at scrape time — never kept by events a write path
// could forget to send — and a few seconds of staleness is the price of
// not walking the repository tree for every one of several concurrent
// scrapers.
const unmanagedTTL = 10 * time.Second

// RegisterUnmanaged registers tobby_store_unmanaged_repositories, the
// repositories no recipe manages, by class (FR-098). Both classes are
// present from the first scrape, at zero when the store holds none.
func (r *Registry) RegisterUnmanaged(count UnmanagedCount) {
	r.MustRegister(&unmanagedCollector{
		desc: prometheus.NewDesc(MetricStoreUnmanaged,
			"Repositories of the store not managed by a recipe (FR-098), by provenance class: unit-import (unit imports and packed FileSets) and seed (pushed through /v2/).",
			[]string{"class"}, nil),
		count: count,
		now:   time.Now,
	})
}

type unmanagedCollector struct {
	desc  *prometheus.Desc
	count UnmanagedCount
	now   func() time.Time

	mu     sync.Mutex
	cached map[string]int
	at     time.Time
}

func (c *unmanagedCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *unmanagedCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	if c.cached == nil || c.now().Sub(c.at) >= unmanagedTTL {
		counts, err := c.count()
		if err != nil {
			c.mu.Unlock()
			return
		}
		c.cached, c.at = counts, c.now()
	}
	counts := c.cached
	c.mu.Unlock()
	for _, class := range []string{ClassUnitImport, ClassSeed} {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(counts[class]), class)
	}
}
