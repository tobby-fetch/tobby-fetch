// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"strings"
	"testing"
	"time"
)

// TestDestinationDefaults: a fresh configuration promotes nothing and
// reconciles every quarter hour — the two halves of an instance that has
// not been told where to push yet.
func TestDestinationDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Destination.Configured() {
		t.Errorf("a default configuration declares a destination: %+v", cfg.Destination)
	}
	if cfg.Destination.Cookbook != DefaultCookbook {
		t.Errorf("default cookbook = %q, want %q", cfg.Destination.Cookbook, DefaultCookbook)
	}
	if time.Duration(cfg.Sync.Interval) != 15*time.Minute {
		t.Errorf("default sync.interval = %v, want 15m", cfg.Sync.Interval)
	}
}

// TestDestinationFromFileAndEnvironment covers the FR-003 layering on the
// new section, environment over file.
func TestDestinationFromFileAndEnvironment(t *testing.T) {
	path := writeFile(t, `
mode: passthrough
destination:
  registry: registry.example.com
  basePath: zone-a
  cookbook: zone-cookbook
sync:
  interval: 5m
`)
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Destination.Registry != "registry.example.com" ||
		cfg.Destination.BasePath != "zone-a" || cfg.Destination.Cookbook != "zone-cookbook" {
		t.Errorf("destination = %+v", cfg.Destination)
	}
	if time.Duration(cfg.Sync.Interval) != 5*time.Minute {
		t.Errorf("sync.interval = %v, want 5m", cfg.Sync.Interval)
	}

	t.Setenv(EnvDestinationRegistry, "other.example.com")
	t.Setenv(EnvDestinationBasePath, "zone-b")
	t.Setenv(EnvDestinationCookbook, "recipes")
	t.Setenv(EnvSyncInterval, "1h")
	cfg, err = Load(path, true)
	if err != nil {
		t.Fatalf("load with environment: %v", err)
	}
	if cfg.Destination.Registry != "other.example.com" ||
		cfg.Destination.BasePath != "zone-b" || cfg.Destination.Cookbook != "recipes" {
		t.Errorf("environment did not win: %+v", cfg.Destination)
	}
	if time.Duration(cfg.Sync.Interval) != time.Hour {
		t.Errorf("sync.interval = %v, want 1h", cfg.Sync.Interval)
	}
}

// TestDestinationValidation: every refusal here is a way of pushing
// somewhere the operator did not name, and an unattended service gets no
// second chance to notice.
func TestDestinationValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{
			name: "a URL instead of a host",
			mut:  func(c *Config) { c.Destination.Registry = "https://registry.example.com" },
			want: "bare registry host, not a URL",
		},
		{
			name: "a host carrying a path",
			mut:  func(c *Config) { c.Destination.Registry = "registry.example.com/zone-a" },
			want: "relocation convention",
		},
		{
			name: "a reference instead of a host",
			mut:  func(c *Config) { c.Destination.Registry = "registry.example.com@sha256:abc" },
			want: "not a reference",
		},
		{
			name: "a base path with a leading separator",
			mut: func(c *Config) {
				c.Destination.Registry = "registry.example.com"
				c.Destination.BasePath = "/zone-a"
			},
			want: "destination.basePath",
		},
		{
			name: "a traversal in the cookbook path",
			mut: func(c *Config) {
				c.Destination.Registry = "registry.example.com"
				c.Destination.Cookbook = "../elsewhere"
			},
			want: "path traversal",
		},
		{
			name: "an uppercase cookbook path",
			mut: func(c *Config) {
				c.Destination.Registry = "registry.example.com"
				c.Destination.Cookbook = "Cookbook"
			},
			want: "OCI name grammar",
		},
		{
			name: "a base path with no destination to apply it to",
			mut:  func(c *Config) { c.Destination.BasePath = "zone-a" },
			want: "silently unused",
		},
		{
			name: "a cookbook with no destination to propagate to",
			mut:  func(c *Config) { c.Destination.Cookbook = "zone-cookbook" },
			want: "FR-034",
		},
		{
			name: "a negative interval",
			mut:  func(c *Config) { c.Sync.Interval = Duration(-time.Minute) },
			want: "sync.interval must not be negative",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Mode = ModePassthrough
			tc.mut(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%+v was accepted", cfg.Destination)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestDestinationAcceptsTheOrdinaryShapes: the validation must not be so
// eager that it refuses what a zone actually deploys.
func TestDestinationAcceptsTheOrdinaryShapes(t *testing.T) {
	for _, d := range []Destination{
		{Registry: "registry.example.com"},
		{Registry: "registry.example.com:5000", BasePath: "zone-a", Cookbook: "cookbook"},
		{Registry: "localhost:5000", BasePath: "a/b/c", Cookbook: "team-1/recipes"},
	} {
		cfg := Default()
		cfg.Mode = ModePassthrough
		cfg.Destination = d
		if err := cfg.Validate(); err != nil {
			t.Errorf("%+v was refused: %v", d, err)
		}
	}
}

// TestDestinationSurvivesTheConfigurationDump: the new section carries no
// secret, and it must be readable back from `tobby config dump` (FR-003).
func TestDestinationSurvivesTheConfigurationDump(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModePassthrough
	cfg.Destination = Destination{Registry: "registry.example.com", BasePath: "zone-a", Cookbook: "cookbook"}
	cfg.Sync.Interval = Duration(30 * time.Minute)

	out, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"registry: registry.example.com", "basePath: zone-a", "interval: 30m0s"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump misses %q:\n%s", want, out)
		}
	}
}
