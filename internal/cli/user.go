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
// hash is never written by hand. Human feedback goes to stderr; stdout
// carries machine output only (the audit record, or the list table).
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
	stdin bool
}

func (f *userFlags) register(cmd *cobra.Command) {
	f.commonFlags.register(cmd)
	cmd.Flags().BoolVar(&f.stdin, "password-stdin", false,
		"read the password from the first line of standard input (for automation)")
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
	flags := &userFlags{}
	var roleFlag string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a local account (the first account defaults to admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			auditLocal(&audit.Event{
				Actor: audit.ActorLocal, Action: audit.ActionAccountCreate,
				Target: name, Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
			})
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
	flags := &userFlags{}
	cmd := &cobra.Command{
		Use:   "passwd <name>",
		Short: "Change a local account's password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			auditLocal(&audit.Event{
				Actor: audit.ActorLocal, Action: audit.ActionAccountPasswd,
				Target: args[0], Outcome: audit.OutcomeSuccess, Origin: audit.OriginLocal,
			})
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "password of %q updated\n", args[0])
			return nil
		},
	}
	flags.register(cmd)
	return cmd
}

func newUserListCmd() *cobra.Command {
	flags := &userFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the local accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := flags.stateStore(cmd)
			if err != nil {
				return err
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

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("standard input is not a terminal: use --password-stdin to pass the password non-interactively")
	}
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	if len(first) == 0 {
		return "", errors.New("password must not be empty")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm password: ")
		second, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("reading confirmation: %w", err)
		}
		if !bytes.Equal(first, second) {
			return "", errors.New("passwords do not match")
		}
	}
	return string(first), nil
}

// auditLocal emits one audit record on stdout — the FR-090 passthrough
// channel — for host-local account operations.
func auditLocal(e *audit.Event) {
	logger := logging.New(os.Stdout, slog.LevelInfo)
	audit.Log(context.Background(), logger, e)
}
