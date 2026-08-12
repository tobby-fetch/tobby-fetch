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
	reg.MustRegister(syncInflight, syncBytes)

	return &Registry{Registry: reg, SyncInflight: syncInflight, SyncBytes: syncBytes}
}
