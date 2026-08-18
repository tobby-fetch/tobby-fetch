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
	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
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
	// runner writes direct-to-storage (ADR-0005).
	queue, err := tasks.Open(cfg.Storage.Root, logger)
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

	// The source policy every surface that can start an import runs
	// under — the runner, the API and the UI. Built here rather than at
	// each call site: that is what makes forgetting it impossible.
	importPolicy := importer.WithSourcePolicy(cfg.Registries, allowlist)
	queue.Register(tasks.TypeUnitImport, importer.NewRunner(st, importPolicy))

	// The recipe engine (milestone 3): substitution-aware remote access
	// (FR-036), trust roots resolved at configuration time (FR-033,
	// RECIPE-SPEC §12.3 — the cache lives in the state directory, never on
	// the transportable store), and the sync task runner (FR-014).
	remotes, err := engine.NewRemotes(cfg.Registries, allowlist)
	if err != nil {
		return err
	}
	trustCache := ""
	if cfg.State.Root != "" {
		trustCache = filepath.Join(cfg.State.Root, "trust-cache")
	}
	trust, err := engine.LoadTrust(cfg.Trust, trustCache, nil)
	if err != nil {
		return err
	}
	eng := engine.New(st, remotes, trust, cfg.Retriever.Source, cfg.Storage.BasePrefix, cfg.Sync)
	eng.SetMeters(engine.Meters{
		TransferStarted: reg.SyncInflight.Inc,
		TransferDone:    reg.SyncInflight.Dec,
		BytesMoved:      func(n int64) { reg.SyncBytes.Add(float64(n)) },
	})

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
	api.RegisterRecipes(restAPI, st, queue, cfg.Retriever.Source, eng.RelaxedScopes(), anonymousNames)
	api.RegisterOpenAPI(restAPI)
	srv.Handle("/api/v1/", restAPI.Handler())

	// The web UI owns the root of the listener (ADR-0015): server-rendered,
	// bilingual, never exposed open (R-01).
	webUI := ui.New(authn, logger, &ui.Options{
		Version:            buildinfo.Version(),
		Mode:               string(cfg.Mode),
		ThemeOverride:      cfg.UI.ThemeOverride,
		ShowUpcoming:       cfg.UI.ShowUpcoming,
		Store:              st,
		Queue:              queue,
		InspectTimeout:     time.Duration(cfg.Import.InspectTimeout),
		ImportPolicy:       importPolicy,
		Allowlist:          allowlist,
		RetrieverSource:    cfg.Retriever.Source,
		RelaxedTrustScopes: eng.RelaxedScopes(),
		AnonymousFileSets:  anonymousNames,
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
// credentials work; the surface is read-only by construction.
func filesAuth(a *auth.Authenticator, anonymous map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, fileserve.RoutePrefix)
		name, _, _ := strings.Cut(rest, "/")
		if anonymous[name] {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := a.Authenticate(r)
		if !ok {
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
