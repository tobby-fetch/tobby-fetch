// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/runid"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// `tobby store` groups the commands that act on a store directory as such,
// whatever mode produced it.
func newStoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Inspect a store (FR-057)",
		Long: `Operate on a store directory.

The commands here read a store; none of them changes one.`,
	}
	cmd.AddCommand(newStoreVerifyCmd())
	return cmd
}

func newStoreVerifyCmd() *cobra.Command {
	var (
		common commonFlags
		report = newReportFlag(outputText, outputJSON)
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify the integrity of a whole store",
		Long: `Verify a whole store on demand (FR-057), in either mode — an instance's
store and a transportable one are verified the same way. Four checks:

  blobs       every blob is re-hashed against the digest it is stored under;
  manifests   every manifest reachable from a recorded recipe is complete —
              its config, its layers, its signature artifacts, its index
              children (a platform-filtered index stays complete);
  signatures  every recorded recipe signature verifies against the trust
              roots configured now (scopes applied); the reduced-assurance
              marker of a recipe admitted unsigned by a scope is re-reported;
  orphans     content that no recorded recipe and no protected root
              (unit imports, seeded content, the vulnerability database)
              reaches is listed.

The verification only reads. It repairs nothing, deletes nothing and
writes nothing into the store — which is byte-identical afterwards — and
the report is timestamped: start and end, run identifier, verdict, and
every defect as a taxonomy code naming the file or object concerned.
Orphaned content is listed as a note and never makes the verdict "defects".

It runs in this process, against the directory, and works on a host where
no instance is running. When an instance IS serving the store, start the
verification from its Store integrity screen or POST /api/v1/store/verify
instead: those hold the instance's content lock, so a removal in progress
cannot read as a defect, and they leave a tracked task. Trust roots come
from the configuration (trust.roots, trust.scopes), never from the store.

Exit codes: 0 the store is intact, 4 a defect was found (a blob that does
not match its digest, an incomplete or unreadable manifest, a signature
that no longer verifies).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A mistyped --output is a usage error whatever else is
			// missing, as everywhere else.
			if err := report.validate(cmd); err != nil {
				return err
			}
			cfg, err := common.loadFor(cmd, config.ScopeStorage)
			if err != nil {
				return err
			}
			return runStoreVerify(cmd, &cfg, report)
		},
	}
	common.register(cmd)
	report.register(cmd)
	return cmd
}

// runStoreVerify opens the store, verifies it, prints the report and turns
// the verdict into the exit class.
func runStoreVerify(cmd *cobra.Command, cfg *config.Config, report *reportFlag) error {
	root := cfg.Storage.Root
	// Opening a store stamps a directory that lacks a format version or a
	// medium identity — a write, and this command promises none. A
	// directory that is not already a Tobby store is refused before
	// anything is opened.
	for _, f := range []string{"format.json", "media-id.json"} {
		if _, err := os.Stat(filepath.Join(root, "meta", f)); err != nil {
			return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
				"detail": root + " is not a Tobby store (no meta/" + f + "): nothing was opened or written. " +
					"Point --storage-root at the store an instance serves or a mirror synchronization produced",
			}).WithCause(err)
		}
	}

	level, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return err
	}
	runID := runid.New()
	logger := logging.WithRunID(logging.New(cmd.ErrOrStderr(), level), runID)

	st, err := store.Open(cmd.Context(), root, logger)
	if err != nil {
		return err
	}
	defer st.Close() //nolint:errcheck // read side

	// THIS instance's trust roots, resolved now (RECIPE-SPEC §12.3) —
	// never read from the store, which carries none as far as this code
	// is concerned.
	egress, err := netx.New(&cfg.Network)
	if err != nil {
		return err
	}
	defer egress.CloseIdleConnections()
	trustCache := ""
	if cfg.State.Root != "" {
		trustCache = filepath.Join(cfg.State.Root, "trust-cache")
	}
	trust, err := engine.LoadTrust(cfg.Trust, trustCache, egress)
	if err != nil {
		return err
	}

	rep, err := check.Verify(cmd.Context(), st, check.Options{
		Trust: engine.MediaTrust{Policy: trust}, RunID: runID, Logger: logger,
	})
	if err != nil {
		return err
	}
	outcome := audit.OutcomeSuccess
	if rep.Verdict == integrity.VerdictDefects {
		outcome = audit.OutcomeFailure
	}
	audit.Log(cmd.Context(), logger, &audit.Event{
		Actor: audit.ActorLocal, Action: audit.ActionIntegrityVerify,
		Target: root, Outcome: outcome, Origin: audit.OriginLocal,
	})

	if err := writeIntegrityReport(cmd, report, rep); err != nil {
		return err
	}
	if p := rep.Principal(); p != nil {
		return p
	}
	return nil
}

// writeIntegrityReport renders the report: the machine document alone on
// stdout under --output json, the narration otherwise.
func writeIntegrityReport(cmd *cobra.Command, report *reportFlag, rep *integrity.Report) error {
	if report.json() {
		return writeJSON(cmd, rep)
	}
	out := report.human(cmd)
	mode := "instance store"
	if rep.Store.Transportable {
		mode = "transportable store"
	}
	_, _ = fmt.Fprintf(out, "%s %s, verified %s → %s (run %s)\n", mode, rep.Store.Root,
		rep.StartedAt.Format(time.RFC3339), rep.FinishedAt.Format(time.RFC3339), rep.RunID)
	_, _ = fmt.Fprintf(out, "verdict: %s — %d blobs (%d bytes), %d manifests, %d recipes checked\n",
		rep.Verdict, rep.Checked.Blobs, rep.Checked.Bytes, rep.Checked.Manifests, rep.Checked.Recipes)
	for i := range rep.Recipes {
		r := &rep.Recipes[i]
		complete := "complete"
		if !r.Complete {
			complete = "INCOMPLETE"
		}
		line := fmt.Sprintf("  recipe %s@%s  %s, signature %s", r.Name, r.Version, complete, r.Signature)
		if r.KeyFingerprint != "" {
			line += " (key " + r.KeyFingerprint + ")"
		}
		if r.Assurance.Reduced() {
			line += " [reduced assurance: scope " + r.Assurance.Scope + "]"
		}
		_, _ = fmt.Fprintln(out, line)
	}
	for i := range rep.Defects {
		d := &rep.Defects[i]
		_, _ = fmt.Fprintf(out, "  DEFECT  %s %s\n", d.Code, d.Path)
	}
	if rep.DefectsOmitted > 0 {
		_, _ = fmt.Fprintf(out, "  … and %d more defects (not listed)\n", rep.DefectsOmitted)
	}
	for i := range rep.Findings {
		f := &rep.Findings[i]
		_, _ = fmt.Fprintf(out, "  orphaned  %s %s (reachable from no recipe nor protected root; never deleted by this command)\n", f.Code, f.Path)
	}
	if rep.FindingsOmitted > 0 {
		_, _ = fmt.Fprintf(out, "  … and %d more orphaned items (not listed)\n", rep.FindingsOmitted)
	}
	return nil
}
