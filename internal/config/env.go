// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"fmt"
	"time"
)

// Environment variable names. The mapping is mechanical — TOBBY_ plus the
// configuration path in upper snake case — and each supported variable is
// listed here explicitly so `tobby config dump --help` and the docs can
// enumerate them.
const (
	EnvMode                = "TOBBY_MODE"
	EnvStorageRoot         = "TOBBY_STORAGE_ROOT"
	EnvServerAddr          = "TOBBY_SERVER_ADDR"
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
	if v, ok := lookup(EnvServerAddr); ok {
		cfg.Server.Addr = v
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
