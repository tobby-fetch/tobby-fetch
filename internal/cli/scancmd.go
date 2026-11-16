// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/scanpolicy"
)

// `tobby scan` groups the scan-policy commands (FR-031). scan.go is the
// serve wiring of the scanner; this file is the command line.

// Where a `tobby scan exceptions` report was read from.
const (
	policySourceConfiguration = "configuration"
	policySourceInstance      = "instance"
)

// scanExceptionsReport is the `tobby scan exceptions --output json`
// document: the effective policy view (the GET /api/v1/policy body) and
// where it was read from.
type scanExceptionsReport struct {
	// Source is "configuration" (this host's configuration file and its
	// OpenVEX documents, read here) or "instance" (a running instance's
	// GET /api/v1/policy, the Retriever's exceptions included).
	Source string `json:"source"`
	// Instance is the base URL queried, for source "instance".
	Instance string `json:"instance,omitempty"`
	*scanpolicy.View
}

func newScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Inspect the vulnerability-scan policy (FR-031)",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newScanExceptionsCmd())
	return cmd
}

func newScanExceptionsCmd() *cobra.Command {
	flags := &commonFlags{}
	instance := &instanceFlags{}
	report := newReportFlag(outputText, outputJSON)
	cmd := &cobra.Command{
		Use:   "exceptions",
		Short: "List the vulnerability exceptions with their origin, scope, expiry and state (R-15)",
		Long: `List the vulnerability exceptions of the scan policy: for each, its origin
(instance, retriever, or vex with the OpenVEX document), its scope (digest,
repository, recipe), its justification, its expiry and its state now —
active, expired (it no longer applies: the findings it covered are judged
again) or unbounded (a Retriever exception of the v1alpha1 format without
expiry, applied and reported as such, TBY-SCN-028). The OpenVEX documents
are listed with the statements they admitted and ignored, and the
hardening switches are stated.

By default the command reads THIS HOST's configuration (--config, the
environment) and its scan.vex documents, and contacts nothing: a document
that cannot be read or validated fails exactly as it would refuse the
start (TBY-SCN-027). The Retriever's exceptions are only known to a
running instance that has served its Retriever, so they are not listed in
that mode, and the report says so.

With --instance (or ` + EnvInstanceURL + ` set) the command reads a running
instance's effective policy instead — GET /api/v1/policy, the document of
its /admin/policy screen — which includes the exceptions of the last
Retriever it served. Authenticate with a static API token, viewer role at
least: ` + EnvAPIToken + `, or --token-file.

The command changes nothing; it exits 0 whatever the states, and with the
error's class when the configuration or a document cannot be read (1, 2 for
a usage error).

Examples:

  tobby scan exceptions
  tobby scan exceptions --config /etc/tobby/config.yaml --output json
  tobby scan exceptions --instance https://tobby.example:8443
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := report.validate(cmd); err != nil {
				return err
			}
			rep, err := scanExceptions(cmd, flags, instance)
			if err != nil {
				return err
			}
			if report.json() {
				return writeJSON(cmd, rep)
			}
			writeScanExceptionsText(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	report.register(cmd)
	flags.register(cmd)
	instance.register(cmd)
	return cmd
}

// scanExceptions reads the policy from the configuration, or from a
// running instance when one is named.
func scanExceptions(cmd *cobra.Command, flags *commonFlags, instance *instanceFlags) (*scanExceptionsReport, error) {
	online := cmd.Flags().Changed("instance") || os.Getenv(EnvInstanceURL) != ""
	if online {
		cfg, err := flags.loadFor(cmd, config.ScopeNetwork)
		if err != nil {
			return nil, err
		}
		client, err := newInstanceClient(cmd, &cfg, instance)
		if err != nil {
			return nil, err
		}
		defer client.close()
		v := &scanpolicy.View{}
		if err := client.do(cmd.Context(), http.MethodGet, "/api/v1/policy", nil, v); err != nil {
			return nil, err
		}
		return &scanExceptionsReport{Source: policySourceInstance, Instance: client.base, View: v}, nil
	}
	for _, f := range []string{"token-file", "request-timeout", "allow-plaintext-token"} {
		if cmd.Flags().Changed(f) {
			return nil, &usageError{
				err:  fmt.Errorf("--%s applies with --instance only: without it the configuration is read here and nothing is contacted", f),
				hint: "see '" + cmd.CommandPath() + " --help'",
			}
		}
	}
	// The scan section is validated at every scope; the state scope
	// demands nothing this command does not read.
	cfg, err := flags.loadFor(cmd, config.ScopeState)
	if err != nil {
		return nil, err
	}
	docs, err := scanpolicy.LoadDocuments(cfg.Scan.VEX)
	if err != nil {
		return nil, err
	}
	src := scanpolicy.New(&scanpolicy.Options{
		Scan: &cfg.Scan, Documents: docs,
		Hardening: hardening.Switches{DisableUnitImport: cfg.Hardening.DisableUnitImport, DisableRegistryPush: cfg.Hardening.DisableRegistryPush},
	})
	return &scanExceptionsReport{Source: policySourceConfiguration, View: src.View()}, nil
}

// writeScanExceptionsText renders the report for a person.
func writeScanExceptionsText(w io.Writer, rep *scanExceptionsReport) {
	v := rep.View
	if rep.Source == policySourceInstance {
		_, _ = fmt.Fprintf(w, "Scan policy of the running instance %s, at %s\n", rep.Instance, v.EvaluatedAt.Format(time.RFC3339))
	} else {
		_, _ = fmt.Fprintf(w, "Scan policy from this host's configuration, at %s\n", v.EvaluatedAt.Format(time.RFC3339))
	}
	posture := "advisory"
	if v.Blocking {
		posture = "blocking"
	}
	_, _ = fmt.Fprintf(w, "  mode %s (%s), threshold %v\n\n", v.Mode, posture, v.Threshold)

	_, _ = fmt.Fprintln(w, "Exceptions:")
	if len(v.Exceptions) == 0 {
		_, _ = fmt.Fprintln(w, "  none")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  ID\tORIGIN\tSTATE\tEXPIRES\tSCOPE\tJUSTIFICATION")
		for _, ex := range v.Exceptions {
			origin := ex.Origin
			if ex.Document != "" {
				origin += " " + ex.Document
			}
			expires := "-"
			if ex.Expires != nil {
				expires = ex.Expires.Format(time.RFC3339)
			}
			scope := "every item of its level"
			if s := ex.Scope.String(); s != "" {
				scope = s
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", ex.ID, origin, ex.State, expires, scope, oneLine(ex.Justification))
		}
		_ = tw.Flush()
	}
	if !v.RetrieverKnown {
		if rep.Source == policySourceConfiguration {
			_, _ = fmt.Fprintln(w, "  The Retriever's exceptions are known only to a running instance: use --instance to list them.")
		} else {
			_, _ = fmt.Fprintln(w, "  The instance has not served a Retriever yet: its exceptions are not known.")
		}
	}

	_, _ = fmt.Fprintln(w, "\nOpenVEX documents:")
	if len(v.Documents) == 0 {
		_, _ = fmt.Fprintln(w, "  none declared (scan.vex)")
	}
	for i := range v.Documents {
		d := &v.Documents[i]
		_, _ = fmt.Fprintf(w, "  %s (%s), expires %s (%s): %d statements, %d admitted, %d ignored, %d exceptions\n",
			d.Path, d.DocumentID, d.Expires.Format(time.RFC3339), d.State, d.Statements, d.Admitted, d.Ignored, d.Exceptions)
		for _, n := range d.Notes {
			line := fmt.Sprintf("    statement %d (%s): %s", n.Statement, n.Vulnerability, n.Reason)
			if n.Product != "" {
				line += ", product " + n.Product
			}
			if n.Detail != "" {
				line += " — " + n.Detail
			}
			_, _ = fmt.Fprintln(w, line)
		}
	}
	_, _ = fmt.Fprintf(w, "\nHardening: %s=%t %s=%t\n",
		hardening.SettingDisableUnitImport, v.Hardening.DisableUnitImport,
		hardening.SettingDisableRegistryPush, v.Hardening.DisableRegistryPush)
}

// oneLine keeps a justification on its table row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
