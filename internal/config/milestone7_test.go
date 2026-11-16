// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"testing"
	"time"
)

// TestMilestone7Defaults locks the defaults of the milestone-7 surface: a
// bounded log window and task count for the diagnostic bundle, a one-hour
// clock tolerance, no consumption override, and both hardening switches
// off.
func TestMilestone7Defaults(t *testing.T) {
	cfg := Default()
	if cfg.Diagnostics.LogRecords != 2000 || cfg.Diagnostics.Tasks != 20 {
		t.Errorf("diagnostics defaults = %+v", cfg.Diagnostics)
	}
	if time.Duration(cfg.Clock.SkewTolerance) != time.Hour {
		t.Errorf("clock.skewTolerance default = %v, want 1h", cfg.Clock.SkewTolerance)
	}
	if cfg.Consumption != (Consumption{}) || cfg.Hardening != (Hardening{}) {
		t.Errorf("consumption/hardening defaults = %+v / %+v", cfg.Consumption, cfg.Hardening)
	}
}

// TestMilestone7FileKeys pins the YAML names of the keys that are already
// honoured — the sizing knobs are accepted from the file today.
func TestMilestone7FileKeys(t *testing.T) {
	path := writeFile(t, `mode: mirror
diagnostics:
  logRecords: 500
  tasks: 5
clock:
  skewTolerance: 10m
`)
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Diagnostics.LogRecords != 500 || cfg.Diagnostics.Tasks != 5 || time.Duration(cfg.Clock.SkewTolerance) != 10*time.Minute {
		t.Errorf("file keys not read: %+v %+v", cfg.Diagnostics, cfg.Clock)
	}
}

// TestMilestone7ReservedOptInsAreRefused keeps every reserved opt-in
// honest: set, it is refused at instance scope, naming its feature, rather
// than silently ignored (FR-075). A track that implements one removes its
// case here and its entry in milestone7Reserved together.
func TestMilestone7ReservedOptInsAreRefused(t *testing.T) {
	cases := map[string]struct {
		tweak func(*Config)
		want  []string
	}{
		"hardening.disableUnitImport": {func(c *Config) { c.Hardening.DisableUnitImport = true },
			[]string{"hardening.disableUnitImport is reserved", "R-17"}},
		"hardening.disableRegistryPush": {func(c *Config) { c.Hardening.DisableRegistryPush = true },
			[]string{"hardening.disableRegistryPush is reserved", "R-17"}},
		"scan.vex": {func(c *Config) {
			c.Scan.VEX = []VEXDocument{{Path: "/etc/tobby/site.openvex.json", Expires: time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)}}
		}, []string{"scan.vex is reserved", "R-15"}},
		"scan.exceptions[].scope": {func(c *Config) {
			c.Scan.Exceptions = []ScanException{{ID: "CVE-2024-12345", Scope: ExceptionScope{Recipe: "wordpress"}}}
		}, []string{"scan.exceptions[].scope is reserved", "R-15"}},
	}
	if len(cases) != len(milestone7Reserved) {
		t.Errorf("%d reserved opt-ins, %d test cases: keep them in step", len(milestone7Reserved), len(cases))
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			checkErr(t, validating(c.tweak), c.want)
		})
	}
	// Outside the instance scope a reserved key is not refused: a command
	// that does not serve does not honour it either way (R-34).
	cfg := Default()
	cfg.Hardening.DisableRegistryPush = true
	if err := cfg.validate(ScopeState); err != nil {
		t.Errorf("state scope refused a reserved key it does not use: %v", err)
	}
}

// TestMilestone7Validation checks the syntax of the milestone-7 keys,
// reserved or not: a malformed value is refused at every scope.
func TestMilestone7Validation(t *testing.T) {
	cases := map[string]struct {
		tweak func(*Config)
		want  []string
	}{
		"zero log window":    {func(c *Config) { c.Diagnostics.LogRecords = 0 }, []string{"diagnostics.logRecords"}},
		"huge log window":    {func(c *Config) { c.Diagnostics.LogRecords = 1 << 20 }, []string{"diagnostics.logRecords"}},
		"zero task count":    {func(c *Config) { c.Diagnostics.Tasks = 0 }, []string{"diagnostics.tasks"}},
		"negative tolerance": {func(c *Config) { c.Clock.SkewTolerance = Duration(-time.Second) }, []string{"clock.skewTolerance", "cannot be switched off"}},
		"month tolerance":    {func(c *Config) { c.Clock.SkewTolerance = Duration(31 * 24 * time.Hour) }, []string{"clock.skewTolerance"}},
		"registry URL":       {func(c *Config) { c.Consumption.Registry = "https://registry.zone.example" }, []string{"consumption.registry", "not a URL"}},
		"registry path":      {func(c *Config) { c.Consumption.Registry = "registry.zone.example/team" }, []string{"consumption.registry", "repository path"}},
		"relative CA":        {func(c *Config) { c.Consumption.CAFile = "ca.crt" }, []string{"consumption.caFile", "absolute"}},
		"VEX URL":            {func(c *Config) { c.Scan.VEX = []VEXDocument{{Path: "https://vex.example/doc.json"}} }, []string{"scan.vex[0].path", "no URL", "scan.vex[0].expires is required"}},
		"scope digest": {func(c *Config) {
			c.Scan.Exceptions = []ScanException{{ID: "CVE-2024-12345", Scope: ExceptionScope{Digest: "sha256:abc"}}}
		}, []string{"scan.exceptions[0].scope.digest"}},
		"scope repository": {func(c *Config) {
			c.Scan.Exceptions = []ScanException{{ID: "CVE-2024-12345", Scope: ExceptionScope{Repository: "docker.io/library/nginx:1.27"}}}
		}, []string{"scan.exceptions[0].scope.repository"}},
		"scope recipe": {func(c *Config) {
			c.Scan.Exceptions = []ScanException{{ID: "CVE-2024-12345", Scope: ExceptionScope{Recipe: "wordpress@"}}}
		}, []string{"scan.exceptions[0].scope.recipe"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			checkErr(t, validating(c.tweak), c.want)
		})
	}
	ok := ExceptionScope{
		Digest:     "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Repository: "docker.io/library/**",
		Recipe:     "wordpress@6.5.2",
	}
	if errs := ok.validate("s"); len(errs) != 0 {
		t.Errorf("a well-formed scope is refused: %v", errs)
	}
}
