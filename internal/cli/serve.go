// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
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
	queue.Register(tasks.TypeUnitImport, importer.NewRunner(st, importer.WithInsecureHosts(cfg.Registries.Insecure)))
	queue.Start(ctx)

	// The versioned REST API (FR-060), strict UI parity (FR-061). Content
	// browsing reads the store through its accessors (FR-062), never the
	// HTTP loopback.
	restAPI := api.New(authn, logger)
	api.RegisterContent(restAPI, st)
	api.RegisterTasks(restAPI, queue, st, time.Duration(cfg.Import.InspectTimeout), cfg.Registries.Insecure)
	api.RegisterAccounts(restAPI, accounts)
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
		InsecureRegistries: cfg.Registries.Insecure,
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
