// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// scriptQuickstart runs `tobby quickstart` with the dialogue scripted on
// stdin and returns the combined stdout+stderr output and the error.
func scriptQuickstart(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := New()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"quickstart"}, args...))
	err := root.Execute()
	return out.String(), err
}

// noStdinQuickstart runs `tobby quickstart` with a non-terminal file as
// input — the deterministic non-interactive path, whatever `go test`'s own
// stdin happens to be.
func noStdinQuickstart(t *testing.T, args ...string) error {
	t.Helper()
	root := New()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() })
	root.SetIn(in)
	root.SetArgs(append([]string{"quickstart"}, args...))
	return root.Execute()
}

// TestQuickstartFullDialogue drives the complete guided run on default
// answers (R-34): storage and state directories created, mode recorded,
// first admin account hashed by the tool (R-01), recap configuration file
// written, and no serve launched.
func TestQuickstartFullDialogue(t *testing.T) {
	t.Chdir(t.TempDir())

	// Answers: storage (default), state (default), mode, account name
	// (default), password (the --password-stdin line), config path
	// (default), start now (default: no).
	out, err := scriptQuickstart(t, "\n\nmirror\n\npw-quick\n\n\n", "--password-stdin")
	if err != nil {
		t.Fatalf("quickstart: %v\n%s", err, out)
	}

	// The written file carries the three answers and loads as a valid
	// serving configuration.
	cfg, err := config.Load("tobby.yaml", true)
	if err != nil {
		t.Fatalf("written configuration does not load: %v\n%s", err, out)
	}
	if cfg.Mode != config.ModeMirror {
		t.Errorf("mode = %q, want mirror", cfg.Mode)
	}
	if cfg.Storage.Root != "./storage" || cfg.State.Root != "./state" {
		t.Errorf("roots = %q / %q, want ./storage / ./state", cfg.Storage.Root, cfg.State.Root)
	}
	for _, dir := range []string{"storage", "state"} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("directory %s was not created: %v", dir, err)
		}
	}

	// The account exists with role admin and the CLI-computed hash.
	store, err := auth.Open("./state")
	if err != nil {
		t.Fatal(err)
	}
	account, ok := store.VerifyPassword("admin", "pw-quick", time.Now())
	if !ok || account.Role != auth.RoleAdmin {
		t.Errorf("admin account verify = (%v, %t), want role admin", account.Role, ok)
	}

	// Declining the launch prints the recap with the exact serve command;
	// nothing was started (the command returned).
	if !strings.Contains(out, "tobby serve --config ./tobby.yaml") {
		t.Errorf("recap misses the serve command:\n%s", out)
	}
	if strings.Contains(out, "starting:") {
		t.Errorf("serve was launched despite the declined question:\n%s", out)
	}
}

// TestQuickstartFlagsSkipQuestions: every answer given by flag is not asked
// again — the dialogue only fills the actual gaps (R-34).
func TestQuickstartFlagsSkipQuestions(t *testing.T) {
	dir := t.TempDir()
	storage := filepath.Join(dir, "store")
	state := filepath.Join(dir, "state")
	cfgPath := filepath.Join(dir, "tobby.yaml")

	// Remaining questions: account name, password line, start now.
	out, err := scriptQuickstart(t, "chief\npw-flags\nn\n",
		"--mode", "passthrough", "--storage-root", storage, "--state-root", state,
		"--config", cfgPath, "--password-stdin")
	if err != nil {
		t.Fatalf("quickstart: %v\n%s", err, out)
	}
	for _, prompt := range []string{"Store directory", "State directory", "Operating mode", "Configuration file to write"} {
		if strings.Contains(out, prompt) {
			t.Errorf("question %q was asked despite its flag:\n%s", prompt, out)
		}
	}

	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModePassthrough || cfg.Storage.Root != storage || cfg.State.Root != state {
		t.Errorf("written configuration = %q %q %q, want the flag values", cfg.Mode, cfg.Storage.Root, cfg.State.Root)
	}
	store, err := auth.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.VerifyPassword("chief", "pw-flags", time.Now()); !ok {
		t.Error("account from the dialogue answers does not verify")
	}
}

// TestQuickstartNonInteractiveWithoutAnswers (R-34): without a terminal on
// stdin and with answers missing, quickstart refuses before any side effect
// and hands out the equivalent flag-driven commands — automation never
// depends on the dialogue.
func TestQuickstartNonInteractiveWithoutAnswers(t *testing.T) {
	t.Chdir(t.TempDir())

	err := noStdinQuickstart(t)
	if err == nil {
		t.Fatal("non-interactive quickstart without answers must be refused")
	}
	for _, want := range []string{"tobby user add", "tobby serve --mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not hand out %q", err, want)
		}
	}
	for _, path := range []string{"storage", "state", "tobby.yaml"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was created by the refused run", path)
		}
	}
}

// TestQuickstartNeverOverwritesConfig: an existing file at the destination
// survives the default answer and the non-interactive path; only an
// explicit yes replaces it.
func TestQuickstartNeverOverwritesConfig(t *testing.T) {
	dir := t.TempDir()
	storage := filepath.Join(dir, "store")
	state := filepath.Join(dir, "state")
	cfgPath := filepath.Join(dir, "tobby.yaml")
	precious := "# precious operator file\n"
	if err := os.WriteFile(cfgPath, []byte(precious), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--mode", "mirror", "--storage-root", storage, "--state-root", state, "--config", cfgPath}

	// The overwrite question defaults to no: an empty answer keeps the
	// file. Dialogue: account name (default), password, overwrite (empty).
	out, err := scriptQuickstart(t, "\npw-quick\n\n", append(base, "--password-stdin")...)
	if err == nil || !strings.Contains(err.Error(), "not overwriting") {
		t.Fatalf("overwrite on default answer = %v, want refusal\n%s", err, out)
	}
	got, err := os.ReadFile(cfgPath) //nolint:gosec // G304: reading back the file this test wrote in its own temp directory
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != precious {
		t.Errorf("existing file was modified:\n%s", got)
	}

	// Non-interactive, the overwrite is refused outright with a message
	// (the account exists from the run above, so the flow reaches it).
	err = noStdinQuickstart(t, base...)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("non-interactive overwrite = %v, want explicit refusal", err)
	}

	// Only an explicit yes replaces the file. Dialogue: overwrite (yes),
	// start now (default: no) — the account step is skipped by now.
	out, err = scriptQuickstart(t, "y\n\n", base...)
	if err != nil {
		t.Fatalf("quickstart with explicit overwrite: %v\n%s", err, out)
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("overwritten configuration does not load: %v", err)
	}
	if cfg.Mode != config.ModeMirror {
		t.Errorf("mode = %q, want mirror", cfg.Mode)
	}
}

// TestQuickstartRejectsStateInsideStorage: the disjoint-roots rule of the
// configuration (R-16: secrets never travel on the media) refuses a state
// directory inside the store before anything is created.
func TestQuickstartRejectsStateInsideStorage(t *testing.T) {
	dir := t.TempDir()
	storage := filepath.Join(dir, "store")
	answers := storage + "\n" + filepath.Join(storage, "state") + "\nmirror\n"

	out, err := scriptQuickstart(t, answers)
	if err == nil || !strings.Contains(err.Error(), "state.root must not live inside storage.root") {
		t.Fatalf("state inside storage = %v, want the disjoint-roots refusal\n%s", err, out)
	}
	if _, err := os.Stat(storage); !errors.Is(err, os.ErrNotExist) {
		t.Error("storage directory was created despite the refusal")
	}
}

// TestQuickstartSkipsExistingAccounts: with accounts already in the state
// directory, the account step says so and passes — no question, no second
// admin (R-01 stays a first-start concern).
func TestQuickstartSkipsExistingAccounts(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	seed, err := auth.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.AddAccount("keeper", auth.RoleAdmin, "pw-keeper", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Only remaining question: start now (default: no).
	out, err := scriptQuickstart(t, "\n",
		"--mode", "passthrough", "--storage-root", filepath.Join(dir, "store"),
		"--state-root", state, "--config", filepath.Join(dir, "tobby.yaml"))
	if err != nil {
		t.Fatalf("quickstart: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipping account creation") {
		t.Errorf("existing accounts were not announced as skipped:\n%s", out)
	}
	if strings.Contains(out, "Admin account name") {
		t.Errorf("account question asked despite existing accounts:\n%s", out)
	}
	after, err := auth.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(after.Accounts()); n != 1 {
		t.Errorf("accounts = %d, want the seeded one only", n)
	}
}
