// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAlone(t *testing.T) {
	cfg := Default()
	if cfg.Server.Addr != ":8080" {
		t.Errorf("default server.addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("default logging.level = %q, want info", cfg.Logging.Level)
	}
	if time.Duration(cfg.Shutdown.GracePeriod) != 30*time.Second {
		t.Errorf("default shutdown.gracePeriod = %v, want 30s", cfg.Shutdown.GracePeriod)
	}
	if cfg.Mode != "" {
		t.Errorf("default mode = %q, want empty (no default mode: FR-001)", cfg.Mode)
	}
	if time.Duration(cfg.Import.InspectTimeout) != 20*time.Second {
		t.Errorf("default import.inspectTimeout = %v, want 20s", cfg.Import.InspectTimeout)
	}
}

// TestLayerPrecedence exercises the FR-003 acceptance criterion: for a
// setting defined at all three levels the flag wins; without the flag the
// environment wins; without either the file wins.
func TestLayerPrecedence(t *testing.T) {
	path := writeFile(t, "mode: passthrough\nlogging:\n  level: warn\n")

	t.Run("file over defaults", func(t *testing.T) {
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Logging.Level != "warn" {
			t.Errorf("level = %q, want warn (file layer)", cfg.Logging.Level)
		}
	})

	t.Run("environment over file", func(t *testing.T) {
		t.Setenv(EnvLoggingLevel, "error")
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Logging.Level != "error" {
			t.Errorf("level = %q, want error (environment layer)", cfg.Logging.Level)
		}
	})

	t.Run("flag over environment and file", func(t *testing.T) {
		t.Setenv(EnvLoggingLevel, "error")
		cfg, err := Load(path, true, func(c *Config) { c.Logging.Level = "debug" })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Logging.Level != "debug" {
			t.Errorf("level = %q, want debug (flag layer)", cfg.Logging.Level)
		}
	})
}

func TestLoadEveryEnvVariable(t *testing.T) {
	path := writeFile(t, "mode: passthrough\n")
	t.Setenv(EnvMode, "mirror")
	t.Setenv(EnvStorageRoot, "/srv/store")
	t.Setenv(EnvServerAddr, ":9090")
	t.Setenv(EnvLoggingLevel, "debug")
	t.Setenv(EnvShutdownGracePeriod, "45s")
	t.Setenv(EnvImportInspectTO, "7s")

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeMirror {
		t.Errorf("mode = %q, want mirror", cfg.Mode)
	}
	if cfg.Storage.Root != "/srv/store" {
		t.Errorf("storage.root = %q, want /srv/store", cfg.Storage.Root)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("server.addr = %q, want :9090", cfg.Server.Addr)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want debug", cfg.Logging.Level)
	}
	if time.Duration(cfg.Shutdown.GracePeriod) != 45*time.Second {
		t.Errorf("gracePeriod = %v, want 45s", cfg.Shutdown.GracePeriod)
	}
	if time.Duration(cfg.Import.InspectTimeout) != 7*time.Second {
		t.Errorf("import.inspectTimeout = %v, want 7s", cfg.Import.InspectTimeout)
	}
}

func TestModeValidation(t *testing.T) {
	// FR-001: any value other than the two modes fails startup with an
	// explicit error; the mode is required.
	for _, tc := range []struct {
		yaml    string
		wantErr string
	}{
		{"", "mode is required"},
		{"mode: sideways\n", `unknown mode "sideways"`},
	} {
		path := writeFile(t, tc.yaml)
		_, err := Load(path, true)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Load(%q) error = %v, want containing %q", tc.yaml, err, tc.wantErr)
		}
	}
}

func TestUnknownFileFieldRejected(t *testing.T) {
	path := writeFile(t, "mode: mirror\ntypo_field: 1\n")
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("unknown field: error = %v, want mention of typo_field", err)
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), true)
	if err == nil {
		t.Fatal("explicitly given missing file must fail")
	}
}

func TestDefaultMissingFileTolerated(t *testing.T) {
	t.Setenv(EnvMode, "passthrough")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false)
	if err != nil {
		t.Fatalf("default-location missing file must be tolerated, got %v", err)
	}
	if cfg.Mode != ModePassthrough {
		t.Errorf("mode = %q, want passthrough (from environment)", cfg.Mode)
	}
}

func TestInvalidEnvDuration(t *testing.T) {
	path := writeFile(t, "mode: mirror\n")
	t.Setenv(EnvShutdownGracePeriod, "not-a-duration")
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), EnvShutdownGracePeriod) {
		t.Errorf("error = %v, want mention of %s", err, EnvShutdownGracePeriod)
	}
}

func TestValidateGracePeriodPositive(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Shutdown.GracePeriod = Duration(-time.Second)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "gracePeriod") {
		t.Errorf("Validate() = %v, want gracePeriod error", err)
	}
}

func TestValidateInspectTimeoutPositive(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Import.InspectTimeout = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "import.inspectTimeout") {
		t.Errorf("Validate() = %v, want import.inspectTimeout error", err)
	}
}

func TestDumpRoundTrips(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Storage.Root = "/srv/store"

	out, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	dec := yaml.NewDecoder(strings.NewReader(out))
	dec.KnownFields(true)
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("dump does not parse back strictly: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, cfg) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", back, cfg)
	}
}

func TestDurationYAML(t *testing.T) {
	var s Shutdown
	if err := yaml.Unmarshal([]byte(`gracePeriod: 1m30s`), &s); err != nil {
		t.Fatal(err)
	}
	if time.Duration(s.GracePeriod) != 90*time.Second {
		t.Errorf("parsed %v, want 1m30s", s.GracePeriod)
	}
	if err := yaml.Unmarshal([]byte(`gracePeriod: ninety`), &s); err == nil {
		t.Error("invalid duration must fail")
	}
	if err := yaml.Unmarshal([]byte(`gracePeriod: [1, 2]`), &s); err == nil {
		t.Error("non-scalar duration must fail")
	}
}

// TestSecretNeverSerializes is the NFR-015 unit test: no serialization path
// may reveal a Secret's value.
func TestSecretNeverSerializes(t *testing.T) {
	const sensitive = "hunter2-swordfish"
	s := NewSecret(sensitive)

	yamlOut, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	jsonOut, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	structOut, err := yaml.Marshal(struct {
		Token Secret `yaml:"token"`
	}{s})
	if err != nil {
		t.Fatal(err)
	}

	renderings := map[string]string{
		"String":         s.String(),
		"fmt %v":         fmt.Sprintf("%v", s), //nolint:gocritic // redundantSprint: the fmt verb path is exactly what is under test
		"fmt %+v":        fmt.Sprintf("%+v", s),
		"fmt %#v":        fmt.Sprintf("%#v", s),
		"fmt %s":         fmt.Sprintf("%s", s), //nolint:gocritic,staticcheck // redundantSprint/S1025: the fmt verb path is exactly what is under test
		"yaml":           string(yamlOut),
		"json":           string(jsonOut),
		"yaml in struct": string(structOut),
	}
	for name, out := range renderings {
		if strings.Contains(out, sensitive) {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
	}

	if s.Reveal() != sensitive {
		t.Error("Reveal must return the value")
	}
	if NewSecret("").String() != "" {
		t.Error("empty secret renders empty, not REDACTED")
	}
}

// TestDisjointRoots is the R-16 guard: the state directory never lives
// inside the transportable store, nor the reverse.
func TestDisjointRoots(t *testing.T) {
	base := t.TempDir()
	for _, tc := range []struct {
		name, state, storage, wantErr string
	}{
		{"disjoint", filepath.Join(base, "state"), filepath.Join(base, "store"), ""},
		{"equal", base, base, "must differ"},
		{"state inside storage", filepath.Join(base, "sub"), base, "must not live inside storage.root"},
		{"storage inside state", base, filepath.Join(base, "sub"), "must not live inside state.root"},
		{"empty state", "", base, ""},
	} {
		err := disjointRoots(tc.state, tc.storage)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestDurationHelpers covers the scalar Duration round trip.
func TestDurationHelpers(t *testing.T) {
	d, err := ParseDuration("1m30s")
	if err != nil || time.Duration(d) != 90*time.Second {
		t.Errorf("ParseDuration = %v, %v", d, err)
	}
	if _, err := ParseDuration("bogus"); err == nil {
		t.Error("invalid duration must fail")
	}
	if s := Duration(20 * time.Second).String(); s != "20s" {
		t.Errorf("String() = %q, want 20s", s)
	}
}

// TestInvalidImportEnvDuration rejects a malformed inspect timeout.
func TestInvalidImportEnvDuration(t *testing.T) {
	path := writeFile(t, "mode: mirror\n")
	t.Setenv(EnvImportInspectTO, "soon")
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), EnvImportInspectTO) {
		t.Errorf("error = %v, want mention of %s", err, EnvImportInspectTO)
	}
}
