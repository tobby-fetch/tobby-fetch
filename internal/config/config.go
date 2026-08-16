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
	Retriever  Retriever  `yaml:"retriever"`
	Sync       Sync       `yaml:"sync"`
	Trust      Trust      `yaml:"trust"`
	Files      Files      `yaml:"files"`
	Logging    Logging    `yaml:"logging"`
	Shutdown   Shutdown   `yaml:"shutdown"`
}

// Retriever locates the desired-state document (FR-010): an HTTP(S) URL,
// an OCI reference, or a local file path — reported as configured through
// the UI and the API.
type Retriever struct {
	Source string `yaml:"source"`
}

// Sync bounds the recipe engine's transfers (NFR-008, FR-029).
type Sync struct {
	// Parallelism caps concurrent ingredient transfers. Default 3.
	Parallelism int `yaml:"parallelism"`
	// Retries bounds per-ingredient retry attempts on transient transfer
	// failures (bounded backoff, FR-029). Default 3.
	Retries int `yaml:"retries"`
}

// Trust configures signature verification (FR-033, ADR-0007,
// RECIPE-SPEC §12.3). Verification is on by default for every recipe;
// relaxation exists only as explicitly declared scopes — never a global
// bypass — and every relaxed scope is visible in the reported
// configuration (FR-075 principle). List-valued: configuration file only.
type Trust struct {
	Roots  []TrustRoot  `yaml:"roots,omitempty"`
	Scopes []TrustScope `yaml:"scopes,omitempty"`
}

// TrustRoot is one trusted public key (cosign key-based, multi-key set for
// rotation by overlap). Exactly one of Key, KeyFile, KeyURL is set; a URL
// is fetched and cached at configuration time, never at verification time
// (RECIPE-SPEC §12.3 — air-gapped instances use inline or file forms).
type TrustRoot struct {
	Name    string `yaml:"name"`
	Key     string `yaml:"key,omitempty"`
	KeyFile string `yaml:"keyFile,omitempty"`
	KeyURL  string `yaml:"keyURL,omitempty"`
}

// TrustScope declares one relaxed verification perimeter (§12.3: "a named
// low-assurance source restricted to specific repositories"). Scopes are
// evaluated in declaration order; the first match applies; no match means
// the strict default (signature required, any configured root). A scope
// must relax or restrict something: AllowUnsigned, or a Roots restriction.
type TrustScope struct {
	Name string `yaml:"name"`
	// Repositories are glob patterns matched against the recipe's nominal
	// cookbook repository path ("lab.example.com/cookbook/*"; "**" spans
	// separators). Trust follows the nominal ref, never the substituted
	// endpoint (FR-036).
	Repositories []string `yaml:"repositories"`
	// AllowUnsigned admits unsigned recipes inside the scope — reported on
	// every surface (banner, logs, task report), never silent (FR-075).
	AllowUnsigned bool `yaml:"allowUnsigned,omitempty"`
	// Roots restricts verification to the named trust roots within the
	// scope (defense in depth for multi-tenant cookbooks).
	Roots []string `yaml:"roots,omitempty"`
}

// Files configures the FileSet HTTP serving surface (FR-047). Disabled by
// default: only FileSets listed here are served. List-valued:
// configuration file only.
type Files struct {
	FileSets []FileSetServe `yaml:"filesets,omitempty"`
}

// FileSetServe enables one verified FileSet under /files/<name>/.
type FileSetServe struct {
	// Name is the URL segment ("debs" serves under /files/debs/).
	Name string `yaml:"name"`
	// Ref is the nominal ingredient reference of the FileSet
	// ("registry.example.com/filesets/site-config": host + repository, no
	// tag or digest — the served content is whatever verified digest the
	// store holds for it).
	Ref string `yaml:"ref"`
	// Version pins the served tag; empty serves the highest semver tag
	// present locally.
	Version string `yaml:"version,omitempty"`
	// Platform selects the platform manifest of a multi-platform FileSet
	// ("linux/amd64"); empty picks the single manifest and fails on an
	// ambiguous index.
	Platform string `yaml:"platform,omitempty"`
	// Anonymous opts this FileSet into unauthenticated reads (bare-host
	// bootstrap) — reported like the FR-075 override, never silent.
	Anonymous bool `yaml:"anonymous,omitempty"`
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
	// Substitutions maps a nominal registry host to a substitute registry
	// base (FR-036, RECIPE-SPEC §11.5), applied at fetch time: a downstream
	// zone fetches "docker.io/…" from its upstream zone registry without
	// modifying the recipes. Substitution changes only the endpoint
	// contacted, never the relocated path (FR-035); credentials apply to
	// the effective host, trust scopes to the nominal ref. List-valued:
	// configuration file only.
	Substitutions map[string]string `yaml:"substitutions,omitempty"`
	// CredentialsFile points at a kubernetes.io/dockerconfigjson payload
	// (FR-004): registry credentials looked up by the effective host
	// actually contacted (RECIPE-SPEC §13.2). Secrets never live in the
	// configuration file itself, and the file must sit outside the
	// transportable store (R-16).
	CredentialsFile string `yaml:"credentialsFile,omitempty"`
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
	// BasePrefix is the optional relocation base prefix (FR-035), applied
	// identically to every ingredient of the instance. Default: none.
	BasePrefix string `yaml:"basePrefix,omitempty"`
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
		Sync:     Sync{Parallelism: 3, Retries: 3},
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
	// ScopeRegistries validates what registry-facing commands need
	// (`tobby recipe push`, R-36): credentials and per-host insecure
	// opt-ins. Like ScopeState it requires no mode — publishing a recipe
	// is an authoring act, it says nothing about how this host serves.
	ScopeRegistries
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
	if c.Sync.Parallelism <= 0 {
		errs = append(errs, errors.New("sync.parallelism must be positive"))
	}
	if c.Sync.Retries < 0 {
		errs = append(errs, errors.New("sync.retries must not be negative"))
	}
	errs = append(errs, c.validateTrust()...)
	errs = append(errs, c.validateFiles()...)
	return errors.Join(errs...)
}

// validateTrust checks the trust-root set and the declared scopes
// (FR-033): a malformed trust configuration must fail startup, never relax
// silently.
func (c *Config) validateTrust() []error {
	var errs []error
	rootNames := map[string]bool{}
	for i, r := range c.Trust.Roots {
		if r.Name == "" {
			errs = append(errs, fmt.Errorf("trust.roots[%d]: name is required", i))
		} else if rootNames[r.Name] {
			errs = append(errs, fmt.Errorf("trust.roots[%d]: duplicate name %q", i, r.Name))
		}
		rootNames[r.Name] = true
		set := 0
		for _, v := range []string{r.Key, r.KeyFile, r.KeyURL} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			errs = append(errs, fmt.Errorf("trust.roots[%d] (%s): exactly one of key, keyFile, keyURL is required", i, r.Name))
		}
		if r.KeyURL != "" && !strings.HasPrefix(r.KeyURL, "https://") {
			errs = append(errs, fmt.Errorf("trust.roots[%d] (%s): keyURL must be https:// (RECIPE-SPEC §12.3)", i, r.Name))
		}
	}
	scopeNames := map[string]bool{}
	for i, s := range c.Trust.Scopes {
		if s.Name == "" {
			errs = append(errs, fmt.Errorf("trust.scopes[%d]: name is required", i))
		} else if scopeNames[s.Name] {
			errs = append(errs, fmt.Errorf("trust.scopes[%d]: duplicate name %q", i, s.Name))
		}
		scopeNames[s.Name] = true
		if len(s.Repositories) == 0 {
			errs = append(errs, fmt.Errorf("trust.scopes[%d] (%s): repositories patterns are required — a scope is always an explicitly declared perimeter", i, s.Name))
		}
		if !s.AllowUnsigned && len(s.Roots) == 0 {
			errs = append(errs, fmt.Errorf("trust.scopes[%d] (%s): a scope must declare what it changes — allowUnsigned and/or a roots restriction", i, s.Name))
		}
		for _, want := range s.Roots {
			if !rootNames[want] {
				errs = append(errs, fmt.Errorf("trust.scopes[%d] (%s): unknown trust root %q", i, s.Name, want))
			}
		}
	}
	return errs
}

// validateFiles checks the FileSet serving declarations (FR-047).
func (c *Config) validateFiles() []error {
	var errs []error
	names := map[string]bool{}
	for i, f := range c.Files.FileSets {
		if !validFileSetName(f.Name) {
			errs = append(errs, fmt.Errorf("files.filesets[%d]: name %q must be a lowercase URL segment ([a-z0-9._-])", i, f.Name))
		} else if names[f.Name] {
			errs = append(errs, fmt.Errorf("files.filesets[%d]: duplicate name %q", i, f.Name))
		}
		names[f.Name] = true
		if f.Ref == "" {
			errs = append(errs, fmt.Errorf("files.filesets[%d] (%s): ref is required", i, f.Name))
		}
	}
	return errs
}

// validFileSetName accepts a safe URL path segment: lowercase alphanumeric
// plus [._-], no traversal, no separator.
func validFileSetName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
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
