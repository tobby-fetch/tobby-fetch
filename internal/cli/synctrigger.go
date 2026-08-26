// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The command-line manual trigger of FR-014 (FR-066): `tobby sync`.
//
// It drives the instance rather than the store — see instance.go for why
// there is no other option — and its report is the task, because the task
// is what the UI and the API report too (FR-061). One synchronization,
// three surfaces, one vocabulary.

// syncTriggerOptions carries what the trigger sends to the instance.
type syncTriggerOptions struct {
	prune bool
	// pruneGiven distinguishes "the operator said nothing about pruning"
	// from "the operator said do not prune". The API body takes a pointer
	// for the same reason: a plain boolean would silently turn the first
	// into the second, which on a mirror instance is a prune nobody asked
	// to skip.
	pruneGiven bool
}

// syncTriggerRequest is the JSON body of POST /api/v1/sync.
type syncTriggerRequest struct {
	Prune *bool `json:"prune,omitempty"`
}

// syncTriggerReport is the machine form of a trigger (R-08). It names the
// instance that did the work: a report that only says "done" is useless
// in a log where three zones run the same pipeline.
type syncTriggerReport struct {
	// Instance is the base URL the task was created on.
	Instance string `json:"instance"`
	// Waited reports whether the command followed the task to its end.
	// Without it, Task.status is a snapshot of a task still moving, and
	// nothing in the document would say so.
	Waited bool `json:"waited"`
	// Task is the task the instance created, in the exact shape
	// /api/v1/tasks/{id} serves it (FR-061).
	Task *tasks.Task `json:"task"`
}

// runSyncTrigger posts the trigger, optionally waits, reports, and exits
// on the task's outcome.
func runSyncTrigger(cmd *cobra.Command, flags *commonFlags, instance *instanceFlags,
	wait *waitFlag, report *reportFlag, opts syncTriggerOptions,
) error {
	timeout, err := wait.duration(cmd)
	if err != nil {
		return err
	}
	if !wait.wait && cmd.Flags().Changed("wait-timeout") {
		return &usageError{
			err:  fmt.Errorf("--wait-timeout without --wait: there is nothing to time out"),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	// Scope: this command needs the network edge and, at most, the listen
	// address to guess the local instance — never a mode and never a
	// store (R-34, B-006). It does not touch the store at all.
	cfg, err := flags.loadFor(cmd, config.ScopeNetwork)
	if err != nil {
		return err
	}
	client, err := newInstanceClient(cmd, &cfg, instance)
	if err != nil {
		return err
	}
	defer client.close()

	body := syncTriggerRequest{}
	if opts.pruneGiven {
		prune := opts.prune
		body.Prune = &prune
	}
	task, err := client.createSync(cmd.Context(), body)
	if err != nil {
		return err
	}

	waited := wait.wait
	var waitErr error
	if waited {
		// The narration goes to stderr in both formats: it is progress,
		// not the report, and it must never land in the middle of the
		// JSON document (B-010).
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "task %s created on %s, waiting for it to finish\n", task.ID, client.base)
		var settled *tasks.Task
		settled, waitErr = waitForTask(cmd.Context(), client, task.ID, timeout)
		if settled != nil {
			task = settled
		}
	}

	if err := writeSyncTriggerReport(cmd, report, &syncTriggerReport{
		Instance: client.base, Waited: waited, Task: task,
	}); err != nil {
		return err
	}
	if waitErr != nil {
		// The deadline expired or the instance stopped answering. The
		// report is already out; this is the operational failure that
		// carries the reason.
		return waitErr
	}
	if !waited {
		return nil
	}
	return syncOutcome(task)
}

// syncOutcome maps a finished task onto the process exit code (R-08:
// `--wait` exits on the task's outcome). A failed task exits on the class
// of its principal error, so a policy refusal on the instance is a policy
// refusal on the command line — the same number a local refusal would
// have produced.
func syncOutcome(task *tasks.Task) error {
	if task.Status != tasks.StatusFailed {
		return nil
	}
	if principal := task.Principal(); principal != nil {
		return principal
	}
	return fmt.Errorf("task %s finished in status %s", task.ID, task.Status)
}

// writeSyncTriggerReport renders the trigger's report.
func writeSyncTriggerReport(cmd *cobra.Command, report *reportFlag, rep *syncTriggerReport) error {
	if report.json() {
		return writeJSON(cmd, rep)
	}
	writeSyncTriggerText(report.human(cmd), rep)
	return nil
}

// writeSyncTriggerText renders the human form. English, like the rest of
// the CLI's own prose; only taxonomy errors follow the host locale.
func writeSyncTriggerText(w io.Writer, rep *syncTriggerReport) {
	// One deliberate ignore point for the whole report, as in the plan
	// renderer: a stdout that cannot be written to takes the process with
	// it long before any of these lines could recover.
	out := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
	t := rep.Task
	out("task %s on %s\n", t.ID, rep.Instance)
	out("  type:      %s\n", t.Type)
	out("  reference: %s\n", t.Reference)
	out("  status:    %s", t.Status)
	if !rep.Waited && t.Active() {
		out(" (not waited for — follow it with 'tobby sync --wait' next time, or read /api/v1/tasks/%s)", t.ID)
	}
	out("\n")
	if !t.Finished.IsZero() && !t.Started.IsZero() {
		out("  duration:  %s\n", t.Finished.Sub(t.Started).Round(time.Second))
	}
	agg := t.Aggregate()
	if agg.Total > 0 {
		out("  items:     %d (%d done, %d skipped, %d failed)\n", agg.Total, agg.Done, agg.Skipped, agg.Failed)
	}
	for i := range t.Items {
		item := &t.Items[i]
		if item.Error == nil {
			continue
		}
		out("  %-8s %s — %s\n", item.Status, item.Name, item.Error.Code)
	}
	if principal := t.Principal(); principal != nil {
		out("  principal: %s (%s)\n", principal.Code(), className(principal))
	}
}

// className names the exit-code class a taxonomy error belongs to, using
// the published machine name (R-08) rather than a second vocabulary.
func className(e *taxonomy.Error) string {
	name, ok := taxonomy.ExitCodeName(e.Entry().Class.ExitCode())
	if !ok {
		return "unknown"
	}
	return name
}
