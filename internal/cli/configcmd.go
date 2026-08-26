// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"gopkg.in/yaml.v3"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the layered configuration",
	}
	cmd.AddCommand(newConfigDumpCmd())
	return cmd
}

// newConfigDumpCmd renders the effective configuration after all layers are
// merged (FR-003). Secrets are redacted by construction (NFR-015).
func newConfigDumpCmd() *cobra.Command {
	flags := &commonFlags{}
	// YAML first: the dump's whole purpose is to be written back as a
	// configuration file, and `tobby config dump > config.yaml` is the
	// corrective action TBY-CFG-001 hands out. JSON is the R-08 machine
	// form for everything that wants to read one key without a YAML
	// parser.
	report := newReportFlag(outputYAML, outputJSON)
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Print the effective configuration (secrets redacted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := report.validate(cmd); err != nil {
				return err
			}
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			out, err := cfg.Dump()
			if err != nil {
				return err
			}
			if report.json() {
				return dumpAsJSON(cmd, out)
			}
			// The effective configuration is machine output: stdout, so
			// `tobby config dump > config.yaml` writes the file the
			// TBY-CFG-001 corrective action tells operators to check
			// (cobra's Print* helpers default to stderr).
			_, _ = fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}
	report.register(cmd)
	flags.register(cmd)
	return cmd
}

// dumpAsJSON re-encodes the YAML dump rather than marshalling the
// configuration a second time. The redaction (NFR-015) lives in
// Config.Dump and in the field types it walks; a second marshalling path
// would be a second place for a secret to escape, and the two would drift
// the first time a field gained a yaml tag its json tag did not get.
func dumpAsJSON(cmd *cobra.Command, dump string) error {
	var doc any
	if err := yaml.Unmarshal([]byte(dump), &doc); err != nil {
		return fmt.Errorf("re-reading the configuration dump: %w", err)
	}
	return writeJSON(cmd, doc)
}
