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

	return &Registry{Registry: reg}
}
