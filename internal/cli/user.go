// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
)

// newUserCmd wires `tobby user` (R-01): local accounts are created and
// updated on the host, the tool computes the argon2id hash — a password
// hash is never written by hand. Human feedback and the audit record go
// to stderr; stdout carries the report and nothing else (R-08, B-010).
func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage the instance's local accounts (R-01)",
	}
	cmd.AddCommand(newUserAddCmd(), newUserPasswdCmd(), newUserListCmd())
	return cmd
}

// userFlags are shared by the user subcommands: they need the state
// directory, resolved through the same configuration layers as serve.
type userFlags struct {
	commonFlags
	report *reportFlag
	stdin  bool
}

func newUserFlags() *userFlags {
	return &userFlags{report: newReportFlag(outputText, outputJSON)}
}

func (f *userFlags) register(cmd *cobra.Command) {
	f.commonFlags.register(cmd)
	f.report.register(cmd)
	cmd.Flags().BoolVar(&f.stdin, "password-stdin", false,
		"read the password from the first line of standard input (for automation)")
}

// accountReport is the machine form of one account operation, and of one
// row of `tobby user list` (R-08). It never carries anything derived from
// the password: a hash is not a report (NFR-015).
type accountReport struct {
	Name string `json:"name"`
	Role string `json:"role"`
	// First marks the account that made this instance administrable, the
	// one R-01 forces to be an admin. Only `add` sets it.
	First     bool   `json:"first,omitempty"`
	Created   string `json:"created,omitempty"`
	LastLogin string `json:"lastLogin,omitempty"`
}

// accountListReport is the machine form of `tobby user list`. An object
// rather than a bare array: a document that can grow a field without
// breaking every reader is the whole point of the R-08 contract.
type accountListReport struct {
	Accounts []accountReport `json:"accounts"`
}

// stateStore opens the account store from the resolved configuration.
// Validation is scoped to what `tobby user` actually uses: the state
// directory — never a mode (R-34, B-006).
func (f *userFlags) stateStore(cmd *cobra.Command) (*auth.Store, error) {
	cfg, err := f.loadFor(cmd, config.ScopeState)
	if err != nil {
		return nil, err
	}
	if cfg.State.Root == "" {
		return nil, fmt.Errorf("state.root is required: set --state-root, %s, or \"state.root:\" in the configuration file", config.EnvStateRoot)
	}
	return auth.Open(cfg.State.Root)
}

func newUserAddCmd() *cobra.Command {
	flags := newUserFlags()
	var roleFlag string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a local account (the first account defaults to admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.report.validate(cmd); err != nil {
				return err
			}
			store, err := flags.stateStore(cmd)
			if err != nil {
				return err
			}
			name := args[0]

			// R-01: the first account of an instance is admin unless stated
			// otherwise — a fresh instance must end up administrable.
			role := auth.RoleViewer
			first := !store.HasAccounts()
			if first {
				role = auth.RoleAdmin
			}
			if cmd.Flags().Changed("role") {
				if role, err = auth.ParseRole(roleFlag); err != nil {
					return err
				}
			}
			if first && role != auth.RoleAdmin {
				return fmt.Errorf("the first account must be admin: an instance without an administrator cannot be managed (drop --role or use --role admin)")
			}

			password, err := readPassword(cmd, flags.stdin, true)
			if err != nil {
				return err
			}
			if err := store.AddAccount(name, role, password, time.Now()); err != nil {
				return err
			}
			auditLocal(cmd, &audit.Event{
				Actor: audit.ActorLocal, Action: audit.ActionAccountCreate,
				Target: name, Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
			})
			if flags.report.json() {
				return writeJSON(cmd, accountReport{Name: name, Role: string(role), First: first})
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "account %q created with role %s\n", name, role)
			if first {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "first account of this instance: role admin\n")
			}
			return nil
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&roleFlag, "role", "", "account role: viewer, operator, or admin (default: admin for the first account, viewer afterwards)")
	return cmd
}

func newUserPasswdCmd() *cobra.Command {
	flags := newUserFlags()
	cmd := &cobra.Command{
		Use:   "passwd <name>",
		Short: "Change a local account's password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.report.validate(cmd); err != nil {
				return err
			}
			store, err := flags.stateStore(cmd)
			if err != nil {
				return err
			}
			password, err := readPassword(cmd, flags.stdin, true)
			if err != nil {
				return err
			}
			if err := store.SetPassword(args[0], password, time.Now()); err != nil {
				return err
			}
			auditLocal(cmd, &audit.Event{
				Actor: audit.ActorLocal, Action: audit.ActionAccountPasswd,
				Target: args[0], Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
			})
			if flags.report.json() {
				acct, _ := store.Account(args[0])
				return writeJSON(cmd, accountReport{Name: args[0], Role: string(acct.Role)})
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "password of %q updated\n", args[0])
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}

func newUserListCmd() *cobra.Command {
	flags := newUserFlags()
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the local accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := flags.report.validate(cmd); err != nil {
				return err
			}
			store, err := flags.stateStore(cmd)
			if err != nil {
				return err
			}
			if flags.report.json() {
				list := accountListReport{Accounts: []accountReport{}}
				for _, a := range store.Accounts() {
					row := accountReport{Name: a.Name, Role: string(a.Role), Created: a.Created.Format(time.RFC3339)}
					if !a.LastLogin.IsZero() {
						row.LastLogin = a.LastLogin.Format(time.RFC3339)
					}
					list.Accounts = append(list.Accounts, row)
				}
				return writeJSON(cmd, list)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tROLE\tCREATED\tLAST LOGIN")
			for _, a := range store.Accounts() {
				last := "-"
				if !a.LastLogin.IsZero() {
					last = a.LastLogin.Format(time.RFC3339)
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, a.Role, a.Created.Format(time.RFC3339), last)
			}
			return w.Flush()
		},
	}
	flags.commonFlags.register(cmd)
	flags.report.register(cmd)
	return cmd
}

// readPassword collects the password: from stdin's first line under
// --password-stdin, otherwise interactively with echo disabled, twice.
func readPassword(cmd *cobra.Command, fromStdin, confirm bool) (string, error) {
	if fromStdin {
		sc := bufio.NewScanner(cmd.InOrStdin())
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return "", fmt.Errorf("reading password from stdin: %w", err)
			}
			return "", errors.New("reading password from stdin: empty input")
		}
		pw := strings.TrimRight(sc.Text(), "\r\n")
		if pw == "" {
			return "", errors.New("password must not be empty")
		}
		return pw, nil
	}

	// The terminal check is the non-interactive guarantee of R-08, not a
	// convenience: without it term.ReadPassword blocks on a pipe with no
	// writer, and a CI job that forgot --password-stdin hangs until its
	// own timeout instead of failing in the first second with the name of
	// the flag it needs. TestNoCommandPromptsWithoutATerminal proves it
	// by running every command with exactly such a pipe on stdin.
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("standard input is not a terminal: use --password-stdin to pass the password non-interactively")
	}
	errW := cmd.ErrOrStderr()
	_, _ = fmt.Fprint(errW, "Password: ")
	first, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(errW)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	if len(first) == 0 {
		return "", errors.New("password must not be empty")
	}
	if confirm {
		_, _ = fmt.Fprint(errW, "Confirm password: ")
		second, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(errW)
		if err != nil {
			return "", fmt.Errorf("reading confirmation: %w", err)
		}
		if !bytes.Equal(first, second) {
			return "", errors.New("passwords do not match")
		}
	}
	return string(first), nil
}

// auditLocal emits one audit record for a host-local account operation
// (FR-094).
//
// It goes to STDERR, where every structured log of this CLI goes. It used
// to go to stdout, which was defensible while `tobby user` reported
// nothing else — but R-08 gives stdout to the report, and a log record
// interleaved with a JSON document makes both unparseable (B-010). The
// record is not lost: an operator collecting the trail redirects stderr,
// the way they already do for `tobby fileset pack`.
func auditLocal(cmd *cobra.Command, e *audit.Event) {
	audit.Log(context.Background(), logging.New(cmd.ErrOrStderr(), slog.LevelInfo), e)
}
