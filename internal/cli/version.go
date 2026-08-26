// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// versionReport is the machine form of `tobby version` (R-08). The fields
// are the ones buildinfo.String() renders on its single line, split apart:
// a pipeline that checks whether the deployed binary matches a signed
// release compares a version and a commit, and should not have to parse a
// sentence to get them.
type versionReport struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	APIMajor  string `json:"apiMajor"`
	ExitCodes []int  `json:"exitCodes"`
}

// apiMajor is the version of the REST API this binary serves (FR-060).
// It travels in the version report because an automation that speaks to
// /api/v1 needs to know it is still v1, and asking the binary is cheaper
// than reaching an instance to find out.
const apiMajor = "v1"

func newVersionCmd() *cobra.Command {
	report := newReportFlag(outputText, outputJSON)
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the Tobby version and build metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := report.validate(cmd); err != nil {
				return err
			}
			if report.json() {
				return writeJSON(cmd, newVersionReport())
			}
			// Machine output goes to stdout so it can be piped or captured
			// (cobra's Print* helpers default to stderr — B-010).
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), buildinfo.String())
			return nil
		},
	}
	report.register(cmd)
	return cmd
}

// newVersionReport assembles the document. The exit-code list comes from
// the taxonomy's published table (R-08), so `tobby version --output json`
// answers "which codes can this binary return" without a documentation
// lookup — and cannot answer it differently from the table.
func newVersionReport() versionReport {
	table := taxonomy.ExitCodes()
	codes := make([]int, 0, len(table))
	for _, e := range table {
		codes = append(codes, e.Code)
	}
	return versionReport{
		Version:   buildinfo.Version(),
		Commit:    buildinfo.Commit(),
		Date:      buildinfo.Date(),
		Go:        runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		APIMajor:  apiMajor,
		ExitCodes: codes,
	}
}
