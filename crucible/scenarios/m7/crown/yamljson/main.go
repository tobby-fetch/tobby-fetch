// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Command yamljson converts one document between YAML and JSON, for the
// crown crucible scenario (crucible/scenarios/m7/z-crown-rke2.sh).
//
// The scenario has to read YAML it did not write — the registries.yaml
// snippet Tobby generates, the recipes it cooks — and the crucible host
// carries jq but no YAML tool. Converting to JSON lets every assertion be a
// jq expression, and converting back writes the cooked recipes as YAML.
// It lives under the scenario because it is harness, not product: nothing
// in Tobby depends on it.
//
//	yamljson -to json <file|->   YAML (or JSON) in, JSON out
//	yamljson -to yaml <file|->   JSON (or YAML) in, YAML out
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	to := flag.String("to", "json", `output format: "json" or "yaml"`)
	flag.Parse()
	if err := run(*to, flag.Args(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "yamljson:", err)
		os.Exit(1)
	}
}

func run(to string, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: yamljson -to json|yaml <file|->")
	}
	in := stdin
	if args[0] != "-" {
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close() //nolint:errcheck // read-only
		in = f
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	// JSON is a subset of YAML 1.2: one decoder reads both.
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parsing: %w", err)
	}
	switch to {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	case "yaml":
		enc := yaml.NewEncoder(stdout)
		enc.SetIndent(2)
		if err := enc.Encode(doc); err != nil {
			return err
		}
		return enc.Close()
	}
	return fmt.Errorf("unknown output format %q", to)
}
