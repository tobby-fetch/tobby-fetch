// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package metrics owns Tobby's Prometheus registry (FR-091, ADR-0012).
//
// The official client library provides the process and Go runtime
// collectors; domain metric families are registered here as their features
// ship, and their names and labels are documented API surface, versioned
// with the same compatibility discipline as the REST API.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
)

// Registry is Tobby's metric registry plus the domain families every
// subsystem records into.
type Registry struct {
	*prometheus.Registry

	// SyncInflight observes the recipe engine's concurrent ingredient
	// transfers — the NFR-008 bound made visible.
	SyncInflight prometheus.Gauge
	// SyncBytes counts the bytes the recipe engine transferred.
	SyncBytes prometheus.Counter
	// PolicyRejections counts refusals by policy class (FR-091). Labeled
	// by taxonomy code rather than by host: a metric labeled with an
	// attacker-supplied host is an unbounded cardinality hole, and the
	// host is in the log record where it belongs.
	PolicyRejections *prometheus.CounterVec

	// PromotionPushes counts promotion outcomes by result (FR-013,
	// FR-028). The "skipped" result is the one that matters: a healthy
	// continuous promotion between two zones settles into almost nothing
	// but skips, so this counter is how "the destination is at the level
	// the Retriever asks for" becomes something a dashboard can assert
	// rather than something an operator has to go and check.
	PromotionPushes *prometheus.CounterVec
	// PromotionBytes counts the bytes promotion actually moved. Its
	// derivative against PromotionPushes{result="skipped"} is the FR-028
	// differential, observed rather than assumed.
	PromotionBytes prometheus.Counter
	// PromotionRefusals counts pushes refused before they happened, by
	// taxonomy code: the destination naming limits of FR-035
	// (TBY-DST-001), the allowlist (TBY-POL-001), the pre-push signature
	// re-verification of FR-033 (TBY-SIG-001).
	PromotionRefusals *prometheus.CounterVec
}

// New builds the registry with the standard process and Go collectors and
// the build-information gauge.
func New() *Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "tobby_build_info",
		Help: "Build metadata of the running binary; the value is always 1.",
	}, []string{"version", "commit"})
	buildInfo.WithLabelValues(buildinfo.Version(), buildinfo.Commit()).Set(1)
	reg.MustRegister(buildInfo)

	syncInflight := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "tobby_sync_transfers_inflight",
		Help: "Ingredient transfers currently in flight, bounded by sync.parallelism (NFR-008).",
	})
	syncBytes := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tobby_sync_transferred_bytes_total",
		Help: "Bytes transferred by recipe synchronizations (skipped up-to-date content moves nothing).",
	})
	policyRejections := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tobby_policy_rejections_total",
		Help: "Transfers refused by policy, by taxonomy code (FR-030 allowlist today).",
	}, []string{"code"})
	promotionPushes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tobby_promotion_pushes_total",
		Help: "Promotion outcomes by result: pushed, or skipped because the destination already held the digest (FR-026, FR-028).",
	}, []string{"result"})
	promotionBytes := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tobby_promotion_pushed_bytes_total",
		Help: "Bytes pushed to the destination registry; an already-synchronized recipe adds nothing (FR-028).",
	})
	promotionRefusals := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "tobby_promotion_refusals_total",
		Help: "Pushes refused before transfer, by taxonomy code (FR-030 allowlist, FR-033 signatures, FR-035 destination limits).",
	}, []string{"code"})
	// Both results exist from the first scrape: a counter that only
	// appears once it fires cannot distinguish "never happened" from
	// "instance not reporting", and on a promotion service the difference
	// is the whole alert.
	promotionPushes.WithLabelValues(ResultPushed)
	promotionPushes.WithLabelValues(ResultSkipped)
	reg.MustRegister(syncInflight, syncBytes, policyRejections,
		promotionPushes, promotionBytes, promotionRefusals)

	return &Registry{
		Registry:          reg,
		SyncInflight:      syncInflight,
		SyncBytes:         syncBytes,
		PolicyRejections:  policyRejections,
		PromotionPushes:   promotionPushes,
		PromotionBytes:    promotionBytes,
		PromotionRefusals: promotionRefusals,
	}
}

// The PromotionPushes result labels. The set is closed and part of the
// documented metric surface.
const (
	// ResultPushed counts an item that crossed into the destination.
	ResultPushed = "pushed"
	// ResultSkipped counts an item the destination already held at the
	// same digest (FR-026 up-to-date): zero bytes moved.
	ResultSkipped = "skipped"
)
