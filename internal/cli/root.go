// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package cli wires Tobby's command-line interface (FR-066).
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// DefaultConfigPath is where Tobby looks for its configuration file when
// --config is not given. The file is optional at this location; an explicit
// --config path must exist.
const DefaultConfigPath = "/etc/tobby/config.yaml"

// commonFlags carries the configuration-relevant flags shared by commands
// that load the layered configuration (FR-003).
type commonFlags struct {
	configPath  string
	mode        string
	storageRoot string
	serverAddr  string
	logLevel    string
	gracePeriod string
}

// register declares the flags on cmd.
func (f *commonFlags) register(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.StringVar(&f.configPath, "config", "", "path to the YAML configuration file (default "+DefaultConfigPath+")")
	fs.StringVar(&f.mode, "mode", "", `operating mode: "passthrough" or "mirror"`)
	fs.StringVar(&f.storageRoot, "storage-root", "", "directory of the self-contained store")
	fs.StringVar(&f.serverAddr, "server-addr", "", "HTTP listen address (default :8080)")
	fs.StringVar(&f.logLevel, "log-level", "", "log level: debug, info, warn, or error (default info)")
	fs.StringVar(&f.gracePeriod, "shutdown-grace-period", "", `graceful-shutdown drain budget, e.g. "30s" (default 30s)`)
}

// load builds the effective configuration from all layers (FR-003:
// flags > environment > file > defaults).
func (f *commonFlags) load(cmd *cobra.Command) (config.Config, error) {
	path, explicit := f.configPath, true
	if path == "" {
		path, explicit = DefaultConfigPath, false
	}

	var overrides []config.Override
	if cmd.Flags().Changed("mode") {
		overrides = append(overrides, func(c *config.Config) { c.Mode = config.Mode(f.mode) })
	}
	if cmd.Flags().Changed("storage-root") {
		overrides = append(overrides, func(c *config.Config) { c.Storage.Root = f.storageRoot })
	}
	if cmd.Flags().Changed("server-addr") {
		overrides = append(overrides, func(c *config.Config) { c.Server.Addr = f.serverAddr })
	}
	if cmd.Flags().Changed("log-level") {
		overrides = append(overrides, func(c *config.Config) { c.Logging.Level = f.logLevel })
	}
	if cmd.Flags().Changed("shutdown-grace-period") {
		d, err := config.ParseDuration(f.gracePeriod)
		if err != nil {
			return config.Config{}, fmt.Errorf("--shutdown-grace-period: %w", err)
		}
		overrides = append(overrides, func(c *config.Config) { c.Shutdown.GracePeriod = d })
	}

	return config.Load(path, explicit, overrides...)
}

// New builds the root command.
func New() *cobra.Command {
	root := &cobra.Command{
		Use:           "tobby",
		Short:         "Tobby fetches OCI assets and carries them across network zones",
		Long:          "Tobby transfers OCI assets — container images, Helm charts, arbitrary\nOCI artifacts, packaged file sets — between network zones, from connected\nenvironments down to air-gapped ones, driven by signed Recipes.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newServeCmd(), newVersionCmd(), newConfigCmd())
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "tobby:", err)
		return 1
	}
	return 0
}
