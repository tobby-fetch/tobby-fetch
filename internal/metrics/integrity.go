// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Track C — on-demand store integrity (FR-057, R-31) and reduced assurance
// (FR-037, R-22). The names, types and labels are the ones reserved in
// reserved.go; this file registers them and feeds them.

// The verdict labels of MetricIntegrityVerifications. The set is closed
// and part of the documented metric surface.
const (
	// VerdictClean is a verification that found no defect: orphaned
	// content, which is listed and never blocking, does not make one.
	VerdictClean = "clean"
	// VerdictDefects is a verification that found at least one defect.
	VerdictDefects = "defects"
)

func newIntegrityVerifications() *prometheus.CounterVec {
	v := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricIntegrityVerifications,
		Help: "On-demand store verifications completed (FR-057), by verdict: clean, or defects found.",
	}, []string{"verdict"})
	// Both verdicts exist from the first scrape: a counter that only
	// appears once it fires cannot tell "never verified" from "instance
	// not reporting".
	v.WithLabelValues(VerdictClean)
	v.WithLabelValues(VerdictDefects)
	return v
}

// ObserveIntegrityVerification records one completed on-demand
// verification: it counts the verdict and dates the last one.
//
// The timestamp gauge is registered by the FIRST completed verification,
// not at startup: a gauge reading 0 before any verification would say the
// last one ran in 1970 — a number, where the truth is "never". Its absence
// is how an alert tells a store that has never been verified from one
// that was verified long ago.
func (r *Registry) ObserveIntegrityVerification(verdict string, finished time.Time) {
	r.IntegrityVerifications.WithLabelValues(verdict).Inc()
	r.integrityLastOnce.Do(func() {
		r.integrityLast = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: MetricIntegrityLastVerification,
			Help: "End time of the last completed on-demand store verification, in Unix seconds; absent until one has completed.",
		})
		r.MustRegister(r.integrityLast)
	})
	r.integrityLast.Set(float64(finished.Unix()))
}

// ReducedRecipes feeds MetricAssuranceReduced.
type ReducedRecipes func() map[string]int

// RegisterReducedRecipes registers tobby_assurance_reduced_recipes, the
// number of recipes currently held under reduced assurance per declared
// trust scope (FR-037).
//
// The value is computed at every scrape from the recipe graph — the
// store's own record of what was admitted — rather than counted by events:
// a gauge fed by "recipe admitted" and "recipe removed" drifts the first
// time a removal path forgets to tell it, and one that restarts at zero
// says "no reduced content" about a store full of it. Every scope that
// relaxes verification is present from the first scrape, at zero when it
// holds nothing, so that clearing the last recipe is as visible as
// admitting the first (the R-33 rule); a scope the configuration no longer
// declares but the store still holds recipes for is reported too.
func (r *Registry) RegisterReducedRecipes(declared []string, count ReducedRecipes) {
	r.MustRegister(&reducedCollector{
		desc: prometheus.NewDesc(MetricAssuranceReduced,
			"Recipes currently held under reduced assurance (FR-037), by the declared trust scope that admitted them.",
			[]string{"scope"}, nil),
		declared: append([]string(nil), declared...),
		count:    count,
	})
}

type reducedCollector struct {
	desc     *prometheus.Desc
	declared []string
	count    ReducedRecipes
}

func (c *reducedCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *reducedCollector) Collect(ch chan<- prometheus.Metric) {
	counts := c.count()
	seen := map[string]bool{}
	scopes := append([]string(nil), c.declared...)
	for _, s := range c.declared {
		seen[s] = true
	}
	for s := range counts {
		if !seen[s] {
			scopes = append(scopes, s)
		}
	}
	sort.Strings(scopes)
	for _, s := range scopes {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(counts[s]), s)
	}
}
