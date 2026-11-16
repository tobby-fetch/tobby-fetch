// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHardeningSwitchesAreHonouredAndDumped locks the configuration half
// of FR-098 (R-17): both switches are accepted at instance scope now that
// the feature exists, read from the file, and shown by the configuration
// dump with their value — false included, since "off" must be read, not
// inferred from an absent key.
func TestHardeningSwitchesAreHonouredAndDumped(t *testing.T) {
	cfg := Default()
	out, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hardening:", "disableUnitImport: false", "disableRegistryPush: false"} {
		if !strings.Contains(out, want) {
			t.Errorf("default dump misses %q:\n%s", want, out)
		}
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "mode: mirror\nhardening:\n  disableUnitImport: true\n  disableRegistryPush: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, true)
	if err != nil {
		t.Fatalf("an instance with both switches set is refused: %v", err)
	}
	if !loaded.Hardening.DisableUnitImport || !loaded.Hardening.DisableRegistryPush {
		t.Fatalf("switches not read from the file: %+v", loaded.Hardening)
	}
	out, err = loaded.Dump()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"disableUnitImport: true", "disableRegistryPush: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("dump misses %q:\n%s", want, out)
		}
	}
}
