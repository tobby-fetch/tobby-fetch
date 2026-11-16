// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The milestone-7 configuration surface, reserved at the opening of the
// milestone so its tracks fill one documented surface instead of inventing
// several: the consumption aids (FR-065 amendment, 7.1), the diagnostic
// bundle (FR-095, R-30), the clock check (FR-096, R-32), the per-instance
// hardening switches (FR-098, R-17), and the scoped, bounded exceptions and
// OpenVEX input of the scan policy (FR-031 amendment, R-15 — their fields
// sit on Scan and ScanException, in milestone6.go).
//
// The rule of the milestone-6 opening holds: an opt-in this build cannot
// honour yet is refused at instance scope rather than silently ignored
// (FR-075). Each track removes the guard of its own keys when its feature
// lands, and TestMilestone7ReservedOptInsAreRefused names what is still
// guarded.

// Consumption configures the consumption aids (FR-065): the mapping table
// export and the K3s/RKE2 registries.yaml snippet. Track A.
type Consumption struct {
	// Registry is the address the zone's clients pull from, as a bare host
	// ("registry.zone.example" or "host:port"), when it is not
	// destination.registry: an instance serving its own /v2/ to the zone
	// (UC3), or a zone registry reached under another name than the one
	// this instance pushes to. Empty uses destination.registry.
	Registry string `yaml:"registry,omitempty"`
	// CAFile is the path, ON THE CLUSTER NODES, of the CA certificate that
	// signs Registry's certificate. Written as-is into the snippet's
	// configs.<host>.tls.ca_file; never read by Tobby.
	CAFile string `yaml:"caFile,omitempty"`
}

// Diagnostics bounds what the diagnostic bundle collects (FR-095). Track B.
type Diagnostics struct {
	// LogRecords is the size of the in-memory window of recent log records
	// the bundle includes. Default 2000; at most 100000. It is a window,
	// never an archive: a passthrough instance logs to stdout and keeps
	// nothing else.
	LogRecords int `yaml:"logRecords"`
	// Tasks is how many of the most recent task reports the bundle
	// includes. Default 20; at most 500.
	Tasks int `yaml:"tasks"`
}

// Clock configures the clock-plausibility check (FR-096). Track B.
type Clock struct {
	// SkewTolerance absorbs ordinary drift between zones: the local clock
	// is implausible only when it is earlier than an anchor (the binary's
	// build time, a medium's resolution time) by more than this. Default
	// 1h; between 0 and 7 days. The check itself cannot be switched off:
	// it warns and records, it changes no verdict.
	SkewTolerance Duration `yaml:"skewTolerance"`
}

// Hardening holds the per-instance switches that remove a way in
// (FR-098). Both default to false. They are hardening, not relaxation: no
// acknowledgement is needed, but each is reported like every security
// setting — which is why neither is omitempty: the configuration dump
// shows both, false included, so "off" is read rather than inferred.
// Track C.
type Hardening struct {
	// DisableUnitImport refuses unit imports (FR-023) and local FileSet
	// packing (FR-048) on every surface, the command line included.
	DisableUnitImport bool `yaml:"disableUnitImport"`
	// DisableRegistryPush makes the embedded registry read-only for every
	// client and every role. Recipe-driven writes (FR-027) are unaffected;
	// seeding (UC3) becomes impossible on this instance.
	DisableRegistryPush bool `yaml:"disableRegistryPush"`
}

// ExceptionScope narrows a scan exception to the items it was meant for
// (FR-031 amendment, R-15). The criteria present combine with AND; none
// at all keeps the exception level-wide.
type ExceptionScope struct {
	// Digest is a manifest digest, "sha256:<64 hex>".
	Digest string `yaml:"digest,omitempty"`
	// Repository is the nominal source repository of the ingredient, host
	// and path, matched with the glob grammar of trust scopes ("*" within
	// a segment, "**" across them), in its canonical form (ADR-0013).
	Repository string `yaml:"repository,omitempty"`
	// Recipe is a recipe name, optionally "name@version".
	Recipe string `yaml:"recipe,omitempty"`
}

// VEXDocument declares one local OpenVEX document (FR-031 amendment,
// R-15). OpenVEX statements carry no expiry, and an exception never goes
// unbounded: the expiry of every exception born of the document is
// declared here, beside it.
type VEXDocument struct {
	// Path is the absolute path of the document — a local file, never a
	// URL: a VEX document is never fetched (NFR-019).
	Path string `yaml:"path"`
	// Expires bounds every exception the document yields. Required.
	Expires time.Time `yaml:"expires"`
}

// IsZero reports whether the scope narrows nothing.
func (s ExceptionScope) IsZero() bool {
	return s.Digest == "" && s.Repository == "" && s.Recipe == ""
}

// Defaults and bounds of the milestone-7 surface, applied by Default.
const (
	DefaultDiagnosticsLogRecords = 2000
	maxDiagnosticsLogRecords     = 100000
	DefaultDiagnosticsTasks      = 20
	maxDiagnosticsTasks          = 500
	DefaultClockSkewTolerance    = time.Hour
	maxClockSkewTolerance        = 7 * 24 * time.Hour
)

// defaultMilestone7 fills the milestone-7 defaults into cfg.
func defaultMilestone7(cfg *Config) {
	cfg.Diagnostics = Diagnostics{
		LogRecords: DefaultDiagnosticsLogRecords,
		Tasks:      DefaultDiagnosticsTasks,
	}
	cfg.Clock = Clock{SkewTolerance: Duration(DefaultClockSkewTolerance)}
}

// reservedOptIn is one milestone-7 setting a build cannot honour yet: set,
// it is refused at instance scope, naming the feature that lands it.
type reservedOptIn struct {
	key     string
	set     func(*Config) bool
	feature string
}

// milestone7Reserved lists the guarded settings. A track that implements
// one removes its entry — and the corresponding case of
// TestMilestone7ReservedOptInsAreRefused — in the same change.
var milestone7Reserved = []reservedOptIn{
	{"consumption.registry", func(c *Config) bool { return c.Consumption.Registry != "" }, "the consumption aids (7.1)"},
	{"consumption.caFile", func(c *Config) bool { return c.Consumption.CAFile != "" }, "the consumption aids (7.1)"},
}

// validateMilestone7 checks the milestone-7 surface.
func (c *Config) validateMilestone7(scope Scope) []error {
	var errs []error
	if c.Consumption.Registry != "" {
		if err := bareRegistryHost("consumption.registry", c.Consumption.Registry); err != nil {
			errs = append(errs, err)
		}
	}
	// The CA path is a path on the cluster nodes, which run Linux: it is
	// checked as a slash path whatever the platform this instance runs on.
	if c.Consumption.CAFile != "" && !path.IsAbs(c.Consumption.CAFile) {
		errs = append(errs, fmt.Errorf("consumption.caFile: %q must be an absolute path on the cluster nodes", c.Consumption.CAFile))
	}
	if d := c.Diagnostics; d.LogRecords <= 0 || d.LogRecords > maxDiagnosticsLogRecords {
		errs = append(errs, fmt.Errorf("diagnostics.logRecords must lie between 1 and %d", maxDiagnosticsLogRecords))
	}
	if d := c.Diagnostics; d.Tasks <= 0 || d.Tasks > maxDiagnosticsTasks {
		errs = append(errs, fmt.Errorf("diagnostics.tasks must lie between 1 and %d", maxDiagnosticsTasks))
	}
	if t := time.Duration(c.Clock.SkewTolerance); t < 0 || t > maxClockSkewTolerance {
		errs = append(errs, fmt.Errorf("clock.skewTolerance must lie between 0 and %s: the clock check itself cannot be switched off (FR-096)", maxClockSkewTolerance))
	}
	for i, ex := range c.Scan.Exceptions {
		errs = append(errs, ex.Scope.validate(fmt.Sprintf("scan.exceptions[%d].scope", i))...)
	}
	for i, v := range c.Scan.VEX {
		// Local files only: a VEX document is never fetched (NFR-019).
		if strings.Contains(v.Path, "://") || !filepath.IsAbs(v.Path) {
			errs = append(errs, fmt.Errorf("scan.vex[%d].path: %q must be an absolute path to a local OpenVEX document — no URL is ever fetched", i, v.Path))
		}
		if v.Expires.IsZero() {
			errs = append(errs, fmt.Errorf("scan.vex[%d].expires is required: OpenVEX statements carry no expiry, and an exception is never unbounded (R-15)", i))
		}
	}
	if scope == ScopeInstance {
		for _, r := range milestone7Reserved {
			if r.set(c) {
				errs = append(errs, fmt.Errorf("%s is reserved for %s, which this build does not implement yet: remove it rather than have it silently ignored", r.key, r.feature))
			}
		}
	}
	return errs
}

func (s ExceptionScope) validate(key string) []error {
	var errs []error
	if s.Digest != "" && !sha256Pattern.MatchString(s.Digest) {
		errs = append(errs, fmt.Errorf("%s.digest: %q must be sha256:<64 lowercase hex digits>", key, s.Digest))
	}
	// Canonical form (ADR-0013): a port's ":" is written "_", so no ":"
	// belongs in the pattern at all — one there is a tag or a scheme.
	if r := s.Repository; r != "" && (strings.ContainsAny(r, ":@") || !strings.Contains(r, "/")) {
		errs = append(errs, fmt.Errorf("%s.repository: %q must be a nominal repository in canonical form — registry host and path, a port written host_5000, no scheme, tag or digest", key, r))
	}
	if r := s.Recipe; r != "" {
		name, version, _ := strings.Cut(r, "@")
		if name == "" || strings.Contains(r, "/") || strings.Count(r, "@") > 1 || (strings.Contains(r, "@") && version == "") {
			errs = append(errs, fmt.Errorf("%s.recipe: %q must be a recipe name, optionally name@version", key, r))
		}
	}
	return errs
}

// bareRegistryHost accepts "host" or "host:port", nothing else — the shape
// destination.registry has, for the same reasons (validateDestination).
func bareRegistryHost(key, value string) error {
	switch {
	case strings.Contains(value, "://"):
		return fmt.Errorf("%s: %q must be a bare registry host, not a URL", key, value)
	case strings.Contains(value, "/"):
		return fmt.Errorf("%s: %q must be a bare registry host, without a repository path", key, value)
	case strings.Contains(value, "@"):
		return fmt.Errorf("%s: %q must be a bare registry host, not a reference", key, value)
	case strings.TrimSpace(value) != value:
		return errors.New(key + ": must not carry surrounding spaces")
	}
	return nil
}
