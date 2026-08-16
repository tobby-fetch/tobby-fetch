// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"

	"github.com/spf13/cobra"
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
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Print the effective configuration (secrets redacted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			out, err := cfg.Dump()
			if err != nil {
				return err
			}
			// The effective configuration is machine output: stdout, so
			// `tobby config dump > config.yaml` writes the file the
			// TBY-CFG-001 corrective action tells operators to check
			// (cobra's Print* helpers default to stderr).
			_, _ = fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}
