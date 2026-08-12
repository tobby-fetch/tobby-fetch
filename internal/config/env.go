// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Environment variable names. The mapping is mechanical — TOBBY_ plus the
// configuration path in upper snake case — and each supported variable is
// listed here explicitly so `tobby config dump --help` and the docs can
// enumerate them.
const (
	EnvMode                = "TOBBY_MODE"
	EnvStorageRoot         = "TOBBY_STORAGE_ROOT"
	EnvStateRoot           = "TOBBY_STATE_ROOT"
	EnvServerAddr          = "TOBBY_SERVER_ADDR"
	EnvAuthDisabled        = "TOBBY_AUTH_DISABLED"
	EnvAuthSessionTTL      = "TOBBY_AUTH_SESSION_TTL"
	EnvRegistriesInsecure  = "TOBBY_REGISTRIES_INSECURE"
	EnvUIThemeOverride     = "TOBBY_UI_THEME_OVERRIDE"
	EnvUIShowUpcoming      = "TOBBY_UI_SHOW_UPCOMING"
	EnvImportInspectTO     = "TOBBY_IMPORT_INSPECT_TIMEOUT"
	EnvRetrieverSource     = "TOBBY_RETRIEVER_SOURCE"
	EnvStorageBasePrefix   = "TOBBY_STORAGE_BASE_PREFIX"
	EnvSyncParallelism     = "TOBBY_SYNC_PARALLELISM"
	EnvSyncRetries         = "TOBBY_SYNC_RETRIES"
	EnvLoggingLevel        = "TOBBY_LOGGING_LEVEL"
	EnvShutdownGracePeriod = "TOBBY_SHUTDOWN_GRACE_PERIOD"
)

// applyEnv overlays environment values onto cfg. lookup is os.LookupEnv in
// production and a map lookup in tests.
func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup(EnvMode); ok {
		cfg.Mode = Mode(v)
	}
	if v, ok := lookup(EnvStorageRoot); ok {
		cfg.Storage.Root = v
	}
	if v, ok := lookup(EnvStateRoot); ok {
		cfg.State.Root = v
	}
	if v, ok := lookup(EnvServerAddr); ok {
		cfg.Server.Addr = v
	}
	if v, ok := lookup(EnvAuthDisabled); ok {
		switch v {
		case "true", "1":
			cfg.Auth.Disabled = true
		case "false", "0", "":
			cfg.Auth.Disabled = false
		default:
			return fmt.Errorf("%s: invalid boolean %q (expected true or false)", EnvAuthDisabled, v)
		}
	}
	if v, ok := lookup(EnvAuthSessionTTL); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q (expected e.g. \"12h\")", EnvAuthSessionTTL, v)
		}
		cfg.Auth.SessionTTL = Duration(d)
	}
	if v, ok := lookup(EnvRegistriesInsecure); ok {
		cfg.Registries.Insecure = nil
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				cfg.Registries.Insecure = append(cfg.Registries.Insecure, h)
			}
		}
	}
	if v, ok := lookup(EnvUIThemeOverride); ok {
		cfg.UI.ThemeOverride = v
	}
	if v, ok := lookup(EnvUIShowUpcoming); ok {
		switch v {
		case "true", "1":
			cfg.UI.ShowUpcoming = true
		case "false", "0", "":
			cfg.UI.ShowUpcoming = false
		default:
			return fmt.Errorf("%s: invalid boolean %q (expected true or false)", EnvUIShowUpcoming, v)
		}
	}
	if v, ok := lookup(EnvImportInspectTO); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q (expected e.g. \"20s\")", EnvImportInspectTO, v)
		}
		cfg.Import.InspectTimeout = Duration(d)
	}
	if v, ok := lookup(EnvRetrieverSource); ok {
		cfg.Retriever.Source = v
	}
	if v, ok := lookup(EnvStorageBasePrefix); ok {
		cfg.Storage.BasePrefix = v
	}
	if v, ok := lookup(EnvSyncParallelism); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q", EnvSyncParallelism, v)
		}
		cfg.Sync.Parallelism = n
	}
	if v, ok := lookup(EnvSyncRetries); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q", EnvSyncRetries, v)
		}
		cfg.Sync.Retries = n
	}
	if v, ok := lookup(EnvLoggingLevel); ok {
		cfg.Logging.Level = v
	}
	if v, ok := lookup(EnvShutdownGracePeriod); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q (expected e.g. \"30s\")", EnvShutdownGracePeriod, v)
		}
		cfg.Shutdown.GracePeriod = Duration(d)
	}
	return nil
}
