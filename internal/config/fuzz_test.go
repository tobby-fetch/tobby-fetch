// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The configuration file is operator input read before anything else
// runs: a hostile or merely broken file must produce a configuration or a
// named error, never a panic, and a configuration that loads must be one
// that validates again.
//
//	go test -run '^$' -fuzz FuzzLoad ./internal/config/
func FuzzLoad(f *testing.F) {
	for _, seed := range []string{
		"mode: mirror\nstorage:\n  root: /var/lib/tobby/store\nstate:\n  root: /var/lib/tobby/state\nauth:\n  disabled: true\n",
		"mode: passthrough\nserver:\n  addr: \":8080\"\nregistries:\n  insecure: [a:5000]\n  substitutions:\n    \"a:5000\": \"b:8080\"\n",
		"mode: mirror\nstorage:\n  occupancyThreshold: 64GiB\ntasks:\n  keepFinished: 30s\n",
		"scan:\n  exceptions:\n    - id: CVE-2026-0001\n      expires: 2027-01-01T00:00:00Z\n      justification: x\n",
		"mode: [1,2]\n", "unknown: 1\n", "&a [*a]\n", "{{{{", "", "\x00\x01",
	} {
		f.Add([]byte(seed))
	}
	dir := f.TempDir()
	path := filepath.Join(dir, "config.yaml")
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path, true)
		if err != nil {
			return
		}
		if cfg.Mode != "" && cfg.Mode != "mirror" && cfg.Mode != "passthrough" {
			t.Fatalf("a loaded configuration carries mode %q", cfg.Mode)
		}
	})
}

// FuzzParseQuantities covers the two scalar parsers every size and
// interval setting goes through.
func FuzzParseQuantities(f *testing.F) {
	for _, s := range []string{"64MiB", "512kB", "0", "-1", "9223372036854775807", "1e9", " 3 GiB ", "30s", "1h30m", "", "∞"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if sz, err := ParseSize(s); err == nil && sz < 0 {
			t.Fatalf("ParseSize(%q) = %d: negative sizes are refused", s, sz)
		}
		_, _ = ParseDuration(s)
	})
}
