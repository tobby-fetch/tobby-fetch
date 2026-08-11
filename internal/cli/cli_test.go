// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// run executes the CLI with args and returns stdout and the error.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := New()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVersionPrintsBuildInfo(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "tobby ") {
		t.Errorf("version output = %q, want tobby …", out)
	}
}

// TestConfigDumpMergesLayers is the FR-003 acceptance path through the real
// CLI: file, environment, and flags merge with the documented precedence and
// the effective configuration is dumpable.
func TestConfigDumpMergesLayers(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("mode: passthrough\nserver:\n  addr: \":7000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvServerAddr, ":7001")

	out, err := run(t, "config", "dump", "--config", path, "--log-level", "debug")
	if err != nil {
		t.Fatal(err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("dump is not YAML: %v\n%s", err, out)
	}
	if cfg.Mode != config.ModePassthrough {
		t.Errorf("mode = %q, want passthrough (file)", cfg.Mode)
	}
	if cfg.Server.Addr != ":7001" {
		t.Errorf("server.addr = %q, want :7001 (environment over file)", cfg.Server.Addr)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want debug (flag)", cfg.Logging.Level)
	}
}

// TestServeRefusesInvalidMode is the FR-001 acceptance criterion: any mode
// value other than the two documented ones fails startup with an explicit
// error and a non-zero exit.
func TestServeRefusesInvalidMode(t *testing.T) {
	_, err := run(t, "serve", "--mode", "sideways", "--storage-root", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `unknown mode "sideways"`) {
		t.Errorf("serve --mode sideways error = %v, want explicit unknown-mode error", err)
	}

	_, err = run(t, "serve", "--storage-root", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "mode is required") {
		t.Errorf("serve without mode error = %v, want mode-required error", err)
	}
}

func TestConfigDumpUnknownFlagFails(t *testing.T) {
	_, err := run(t, "config", "dump", "--no-such-flag")
	if err == nil {
		t.Error("unknown flag must fail")
	}
}
