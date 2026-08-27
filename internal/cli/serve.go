// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/medialog"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
	"github.com/tobby-fetch/tobby-fetch/internal/tlsadmin"
	"github.com/tobby-fetch/tobby-fetch/internal/ui"
)

func newServeCmd() *cobra.Command {
	flags := &commonFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Tobby instance (HTTP listener, embedded registry)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			return runServe(cmd.Context(), &cfg)
		},
	}
	flags.register(cmd)
	return cmd
}

// serveHook, when non-nil, receives the listener the instance is about
// to run. It exists for the serve tests only: they bind "127.0.0.1:0" —
// a fixed port collides with parallel `go test` runs and with whatever
// already listens on a developer host — and the effective address is
// only knowable from the running server (server.Addr). No production
// caller sets it.
var serveHook func(*server.Server)

func runServe(ctx context.Context, cfg *config.Config) error {
	level, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, level)

	logger.LogAttrs(ctx, slog.LevelInfo, "tobby starting",
		slog.String("version", buildinfo.Version()),
		slog.String("commit", buildinfo.Commit()),
		slog.String("mode", string(cfg.Mode)))

	if cfg.Storage.Root == "" {
		return fmt.Errorf("storage.root is required to serve: set --storage-root, %s, or \"storage.root:\" in the configuration file", config.EnvStorageRoot)
	}
	if err := ensureWritableDir(cfg.Storage.Root); err != nil {
		return fmt.Errorf("storage root: %w", err)
	}

	// Secrets never travel (NFR-020, R-16). The store leaves the site on a
	// medium; a secret configured inside it is a credential handed to
	// whoever plugs the medium in next. Checked here rather than in
	// config.validate because the verdict is a filesystem one — the
	// directories have just been created, so a symlink pointing back into
	// the store resolves for real — and because `tobby config dump` must
	// stay usable on the very configuration being refused.
	if offenders := cfg.SecretsInStore(); len(offenders) > 0 {
		return taxonomy.New(taxonomy.CodeSecretInStore, taxonomy.Params{
			"paths": config.FormatSecretPaths(offenders),
			"root":  cfg.StoreRootResolved(),
		})
	}

	// Authentication state (R-01): without the explicit FR-075 opt-out, the
	// instance refuses to start until a local account exists — no surface is
	// ever exposed open.
	var accounts *auth.Store
	if !cfg.Auth.Disabled {
		if cfg.State.Root == "" {
			return fmt.Errorf("state.root is required to serve: set --state-root, %s, or \"state.root:\" in the configuration file", config.EnvStateRoot)
		}
		accounts, err = auth.Open(cfg.State.Root)
		if err != nil {
			return err
		}
		if !accounts.HasAccounts() {
			return taxonomy.New(taxonomy.CodeNoAccount, nil)
		}
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Duration(cfg.Auth.SessionTTL)),
		Disabled: cfg.Auth.Disabled,
		Logger:   logger,
	}

	reg := metrics.New()
	srv := server.New(cfg.Server.Addr, time.Duration(cfg.Shutdown.GracePeriod), reg, logger)
	if serveHook != nil {
		serveHook(srv)
	}

	// The listener's own certificate (FR-082): the administrator's pair,
	// or a self-signed fallback whose fingerprint is logged — an
	// operator has to be able to compare what the instance presents
	// against what their client saw.
	// Declared as the interface the administration surfaces take, so that
	// a plain-HTTP instance hands them a nil one rather than a non-nil
	// interface wrapping a nil pointer.
	var serverCert tlsadmin.ServerCert
	if cfg.Server.TLS.Serves() {
		cert, cerr := netx.NewServerCert(cfg.Server.TLS, cfg.State.Root)
		if cerr != nil {
			return cerr
		}
		srv.SetTLS(cert.TLSConfig())
		serverCert = cert
		logger.LogAttrs(ctx, slog.LevelInfo, "serving TLS",
			slog.String("certificate", cert.Source()),
			slog.Bool("self_signed", cert.SelfSigned()),
			slog.String("fingerprint_sha256", cert.Fingerprint()),
			slog.String("requirement", "FR-082"))
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// The operation log on the medium (FR-053): in mirror mode the store
	// IS a transport medium, and both sides of a transfer record what
	// they did with it there — the source what it put on, the destination
	// what it made of it. It lives outside the media manifest's coverage
	// (FR-054), which medialog enforces rather than assumes, and it is
	// teed into the instance stream so one schema serves both.
	var mediaLog *medialog.Writer
	if cfg.Mode == config.ModeMirror && !cfg.Logging.Media.Disabled {
		mediaLog, err = medialog.Open(cfg.Storage.Root, medialog.Options{
			Path:     cfg.Logging.Media.File,
			MaxBytes: int64(cfg.Logging.Media.MaxSize),
			Keep:     cfg.Logging.Media.Keep,
		})
		if err != nil {
			return err
		}
		defer func() {
			if cerr := mediaLog.Close(); cerr != nil {
				logger.LogAttrs(context.Background(), slog.LevelError, "closing the medium's log",
					slog.String("error", cerr.Error()))
			}
		}()
		logger = logging.Tee(logger, logging.New(mediaLog, level))
		logger.LogAttrs(ctx, slog.LevelInfo, "operation log written onto the medium",
			slog.String("path", mediaLog.Path()),
			slog.String("requirement", "FR-053/FR-056"))
	}

	st, err := store.Open(ctx, cfg.Storage.Root, logger)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			logger.LogAttrs(context.Background(), slog.LevelError, "closing store",
				slog.String("error", cerr.Error()))
		}
	}()
	// Store occupancy (FR-045 amendment, R-33): a passthrough instance
	// reconciles unattended for months and nothing bounds what its
	// transit store accumulates, so the operator's threshold is watched
	// on its own cadence — the metric moves on every sample, the banner
	// and the API read the latest one. Without a configured threshold the
	// loop never starts and every surface reports "not monitored".
	occupancy := store.NewOccupancyMonitor(st, cfg.Storage.OccupancyThreshold.Bytes(), logger)
	occupancy.Observe(func(o store.Occupancy) {
		reg.StoreBytes.Set(float64(o.Bytes))
		reg.StoreThresholdBytes.Set(float64(o.Threshold))
		exceeded := 0.0
		if o.Exceeded {
			exceeded = 1
		}
		reg.StoreOccupancyExceeded.Set(exceeded)
	})
	reg.StoreThresholdBytes.Set(float64(cfg.Storage.OccupancyThreshold.Bytes()))
	if cfg.Storage.OccupancyThreshold > 0 {
		go occupancy.Run(ctx)
		logger.LogAttrs(ctx, slog.LevelInfo, "store occupancy monitored",
			slog.String("threshold", cfg.Storage.OccupancyThreshold.String()),
			slog.String("requirement", "FR-045"))
	}

	// The FR-054 serving gate. Built here, immediately after the store is
	// open and BEFORE the first content surface is mounted: an instance
	// that opened a transported medium serves nothing off it until a
	// verification has cleared it, and a gate installed later would leave
	// /v2/ unguarded for the length of a startup. It is inert on anything
	// that is not a destination side holding a medium, and its
	// verification is wired in once the engine exists.
	mediaGate := mediagate.Open(ctx, cfg.Storage.Root, cfg.Zone, logger)
	if mediaGate.Guarded() {
		logger.LogAttrs(ctx, slog.LevelWarn, "transported medium not verified: the registry and the file surfaces are closed",
			slog.String("zone", cfg.Zone), slog.String("store", cfg.Storage.Root),
			slog.String("action", "verify the medium on "+mediagate.Screen+" or through POST /api/v1/media/verify: only a verification this instance performs opens its surfaces"),
			slog.String("requirement", "FR-054"))
	}
	srv.SetReadyDetail(mediaGate.ReadyDetail)

	// The embedded registry serves the standard OCI Distribution API on the
	// shared listener (FR-040); nested relocated repository names are
	// first-class (ADR-0013). Reads need viewer, writes need operator
	// (ADR-0009) — docker/helm/oras authenticate with the same accounts and
	// tokens as the UI and the API (FR-076).
	srv.Handle("/v2/", mediaGate.Guard("/v2/", mediagate.RegistryRefusal, authn.Registry(st.APIHandler())))

	// The persistent task queue lives inside the store (FR-050) and
	// re-queues interrupted tasks at startup (FR-029). The unit-import
	// runner writes direct-to-storage (ADR-0005). Finished-task retention
	// (tasks.keepFinished) bounds the history a long-lived instance
	// accumulates — without it, every cycle leaves a task in RAM and two
	// files in the store, forever (2026-08 audit).
	//
	// FR-056: the medium's log is fsync'd at every task boundary, so a
	// yanked medium loses at most the entries of the task in progress.
	queueOpts := []tasks.QueueOption{tasks.WithRetention(cfg.Tasks.KeepFinished)}
	if mediaLog != nil {
		queueOpts = append(queueOpts, tasks.WithBoundary(func() {
			if serr := mediaLog.Sync(); serr != nil {
				logger.LogAttrs(context.Background(), slog.LevelWarn, "flushing the medium's log",
					slog.String("error", serr.Error()))
			}
		}))
	}
	queue, err := tasks.Open(cfg.Storage.Root, logger, queueOpts...)
	if err != nil {
		return err
	}
	// One allowlist for the whole instance (FR-030): every outbound path
	// — unit import, the recipe engine, publication — consults the same
	// object, and refusals are counted once, in one place.
	allowlist, err := policy.NewAllowlist(cfg.Registries.Allowlist)
	if err != nil {
		return err
	}
	allowlist.Observe(func(string) {
		reg.PolicyRejections.WithLabelValues(string(taxonomy.CodeNotAllowlisted)).Inc()
	})
	if !allowlist.Declared() {
		logger.LogAttrs(ctx, slog.LevelInfo, "no registry allowlist configured",
			slog.String("policy", "unrestricted"),
			slog.String("requirement", "FR-030"))
	} else {
		logger.LogAttrs(ctx, slog.LevelInfo, "registry allowlist active",
			slog.Any("registries", allowlist.Patterns()),
			slog.String("requirement", "FR-030"))
	}

	// One outbound transport for the whole instance (FR-080, FR-081):
	// the proxy every fetch path goes through and the private
	// authorities they all trust. Built here, beside the allowlist, and
	// handed to every outbound component — there is deliberately no
	// other place a transport can come from, because a path that built
	// its own would not fail visibly in a zone that blocks direct
	// egress, it would hang.
	egress, err := netx.New(&cfg.Network)
	if err != nil {
		return err
	}
	defer egress.CloseIdleConnections()
	logger.LogAttrs(ctx, slog.LevelInfo, "outbound network configured",
		slog.String("egress", egress.Describe()),
		slog.String("requirement", "FR-080/FR-081"))

	// In-blob resumption of large downloads (FR-029, R-29). One resumer
	// for the instance, on the same shared transport as everything else,
	// spooling into the STATE directory — never the store, which is
	// transportable and must not carry a half-received blob across a zone
	// boundary (R-16). An instance without a state directory, or with the
	// threshold at zero, keeps the plain streaming path.
	resumer := blobfetch.New(egress, nil, cfg.State.Root, cfg.Transfer.ResumeThreshold)
	if resumer.Threshold() > 0 {
		logger.LogAttrs(ctx, slog.LevelInfo, "large transfers are resumable",
			slog.String("threshold", cfg.Transfer.ResumeThreshold.String()),
			slog.String("partials", filepath.Join(cfg.State.Root, "partials")),
			slog.String("requirement", "FR-029"))
	} else {
		logger.LogAttrs(ctx, slog.LevelInfo, "large transfers are not resumable",
			slog.String("reason", resumeDisabledReason(cfg)),
			slog.String("requirement", "FR-029"))
	}

	// The source policy every surface that can start an import runs
	// under — the runner, the API and the UI. Built here rather than at
	// each call site: that is what makes forgetting it impossible.
	importPolicy := importer.WithSourcePolicy(cfg.Registries, allowlist, egress)
	queue.Register(tasks.TypeUnitImport, importer.NewRunner(st, importPolicy, importer.WithResume(resumer)))

	// The recipe engine (milestone 3): substitution-aware remote access
	// (FR-036), trust roots resolved at configuration time (FR-033,
	// RECIPE-SPEC §12.3 — the cache lives in the state directory, never on
	// the transportable store), and the sync task runner (FR-014).
	remotes, err := engine.NewRemotes(cfg.Registries, allowlist, egress)
	if err != nil {
		return err
	}
	// The engine resumes through the same object as the import path, but
	// with the registry keychain attached: a synchronization reads private
	// cookbooks, and a resumed blob must authenticate exactly like the
	// manifest that named it (FR-004).
	remotes.SetResumer(blobfetch.New(egress, remotes.Keychain(), cfg.State.Root, cfg.Transfer.ResumeThreshold))
	trustCache := ""
	if cfg.State.Root != "" {
		trustCache = filepath.Join(cfg.State.Root, "trust-cache")
	}
	trust, err := engine.LoadTrust(cfg.Trust, trustCache, egress)
	if err != nil {
		return err
	}
	eng := engine.New(st, remotes, trust, cfg.Retriever.Source, cfg.Storage.BasePrefix, cfg.Sync)
	// The FR-055 pre-flight: the safety margin, and the explicit opt-out
	// that turns the gate into a report. The opt-out is announced at
	// startup like every other removed safety (FR-075) — an instance that
	// will start a transfer it cannot finish must say so before it does,
	// not in the task that fails.
	eng.SetPreflight(string(cfg.Mode), cfg.Preflight)
	if cfg.Preflight.Disabled {
		logger.LogAttrs(ctx, slog.LevelWarn, "pre-flight gate disabled",
			slog.String("setting", "preflight.disabled"),
			slog.String("effect", "volumes and filesystem verdicts are still reported; they no longer refuse a synchronization"),
			slog.String("requirement", "FR-055/FR-075"))
	}
	eng.SetMeters(engine.Meters{
		TransferStarted: reg.SyncInflight.Inc,
		TransferDone:    reg.SyncInflight.Dec,
		BytesMoved:      func(n int64) { reg.SyncBytes.Add(float64(n)) },
		PushDone:        func() { reg.PromotionPushes.WithLabelValues(metrics.ResultPushed).Inc() },
		PushSkipped:     func() { reg.PromotionPushes.WithLabelValues(metrics.ResultSkipped).Inc() },
		PushedBytes:     func(n int64) { reg.PromotionBytes.Add(float64(n)) },
		PushRefused:     func(code string) { reg.PromotionRefusals.WithLabelValues(code).Inc() },
	})

	// The promotion target (milestone 4, FR-013): built from the same
	// allowlist and the same outbound transport as every other path, and
	// deliberately not from the substitution-aware reading side — a
	// promotion goes exactly where destination.registry names.
	destination, err := engine.NewDestination(cfg.Destination, cfg.Registries, allowlist, egress)
	if err != nil {
		return err
	}
	// The publishing side (R-36, R-40): the same credentials, the same
	// allowlist and the same outbound transport as everything else, and
	// deliberately not the substitution-aware reading side — a
	// publication goes exactly where its reference says.
	publisher, err := engine.NewPublisher(cfg.Registries, allowlist, egress)
	if err != nil {
		return err
	}
	// The media manifest (FR-054): mirror mode, and only mirror mode,
	// ends each synchronization by inventorying the store it just
	// produced — that store is what crosses the air gap, and the manifest
	// is what the destination side verifies it against. A passthrough
	// store is a cache, not a medium, and gets no manifest.
	if cfg.Mode == config.ModeMirror {
		eng.SetMediaManifest(func(ctx context.Context, zone, runID string, resolvedAt time.Time) error {
			m, err := media.Write(ctx, st, media.WriteOptions{
				Zone: zone, RunID: runID, ResolvedAt: resolvedAt,
			})
			if err != nil {
				return err
			}
			// R-28: the medium's identity travels in the operation logs of
			// both sides, so an incident traces back to a physical object.
			logger.LogAttrs(ctx, slog.LevelInfo, "media manifest written",
				slog.String("media_id", m.MediaID), slog.String("zone", m.Zone),
				slog.String("run_id", runID),
				slog.Int("files", m.Totals.Files), slog.Int64("bytes", m.Totals.Bytes))
			return nil
		})
	}

	// Prune to the Retriever (FR-045). The two modes default differently
	// and both defaults are deliberate: a mirror store IS the delivery
	// unit and its operator stands in front of the trigger, sees the list
	// and the total size, and decides — so prune is on. A passthrough
	// transit store is nobody's delivery unit and nobody watches the
	// loop, so it prunes only when sync.prune says to.
	//
	// Either way it is stated on three channels at startup — the log, the
	// audit trail, and the retriever screen with its API mirror — because
	// an instance that deletes content is a posture, not a detail.
	prunesByDefault := pruneDefaultFor(cfg.Mode, cfg.Sync.Prune)
	eng.SetPruneDefault(prunesByDefault)
	logger.LogAttrs(ctx, slog.LevelInfo, "prune to the Retriever",
		slog.Bool("default", prunesByDefault),
		slog.String("decided", pruneDecisionSite(cfg.Mode)),
		slog.String("protected", "unit imports (FR-023), the offline vulnerability database (FR-032), and content seeded through /v2/ (UC3)"),
		slog.String("requirement", "FR-045"))
	// The destination side of a physical transfer (FR-052, feature 5.4):
	// this instance's zone identity, and the per-zone freshness register
	// (R-28) — which lives in the INSTANCE state directory and never on
	// the medium, because a register carried on the medium would be
	// rewritten by whoever holds it.
	imports, err := media.OpenImports(cfg.State.Root)
	if err != nil {
		return err
	}
	eng.SetMediaImport(cfg.Zone, imports)
	// FR-054: every verification this engine performs reaches the serving
	// gate, and the gate's own Verify step is this engine. One funnel in
	// each direction, so no surface can reach a verdict the gate does not
	// hear, and no screen can open the gate by any other means.
	eng.SetMediaVerdicts(mediaGate.Observe)
	mediaGate.SetVerify(func(vctx context.Context, opts mediagate.Options, progress func(media.Progress)) (*media.Report, error) {
		return eng.VerifyMedia(vctx, logger, engine.MediaOptions{
			AllowZoneMismatch: opts.AllowZoneMismatch,
			AllowStale:        opts.AllowStale,
			Progress:          progress,
		})
	})
	switch {
	case cfg.Zone == "":
		logger.LogAttrs(ctx, slog.LevelInfo, "no zone identity configured: this instance imports no medium",
			slog.String("requirement", "FR-052"))
	case cfg.State.Root == "":
		// FR-075 posture: a guard that cannot persist is announced, never
		// silently absent. Without a state directory the freshness record
		// evaporates on restart, and the R-28 guard with it.
		logger.LogAttrs(ctx, slog.LevelWarn, "zone configured without a state directory: the media freshness guard will not persist",
			slog.String("zone", cfg.Zone), slog.String("requirement", "R-28"))
	default:
		logger.LogAttrs(ctx, slog.LevelInfo, "zone identity configured",
			slog.String("zone", cfg.Zone),
			slog.Any("last_imports", imports.Zones()),
			slog.String("requirement", "FR-052/R-28"))
	}
	queue.Register(tasks.TypeMediaImport, eng.MediaImportRunner())
	eng.SetDestination(destination)
	if destination != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, "promotion destination configured",
			slog.String("registry", destination.Host()),
			slog.String("cookbook", destination.Cookbook()),
			slog.String("requirement", "FR-013/FR-034"))
	}

	// The FileSet HTTP surface (FR-047): explicitly enabled FileSets are
	// extracted (RECIPE-SPEC §7.4/§14.5) into a store-local cache and
	// served read-only under /files/. Refreshed after every sync.
	fsrv := fileserve.NewServer(storeBlobs{st: st}, filepath.Join(cfg.Storage.Root, "meta", "fileserve"), fileserve.Limits{}, logger)
	refreshFileSets := func(runCtx context.Context) {
		syncFileSets(runCtx, fsrv, st, cfg, logger)
	}
	refreshFileSets(ctx)
	queue.Register(tasks.TypeSync, func(runCtx context.Context, t *tasks.Task, taskLogger *slog.Logger, save func()) error {
		err := eng.Runner()(runCtx, t, taskLogger, save)
		refreshFileSets(runCtx)
		// A cycle is the one moment the footprint moves by a lot — it
		// fetched, and it may have pruned. Re-sampling here is what makes
		// the warning retract when the store comes back under the
		// threshold, instead of at the next tick (R-33).
		occupancy.Refresh(runCtx)
		return err
	})
	queue.Start(ctx)

	// The reconciliation cadence (FR-013). The scheduler exists in
	// passthrough mode only: FR-014 requires mirror-mode synchronization
	// to be triggered manually and forbids it running unattended, so a
	// mirror instance has no loop to disable — it has none at all.
	interval, err := schedule.Open(cfg.State.Root, time.Duration(cfg.Sync.Interval))
	if err != nil {
		return err
	}
	if cfg.Mode == config.ModePassthrough && cfg.Retriever.Source != "" {
		sched := schedule.NewScheduler(interval, syncTrigger(queue, cfg.Retriever.Source, cfg.Sync.Prune, logger), logger)
		go sched.Run(ctx)
		logger.LogAttrs(ctx, slog.LevelInfo, "promotion scheduler started",
			slog.Duration("interval", interval.Effective()),
			slog.Bool("enabled", interval.Effective() > 0),
			slog.Bool("overridden", interval.Overridden()),
			slog.String("requirement", "FR-013"))
	} else {
		// Say why rather than leave an operator to infer it from a loop
		// that never fires: in mirror mode this is a requirement, and
		// without a Retriever source there is nothing to reconcile.
		interval = nil
		logger.LogAttrs(ctx, slog.LevelInfo, "no promotion scheduler",
			slog.String("mode", string(cfg.Mode)),
			slog.Bool("retriever_configured", cfg.Retriever.Source != ""),
			slog.String("requirement", "FR-013/FR-014"))
	}

	anonymous := map[string]bool{}
	var anonymousNames []string
	for _, f := range cfg.Files.FileSets {
		if f.Anonymous {
			anonymous[f.Name] = true
			anonymousNames = append(anonymousNames, f.Name)
		}
	}
	srv.Handle(fileserve.RoutePrefix, mediaGate.Guard(fileserve.RoutePrefix, mediagate.FilesRefusal,
		filesAuth(authn, anonymous, fsrv.Handler())))

	// The FileSets surface (FR-047 inventory, FR-048 packing), shared by
	// the screen and the endpoints so the two give one answer (FR-061).
	// The packer is confined to files.packRoots: reaching a directory of
	// the host over the network takes a configuration entry, not just an
	// administrator session, and no entry means no directory (FR-075).
	// `tobby fileset pack` on the host builds its own, unconfined.
	fileSets := &fileserve.Surface{
		Catalog: storeCatalog{st: st},
		Packer: fileserve.NewPacker(st, cfg.Storage.BasePrefix, logger,
			fileserve.WithPackRoots(cfg.Files.PackRoots)),
		Served:      fsrv.Enabled,
		Declared:    declaredFileSets(cfg),
		BasePrefix:  cfg.Storage.BasePrefix,
		PackEnabled: len(cfg.Files.PackRoots) > 0,
	}

	// The versioned REST API (FR-060), strict UI parity (FR-061). Content
	// browsing reads the store through its accessors (FR-062), never the
	// HTTP loopback.
	// Interoperability (FR-051) and the store reset (FR-046): one service
	// behind the API, the UI, and the queue, so the confirmation rule and
	// the selection rules cannot drift between surfaces (FR-061).
	interopSvc := interop.New(st, queue, cfg.Storage.BasePrefix, logger)
	interopSvc.Register()

	restAPI := api.New(authn, logger)
	api.RegisterContent(restAPI, st, occupancy)
	api.RegisterTasks(restAPI, queue, st, time.Duration(cfg.Import.InspectTimeout), importPolicy)
	api.RegisterAccounts(restAPI, accounts)
	api.RegisterRecipes(restAPI, &api.RecipeOptions{
		Store: st, Queue: queue,
		Source:            cfg.Retriever.Source,
		RelaxedScopes:     eng.RelaxedScopes(),
		AnonymousFileSets: anonymousNames,
		Destination:       destination.Host(),
		Cookbook:          destination.Cookbook(),
		Interval:          interval,
		Prune:             eng.PrunesByDefault(),
		Projector:         eng,
		Occupancy:         occupancy,
	})
	api.RegisterPublish(restAPI, publisher)
	api.RegisterPlan(restAPI, &api.PlanOptions{Planner: eng.Planner()})
	api.RegisterMedia(restAPI, &api.MediaOptions{
		Engine: eng, Queue: queue, StorageRoot: cfg.Storage.Root, Gate: mediaGate,
	})
	api.RegisterNetwork(restAPI, &api.NetworkOptions{
		Cert:     serverCert,
		CertFile: cfg.Server.TLS.CertFile,
		KeyFile:  cfg.Server.TLS.KeyFile,
		Egress:   egress,
	})
	api.RegisterOCILayout(restAPI, interopSvc, queue)
	api.RegisterFileSets(restAPI, fileSets)
	api.RegisterOpenAPI(restAPI)
	srv.Handle("/api/v1/", restAPI.Handler())

	// The web UI owns the root of the listener (ADR-0015): server-rendered,
	// bilingual, never exposed open (R-01).
	webUI := ui.New(authn, logger, &ui.Options{
		Version:            buildinfo.Version(),
		Mode:               string(cfg.Mode),
		ThemeOverride:      cfg.UI.ThemeOverride,
		ShowUpcoming:       cfg.UI.ShowUpcoming,
		SecureCookies:      cfg.Server.SecureCookies,
		Store:              st,
		Queue:              queue,
		InspectTimeout:     time.Duration(cfg.Import.InspectTimeout),
		ImportPolicy:       importPolicy,
		Allowlist:          allowlist,
		RetrieverSource:    cfg.Retriever.Source,
		RelaxedTrustScopes: eng.RelaxedScopes(),
		AnonymousFileSets:  anonymousNames,
		Destination:        destination.Host(),
		Cookbook:           destination.Cookbook(),
		Interval:           interval,
		Publisher:          publisher,
		Planner:            eng.Planner(),
		FileSets:           fileSets,
		ServerCert:         serverCert,
		Egress:             egress,
		Interop:            interopSvc,
		Occupancy:          occupancy,
		PrunesToRetriever:  eng.PrunesByDefault(),
		Projector:          eng,
		MediaZone:          eng,
		MediaGate:          mediaGate,
	})
	webUI.Mount(srv.Mux())

	if cfg.Auth.Disabled {
		// The FR-075 override is loud on every channel: audit record at
		// startup here, permanent UI banner, log line. Never silent.
		audit.Log(ctx, logger, &audit.Event{
			Actor:   audit.ActorLocal,
			Action:  audit.ActionAuthOverride,
			Target:  "instance",
			Outcome: audit.OutcomeSuccess,
			Origin:  audit.OriginLocal,
		})
	}

	if cfg.Sync.Prune {
		audit.Log(ctx, logger, &audit.Event{
			Actor:   audit.ActorLocal,
			Action:  audit.ActionPruneActive,
			Target:  cfg.Retriever.Source,
			Outcome: audit.OutcomeSuccess,
			Origin:  audit.OriginLocal,
		})
	}

	audit.Log(ctx, logger, &audit.Event{
		Actor:   audit.ActorLocal,
		Action:  audit.ActionInstanceStart,
		Target:  string(cfg.Mode),
		Outcome: audit.OutcomeSuccess,
		Origin:  audit.OriginLocal,
	})

	// Storage checked and configuration valid: the instance can serve
	// (FR-092 readiness conditions at this milestone).
	srv.SetReady(true)

	runErr := srv.Run(ctx)

	// The HTTP listener has drained; the task worker has not necessarily.
	// Waiting for it is what makes "the instance stopped" true of the
	// STORE and not only of the socket: until the worker returns, the
	// running task's own log file is still open under the storage root
	// (FR-053). On Unix nobody would notice — a file can be removed or a
	// directory renamed out from under an open handle. On the mirror
	// workstation NFR-018 puts in scope, that open handle is exactly what
	// tells an operator the medium they are trying to eject is in use
	// (B-026).
	//
	// Bounded by the same grace period the listener drains under, and for
	// the same reason: a runner that does not honour cancellation must
	// delay a shutdown, never prevent one. Expiring is reported, because
	// an operator whose eject then fails deserves to have been told which
	// task was still holding the store.
	waitForWorker(ctx, queue, time.Duration(cfg.Shutdown.GracePeriod), logger)

	// And the FileSet cache, for the same reason and with more force: it
	// lives at <storage.root>/meta/fileserve, INSIDE the transportable
	// store, and the server holds one directory handle per served FileSet
	// for as long as it exists. On Windows an open handle is what makes a
	// volume refuse to unmount — so an instance that had served a single
	// FileSet left the operator unable to eject the medium they had just
	// shut it down to carry away (B-024, NFR-018, FR-050).
	if err := fsrv.Close(); err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "the fileset cache was not fully released",
			slog.String("error", err.Error()))
	}

	outcome := audit.OutcomeSuccess
	if runErr != nil {
		outcome = audit.OutcomeFailure
	}
	audit.Log(context.Background(), logger, &audit.Event{
		Actor:   audit.ActorLocal,
		Action:  audit.ActionInstanceStop,
		Target:  string(cfg.Mode),
		Outcome: outcome,
		Origin:  audit.OriginLocal,
	})
	return runErr
}

// waitForWorker blocks until the task worker has returned, or until the
// grace period expires. See its call site for why the wait exists at all.
func waitForWorker(ctx context.Context, queue *tasks.Queue, grace time.Duration, logger *slog.Logger) {
	if grace <= 0 {
		grace = 30 * time.Second
	}
	done := make(chan struct{})
	go func() {
		queue.Wait()
		close(done)
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		logger.LogAttrs(ctx, slog.LevelWarn,
			"the task worker did not stop within the grace period",
			slog.Duration("grace_period", grace),
			slog.String("consequence", "a task log under the storage root may still be open"),
			slog.String("requirement", "FR-093"))
	}
}

// syncTrigger builds the scheduler's cycle trigger: enqueue one sync of
// the configured source — unless one is already pending. The queue's
// worker is serial, so once a cycle outlasts the interval the old
// unconditional Create piled an identical task onto the queue at every
// tick (2026-08 audit), a backlog that only ever grew. Skipping loses
// nothing: the pending task reads the latest desired state when it
// finally runs. The skip is logged so an operator watching cycle times
// drift past sync.interval sees the coalescence happen (FR-013).
func syncTrigger(queue *tasks.Queue, source string, prune bool, logger *slog.Logger) schedule.Trigger {
	return func(ctx context.Context) error {
		if queue.HasPending(tasks.TypeSync) {
			logger.LogAttrs(ctx, slog.LevelInfo,
				"promotion cycle skipped: a synchronization is already pending",
				slog.String("source", source),
				slog.String("requirement", "FR-013"))
			return nil
		}
		// The scheduled cycle carries the configured decision explicitly:
		// nothing about an unattended run may depend on a default read
		// somewhere else at the moment it happens (FR-045).
		_, err := queue.Create(tasks.TypeSync, source, audit.ActorLocal, nil, tasks.WithPrune(prune))
		return err
	}
}

// declaredFileSets flattens the files.filesets configuration for the
// FR-047/FR-048 inventory.
func declaredFileSets(cfg *config.Config) []fileserve.Declared {
	out := make([]fileserve.Declared, 0, len(cfg.Files.FileSets))
	for _, f := range cfg.Files.FileSets {
		out = append(out, fileserve.Declared{
			Name: f.Name, Ref: f.Ref, Version: f.Version, Anonymous: f.Anonymous,
		})
	}
	return out
}

// storeCatalog adapts the embedded store to the inventory read surface.
// The provenance is flattened here rather than in fileserve, which stays
// free of the store's types.
type storeCatalog struct{ st *store.Store }

func (c storeCatalog) Repositories(ctx context.Context) ([]string, error) {
	return c.st.Repositories(ctx)
}

func (c storeCatalog) Tags(ctx context.Context, repo string) ([]string, error) {
	return c.st.Tags(ctx, repo)
}

// Provenance mirrors the rule the content screens apply (ui/content.go):
// the live recipe graph wins over the recorded ledger, and content with
// no record at all was pushed through /v2/ by a standard client.
func (c storeCatalog) Provenance(repo string) string {
	if len(c.st.ManagingRecipes(repo)) > 0 {
		return fileserve.FromRecipe
	}
	p, ok := c.st.ProvenanceOf(repo)
	if !ok {
		return fileserve.FromSeed
	}
	switch {
	case p.Class == store.ProvenanceRecipe:
		return fileserve.FromRecipe
	case p.Origin == store.OriginLocalPack:
		return fileserve.FromManualImport
	case p.Class == store.ProvenanceUnitImport:
		return fileserve.FromUnitImport
	default:
		return fileserve.FromSeed
	}
}

// pruneDefaultFor answers what an unqualified synchronization does about
// content the resolved Retriever no longer references (FR-045).
//
// Mirror mode prunes: the store IS the delivery unit, so a medium that
// still carries what the zone stopped asking for is a medium that lies
// about what it delivers — and the operator triggering it is standing
// right there, shown the list and the total size before confirming.
//
// Passthrough mode takes sync.prune, which is off: a transit store is
// nobody's delivery unit, nobody is watching the loop, and a store that
// quietly shrinks between two cycles is one the zone below discovers has
// lost content.
func pruneDefaultFor(mode config.Mode, configured bool) bool {
	if mode == config.ModeMirror {
		return true
	}
	return configured
}

// pruneDecisionSite names WHERE the prune decision is made, so the
// startup line says more than a boolean: in mirror mode an operator
// confirms it per run against the list and the total size (FR-045), in
// passthrough mode the configuration decides once and the loop obeys.
func pruneDecisionSite(mode config.Mode) string {
	if mode == config.ModeMirror {
		return "at trigger time, against the projected list and total size"
	}
	return "by sync.prune, applied at every reconciliation cycle"
}

// storeBlobs adapts the embedded store to the fileserve read surface.
type storeBlobs struct{ st *store.Store }

func (b storeBlobs) Manifest(ctx context.Context, repo, dgst string) ([]byte, error) {
	payload, _, _, err := b.st.RawManifest(ctx, repo, dgst)
	return payload, err
}

func (b storeBlobs) Blob(ctx context.Context, repo, dgst string) (io.ReadCloser, error) {
	return b.st.BlobReader(ctx, repo, dgst)
}

// filesAuth gates the /files/ surface (FR-047): reads need viewer, except
// FileSets explicitly opted into anonymous access (bare-host bootstrap,
// reported like the FR-075 override). Basic auth so apt/dnf URL
// credentials work; the surface is read-only by construction. Like every
// credential-verifying surface, an origin over its failure budget is
// answered 429 BEFORE anything is verified (v0.4.2 hardening): apt and
// dnf re-present the credential per file, and each failed check costs an
// argon2id computation.
func filesAuth(a *auth.Authenticator, anonymous map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, fileserve.RoutePrefix)
		name, _, _ := strings.Cut(rest, "/")
		if anonymous[name] {
			next.ServeHTTP(w, r)
			return
		}
		presented := r.Header.Get("Authorization") != ""
		origin := auth.ClientOrigin(r)
		if presented && !a.FailureAllowed(origin) {
			w.Header().Set("Retry-After", auth.RetryAfter)
			http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
			return
		}
		id, ok := a.Authenticate(r)
		if !ok {
			if presented {
				a.RecordFailure(origin)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="tobby"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if !id.Role.AtLeast(auth.RoleViewer) {
			http.Error(w, "insufficient role", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}

// syncFileSets resolves the declared FileSets and hands what resolved to
// the server.
//
// A FileSet that did not resolve is reported and skipped, never fatal to
// the others: resolveFileSets already documents that content which has
// not arrived yet is not an instance failure, but the caller used to
// abandon the whole refresh on the joined error — so one declaration
// waiting for its recipe kept every other FileSet, packed ones included,
// out of /files/. Found by running the FR-048 flow end to end against a
// configuration declaring both a packed FileSet and one still to come.
func syncFileSets(ctx context.Context, fsrv *fileserve.Server, st *store.Store, cfg *config.Config, logger *slog.Logger) {
	sets, err := resolveFileSets(ctx, st, cfg)
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "fileset resolution failed",
			slog.Int("resolved", len(sets)),
			slog.String("error", err.Error()))
	}
	if err := fsrv.Sync(ctx, sets); err != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "fileset extraction failed",
			slog.String("error", err.Error()))
	}
}

// resolveFileSets maps the files.filesets configuration onto concrete
// store content: each enabled FileSet resolves to one image manifest —
// pinned version or highest local semver tag, platform selected on an
// index (FR-047).
func resolveFileSets(ctx context.Context, st *store.Store, cfg *config.Config) ([]fileserve.FileSet, error) {
	var sets []fileserve.FileSet
	var errs []error
	for _, f := range cfg.Files.FileSets {
		repo, err := relocate.PathWithBase(cfg.Storage.BasePrefix, f.Ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("fileset %s: %w", f.Name, err))
			continue
		}
		set, err := resolveFileSet(ctx, st, f, repo)
		if err != nil {
			// A FileSet whose content has not arrived yet is not an
			// instance failure: it starts serving after the sync lands.
			errs = append(errs, fmt.Errorf("fileset %s: %w", f.Name, err))
			continue
		}
		sets = append(sets, set)
	}
	return sets, errors.Join(errs...)
}

func resolveFileSet(ctx context.Context, st *store.Store, f config.FileSetServe, repo string) (fileserve.FileSet, error) {
	tag := f.Version
	if tag == "" {
		tags, err := st.Tags(ctx, repo)
		if err != nil {
			return fileserve.FileSet{}, err
		}
		versions := make([]string, 0, len(tags))
		for _, t := range tags {
			if !strings.HasPrefix(t, "sha256-") {
				versions = append(versions, t)
			}
		}
		tag, err = engine.ResolveVersion("*", versions)
		if err != nil {
			return fileserve.FileSet{}, err
		}
	}
	payload, mediaType, dgst, err := st.RawManifest(ctx, repo, tag)
	if err != nil {
		return fileserve.FileSet{}, err
	}
	dgst, err = selectFileSetManifest(ctx, st, repo, payload, mediaType, dgst, f.Platform)
	if err != nil {
		return fileserve.FileSet{}, err
	}
	return fileserve.FileSet{Name: f.Name, Repo: repo, ManifestDigest: dgst, Anonymous: f.Anonymous}, nil
}

// selectFileSetManifest picks the platform manifest of an index (§7.4
// step 1): the configured platform, or the single entry of a
// single-manifest FileSet; an ambiguous index without a configured
// platform is refused.
func selectFileSetManifest(ctx context.Context, st *store.Store, repo string, payload []byte, mediaType, dgst, platform string) (string, error) {
	if !strings.Contains(mediaType, "index") && !strings.Contains(mediaType, "manifest.list") {
		return dgst, nil
	}
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				OS      string `json:"os"`
				Arch    string `json:"architecture"`
				Variant string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(payload, &idx); err != nil {
		return "", fmt.Errorf("parsing index: %w", err)
	}
	var candidates []string
	for _, m := range idx.Manifests {
		if m.Platform == nil || m.Platform.OS == "unknown" {
			continue
		}
		if platform == "" {
			candidates = append(candidates, m.Digest)
			continue
		}
		// Same optional-variant rule as an ingredient's platforms list —
		// same notation, same matcher (B-020): a configured
		// "linux/arm64" must find the index child the registry describes
		// as linux/arm64 variant v8.
		if importer.MatchesPlatform(platform, m.Platform.OS, m.Platform.Arch, m.Platform.Variant) {
			if !st.HasManifest(ctx, repo, m.Digest) {
				return "", fmt.Errorf("platform %s of %s is not present locally (sparse index)", platform, repo)
			}
			return m.Digest, nil
		}
	}
	if platform != "" {
		return "", fmt.Errorf("platform %s not found in the index of %s", platform, repo)
	}
	if len(candidates) == 1 {
		if !st.HasManifest(ctx, repo, candidates[0]) {
			return "", fmt.Errorf("the single platform of %s is not present locally", repo)
		}
		return candidates[0], nil
	}
	return "", fmt.Errorf("multi-platform FileSet %s needs files.filesets[].platform to pick one", repo)
}

// ensureWritableDir verifies the storage root exists (creating it if
// needed) and is writable — the readiness precondition of FR-092.
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	probe, err := os.CreateTemp(dir, ".tobby-write-probe-*")
	if err != nil {
		return fmt.Errorf("directory %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return err
	}
	return os.Remove(filepath.Clean(name))
}

// resumeDisabledReason says WHY an instance does not resume large
// transfers, because the two reasons have different fixes and an operator
// staring at a restarted 6 GB layer should not have to guess which one
// applies (FR-029).
func resumeDisabledReason(cfg *config.Config) string {
	if cfg.Transfer.ResumeThreshold <= 0 {
		return "transfer.resumeThreshold is 0"
	}
	return "no state.root is configured, and partial downloads never live in the transportable store"
}
