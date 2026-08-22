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
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
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
	// The embedded registry serves the standard OCI Distribution API on the
	// shared listener (FR-040); nested relocated repository names are
	// first-class (ADR-0013). Reads need viewer, writes need operator
	// (ADR-0009) — docker/helm/oras authenticate with the same accounts and
	// tokens as the UI and the API (FR-076).
	srv.Handle("/v2/", authn.Registry(st.APIHandler()))

	// The persistent task queue lives inside the store (FR-050) and
	// re-queues interrupted tasks at startup (FR-029). The unit-import
	// runner writes direct-to-storage (ADR-0005). Finished-task retention
	// (tasks.keepFinished) bounds the history a long-lived instance
	// accumulates — without it, every cycle leaves a task in RAM and two
	// files in the store, forever (2026-08 audit).
	queue, err := tasks.Open(cfg.Storage.Root, logger, tasks.WithRetention(cfg.Tasks.KeepFinished))
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
		sets, err := resolveFileSets(runCtx, st, cfg)
		if err != nil {
			logger.LogAttrs(runCtx, slog.LevelWarn, "fileset resolution failed",
				slog.String("error", err.Error()))
			return
		}
		if err := fsrv.Sync(runCtx, sets); err != nil {
			logger.LogAttrs(runCtx, slog.LevelWarn, "fileset extraction failed",
				slog.String("error", err.Error()))
		}
	}
	refreshFileSets(ctx)
	queue.Register(tasks.TypeSync, func(runCtx context.Context, t *tasks.Task, taskLogger *slog.Logger, save func()) error {
		err := eng.Runner()(runCtx, t, taskLogger, save)
		refreshFileSets(runCtx)
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
		sched := schedule.NewScheduler(interval, syncTrigger(queue, cfg.Retriever.Source, logger), logger)
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
	srv.Handle(fileserve.RoutePrefix, filesAuth(authn, anonymous, fsrv.Handler()))

	// The versioned REST API (FR-060), strict UI parity (FR-061). Content
	// browsing reads the store through its accessors (FR-062), never the
	// HTTP loopback.
	restAPI := api.New(authn, logger)
	api.RegisterContent(restAPI, st)
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
	})
	api.RegisterPublish(restAPI, publisher)
	api.RegisterNetwork(restAPI, &api.NetworkOptions{
		Cert:     serverCert,
		CertFile: cfg.Server.TLS.CertFile,
		KeyFile:  cfg.Server.TLS.KeyFile,
		Egress:   egress,
	})
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
		ServerCert:         serverCert,
		Egress:             egress,
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

// syncTrigger builds the scheduler's cycle trigger: enqueue one sync of
// the configured source — unless one is already pending. The queue's
// worker is serial, so once a cycle outlasts the interval the old
// unconditional Create piled an identical task onto the queue at every
// tick (2026-08 audit), a backlog that only ever grew. Skipping loses
// nothing: the pending task reads the latest desired state when it
// finally runs. The skip is logged so an operator watching cycle times
// drift past sync.interval sees the coalescence happen (FR-013).
func syncTrigger(queue *tasks.Queue, source string, logger *slog.Logger) schedule.Trigger {
	return func(ctx context.Context) error {
		if queue.HasPending(tasks.TypeSync) {
			logger.LogAttrs(ctx, slog.LevelInfo,
				"promotion cycle skipped: a synchronization is already pending",
				slog.String("source", source),
				slog.String("requirement", "FR-013"))
			return nil
		}
		_, err := queue.Create(tasks.TypeSync, source, audit.ActorLocal, nil)
		return err
	}
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
		label := m.Platform.OS + "/" + m.Platform.Arch
		if m.Platform.Variant != "" {
			label += "/" + m.Platform.Variant
		}
		if platform == "" {
			candidates = append(candidates, m.Digest)
			continue
		}
		if label == platform {
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
