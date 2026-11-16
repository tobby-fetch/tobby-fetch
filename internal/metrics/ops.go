// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Supervision families (R-11, FR-091 amendment), the clock verdict
// (FR-096) and the diagnostic bundle counter (FR-095) — milestone-7
// track B. The names were reserved at the opening of the milestone
// (reserved.go); they are registered here now that they are fed.

// MetricSyncParallelism is the configured bound on concurrent transfers
// (sync.parallelism): gauge. It makes "transfers saturating the bound" an
// expression over published families instead of a number copied into a
// rule file.
const MetricSyncParallelism = "tobby_sync_parallelism"

// The closed label sets of the families below.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"

	// Diagnostic bundle outcomes.
	BundleWritten = "written"
	BundleRefused = "refused"
	BundleFailed  = "failed"

	// Ingredient statuses of the last run (FR-026).
	IngredientNew      = "new"
	IngredientOutdated = "outdated"
	IngredientUpToDate = "up_to_date"
)

var (
	syncOutcomes       = []string{OutcomeSuccess, OutcomeFailure}
	bundleOutcomes     = []string{BundleWritten, BundleRefused, BundleFailed}
	ingredientStatuses = []string{IngredientNew, IngredientOutdated, IngredientUpToDate}
	clockAnchors       = []string{"build", "medium"}
)

// SyncBuckets are the duration buckets of MetricSyncDuration: from a
// cycle that finds nothing to do to a first synchronization of a large
// cookbook over a slow link.
var SyncBuckets = []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600, 7200, 14400, 28800}

// SyncRun is the outcome of one synchronization run.
type SyncRun struct {
	// Success is false for a run that failed or left an item failed.
	Success  bool
	Duration time.Duration
	// Finished is the end time of the run.
	Finished time.Time
	// Ingredients is the per-status count of the run (FR-026), keyed by
	// the Ingredient* constants.
	Ingredients map[string]int
}

// ops holds the state behind the Track B families.
type ops struct {
	runs     *prometheus.CounterVec
	duration *prometheus.HistogramVec
	bundles  *prometheus.CounterVec

	mu          sync.Mutex
	lastSuccess time.Time
	haveRun     bool
	ingredients map[string]int
	clock       func() []ClockReading
	cert        func() (time.Time, bool)

	lastSuccessDesc, ingredientsDesc, clockDesc, certDesc *prometheus.Desc
}

// ClockReading is one anchor's current verdict (FR-096).
type ClockReading struct {
	// Anchor is "build" or "medium".
	Anchor      string
	Implausible bool
}

func newOps(reg *prometheus.Registry) *ops {
	o := &ops{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricSyncRuns,
			Help: "Synchronization runs (mirror trigger or passthrough cycle) by outcome: success, or failure (the run failed or left an item failed) (R-11).",
		}, []string{"outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricSyncDuration,
			Help:    "Duration of synchronization runs, in seconds, by outcome (R-11).",
			Buckets: SyncBuckets,
		}, []string{"outcome"}),
		bundles: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricDiagnosticsBundles,
			Help: "Diagnostic bundles by outcome: written, refused (redaction failed closed, TBY-DIA-002) or failed (TBY-DIA-001) (FR-095).",
		}, []string{"outcome"}),
		ingredients: map[string]int{},

		lastSuccessDesc: prometheus.NewDesc(MetricSyncLastSuccess,
			"End time of the last successful synchronization, in Unix seconds; absent until one has succeeded since this process started (R-11).", nil, nil),
		ingredientsDesc: prometheus.NewDesc(MetricSyncIngredients,
			"Ingredients of the last synchronization run by status (FR-026): new, outdated, up_to_date; absent until a run has completed.", []string{"status"}, nil),
		clockDesc: prometheus.NewDesc(MetricClockImplausible,
			"1 while the local clock was found earlier than an anchor beyond clock.skewTolerance, 0 otherwise, by anchor: build (checked at start) or medium (once one has been opened) (FR-096).", []string{"anchor"}, nil),
		certDesc: prometheus.NewDesc(MetricServerCertificateExpiry,
			"NotAfter of the certificate the listener presents, in Unix seconds, self-signed fallback included; absent while the listener serves plain HTTP (FR-082).", nil, nil),
	}
	// Both outcomes exist from the first scrape: no run yet must read
	// differently from an instance that does not report runs.
	for _, v := range syncOutcomes {
		o.runs.WithLabelValues(v)
		o.duration.WithLabelValues(v)
	}
	for _, v := range bundleOutcomes {
		o.bundles.WithLabelValues(v)
	}
	reg.MustRegister(o.runs, o.duration, o.bundles, o)
	return o
}

func (o *ops) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{o.lastSuccessDesc, o.ingredientsDesc, o.clockDesc, o.certDesc} {
		ch <- d
	}
}

func (o *ops) Collect(ch chan<- prometheus.Metric) {
	o.mu.Lock()
	last, haveRun, ing, clock, cert := o.lastSuccess, o.haveRun, o.ingredients, o.clock, o.cert
	o.mu.Unlock()
	if !last.IsZero() {
		ch <- prometheus.MustNewConstMetric(o.lastSuccessDesc, prometheus.GaugeValue, float64(last.Unix()))
	}
	if haveRun {
		for _, s := range ingredientStatuses {
			ch <- prometheus.MustNewConstMetric(o.ingredientsDesc, prometheus.GaugeValue, float64(ing[s]), s)
		}
	}
	if clock != nil {
		for _, r := range clock() {
			if member(clockAnchors, r.Anchor) {
				ch <- prometheus.MustNewConstMetric(o.clockDesc, prometheus.GaugeValue, b2f(r.Implausible), r.Anchor)
			}
		}
	}
	if cert != nil {
		if notAfter, ok := cert(); ok {
			ch <- prometheus.MustNewConstMetric(o.certDesc, prometheus.GaugeValue, float64(notAfter.Unix()))
		}
	}
}

// ObserveSync records one completed synchronization run.
func (r *Registry) ObserveSync(run SyncRun) {
	o := r.ops
	outcome := OutcomeFailure
	if run.Success {
		outcome = OutcomeSuccess
	}
	o.runs.WithLabelValues(outcome).Inc()
	o.duration.WithLabelValues(outcome).Observe(run.Duration.Seconds())
	o.mu.Lock()
	defer o.mu.Unlock()
	o.haveRun = true
	o.ingredients = map[string]int{}
	for k, v := range run.Ingredients {
		if member(ingredientStatuses, k) {
			o.ingredients[k] = v
		}
	}
	if run.Success && run.Finished.After(o.lastSuccess) {
		o.lastSuccess = run.Finished
	}
}

// ObserveDiagnosticsBundle counts one bundle by outcome.
func (r *Registry) ObserveDiagnosticsBundle(outcome string) {
	if member(bundleOutcomes, outcome) {
		r.ops.bundles.WithLabelValues(outcome).Inc()
	}
}

// SetClock installs the reader of the clock verdicts (FR-096). Until one
// is installed, and for an anchor not yet checked, nothing is exposed.
func (r *Registry) SetClock(f func() []ClockReading) {
	r.ops.mu.Lock()
	r.ops.clock = f
	r.ops.mu.Unlock()
}

// SetServerCertificate installs the reader of the presented certificate's
// NotAfter (FR-082); false means the listener serves plain HTTP. It is
// read at every scrape, so a replacement is followed without a restart.
func (r *Registry) SetServerCertificate(f func() (time.Time, bool)) {
	r.ops.mu.Lock()
	r.ops.cert = f
	r.ops.mu.Unlock()
}

// SetSyncParallelism publishes the configured bound on concurrent
// transfers.
func (r *Registry) SetSyncParallelism(n int) { r.syncParallelism.Set(float64(n)) }
