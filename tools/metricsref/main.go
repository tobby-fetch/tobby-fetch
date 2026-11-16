// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command metricsref writes the metrics reference (R-11) from the running
// registry:
//
//	go run ./tools/metricsref [-o docs/metrics-reference.json]
//
// The release workflow signs the output beside openapi.yaml; a test in
// internal/metrics fails when the committed copy is stale.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
)

func main() {
	out := flag.String("o", "docs/metrics-reference.json", "output file (- for stdout)")
	flag.Parse()
	doc, err := metrics.Describe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "metricsref:", err)
		os.Exit(1)
	}
	raw, err := metrics.Render(doc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "metricsref:", err)
		os.Exit(1)
	}
	if *out == "-" {
		_, _ = os.Stdout.Write(raw)
		return
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil { //nolint:gosec // G306: a published document, not a secret
		fmt.Fprintln(os.Stderr, "metricsref:", err)
		os.Exit(1)
	}
}
