// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
)

// `tobby state inspect` (FR-097, R-27): the inventory of the instance
// state directory — the single backup target of an instance beside its
// store. Names, classes, sizes and modes; never contents.

func newStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Inspect the instance state directory (R-27)",
	}
	cmd.AddCommand(newStateInspectCmd())
	return cmd
}

func newStateInspectCmd() *cobra.Command {
	flags := &commonFlags{}
	report := newReportFlag(outputText, outputJSON)
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "List the entries of the state directory with their class, size and permissions",
		Long: `List what the state directory holds and what a backup must do with it
(FR-097).

Each documented entry is shown with its class — identity (back it up: a
rebuild needs it) or discardable (a cache or spool, rebuilt on demand) —
whether it is secret, its size and its permissions. A secret entry readable by
every user of the host is flagged: the instance refuses to start on it
(TBY-STA-002). Entries the layout does not document are listed too. Nothing
is ever read out of an entry: names, sizes and modes only.

It reads the directory only; it is safe beside a running instance. Exit
codes: 0 listed, 3 when a secret entry is readable by every user, 1 when the
directory cannot be read.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := report.validate(cmd); err != nil {
				return err
			}
			cfg, err := flags.loadFor(cmd, config.ScopeState)
			if err != nil {
				return err
			}
			if cfg.State.Root == "" {
				return fmt.Errorf("state.root is required: set --state-root, %s, or \"state.root:\" in the configuration file", config.EnvStateRoot)
			}
			inv, err := statedir.Inspect(cfg.State.Root)
			if err != nil {
				return err
			}
			if err := writeInventory(cmd, report, inv); err != nil {
				return err
			}
			return inventoryVerdict(inv)
		},
	}
	flags.register(cmd)
	report.register(cmd)
	return cmd
}

// inventoryVerdict turns a broad secret entry into the policy refusal the
// start would give.
func inventoryVerdict(inv *statedir.Inventory) error {
	if inv.Layout > statedir.LayoutVersion || inv.Layout < 0 {
		_, err := statedir.Prepare(inv.Root, statedir.Expect{}, "")
		return err
	}
	for i := range inv.Items {
		if it := &inv.Items[i]; it.Present && it.Secret && it.Access == statedir.AccessBroad {
			_, err := statedir.Prepare(inv.Root, statedir.Expect{}, "")
			return err
		}
	}
	return nil
}

func writeInventory(cmd *cobra.Command, report *reportFlag, inv *statedir.Inventory) error {
	if report.json() {
		return writeJSON(cmd, inv)
	}
	w := report.human(cmd)
	layout := fmt.Sprint(inv.Layout)
	if inv.Layout == 0 {
		layout = "none recorded (a start will record " + fmt.Sprint(inv.Supported) + ")"
	}
	_, _ = fmt.Fprintf(w, "state directory %s — layout %s\n", inv.Root, layout)
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ENTRY\tCLASS\tSECRET\tPRESENT\tSIZE\tMODE\tACCESS")
	for i := range inv.Items {
		it := &inv.Items[i]
		class := string(it.Class)
		if !it.Documented {
			class = "(undocumented)"
		}
		access := string(it.Access)
		if it.Access == statedir.AccessBroad {
			access = "BROAD — refuses the start"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%d\t%s\t%s\n", it.Name, class, it.Secret, it.Present, it.Size, it.Mode, access)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w, "back up the identity entries, the configuration file and the secret files it references; the store has its own copy (FR-097)")
	return nil
}
