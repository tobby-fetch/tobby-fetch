// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Tobby version and build metadata",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			// Machine output goes to stdout so it can be piped or captured
			// (cobra's Print* helpers default to stderr).
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.String())
		},
	}
}
