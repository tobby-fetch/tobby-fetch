// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/ui/format"
)

// `tobby sync --dry-run` — plan mode on the command line (FR-055
// amendment R-04, FR-066).
//
// The command exists for the gate: a merge request that changes a
// Retriever should be able to ask "what would this do" and get an answer
// a pipeline can branch on, with no instance running and nothing written
// anywhere. Its exit codes are the contract — 0 nothing to do, 5 changes
// planned, 3 refused by policy, 4 verification failed, 1 the plan could
// not complete — and `--output json` is the machine form of the same
// report (R-08).
//
// `--dry-run` is required at this milestone, and the refusal without it
// says so rather than pretending. Triggering a real synchronization from
// the command line (FR-066) means opening the store for WRITING, which a
// second process cannot do while an instance is serving from it; the
// live trigger therefore goes through that instance — POST /api/v1/sync
// or the button beside it — and the command-line trigger lands with the
// media automation work.

func newSyncCmd() *cobra.Command {
	flags := &commonFlags{}
	var (
		dryRun    bool
		retriever string
		output    string
		skipDest  bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Plan a synchronization without performing it (--dry-run)",
		Long: `Simulate the next synchronization and report everything it would do,
without writing one byte anywhere.

The report covers the resolved versions of every recipe, the per-digest
status of every ingredient against the local store, the deduplicated
volume to transfer, the projected store size against the target's free
space and filesystem capability, the content a prune would remove, and
the policy verdicts that need no transfer — the registry allow-list and
the recipes' own signatures.

Nothing is written to the store, nothing is pushed, and the automatic
reconciliation cadence of a passthrough instance is left exactly where it
was.

Exit codes, for use as a gate:

  0  nothing to do
  5  changes are planned
  3  refused by policy (a registry outside the allow-list)
  4  verification failed (a recipe signature no trust root validates)
  1  the plan could not complete
  2  usage error

Examples:

  tobby sync --dry-run
  tobby sync --dry-run --retriever ./retriever.yaml --output json
`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dryRun {
				return &usageError{
					err: fmt.Errorf("this command only runs with --dry-run: triggering a real synchronization needs the serving instance " +
						"(the \"Synchronize\" button, or POST /api/v1/sync), because the store cannot be opened for writing by a second process"),
					hint: "see '" + cmd.CommandPath() + " --help'",
				}
			}
			if output != "text" && output != "json" {
				return &usageError{
					err:  fmt.Errorf("--output accepts \"text\" or \"json\", not %q", output),
					hint: "see '" + cmd.CommandPath() + " --help'",
				}
			}
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			return runPlan(cmd, &cfg, planFlags{
				retriever:       retriever,
				output:          output,
				skipDestination: skipDest,
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what a synchronization would do, without doing any of it")
	cmd.Flags().StringVar(&retriever, "retriever", "", "plan a candidate Retriever instead of the configured one (file path, HTTP(S) URL, or OCI reference)")
	cmd.Flags().StringVar(&output, "output", "text", `report format: "text" or "json"`)
	cmd.Flags().BoolVar(&skipDest, "skip-destination", false, "do not contact the promotion destination")
	flags.register(cmd)
	return cmd
}

// planFlags carries the command's own options.
type planFlags struct {
	retriever       string
	output          string
	skipDestination bool
}

// runPlan opens the store read-only, builds the plan engine from the same
// configuration a serving instance would, and reports.
func runPlan(cmd *cobra.Command, cfg *config.Config, f planFlags) error {
	if cfg.Storage.Root == "" {
		return fmt.Errorf("storage.root is required to plan: set --storage-root, %s, or \"storage.root:\" in the configuration file", config.EnvStorageRoot)
	}
	// The plan reads the store to answer FR-026 and FR-055; it never
	// writes to it. Opening it is a precondition of reading, and it is
	// the only filesystem work this command does — the store's own
	// format stamp on a directory that has never been opened before
	// belongs to store.Open, not to the plan (see store.checkFormat).
	logger := logging.New(io.Discard, slog.LevelError)
	st, err := store.Open(cmd.Context(), cfg.Storage.Root, logger)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	allowlist, err := policy.NewAllowlist(cfg.Registries.Allowlist)
	if err != nil {
		return err
	}
	egress, err := netx.New(&cfg.Network)
	if err != nil {
		return err
	}
	defer egress.CloseIdleConnections()

	remotes, err := engine.NewRemotes(cfg.Registries, allowlist, egress)
	if err != nil {
		return err
	}
	remotes.SetResumer(blobfetch.New(egress, remotes.Keychain(), cfg.State.Root, cfg.Transfer.ResumeThreshold))
	trustCache := ""
	if cfg.State.Root != "" {
		trustCache = filepath.Join(cfg.State.Root, "trust-cache")
	}
	trust, err := engine.LoadTrust(cfg.Trust, trustCache, egress)
	if err != nil {
		return err
	}

	planner := engine.NewPlanner(st, remotes, trust, cfg.Retriever.Source, engine.PlanConfig{
		Mode:           string(cfg.Mode),
		BasePrefix:     cfg.Storage.BasePrefix,
		MarginPercent:  cfg.Preflight.SafetyMarginPercent,
		MarginDisabled: cfg.Preflight.Disabled,
	})
	destination, err := engine.NewDestination(cfg.Destination, cfg.Registries, allowlist, egress)
	if err != nil {
		return err
	}
	planner.SetDestination(destination)

	plan, err := planner.Plan(cmd.Context(), engine.PlanOptions{
		Retriever:       f.retriever,
		SkipDestination: f.skipDestination,
	})
	if err != nil {
		return err
	}

	if f.output == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(plan); err != nil {
			return err
		}
	} else {
		writePlanText(cmd.OutOrStdout(), plan)
	}
	if code := plan.ExitCode(); code != taxonomy.ExitOK {
		return &exitError{code: code}
	}
	return nil
}

// exitError carries a process exit code that is not a failure to report.
//
// A plan that found changes exits 5 and has nothing to complain about;
// one refused by policy exits 3 and has already printed the refusal in
// its report. Both are outcomes, not errors, and printing "tobby: …"
// after a complete report would be noise on the one stream a pipeline
// reads.
type exitError struct{ code int }

func (e *exitError) Error() string { return "" }

// writePlanText renders the report for a human. English only: the CLI
// follows the host convention for the taxonomy's own messages (cliLang)
// and states everything else in the project language, the way `tobby
// recipe push` does. The numbers are exact bytes AND a readable size —
// an operator sizing a medium needs the second, a script diffing two runs
// needs the first, and `--output json` is where the first one lives
// unambiguously.
func writePlanText(w io.Writer, plan *engine.Plan) {
	// One deliberate ignore point for the whole report. A report is
	// written to stdout, and a stdout that cannot be written to takes the
	// process with it long before any of these lines could recover from
	// it — checking six hundred bytes of formatting one Fprintf at a time
	// would add noise and no behaviour.
	out := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	line := func(s string) { _, _ = fmt.Fprintln(w, s) }

	out("plan of %s", plan.Source)
	if plan.Zone != "" {
		out(" (zone %s)", plan.Zone)
	}
	out("\n  outcome: %s\n", plan.Outcome)

	for i := range plan.Checks {
		c := &plan.Checks[i]
		out("\npre-flight (%s at %s)\n", c.Target, c.Path)
		fs := "not identified"
		if c.Filesystem.Identified {
			fs = fmt.Sprintf("%s, largest file %s", c.Filesystem.Type, format.Bytes("en", c.Filesystem.MaxFileSize))
		} else if c.Filesystem.Type != "" {
			fs = c.Filesystem.Type + " (limit unknown to this build)"
		}
		out("  filesystem:  %s\n", fs)
		if c.Space.Known {
			out("  free:        %s (%d bytes)\n", format.Bytes("en", c.Space.FreeBytes), c.Space.FreeBytes)
			out("  margin:      %d %% (%s held back)\n", c.MarginPercent, format.Bytes("en", c.ReservedBytes))
			out("  usable:      %s\n", format.Bytes("en", c.UsableBytes))
		} else {
			out("  free:        could not be measured\n")
		}
		out("  to write:    %s (%d bytes)\n", format.Bytes("en", c.ProjectedBytes), c.ProjectedBytes)
		out("  largest:     %s\n", format.Bytes("en", c.LargestFileBytes))
		for _, warn := range c.Warnings {
			out("  warning:     %s\n", warn)
		}
		if !c.OK() {
			out("  REFUSED:     %s", c.RefusalCode)
			if c.ShortfallBytes > 0 {
				out(" — short by %s (%d bytes)", format.Bytes("en", c.ShortfallBytes), c.ShortfallBytes)
			}
			out("\n")
		}
	}

	t := &plan.Totals
	out("\ntotals\n")
	out("  recipes:     %d\n", t.Recipes)
	out("  ingredients: %d (%d new, %d outdated, %d up-to-date)\n", t.Ingredients, t.New, t.Outdated, t.UpToDate)
	out("  to transfer: %s (%d bytes, deduplicated by digest, net of the local store)\n",
		format.Bytes("en", t.TransferBytes), t.TransferBytes)
	out("  store:       %s → %s\n", format.Bytes("en", t.StoreBytes), format.Bytes("en", t.ProjectedStoreBytes))
	if t.PushEvaluated {
		out("  to promote:  at most %s towards %s\n", format.Bytes("en", t.PushUpperBoundBytes), plan.Destination)
	}

	if len(plan.Recipes) > 0 {
		out("\nrecipes\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  NAME\tREQUESTED\tRESOLVED\tSIGNATURE\tTRANSFER")
		for i := range plan.Recipes {
			r := &plan.Recipes[i]
			resolved := r.Resolved
			if resolved == "" {
				resolved = "-"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", r.Name, r.Requested, resolved, r.Signature, format.Bytes("en", r.TransferBytes))
		}
		_ = tw.Flush()
	}

	out("\nregistry policy\n")
	if plan.Policy.AllowlistDeclared {
		out("  allow-list:  %s\n", strings.Join(plan.Policy.AllowlistPatterns, ", "))
	} else {
		out("  allow-list:  not declared (no registry is refused on that ground)\n")
	}
	for _, h := range plan.Policy.Hosts {
		verdict := "allowed"
		if !h.Allowed {
			verdict = "REFUSED"
		}
		out("  %-40s %s (%s)\n", h.Host, verdict, h.Role)
	}

	out("\nprojected removal\n")
	switch {
	case !plan.Prune.Evaluated:
		out("  not evaluated: %s\n", plan.Prune.Reason)
	case len(plan.Prune.Repositories) == 0:
		out("  nothing would be removed\n")
	default:
		out("  %d repositories, %s\n", len(plan.Prune.Repositories), format.Bytes("en", plan.Prune.TotalBytes))
		for _, p := range plan.Prune.Repositories {
			out("    %s (%s, brought by %s)\n", p.Repo, format.Bytes("en", p.Bytes), p.Recipe)
		}
	}

	if len(plan.Problems) > 0 {
		out("\nproblems\n")
		seen := map[string]bool{}
		lines := make([]string, 0, len(plan.Problems))
		for i := range plan.Problems {
			p := &plan.Problems[i]
			row := fmt.Sprintf("  %s  %-14s %s", p.Code, p.Class, p.Subject)
			if seen[row] {
				continue
			}
			seen[row] = true
			lines = append(lines, row)
		}
		sort.Strings(lines)
		for _, l := range lines {
			line(l)
		}
		out("\n  Every code above is documented at /help#<code> on a running instance.\n")
	}
}
