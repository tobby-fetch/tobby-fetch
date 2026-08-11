// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
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
			cmd.Print(out)
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}
