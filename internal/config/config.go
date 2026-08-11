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

	Storage  Storage  `yaml:"storage"`
	Server   Server   `yaml:"server"`
	Logging  Logging  `yaml:"logging"`
	Shutdown Shutdown `yaml:"shutdown"`
}

// Storage locates the self-contained store (FR-050).
type Storage struct {
	// Root is the store directory. Required for serving: everything Tobby
	// holds — artifacts, recipes, operation logs — lives under it.
	Root string `yaml:"root"`
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
		Logging:  Logging{Level: "info"},
		Shutdown: Shutdown{GracePeriod: Duration(30 * time.Second)},
	}
}

// Load builds the effective configuration: defaults, overlaid with the YAML
// file at path (skipped when path is empty and optional), then environment
// variables, then flag overrides. Validation runs on the merged result.
func Load(path string, pathExplicit bool, overrides ...Override) (Config, error) {
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

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Override is one flag-level override, the highest configuration layer.
// The CLI translates every set flag into one Override.
type Override func(*Config)

// Validate checks the merged configuration. Error messages state what to fix.
func (c Config) Validate() error {
	var errs []error
	switch c.Mode {
	case ModePassthrough, ModeMirror:
	case "":
		errs = append(errs, errors.New(`mode is required: set "passthrough" or "mirror" (flag --mode, env TOBBY_MODE, or "mode:" in the configuration file)`))
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
	return errors.Join(errs...)
}

// Dump renders the effective configuration as YAML. Secret values are
// redacted by construction: the Secret type cannot serialize its content.
func (c Config) Dump() (string, error) {
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
