// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package config loads Tobby's layered configuration (FR-003).
//
// Precedence, lowest to highest: built-in defaults, then the YAML
// configuration file, then TOBBY_* environment variables, then command-line
// flags. The effective configuration — secrets redacted by construction
// (NFR-015) — is dumpable through `tobby config dump`.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode is the operating mode, selected by configuration at startup (FR-001).
type Mode string

const (
	// ModePassthrough runs Tobby as a long-lived service continuously
	// promoting content between two connected zones.
	ModePassthrough Mode = "passthrough"
	// ModeMirror runs Tobby against a self-contained transportable store
	// that physically crosses an air gap.
	ModeMirror Mode = "mirror"
)

// Config is the effective configuration of one Tobby instance.
type Config struct {
	// Mode selects the operating mode. Required; there is no default: an
	// instance must state what it is (FR-001).
	Mode Mode `yaml:"mode"`

	Storage    Storage    `yaml:"storage"`
	State      State      `yaml:"state"`
	Server     Server     `yaml:"server"`
	Auth       Auth       `yaml:"auth"`
	Registries Registries `yaml:"registries"`
	UI         UI         `yaml:"ui"`
	Import     Import     `yaml:"import"`
	Logging    Logging    `yaml:"logging"`
	Shutdown   Shutdown   `yaml:"shutdown"`
}

// Import configures the unit-import screens and endpoints (FR-023).
type Import struct {
	// InspectTimeout bounds one remote inspection (UI-SPEC §5.6): a
	// deadline hit maps to the dedicated TBY-REG-004 code, distinct from
	// "unreachable". Default 20s.
	InspectTimeout Duration `yaml:"inspectTimeout"`
}

// Registries configures how source registries are reached.
type Registries struct {
	// Insecure lists source hosts reachable over plain HTTP or
	// unverifiable TLS ("host" or "host:port"). Per-host and explicit —
	// never a global switch (FR-075). The enterprise TLS/PKI support of
	// milestone 4 (roadmap 4.4) supersedes this for verified private CAs.
	Insecure []string `yaml:"insecure,omitempty"`
}

// UI configures the web interface (ADR-0010, ADR-0015).
type UI struct {
	// ThemeOverride is an optional operator stylesheet served after the
	// embedded design tokens: rebranding without rebuild (FR-064). The
	// default tokens pass WCAG AA; overrides carry that responsibility.
	ThemeOverride string `yaml:"themeOverride"`
	// ShowUpcoming renders the navigation entries of future milestones as
	// inert, labeled placeholders (demo mode). Off by default: production
	// navigation only shows what works.
	ShowUpcoming bool `yaml:"showUpcoming"`
}

// Storage locates the self-contained store (FR-050).
type Storage struct {
	// Root is the store directory. Required for serving: everything Tobby
	// holds — artifacts, recipes, operation logs — lives under it.
	Root string `yaml:"root"`
}

// State locates the instance state directory: accounts and tokens today,
// trust roots, certificates and configuration tables as they land. It is
// the instance's identity and must stay strictly outside the transportable
// store: secrets never travel on the media (R-16), and the directory is the
// single backup target (R-27).
type State struct {
	// Root is the state directory. Required to serve unless authentication
	// is explicitly disabled.
	Root string `yaml:"root"`
}

// Auth configures authentication (ADR-0009; FR-072 to FR-075).
type Auth struct {
	// Disabled switches authentication off for every surface. Secure by
	// default: false. Disabling is a deliberate opt-in — settable only in
	// the configuration file or TOBBY_AUTH_DISABLED, never by flag — and
	// the UI shows a permanent banner while it is set (FR-075).
	Disabled bool `yaml:"disabled"`
	// SessionTTL bounds a UI session's lifetime. Sessions live in memory:
	// an instance restart signs everyone out. Default 12h.
	SessionTTL Duration `yaml:"sessionTTL"`
}

// Server configures the HTTP listener (UI, API, registry, probes, metrics).
type Server struct {
	// Addr is the listen address, host:port. Default ":8080".
	Addr string `yaml:"addr"`
}

// Logging configures the structured JSON logs (FR-090).
type Logging struct {
	// Level is one of debug, info, warn, error. Default "info".
	Level string `yaml:"level"`
}

// Shutdown configures the graceful-shutdown behavior (FR-093, ADR-0012).
type Shutdown struct {
	// GracePeriod is how long in-flight work gets to finish or checkpoint
	// after SIGTERM/SIGINT before the process exits. Default 30s.
	GracePeriod Duration `yaml:"gracePeriod"`
}

// Default returns the built-in defaults, the lowest configuration layer.
func Default() Config {
	return Config{
		Server:   Server{Addr: ":8080"},
		Auth:     Auth{SessionTTL: Duration(12 * time.Hour)},
		Import:   Import{InspectTimeout: Duration(20 * time.Second)},
		Logging:  Logging{Level: "info"},
		Shutdown: Shutdown{GracePeriod: Duration(30 * time.Second)},
	}
}

// Scope names the configuration slice a command actually uses: validation
// is per-command (R-34) — a command must never demand a setting it ignores
// (B-006: `tobby user` only needs the state directory, not a mode).
type Scope int

const (
	// ScopeInstance validates the full instance configuration — what
	// serving requires, mode included (FR-001).
	ScopeInstance Scope = iota
	// ScopeState validates only what state-directory commands need:
	// everything set must be coherent, but no mode is required.
	ScopeState
)

// Load builds the effective configuration: defaults, overlaid with the YAML
// file at path (skipped when path is empty and optional), then environment
// variables, then flag overrides. Validation runs on the merged result, for
// the full instance scope.
func Load(path string, pathExplicit bool, overrides ...Override) (Config, error) {
	return LoadFor(ScopeInstance, path, pathExplicit, overrides...)
}

// LoadFor is Load with per-command validation (R-34): the merged result is
// validated only against what the command's scope actually uses.
func LoadFor(scope Scope, path string, pathExplicit bool, overrides ...Override) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // G304: reading the operator-supplied configuration file is the feature (FR-003)
		switch {
		case err == nil:
			dec := yaml.NewDecoder(bytes.NewReader(data))
			dec.KnownFields(true)
			if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
				// io.EOF means an empty file: nothing to overlay.
				return Config{}, fmt.Errorf("configuration file %s: %w", path, err)
			}
		case os.IsNotExist(err) && !pathExplicit:
			// The default file location is optional; an explicitly given
			// path must exist.
		default:
			return Config{}, fmt.Errorf("configuration file: %w", err)
		}
	}

	if err := applyEnv(&cfg, os.LookupEnv); err != nil {
		return Config{}, err
	}
	for _, o := range overrides {
		o(&cfg)
	}

	if err := cfg.validate(scope); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Override is one flag-level override, the highest configuration layer.
// The CLI translates every set flag into one Override.
type Override func(*Config)

// Validate checks the merged configuration for the full instance scope.
// Error messages state what to fix.
func (c *Config) Validate() error { return c.validate(ScopeInstance) }

// validate checks the merged configuration against one command scope: what
// is set must always be coherent; what is absent is only an error when the
// scope requires it (R-34).
func (c *Config) validate(scope Scope) error {
	var errs []error
	switch c.Mode {
	case ModePassthrough, ModeMirror:
	case "":
		if scope == ScopeInstance {
			errs = append(errs, errors.New(`mode is required: set "passthrough" or "mirror" (flag --mode, env TOBBY_MODE, or "mode:" in the configuration file)`))
		}
	default:
		errs = append(errs, fmt.Errorf(`unknown mode %q: valid modes are "passthrough" and "mirror"`, c.Mode))
	}
	if _, err := parseLevel(c.Logging.Level); err != nil {
		errs = append(errs, fmt.Errorf("logging.level: %w", err))
	}
	if c.Server.Addr == "" {
		errs = append(errs, errors.New("server.addr must not be empty"))
	}
	if time.Duration(c.Shutdown.GracePeriod) <= 0 {
		errs = append(errs, errors.New("shutdown.gracePeriod must be positive"))
	}
	if time.Duration(c.Auth.SessionTTL) <= 0 {
		errs = append(errs, errors.New("auth.sessionTTL must be positive"))
	}
	if time.Duration(c.Import.InspectTimeout) <= 0 {
		errs = append(errs, errors.New("import.inspectTimeout must be positive"))
	}
	if err := disjointRoots(c.State.Root, c.Storage.Root); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// disjointRoots refuses a state directory inside the transportable store or
// the reverse: secrets and instance identity never travel on the media
// (R-16), and the store must stay relocatable without dragging state along.
func disjointRoots(state, storage string) error {
	if state == "" || storage == "" {
		return nil
	}
	s, err1 := filepath.Abs(state)
	g, err2 := filepath.Abs(storage)
	if err1 != nil || err2 != nil {
		return nil // path resolution problems surface later, on use
	}
	rel, err := filepath.Rel(g, s)
	if err == nil && rel == "." {
		return fmt.Errorf("state.root and storage.root must differ (%s)", s)
	}
	if err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("state.root must not live inside storage.root: secrets never travel on the transportable store (state.root=%s, storage.root=%s)", s, g)
	}
	rel, err = filepath.Rel(s, g)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("storage.root must not live inside state.root (state.root=%s, storage.root=%s)", s, g)
	}
	return nil
}

// Dump renders the effective configuration as YAML. Secret values are
// redacted by construction: the Secret type cannot serialize its content.
func (c *Config) Dump() (string, error) {
	out, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("rendering configuration: %w", err)
	}
	return string(out), nil
}

// parseLevel mirrors logging.ParseLevel's accepted values without importing
// the logging package (config stays dependency-free within the module).
func parseLevel(s string) (struct{}, error) {
	switch s {
	case "debug", "info", "warn", "warning", "error", "":
		return struct{}{}, nil
	}
	return struct{}{}, fmt.Errorf("unknown log level %q (expected debug, info, warn, or error)", s)
}
