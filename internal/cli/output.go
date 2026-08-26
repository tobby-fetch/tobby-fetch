// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// The command-line reporting contract (FR-066, amendment 2026-08-11 /
// R-08).
//
// R-08 asks for one thing an automation can rely on across versions:
// `--output json` on every command that reports anything, spelled the
// same way everywhere, with the machine document alone on stdout. Three
// milestone-5 lots each wrote their own `--output` or `--json` before this
// file existed, which is how a "stable contract" acquires two spellings
// and three validation messages. Everything about the flag now lives
// here: its name, its accepted values, the refusal when it is given
// something else, and the annotation the guard tests walk
// (TestEveryReportingCommandAcceptsJSON).
//
// B-010 is the other half and it is not a detail: cobra's cmd.Print*
// helpers write to stderr when no writer is set, so a report printed with
// them silently vanishes from `tobby … | jq`. Machine output goes through
// writeJSON, which writes to cmd.OutOrStdout() and nowhere else; human
// narration, structured logs and audit records go to cmd.ErrOrStderr().

// Report formats. They are constants because the flag's help text, its
// refusal message, the schema document and the guard tests must all name
// the same strings.
const (
	outputText = "text"
	outputJSON = "json"
	outputYAML = "yaml"
)

// annotationReports marks a command as one that reports something, and
// records the formats it accepts. Set by reportFlag.register, read by the
// guard test: a reporting command that forgot the flag has no annotation,
// and a command carrying the annotation without a working --output fails
// the same test.
const annotationReports = "tobby.reports"

// annotationStartsTask marks a command that starts a task on an instance
// and therefore owes `--wait` (R-08). Set by waitFlag.register.
const annotationStartsTask = "tobby.starts-task"

// reportFlag is the shared `--output`. The first accepted format is the
// default, which keeps every command's pre-R-08 behaviour: `--output` is
// how a caller asks for the machine form, never how they lose the human
// one.
type reportFlag struct {
	value   string
	allowed []string
}

// newReportFlag declares the formats a command reports in, default first.
func newReportFlag(allowed ...string) *reportFlag {
	if len(allowed) < 2 {
		panic("cli: a report flag needs a default format and at least one alternative")
	}
	return &reportFlag{value: allowed[0], allowed: allowed}
}

// register declares --output on cmd and marks it as a reporting command.
func (f *reportFlag) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.value, "output", f.allowed[0],
		"report format: "+quotedList(f.allowed)+" (the machine document is alone on stdout)")
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationReports] = strings.Join(f.allowed, ",")
}

// validate refuses an unknown format as a usage error (exit code 2), not
// as an operational failure: nothing was attempted.
func (f *reportFlag) validate(cmd *cobra.Command) error {
	for _, ok := range f.allowed {
		if f.value == ok {
			return nil
		}
	}
	return &usageError{
		err:  fmt.Errorf("--output %q: expected %s", f.value, quotedList(f.allowed)),
		hint: "see '" + cmd.CommandPath() + " --help'",
	}
}

// json reports whether the machine form was asked for.
func (f *reportFlag) json() bool { return f.value == outputJSON }

// human returns the stream a command's narration belongs on. In text mode
// the narration IS the report and goes to stdout; under --output json the
// document owns stdout alone, so everything a person reads moves to
// stderr (B-010).
func (f *reportFlag) human(cmd *cobra.Command) io.Writer {
	if f.json() {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

// quotedList renders the accepted values for a help line or a refusal.
func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, `"`+v+`"`)
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// writeJSON emits the machine document on stdout, indented and newline
// terminated. The single place JSON reaches the process's standard
// output: B-010 came back as often as it did because every command had
// its own printer.
func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// waitFlag is the shared `--wait` of R-08: a command that starts a task
// on an instance can block until that task is terminal and exit on its
// outcome, so a pipeline has something to wait for other than a sleep.
//
// Commands that run their task in this process — `tobby media import`,
// `tobby fileset pack` — do not carry it: they are already blocking, and
// a no-op flag that means "yes, really" on some commands and nothing on
// others is worse than no flag.
type waitFlag struct {
	wait    bool
	timeout string
}

func (f *waitFlag) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.wait, "wait", false,
		"block until the task reaches a terminal state, and exit on its outcome")
	cmd.Flags().StringVar(&f.timeout, "wait-timeout", "",
		`give up waiting after this long, e.g. "30m" (default: wait as long as the task runs)`)
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotationStartsTask] = "true"
}

// duration resolves --wait-timeout. Zero means "no deadline of our own":
// the task's own end, or the operator's interrupt, is what stops the
// wait.
func (f *waitFlag) duration(cmd *cobra.Command) (time.Duration, error) {
	if f.timeout == "" {
		return 0, nil
	}
	d, err := config.ParseDuration(f.timeout)
	if err != nil {
		return 0, &usageError{
			err:  fmt.Errorf("--wait-timeout: %w", err),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	if d <= 0 {
		return 0, &usageError{
			err:  fmt.Errorf("--wait-timeout %q: a deadline that has already passed is not a deadline", f.timeout),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	return time.Duration(d), nil
}
