// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
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

// TestExitCodeClasses locks the FR-066 mapping: taxonomy classes decide the
// process exit code, and the full published table ships with R-08.
func TestExitCodeClasses(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"plain error", os.ErrNotExist, 1},
		{"operational", taxonomy.New(taxonomy.CodeRegistryAuth, taxonomy.Params{"host": "h"}), 1},
		{"policy", taxonomy.New(taxonomy.CodeNoAccount, nil), 3},
		{"verification", taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{"recipe": "r"}), 4},
		{"wrapped taxonomy error", fmt.Errorf("startup: %w", taxonomy.New(taxonomy.CodeNoAccount, nil)), 3},
	}
	for _, tc := range cases {
		if got := exitCodeFor(tc.err); got != tc.want {
			t.Errorf("%s: exit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestUserAddFirstAccountIsAdmin drives `tobby user add` end to end through
// the CLI (R-01): hash computed by the tool, first account admin by
// default, refusal of a non-admin first account, then a viewer second
// account listed correctly.
func TestUserAddFirstAccountIsAdmin(t *testing.T) {
	state := t.TempDir()

	run := func(stdin string, args ...string) (string, error) {
		root := New()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}

	// A non-admin first account is refused: the instance must stay
	// administrable.
	if _, err := run("pw\n", "user", "add", "eve", "--role", "viewer",
		"--state-root", state, "--password-stdin"); err == nil {
		t.Fatal("first account with --role viewer must be refused")
	}

	if _, err := run("pw-admin\n", "user", "add", "alexis",
		"--state-root", state, "--password-stdin"); err != nil {
		t.Fatalf("first user add: %v", err)
	}
	if _, err := run("pw-view\n", "user", "add", "lecteur", "--role", "viewer",
		"--state-root", state, "--password-stdin"); err != nil {
		t.Fatalf("second user add: %v", err)
	}

	// No --mode anywhere above: `tobby user` only uses the state directory,
	// and per-command validation must not demand more (R-34, B-006).
	out, err := run("", "user", "list", "--state-root", state)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alexis") || !strings.Contains(out, "admin") ||
		!strings.Contains(out, "lecteur") || !strings.Contains(out, "viewer") {
		t.Errorf("user list output:\n%s", out)
	}

	// The store on disk verifies the passwords the CLI hashed.
	s, err := auth.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyPassword("alexis", "pw-admin", time.Now()); !ok {
		t.Error("CLI-created admin password refused")
	}
}

// TestUserPasswdStdin drives `tobby user passwd --password-stdin`: the
// changed password verifies, the old one no longer does (R-01 CLI-only
// password change at this milestone).
func TestUserPasswdStdin(t *testing.T) {
	state := t.TempDir()

	run := func(stdin string, args ...string) error {
		root := New()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(args)
		return root.Execute()
	}

	if err := run("pw-one\n", "user", "add", "alexis",
		"--state-root", state, "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	if err := run("pw-two\n", "user", "passwd", "alexis",
		"--state-root", state, "--password-stdin"); err != nil {
		t.Fatalf("user passwd: %v", err)
	}
	// An empty stdin password is refused.
	if err := run("\n", "user", "passwd", "alexis",
		"--state-root", state, "--password-stdin"); err == nil {
		t.Error("empty password must be refused")
	}

	s, err := auth.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyPassword("alexis", "pw-two", time.Now()); !ok {
		t.Error("new password refused after passwd")
	}
	if _, ok := s.VerifyPassword("alexis", "pw-one", time.Now()); ok {
		t.Error("old password still accepted after passwd")
	}
}

// TestUserScopedValidation (B-006): `tobby user` runs without any mode —
// but what IS set must still be coherent (an unknown mode stays refused,
// and state.root remains required with an actionable message).
func TestUserScopedValidation(t *testing.T) {
	run := func(args ...string) error {
		root := New()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		return root.Execute()
	}
	if err := run("user", "list", "--state-root", t.TempDir()); err != nil {
		t.Errorf("user list without --mode: %v", err)
	}
	if err := run("user", "list", "--state-root", t.TempDir(), "--mode", "bogus"); err == nil {
		t.Error("unknown mode must stay refused even out of scope")
	}
	err := run("user", "list")
	if err == nil || !strings.Contains(err.Error(), "state.root") {
		t.Errorf("missing state.root error = %v, want an actionable state.root message", err)
	}
}

// runProcess drives Execute() exactly as main does — through os.Args and
// the process's own standard streams — and returns what reached stderr
// together with the exit code.
func runProcess(t *testing.T, args ...string) (stderr string, code int) {
	t.Helper()
	drain := func(r *os.File) <-chan string {
		ch := make(chan string, 1)
		go func() {
			var b strings.Builder
			_, _ = io.Copy(&b, r)
			ch <- b.String()
		}()
		return ch
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outCh, errCh := drain(outR), drain(errR)

	savedArgs, savedOut, savedErr := os.Args, os.Stdout, os.Stderr
	defer func() {
		os.Args, os.Stdout, os.Stderr = savedArgs, savedOut, savedErr
		_ = outR.Close()
		_ = errR.Close()
	}()
	os.Args = append([]string{"tobby"}, args...)
	os.Stdout, os.Stderr = outW, errW

	code = Execute()

	_ = outW.Close()
	_ = errW.Close()
	<-outCh
	return <-errCh, code
}

// TestExecuteRendersErrorsAndExitCodes is the FR-066 acceptance on the real
// process entry point: a taxonomy error prints its structured what / cause /
// action form on stderr and exits with its class, a plain error keeps the
// one-line form and the operational class, and a successful command says
// nothing on stderr.
func TestExecuteRendersErrorsAndExitCodes(t *testing.T) {
	// An empty configuration file keeps the run independent of whatever
	// happens to exist at the default location on the host.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("mode: mirror\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	common := []string{"--config", cfgPath,
		"--storage-root", filepath.Join(t.TempDir(), "store"),
		"--state-root", filepath.Join(t.TempDir(), "state"),
		"--server-addr", "127.0.0.1:0"}

	// R-01: a fresh instance with no account refuses to serve. The operator
	// must get the code, the cause and the action, not a bare message.
	stderr, code := runProcess(t, append([]string{"serve"}, common...)...)
	if code != taxonomy.ExitPolicy {
		t.Errorf("exit = %d, want %d (policy refusal)", code, taxonomy.ExitPolicy)
	}
	for _, want := range []string{string(taxonomy.CodeNoAccount), "cause:", "action:", "see:"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr misses %q:\n%s", want, stderr)
		}
	}

	// A non-taxonomized failure keeps the plain "tobby: …" form and the
	// operational exit class.
	stderr, code = runProcess(t, append([]string{"serve", "--mode", "sideways"}, common...)...)
	if code != taxonomy.ExitFailure {
		t.Errorf("exit = %d, want %d (operational failure)", code, taxonomy.ExitFailure)
	}
	if !strings.HasPrefix(stderr, "tobby: ") || !strings.Contains(stderr, `unknown mode "sideways"`) {
		t.Errorf("stderr = %q, want the plain one-line form naming the mode", stderr)
	}

	// A successful command exits 0 and renders no error at all — neither
	// the taxonomy form nor the plain one.
	stderr, code = runProcess(t, "version")
	if code != taxonomy.ExitOK {
		t.Errorf("version exit = %d, want 0", code)
	}
	if strings.Contains(stderr, "tobby: ") || strings.Contains(stderr, "TBY-") {
		t.Errorf("successful command rendered an error: %q", stderr)
	}
}

// TestCLILang follows the host locale convention for CLI messages.
func TestCLILang(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "")
	if got := cliLang(); got != "en" {
		t.Errorf("default lang = %q, want en", got)
	}
	t.Setenv("LANG", "fr_FR.UTF-8")
	if got := cliLang(); got != "fr" {
		t.Errorf("LANG=fr lang = %q, want fr", got)
	}
	t.Setenv("LANG", "")
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	if got := cliLang(); got != "fr" {
		t.Errorf("LC_ALL=fr lang = %q, want fr", got)
	}
}
