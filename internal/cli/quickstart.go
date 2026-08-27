// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// Defaults proposed by the quickstart dialogue. Relative paths keep the
// first run self-contained in the invocation directory.
const (
	quickstartDefaultStorage = "./storage"
	quickstartDefaultState   = "./state"
	quickstartDefaultConfig  = "./tobby.yaml"
	quickstartDefaultName    = "admin"
)

// errQuickstartNonInteractive refuses the dialogue when standard input
// cannot answer it (R-34): quickstart is a local aid, never a requirement —
// the message hands out the equivalent flag-driven commands so automation
// and containers keep working without it.
var errQuickstartNonInteractive = errors.New(`standard input is not a terminal and answers are missing: quickstart is an interactive aid, never a requirement — script the same setup with flags:
  tobby user add admin --state-root ./state --password-stdin
  tobby serve --mode <passthrough|mirror> --storage-root ./storage --state-root ./state`)

// quickstartFlags carries the pre-answers: a flag already given skips its
// question, so a partially scripted run only asks for the actual gaps.
type quickstartFlags struct {
	configPath    string
	mode          string
	storageRoot   string
	stateRoot     string
	passwordStdin bool
	serve         bool
}

// newQuickstartCmd wires `tobby quickstart` (R-34, extending R-01): an
// interactive helper that fills the configuration gaps one by one — store
// directory, state directory, mode, first admin account, recap
// configuration file — for a guided, secure first start, then optionally
// hands over to serve.
//
// Quickstart deliberately does not read the layered configuration
// (environment, existing file): it gathers explicit answers and writes them
// down. Only flags pre-answer questions; everything else is asked.
func newQuickstartCmd() *cobra.Command {
	flags := &quickstartFlags{}
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Guided first start: answer a few questions, get a serving instance (R-34)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuickstart(cmd, flags)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&flags.configPath, "config", "", "destination of the written configuration file (default "+quickstartDefaultConfig+")")
	fs.StringVar(&flags.mode, "mode", "", `operating mode: "passthrough" or "mirror"`)
	fs.StringVar(&flags.storageRoot, "storage-root", "", "directory of the self-contained store (default "+quickstartDefaultStorage+")")
	fs.StringVar(&flags.stateRoot, "state-root", "", "directory of the instance state (accounts, tokens) — outside the store (default "+quickstartDefaultState+")")
	fs.BoolVar(&flags.passwordStdin, "password-stdin", false, "read the admin password from one line of standard input (for automation)")
	fs.BoolVar(&flags.serve, "serve", false, "start the instance right after writing the configuration")
	return cmd
}

// runQuickstart drives the dialogue. Prompts go to stderr and answers come
// from the command input — stdout stays machine-only, like `tobby user` —
// so tests script the whole exchange with buffers.
func runQuickstart(cmd *cobra.Command, flags *quickstartFlags) error {
	d := newDialogue(cmd)
	errW := cmd.ErrOrStderr()

	storageRoot, err := d.answer("storage-root", flags.storageRoot,
		"Store directory (storage root)", quickstartDefaultStorage)
	if err != nil {
		return err
	}
	stateRoot, err := d.answer("state-root", flags.stateRoot,
		"State directory (state root)", quickstartDefaultState)
	if err != nil {
		return err
	}
	// The mode has no default: an instance must state what it is (FR-001).
	mode, err := d.answer("mode", flags.mode,
		`Operating mode ("passthrough" or "mirror")`, "")
	if err != nil {
		return err
	}

	// The config package stays the single validation authority (FR-003): a
	// missing or unknown mode and a state directory inside the store
	// (disjoint roots, R-16) are refused with its own actionable messages,
	// before anything is created on disk.
	cfg := config.Default()
	cfg.Mode = config.Mode(mode)
	cfg.Storage.Root = storageRoot
	cfg.State.Root = stateRoot
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Missing directories are created: storage 0750 with the same write
	// probe serve runs (FR-092), state 0700 by auth.Open (NFR-018).
	if err := ensureWritableDir(storageRoot); err != nil {
		return fmt.Errorf("storage root: %w", err)
	}
	store, err := auth.Open(stateRoot)
	if err != nil {
		return err
	}

	if err := d.createFirstAccount(store, flags.passwordStdin); err != nil {
		return err
	}

	configPath, err := d.answer("config", flags.configPath,
		"Configuration file to write", quickstartDefaultConfig)
	if err != nil {
		return err
	}
	if err := d.writeConfigFile(configPath, &cfg); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(errW, "configuration written to %s\n", configPath)

	serveNow := flags.serve
	if !flags.serve {
		serveNow, err = d.confirm("Start the instance now", false, nil)
		if err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(errW, "\nSetup complete:\n  mode:          %s\n  storage root:  %s\n  state root:    %s\n  configuration: %s\n",
		mode, storageRoot, stateRoot, configPath)
	if !serveNow {
		_, _ = fmt.Fprintf(errW, "Start the instance with:\n  tobby serve --config %s\n", configPath)
		return nil
	}

	// Handing over to serve reuses the exact `tobby serve --config <file>`
	// path: newServeCmd's RunE is load-then-runServe, and with only
	// --config set its load reduces to config.Load(path, explicit). The
	// freshly written file goes through the same layered load (defaults,
	// file, environment) any later start will use — no in-process cobra
	// re-invocation, no serve.go change.
	_, _ = fmt.Fprintf(errW, "starting: tobby serve --config %s\n", configPath)
	serveCfg, err := config.Load(configPath, true)
	if err != nil {
		return err
	}
	return runServe(cmd.Context(), &serveCfg)
}

// dialogue asks the quickstart questions: answers are read line by line
// from the command input, prompts go to stderr. interactive reports whether
// questions may be asked at all — a replaced input (tests, embedding
// callers) is scriptable, the process stdin only when it is a real
// terminal. A plain pipe stays non-interactive: automation must use flags,
// never a brittle answer script (R-34).
type dialogue struct {
	cmd         *cobra.Command
	in          *bufio.Reader
	interactive bool
}

func newDialogue(cmd *cobra.Command) *dialogue {
	in := cmd.InOrStdin()
	interactive := true
	if f, ok := in.(*os.File); ok {
		interactive = term.IsTerminal(int(f.Fd()))
	}
	return &dialogue{cmd: cmd, in: bufio.NewReader(in), interactive: interactive}
}

// answer returns the flag value when the flag was given — a provided flag
// skips its question — and asks otherwise.
func (d *dialogue) answer(flag, value, prompt, def string) (string, error) {
	if d.cmd.Flags().Changed(flag) {
		return value, nil
	}
	return d.ask(prompt, def)
}

// ask prints the prompt with the editable default and returns the trimmed
// answer, or the default on an empty line. Without an interactive input the
// default is taken as-is, and a question without one is the R-34 refusal:
// automation is never blocked on a prompt.
func (d *dialogue) ask(prompt, def string) (string, error) {
	if !d.interactive {
		if def == "" {
			return "", errQuickstartNonInteractive
		}
		return def, nil
	}
	if def != "" {
		_, _ = fmt.Fprintf(d.cmd.ErrOrStderr(), "%s [%s]: ", prompt, def)
	} else {
		_, _ = fmt.Fprintf(d.cmd.ErrOrStderr(), "%s: ", prompt)
	}
	line, err := d.readLine()
	if err != nil {
		return "", err
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

// confirm asks a yes/no question; def is the empty-answer outcome. When the
// input cannot answer, nonInteractive decides: nil takes the default,
// anything else is returned as the refusal.
func (d *dialogue) confirm(prompt string, def bool, nonInteractive error) (bool, error) {
	if !d.interactive {
		if nonInteractive != nil {
			return false, nonInteractive
		}
		return def, nil
	}
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		_, _ = fmt.Fprintf(d.cmd.ErrOrStderr(), "%s [%s]: ", prompt, hint)
		line, err := d.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(line) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		// Anything else: ask again — a yes/no answer must be explicit.
	}
}

// readLine reads one answer line from the shared buffered input. Running
// out of scripted answers is an explicit error, never a silent default.
func (d *dialogue) readLine() (string, error) {
	line, err := d.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading answer: %w", err)
	}
	if line == "" && errors.Is(err, io.EOF) {
		return "", errors.New("reading answer: unexpected end of input")
	}
	return strings.TrimSpace(line), nil
}

// password collects the first admin password. The terminal path is `tobby
// user add`'s readPassword: echo off, asked twice (R-01).
func (d *dialogue) password(fromStdin bool) (string, error) {
	if !fromStdin {
		if !d.interactive {
			return "", errQuickstartNonInteractive
		}
		return readPassword(d.cmd, false, true)
	}
	// --password-stdin: the password is one line of the same scripted input
	// as the other answers. It is read from the shared buffered reader —
	// not through readPassword, whose Scanner would buffer the stream past
	// the password line and starve the questions that follow. Semantics
	// match readPassword's stdin branch: line break trimmed, empty refused.
	line, err := d.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading password from stdin: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("password must not be empty")
	}
	return pw, nil
}

// createFirstAccount creates the first admin account when the store has
// none (R-01): name editable, hash computed by the tool, audit record on
// stdout exactly like `tobby user add` (FR-094). With accounts already
// present the step says so and passes.
func (d *dialogue) createFirstAccount(store *auth.Store, passwordStdin bool) error {
	if store.HasAccounts() {
		_, _ = fmt.Fprintln(d.cmd.ErrOrStderr(), "local accounts already exist: skipping account creation")
		return nil
	}
	name, err := d.ask("Admin account name", quickstartDefaultName)
	if err != nil {
		return err
	}
	password, err := d.password(passwordStdin)
	if err != nil {
		return err
	}
	if err := store.AddAccount(name, auth.RoleAdmin, password, time.Now()); err != nil {
		return err
	}
	auditLocal(d.cmd, &audit.Event{
		Actor: audit.ActorLocal, Action: audit.ActionAccountCreate,
		Target: name, Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
	})
	_, _ = fmt.Fprintf(d.cmd.ErrOrStderr(), "account %q created with role admin\n", name)
	return nil
}

// writeConfigFile records the gathered answers — mode, storage.root,
// state.root, nothing else — as YAML at path. An existing file is never
// replaced without an explicit yes; without an interactive input the
// overwrite is refused with an actionable message.
func (d *dialogue) writeConfigFile(path string, cfg *config.Config) error {
	switch _, err := os.Stat(path); {
	case err == nil:
		overwrite, cerr := d.confirm(
			fmt.Sprintf("File %s already exists — overwrite it", path), false,
			fmt.Errorf("refusing to overwrite %s: overwriting needs an explicit confirmation and standard input is not a terminal (pick another --config path or remove the file)", path))
		if cerr != nil {
			return cerr
		}
		if !overwrite {
			return fmt.Errorf("not overwriting %s: rerun with another --config path or remove the file", path)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("checking %s: %w", path, err)
	}

	// Only the three answers, never the full effective configuration: the
	// file must read as what the operator chose, and the built-in defaults
	// stay live underneath (FR-003).
	minimal := struct {
		Mode    config.Mode `yaml:"mode"`
		Storage struct {
			Root string `yaml:"root"`
		} `yaml:"storage"`
		State struct {
			Root string `yaml:"root"`
		} `yaml:"state"`
	}{Mode: cfg.Mode}
	minimal.Storage.Root = cfg.Storage.Root
	minimal.State.Root = cfg.State.Root
	out, err := yaml.Marshal(minimal)
	if err != nil {
		return fmt.Errorf("rendering configuration: %w", err)
	}
	// 0600 like every operator-owned instance file (NFR-018): the file
	// holds no secret but stays private by default.
	return os.WriteFile(path, out, 0o600)
}
