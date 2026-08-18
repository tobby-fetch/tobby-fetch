// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseSizeAcceptsTheUnitsOperatorsWrite(t *testing.T) {
	t.Parallel()
	cases := map[string]int64{
		"0":         0,
		"4096":      4096,
		"64MiB":     64 << 20,
		"64mib":     64 << 20, // operators type both spellings
		"\t8 KiB\n": 8 << 10,  // a value copied out of a terminal, with its whitespace
		"2GiB":      2 << 30,
		"1TiB":      1 << 40,
		"512kB":     512_000,
		"5MB":       5_000_000,
		"3GB":       3_000_000_000,
		"7B":        7,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", in, err)
			continue
		}
		if got.Bytes() != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got.Bytes(), want)
		}
	}
}

func TestParseSizeRefusesWhatCannotBeAThreshold(t *testing.T) {
	t.Parallel()
	// A negative threshold has no meaning and would silently behave like
	// a different setting; so would a unit nobody defined.
	for _, in := range []string{"", "  ", "-1", "-4MiB", "12 pebibytes", "MiB", "1.5MiB", "9999999999TiB"} {
		if got, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) = %d, want an error", in, got.Bytes())
		}
	}
}

func TestSizeRoundTripsThroughYAML(t *testing.T) {
	t.Parallel()
	// A dumped configuration must read like the one that was written
	// (`tobby config dump` is an audit surface, not a debug print).
	for _, spelling := range []string{"64MiB", "1GiB", "0B", "3KiB", "4097B"} {
		var s Size
		if err := yaml.Unmarshal([]byte(spelling), &s); err != nil {
			t.Fatalf("unmarshal %q: %v", spelling, err)
		}
		raw, err := yaml.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(raw)); got != spelling {
			t.Errorf("round trip of %q produced %q", spelling, got)
		}
	}
	// A byte count that divides no binary unit exactly stays a byte count
	// rather than being rounded into a prettier lie.
	if got := Size(4097).String(); got != "4097B" {
		t.Errorf("Size(4097) = %q, want \"4097B\"", got)
	}
}

func TestSizeUnmarshalsABareInteger(t *testing.T) {
	t.Parallel()
	var cfg struct {
		N Size `yaml:"n"`
	}
	if err := yaml.Unmarshal([]byte("n: 1048576\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.N != 1<<20 {
		t.Errorf("n = %d, want %d", cfg.N.Bytes(), 1<<20)
	}
	if err := yaml.Unmarshal([]byte("n: -5\n"), &cfg); err == nil {
		t.Error("a negative byte count was accepted")
	}
	if err := yaml.Unmarshal([]byte("n: [1]\n"), &cfg); err == nil {
		t.Error("a list was accepted as a size")
	}
}

func TestResumeThresholdPrecedenceAndValidation(t *testing.T) {
	if got := Default().Transfer.ResumeThreshold; got != 64*MiB {
		t.Errorf("default resumeThreshold = %s, want 64MiB", got)
	}

	dir := t.TempDir()
	path := writeFile(t, "mode: mirror\nstorage:\n  root: "+dir+"/store\nstate:\n  root: "+dir+
		"/state\ntransfer:\n  resumeThreshold: 32MiB\n")

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transfer.ResumeThreshold != 32*MiB {
		t.Errorf("from file = %s, want 32MiB", cfg.Transfer.ResumeThreshold)
	}

	// Environment over file, as everywhere else in this package.
	t.Setenv(EnvTransferResumeMin, "8MiB")
	cfg, err = Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transfer.ResumeThreshold != 8*MiB {
		t.Errorf("the environment did not override the file: %s", cfg.Transfer.ResumeThreshold)
	}

	// Flags over environment, through the same override mechanism serve
	// uses.
	cfg, err = Load(path, true, func(c *Config) { c.Transfer.ResumeThreshold = 1 * MiB })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transfer.ResumeThreshold != 1*MiB {
		t.Errorf("an override did not win: %s", cfg.Transfer.ResumeThreshold)
	}

	t.Setenv(EnvTransferResumeMin, "not a size")
	if _, err = Load(path, true); err == nil {
		t.Error("an unparseable threshold was accepted from the environment")
	}

	// Zero is the documented off switch, so it must validate; negative
	// cannot come from the parser but can from a hand-built Config.
	c := Default()
	c.Mode, c.Storage.Root, c.State.Root = ModeMirror, dir+"/s", dir+"/st"
	c.Transfer.ResumeThreshold = 0
	if err := c.Validate(); err != nil {
		t.Errorf("resumeThreshold 0 was refused: %v", err)
	}
	c.Transfer.ResumeThreshold = -1
	if err := c.Validate(); err == nil {
		t.Error("a negative resumeThreshold was accepted")
	}
}
