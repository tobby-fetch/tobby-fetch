// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/medialog"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/runid"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// `tobby media` is the destination-side operator's command (FR-052,
// FR-066): the person standing in an isolated zone with a disk in their
// hand, on a host that may not be running an instance at all.
//
// It runs in-process against the transported store rather than talking to
// a serving instance, because in that zone there may be nothing to talk
// to yet — the medium is often what brings the instance itself. The
// engine, the trust policy and the push path are the same objects the
// server wires: there is one implementation of the FR-054 order, not a
// server one and a CLI one.

// newMediaCmd wires `tobby media`.
func newMediaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Verify and import a transported store (FR-052)",
		Long: `Operate on a store that arrived on a physical medium.

The medium is untrusted until proven otherwise. Every subcommand here
re-verifies it first — the manifest's completeness and checksums, then the
recipes' signatures against THIS instance's trust roots, then every
ingredient against its pinned digest — and only then acts on it. Trust
roots present on the medium are ignored.`,
	}
	cmd.AddCommand(newMediaVerifyCmd(), newMediaImportCmd())
	return cmd
}

// mediaFlags are the flags both media subcommands share.
type mediaFlags struct {
	common            commonFlags
	report            *reportFlag
	zone              string
	allowZoneMismatch bool
	allowStale        bool
}

func newMediaFlags() *mediaFlags {
	return &mediaFlags{report: newReportFlag(outputText, outputJSON)}
}

func (f *mediaFlags) register(cmd *cobra.Command) {
	f.common.register(cmd)
	f.report.register(cmd)
	fs := cmd.Flags()
	fs.StringVar(&f.zone, "zone", "", "identity of the zone this instance serves (default: the configured zone)")
	// The two waivable guards of FR-054, one flag each and never a
	// combined one: an operator waiving the zone check has not thereby
	// decided anything about freshness, and a single --force would let
	// them do both while meaning one.
	//
	// Both commands take them. On `import` a waiver authorizes; on
	// `verify` — which writes nothing and pushes nothing — it previews,
	// so an operator can see what a waived import would do before
	// committing to it. That is the same pair the API's two endpoints
	// take, for the same reason (FR-061).
	fs.BoolVar(&f.allowZoneMismatch, "allow-zone-mismatch", false,
		"proceed on a medium addressed to another zone (administrator override, audited)")
	fs.BoolVar(&f.allowStale, "allow-stale", false,
		"proceed on a medium older than the last one imported for this zone (administrator override, audited)")
}

// load builds the effective configuration for a media command and settles
// the zone identity: the flag wins over the configuration, and neither
// being set is a refusal rather than a guess (FR-054 — an instance that
// does not know which zone it serves cannot tell whether a medium is
// addressed to it).
func (f *mediaFlags) load(cmd *cobra.Command) (config.Config, error) {
	// The report format is checked first: a mistyped --output is a usage
	// error (exit 2) whatever else is missing, and reporting a
	// configuration problem for a command line the parser already
	// rejected sends the operator to the wrong file.
	if err := f.report.validate(cmd); err != nil {
		return config.Config{}, err
	}
	cfg, err := f.common.loadFor(cmd, config.ScopeMedia)
	if err != nil {
		return config.Config{}, err
	}
	if f.zone != "" {
		cfg.Zone = f.zone
	}
	switch {
	case cfg.Storage.Root == "":
		return config.Config{}, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no store to read: set --storage-root, " + config.EnvStorageRoot +
				", or \"storage.root:\" in the configuration file",
		})
	case cfg.Zone == "":
		return config.Config{}, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": "no zone identity: set --zone, " + config.EnvZone +
				", or \"zone:\" in the configuration file (FR-052)",
		})
	}
	return cfg, nil
}

func newMediaVerifyCmd() *cobra.Command {
	flags := newMediaFlags()
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Re-verify a transported store without pushing anything",
		Long: `Re-verify a transported store and report, without pushing anything and
without writing to the store.

The report names, for every refusal, both why and which file. A recipe
whose signature verifies and whose every reachable file matches its pinned
digest is pushable; any other is blocked whole, with no override. A
missing or unreadable manifest, and an altered recipe graph, block the
medium as a whole. A medium addressed to another zone, or older than the
last one imported here, block it too — those two an administrator may
waive on 'tobby media import'.

Exit codes: 0 every delivery is pushable, 3 refused by policy (zone
identity, freshness), 4 a verification failure.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			return runMediaVerify(cmd, &cfg, flags)
		},
	}
	flags.register(cmd)
	return cmd
}

func newMediaImportCmd() *cobra.Command {
	flags := newMediaFlags()
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Verify a transported store, then push what it cleared into the zone",
		Long: `Verify a transported store and push what verification cleared into the
zone registry.

The order is not a sequence of steps, it is the guarantee: nothing is
pushed, served or written before the whole medium has been re-verified.
What is pushed then goes through the same controls a passthrough
promotion goes through — the registry allow-list and the recipe
signatures, re-checked over the exact bytes about to leave — and the
signed recipes land in the zone's own cookbook. The operation is
journalled onto the medium itself, outside the manifest's coverage, as
the return audit channel.

Content the medium carries that no verified recipe reaches is reported and
never pushed.

Exit codes: 0 imported, 1 a push failed, 3 refused by policy, 4 a
verification failure.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			return runMediaImport(cmd, &cfg, flags)
		},
	}
	flags.register(cmd)
	return cmd
}

// runMediaVerify is the read-only half: verify, report, exit.
func runMediaVerify(cmd *cobra.Command, cfg *config.Config, flags *mediaFlags) error {
	op, err := openMedia(cmd.Context(), cfg, cmd.ErrOrStderr(), verifyOnly)
	if err != nil {
		return err
	}
	defer op.close()

	rep, err := op.eng.VerifyMedia(cmd.Context(), op.logger, engine.MediaOptions{
		AllowZoneMismatch: flags.allowZoneMismatch,
		AllowStale:        flags.allowStale,
	})
	if err != nil {
		return err
	}
	if err := writeReport(cmd, flags.report, rep); err != nil {
		return err
	}
	if refusal := engine.MediaRefusal(rep); refusal != nil {
		return refusal
	}
	return nil
}

// runMediaImport is the whole journey.
func runMediaImport(cmd *cobra.Command, cfg *config.Config, flags *mediaFlags) error {
	op, err := openMedia(cmd.Context(), cfg, cmd.ErrOrStderr(), importMedium)
	if err != nil {
		return err
	}
	defer op.close()

	// FR-094: a waived guard is recorded with an actor and an origin
	// before it is applied, not after it worked. On this surface the
	// actor is the local invocation — whoever holds the host holds the
	// medium — and the trail is written to the medium's own log too.
	var overrides []string
	if flags.allowZoneMismatch {
		overrides = append(overrides, tasks.OverrideZone)
	}
	if flags.allowStale {
		overrides = append(overrides, tasks.OverrideFreshness)
	}
	for _, guard := range overrides {
		audit.Log(cmd.Context(), op.logger, &audit.Event{
			Actor: audit.ActorLocal, Action: audit.ActionMediaOverride,
			Target: guard, Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
		})
	}
	audit.Log(cmd.Context(), op.logger, &audit.Event{
		Actor: audit.ActorLocal, Action: audit.ActionMediaImport,
		Target: cfg.Storage.Root, Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
	})

	// The task is fabricated here rather than queued: this command IS the
	// task, and the queue exists to survive a restart of a long-lived
	// service. Its items are still what the report is built from, so the
	// two surfaces narrate an import identically (FR-061).
	task := &tasks.Task{
		ID: "cli", RunID: op.runID, Type: tasks.TypeMediaImport,
		Reference: cfg.Storage.Root, Actor: audit.ActorLocal,
		Status: tasks.StatusRunning, Created: time.Now().UTC(),
		MediaOverrides: overrides,
	}
	runErr := op.eng.MediaImportRunner()(cmd.Context(), task, op.logger, func() {})
	settleTask(task, runErr)

	// The report is printed whatever happened: a refused import is
	// exactly when an operator needs to read it.
	if err := writeImportReport(cmd, flags.report, task); err != nil {
		return err
	}
	// FR-056: the medium's log is flushed at the task boundary, which on
	// this surface is right here.
	op.flush()
	if runErr != nil {
		return runErr
	}
	if agg := task.Aggregate(); agg.Failed > 0 {
		if p := task.Principal(); p != nil {
			return p
		}
	}
	return nil
}

// settleTask closes the fabricated task the way the queue's own finish()
// would, so the report this command prints and the one a queued import
// leaves behind describe the same operation in the same words (FR-061).
func settleTask(task *tasks.Task, runErr error) {
	task.Finished = time.Now().UTC()
	task.Status = tasks.StatusDone
	if runErr != nil || task.Aggregate().Failed > 0 {
		task.Status = tasks.StatusFailed
	}
	var te *taxonomy.Error
	if errors.As(runErr, &te) {
		task.Error = tasks.FromTaxonomy(te)
	}
}

// mediaOp is one assembled destination-side operation: the store, the
// engine, and the medium's own log.
type mediaOp struct {
	st     *store.Store
	eng    *engine.Engine
	logger *slog.Logger
	runID  string
	mlog   *medialog.Writer
	egress *netx.Egress
}

// mediaIntent says which of the two commands is being assembled. The two
// differ in what they may touch, which is why it is a mode and not a pair
// of booleans threaded separately.
type mediaIntent int

const (
	// verifyOnly reads and reports. It needs no destination registry, and
	// it writes NOTHING — not even the medium's own operation log: the
	// command promises a store left untouched, and FR-054 puts every
	// local write after verification, not around it.
	verifyOnly mediaIntent = iota
	// importMedium is the whole journey. It requires a destination and it
	// journals onto the medium, in the dedicated path outside the
	// manifest's coverage FR-054 provides for (FR-053).
	importMedium
)

// openMedia assembles what a media command needs, in the order that keeps
// the FR-054 promise reachable: the medium is checked to BE a medium
// before the store is opened, because opening one stamps a directory that
// is not a store with a format file and an identity — a local write, and
// the one FR-054 forbids before verification.
func openMedia(ctx context.Context, cfg *config.Config, stderr io.Writer, intent mediaIntent) (*mediaOp, error) {
	manifest := filepath.Join(cfg.Storage.Root, filepath.FromSlash(media.ManifestPath))
	if _, err := os.Stat(manifest); err != nil {
		return nil, taxonomy.New(taxonomy.CodeMediaManifestMissing, taxonomy.Params{
			"path": media.ManifestPath,
		}).WithCause(err)
	}

	level, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return nil, err
	}
	op := &mediaOp{runID: runid.New()}
	// The instance stream first, so a failure to open the medium's own
	// log is still reported somewhere.
	logger := logging.New(stderr, level)

	// FR-053: the operation log on the medium is the return audit
	// channel. It goes outside the manifest's coverage — medialog refuses
	// any other location — so writing it cannot invalidate the inventory
	// this very command just verified.
	if intent == importMedium && !cfg.Logging.Media.Disabled {
		mlog, mErr := medialog.Open(cfg.Storage.Root, medialog.Options{
			Path:     cfg.Logging.Media.File,
			MaxBytes: int64(cfg.Logging.Media.MaxSize),
			Keep:     cfg.Logging.Media.Keep,
		})
		if mErr != nil {
			return nil, mErr
		}
		op.mlog = mlog
		logger = logging.Tee(logger, logging.New(mlog, slog.LevelDebug))
	}
	op.logger = logging.WithRunID(logger, op.runID)

	st, err := store.Open(ctx, cfg.Storage.Root, op.logger)
	if err != nil {
		op.close()
		return nil, err
	}
	op.st = st

	allowlist, err := policy.NewAllowlist(cfg.Registries.Allowlist)
	if err != nil {
		op.close()
		return nil, err
	}
	egress, err := netx.New(&cfg.Network)
	if err != nil {
		op.close()
		return nil, err
	}
	op.egress = egress

	// Trust roots come from THIS instance's configuration and are
	// resolved now, at configuration time (RECIPE-SPEC §12.3) — never
	// from the medium, which has none as far as this code is concerned.
	trustCache := ""
	if cfg.State.Root != "" {
		trustCache = filepath.Join(cfg.State.Root, "trust-cache")
	}
	trust, err := engine.LoadTrust(cfg.Trust, trustCache, egress)
	if err != nil {
		op.close()
		return nil, err
	}
	// The remote access is built without substitutions on purpose: an
	// isolated zone reads nothing upstream (NFR-019), and the media path
	// never fetches — it pushes what the medium already carries.
	remotes, err := engine.NewRemotes(cfg.Registries, allowlist, egress)
	if err != nil {
		op.close()
		return nil, err
	}
	eng := engine.New(st, remotes, trust, cfg.Retriever.Source, cfg.Storage.BasePrefix, cfg.Sync)

	// The freshness register lives in the INSTANCE state directory, never
	// on the medium (R-28): a register carried on the medium would be
	// rewritten by whoever holds it.
	imports, err := media.OpenImports(cfg.State.Root)
	if err != nil {
		op.close()
		return nil, err
	}
	eng.SetMediaImport(cfg.Zone, imports)

	if intent == importMedium {
		dest, dErr := engine.NewDestination(cfg.Destination, cfg.Registries, allowlist, egress)
		if dErr != nil {
			op.close()
			return nil, dErr
		}
		if dest == nil {
			op.close()
			return nil, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
				"detail": "no destination registry is configured (destination.registry, FR-052): " +
					"`tobby media verify` needs none, importing does",
			})
		}
		eng.SetDestination(dest)
	}
	op.eng = eng
	return op, nil
}

// flush forces the medium's log to stable storage (FR-056).
func (o *mediaOp) flush() {
	if o.mlog != nil {
		if err := o.mlog.Sync(); err != nil && o.logger != nil {
			o.logger.LogAttrs(context.Background(), slog.LevelWarn, "flushing the medium's log",
				slog.String("error", err.Error()))
		}
	}
}

// close releases everything openMedia acquired, flushing the medium's log
// last so it carries the closing records too.
func (o *mediaOp) close() {
	if o.st != nil {
		_ = o.st.Close()
	}
	if o.egress != nil {
		o.egress.CloseIdleConnections()
	}
	if o.mlog != nil {
		_ = o.mlog.Close()
	}
}

// writeReport renders a verification report.
func writeReport(cmd *cobra.Command, report *reportFlag, rep *media.Report) error {
	if report.json() {
		return writeJSON(cmd, rep)
	}
	out := report.human(cmd)
	if rep.Media != nil {
		_, _ = fmt.Fprintf(out, "medium %s addressed to zone %s, resolved %s, produced by tobby %s\n",
			rep.Media.MediaID, rep.Media.Zone, rep.Media.ResolvedAt.Format(time.RFC3339), rep.Media.ProducedBy.Version)
	}
	_, _ = fmt.Fprintf(out, "verdict: %s (%d checked files, %d bytes)\n",
		rep.Verdict, rep.Checked.Files, rep.Checked.Bytes)
	for i := range rep.Blocks {
		b := &rep.Blocks[i]
		state := "blocking"
		switch {
		case b.Overridden:
			state = "OVERRIDDEN by an administrator"
		case b.Overridable:
			state = "blocking (an administrator may override it)"
		}
		_, _ = fmt.Fprintf(out, "  %s %s\n", b.Code, state)
	}
	for i := range rep.Recipes {
		v := &rep.Recipes[i]
		if v.Pushable {
			_, _ = fmt.Fprintf(out, "  pushable  %s@%s (%d files, %d bytes)\n", v.Name, v.Version, v.Files, v.Bytes)
			continue
		}
		// The refusal names the file (FR-054 acceptance).
		where := v.Reason.Path
		if where == "" {
			where = v.CookbookRepo
		}
		_, _ = fmt.Fprintf(out, "  BLOCKED   %s@%s — %s at %s\n", v.Name, v.Version, v.Reason.Code, where)
	}
	for i := range rep.Findings {
		_, _ = fmt.Fprintf(out, "  reported  %s %s (never pushed)\n", rep.Findings[i].Code, rep.Findings[i].Path)
	}
	return nil
}

// writeImportReport renders what an import did, from the task the runner
// filled — the same items the UI and the API read (FR-061).
func writeImportReport(cmd *cobra.Command, report *reportFlag, task *tasks.Task) error {
	if report.json() {
		return writeJSON(cmd, task)
	}
	out := report.human(cmd)
	agg := task.Aggregate()
	_, _ = fmt.Fprintf(out, "import %s: %d done, %d skipped, %d failed\n",
		task.Status, agg.Done, agg.Skipped, agg.Failed)
	for i := range task.Items {
		item := &task.Items[i]
		if item.Error == nil {
			_, _ = fmt.Fprintf(out, "  %-8s %s\n", item.Status, item.Name)
			continue
		}
		_, _ = fmt.Fprintf(out, "  %-8s %s — %s\n", item.Status, item.Name, item.Error.Code)
	}
	return nil
}
