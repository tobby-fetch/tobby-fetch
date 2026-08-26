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
	EnvZone                = "TOBBY_ZONE"
	EnvStorageRoot         = "TOBBY_STORAGE_ROOT"
	EnvStateRoot           = "TOBBY_STATE_ROOT"
	EnvServerAddr          = "TOBBY_SERVER_ADDR"
	EnvAuthDisabled        = "TOBBY_AUTH_DISABLED"
	EnvAuthSessionTTL      = "TOBBY_AUTH_SESSION_TTL"
	EnvRegistriesInsecure  = "TOBBY_REGISTRIES_INSECURE"
	EnvUIThemeOverride     = "TOBBY_UI_THEME_OVERRIDE"
	EnvUIShowUpcoming      = "TOBBY_UI_SHOW_UPCOMING"
	EnvImportInspectTO     = "TOBBY_IMPORT_INSPECT_TIMEOUT"
	EnvTransferResumeMin   = "TOBBY_TRANSFER_RESUME_THRESHOLD"
	EnvRetrieverSource     = "TOBBY_RETRIEVER_SOURCE"
	EnvStorageBasePrefix   = "TOBBY_STORAGE_BASE_PREFIX"
	EnvSyncParallelism     = "TOBBY_SYNC_PARALLELISM"
	EnvSyncRetries         = "TOBBY_SYNC_RETRIES"
	EnvSyncInterval        = "TOBBY_SYNC_INTERVAL"
	EnvTasksKeepFinished   = "TOBBY_TASKS_KEEP_FINISHED"
	EnvLoggingLevel        = "TOBBY_LOGGING_LEVEL"
	EnvShutdownGracePeriod = "TOBBY_SHUTDOWN_GRACE_PERIOD"

	// Outbound network (FR-080, FR-081). The proxy password is
	// deliberately readable from the environment and from the
	// configuration file, but from no flag: a flag value sits in the
	// process table for anyone with a shell on the host.
	EnvNetworkProxyURL      = "TOBBY_NETWORK_PROXY_URL"
	EnvNetworkProxyHTTPSURL = "TOBBY_NETWORK_PROXY_HTTPS_URL"
	EnvNetworkProxyNoProxy  = "TOBBY_NETWORK_PROXY_NO_PROXY"
	EnvNetworkProxyUsername = "TOBBY_NETWORK_PROXY_USERNAME"
	EnvNetworkProxyPassword = "TOBBY_NETWORK_PROXY_PASSWORD" //nolint:gosec // G101: the NAME of an environment variable, not a credential
	EnvNetworkTLSCAFiles    = "TOBBY_NETWORK_TLS_CA_FILES"

	// Promotion destination (FR-013, FR-034, FR-035). No credential
	// variable belongs here: pushing authenticates through
	// registries.credentialsFile like every read does (FR-004).
	EnvDestinationRegistry = "TOBBY_DESTINATION_REGISTRY"
	EnvDestinationBasePath = "TOBBY_DESTINATION_BASE_PATH"
	EnvDestinationCookbook = "TOBBY_DESTINATION_COOKBOOK"

	// Listener certificate (FR-082).
	EnvServerTLSEnabled  = "TOBBY_SERVER_TLS_ENABLED"
	EnvServerTLSCertFile = "TOBBY_SERVER_TLS_CERT_FILE"
	EnvServerTLSKeyFile  = "TOBBY_SERVER_TLS_KEY_FILE"

	// Secure attribute on UI cookies behind a TLS-terminating proxy
	// (NFR-015; see config.Server.SecureCookies for the why).
	EnvServerSecureCookies = "TOBBY_SERVER_SECURE_COOKIES"
)

// splitList parses a comma-separated environment list, dropping empty
// entries: an environment variable is one string, so a list arrives
// flattened. The configuration file keeps the structured form.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// parseBool reads the boolean spelling the other TOBBY_* booleans accept.
func parseBool(name, v string) (bool, error) {
	switch v {
	case "true", "1":
		return true, nil
	case "false", "0", "":
		return false, nil
	}
	return false, fmt.Errorf("%s: invalid boolean %q (expected true or false)", name, v)
}

// applyEnv overlays environment values onto cfg. lookup is os.LookupEnv in
// production and a map lookup in tests.
func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup(EnvMode); ok {
		cfg.Mode = Mode(v)
	}
	if v, ok := lookup(EnvZone); ok {
		cfg.Zone = v
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
		b, err := parseBool(EnvAuthDisabled, v)
		if err != nil {
			return err
		}
		cfg.Auth.Disabled = b
	}
	if v, ok := lookup(EnvAuthSessionTTL); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q (expected e.g. \"12h\")", EnvAuthSessionTTL, v)
		}
		cfg.Auth.SessionTTL = Duration(d)
	}
	if v, ok := lookup(EnvRegistriesInsecure); ok {
		cfg.Registries.Insecure = splitList(v)
	}
	if v, ok := lookup(EnvNetworkProxyURL); ok {
		cfg.Network.Proxy.URL = v
	}
	if v, ok := lookup(EnvNetworkProxyHTTPSURL); ok {
		cfg.Network.Proxy.HTTPSURL = v
	}
	if v, ok := lookup(EnvNetworkProxyNoProxy); ok {
		cfg.Network.Proxy.NoProxy = splitList(v)
	}
	if v, ok := lookup(EnvNetworkProxyUsername); ok {
		cfg.Network.Proxy.Username = v
	}
	if v, ok := lookup(EnvNetworkProxyPassword); ok {
		cfg.Network.Proxy.Password = NewSecret(v)
	}
	if v, ok := lookup(EnvNetworkTLSCAFiles); ok {
		cfg.Network.TLS.CAFiles = splitList(v)
	}
	if v, ok := lookup(EnvServerTLSEnabled); ok {
		b, err := parseBool(EnvServerTLSEnabled, v)
		if err != nil {
			return err
		}
		cfg.Server.TLS.Enabled = b
	}
	if v, ok := lookup(EnvServerTLSCertFile); ok {
		cfg.Server.TLS.CertFile = v
	}
	if v, ok := lookup(EnvServerTLSKeyFile); ok {
		cfg.Server.TLS.KeyFile = v
	}
	if v, ok := lookup(EnvServerSecureCookies); ok {
		b, err := parseBool(EnvServerSecureCookies, v)
		if err != nil {
			return err
		}
		cfg.Server.SecureCookies = b
	}
	if v, ok := lookup(EnvUIThemeOverride); ok {
		cfg.UI.ThemeOverride = v
	}
	if v, ok := lookup(EnvUIShowUpcoming); ok {
		b, err := parseBool(EnvUIShowUpcoming, v)
		if err != nil {
			return err
		}
		cfg.UI.ShowUpcoming = b
	}
	if v, ok := lookup(EnvImportInspectTO); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q (expected e.g. \"20s\")", EnvImportInspectTO, v)
		}
		cfg.Import.InspectTimeout = Duration(d)
	}
	if v, ok := lookup(EnvTransferResumeMin); ok {
		sz, err := ParseSize(v)
		if err != nil {
			return fmt.Errorf("%s: %w", EnvTransferResumeMin, err)
		}
		cfg.Transfer.ResumeThreshold = sz
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
	if v, ok := lookup(EnvSyncInterval); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q (expected e.g. \"15m\")", EnvSyncInterval, v)
		}
		cfg.Sync.Interval = Duration(d)
	}
	if v, ok := lookup(EnvTasksKeepFinished); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: invalid integer %q", EnvTasksKeepFinished, v)
		}
		cfg.Tasks.KeepFinished = n
	}
	if v, ok := lookup(EnvDestinationRegistry); ok {
		cfg.Destination.Registry = v
	}
	if v, ok := lookup(EnvDestinationBasePath); ok {
		cfg.Destination.BasePath = v
	}
	if v, ok := lookup(EnvDestinationCookbook); ok {
		cfg.Destination.Cookbook = v
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
