// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The command-line contract of FR-066's R-08 amendment, held by tests
// rather than by prose.
//
// Four promises, four guards: `--output json` on every command that
// reports anything, an exhaustive published table of exit codes, a
// guaranteed non-interactive mode, and `--wait` on every command that
// starts a task. Each guard walks the real command tree, so a command
// added later is covered the day it is added and not the day someone
// remembers this file exists.

// nonReporting names the runnable commands that report nothing and
// therefore owe no `--output`. It is deliberately short and deliberately
// justified in place: adding a name here is the one way to opt a command
// out of the contract, and it should read like the argument it is.
var nonReporting = map[string]string{
	"tobby serve": "runs the instance until it is stopped; its output is the " +
		"structured log stream, which has its own schema (FR-090) and its own " +
		"destination (stderr) — a report is something a command finishes by " +
		"writing, and this one never finishes",
	"tobby quickstart": "an interactive aid (R-34): it asks questions and writes a " +
		"configuration file. What it produces is that file, and `tobby config dump` " +
		"is the command that reports it",
}

// commandFixture is one runnable command with arguments that reach its
// own logic — past cobra's positional-argument check — and fail or finish
// quickly. Every guard below runs the whole tree, so the fixtures live in
// one place.
type commandFixture struct {
	path string
	args func(t *testing.T) []string
}

// fixtures returns one entry per runnable command of the tree. A command
// with no entry fails TestEveryRunnableCommandHasAFixture, which is what
// keeps the guards honest as the tree grows.
func fixtures() []commandFixture {
	tmp := func(t *testing.T) string { t.Helper(); return t.TempDir() }
	return []commandFixture{
		{path: "tobby version", args: func(*testing.T) []string { return nil }},
		{path: "tobby config dump", args: func(t *testing.T) []string {
			return []string{"--mode", "passthrough", "--storage-root", tmp(t)}
		}},
		{path: "tobby export", args: func(t *testing.T) []string {
			return []string{filepath.Join(tmp(t), "payload.tar"), "--storage-root", tmp(t)}
		}},
		{path: "tobby import", args: func(t *testing.T) []string {
			return []string{filepath.Join(tmp(t), "absent.tar"), "--storage-root", tmp(t)}
		}},
		{path: "tobby fileset pack", args: func(t *testing.T) []string {
			return []string{tmp(t), "set:1.0.0", "--storage-root", tmp(t)}
		}},
		{path: "tobby media verify", args: func(t *testing.T) []string {
			return []string{"--storage-root", tmp(t), "--zone", "zone-a"}
		}},
		{path: "tobby media import", args: func(t *testing.T) []string {
			return []string{"--storage-root", tmp(t), "--zone", "zone-a"}
		}},
		{path: "tobby recipe push", args: func(t *testing.T) []string {
			file := filepath.Join(tmp(t), "recipe.yaml")
			if err := os.WriteFile(file, []byte("not: a recipe\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{file, "127.0.0.1:1/cookbook/x:1.0.0"}
		}},
		{path: "tobby user add", args: func(t *testing.T) []string {
			return []string{"alice", "--state-root", tmp(t)}
		}},
		{path: "tobby user passwd", args: func(t *testing.T) []string {
			return []string{"alice", "--state-root", tmp(t)}
		}},
		{path: "tobby user list", args: func(t *testing.T) []string {
			return []string{"--state-root", tmp(t)}
		}},
		// An address nothing listens on: the trigger fails at connect,
		// which is exactly what a guard needs — fast, and with no
		// dependency on what happens to run on this host.
		{path: "tobby sync", args: func(*testing.T) []string {
			return []string{"--instance", "http://127.0.0.1:1"}
		}},
		{path: "tobby serve", args: func(*testing.T) []string {
			return []string{"--mode", "sideways"}
		}},
		{path: "tobby quickstart", args: func(t *testing.T) []string {
			return []string{"--config", filepath.Join(tmp(t), "tobby.yaml"),
				"--storage-root", tmp(t), "--state-root", tmp(t)}
		}},
	}
}

// walk visits every command of the tree, root included.
func walk(c *cobra.Command, visit func(*cobra.Command)) {
	visit(c)
	for _, sub := range c.Commands() {
		walk(sub, visit)
	}
}

// runnableCommands lists the leaf commands the contract applies to.
// cobra's own generated `completion` and `help` are not in the tree until
// Execute builds them, and they are not Tobby's contract to keep.
func runnableCommands() []*cobra.Command {
	var out []*cobra.Command
	walk(New(), func(c *cobra.Command) {
		if c.Runnable() {
			out = append(out, c)
		}
	})
	return out
}

// argsFor returns the fixture arguments of a command path.
func argsFor(t *testing.T, path string) ([]string, bool) {
	t.Helper()
	for _, f := range fixtures() {
		if f.path == path {
			return f.args(t), true
		}
	}
	return nil, false
}

// invocation turns a command path and its fixture arguments into the
// argument vector run() and runProcess() take.
func invocation(path string, args []string) []string {
	return append(strings.Fields(strings.TrimPrefix(path, "tobby ")), args...)
}

// TestEveryRunnableCommandHasAFixture keeps the guards from silently
// skipping a new command: every one of them must be exercisable here.
func TestEveryRunnableCommandHasAFixture(t *testing.T) {
	for _, cmd := range runnableCommands() {
		if _, ok := argsFor(t, cmd.CommandPath()); !ok {
			t.Errorf("%s has no fixture in contract_test.go: the R-08 guards cannot exercise it\n"+
				"add one with arguments that reach the command's own logic and return quickly",
				cmd.CommandPath())
		}
	}
}

// TestEveryReportingCommandAcceptsJSON is the first promise of R-08:
// `--output json` on every command that reports anything, spelled the
// same way everywhere. A command that reports and forgot the flag has no
// annotation and is not on the exemption list, which is what fails here.
func TestEveryReportingCommandAcceptsJSON(t *testing.T) {
	for _, cmd := range runnableCommands() {
		path := cmd.CommandPath()
		formats, declared := cmd.Annotations[annotationReports]
		if !declared {
			if _, exempt := nonReporting[path]; !exempt {
				t.Errorf("%s reports nothing declared and is not on the exemption list.\n"+
					"Every command that reports anything owes `--output json` (FR-066, R-08): "+
					"register a reportFlag, or add %q to nonReporting with the reason it reports nothing.",
					path, path)
			}
			continue
		}
		if reason, exempt := nonReporting[path]; exempt {
			t.Errorf("%s declares a report AND claims to report nothing (%q): pick one", path, reason)
		}
		if !strings.Contains(formats, outputJSON) {
			t.Errorf("%s accepts %q but not %q: the machine form is the contract", path, formats, outputJSON)
		}
		flag := cmd.Flags().Lookup("output")
		if flag == nil {
			t.Errorf("%s is annotated as reporting but has no --output flag", path)
			continue
		}
		if want := strings.Split(formats, ",")[0]; flag.DefValue != want {
			t.Errorf("%s: --output defaults to %q, want the first declared format %q — "+
				"the flag is how a caller asks for the machine form, never how they lose the human one",
				path, flag.DefValue, want)
		}
	}
}

// TestUnknownOutputFormatIsAUsageError: `--output` is a published
// contract, so an unrecognized value is refused with the usage code and
// never falls back silently to text. Checked on every reporting command,
// because a fallback in one of them is a pipeline reading prose it
// believes to be JSON.
func TestUnknownOutputFormatIsAUsageError(t *testing.T) {
	for _, cmd := range runnableCommands() {
		if _, declared := cmd.Annotations[annotationReports]; !declared {
			continue
		}
		path := cmd.CommandPath()
		args, ok := argsFor(t, path)
		if !ok {
			continue // reported by TestEveryRunnableCommandHasAFixture
		}
		err := execute(t, append(invocation(path, args), "--output", "sgml")...)
		if err == nil {
			t.Errorf("%s accepted --output sgml", path)
			continue
		}
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: --output sgml returned %T (%v), want a usage error — "+
				"an unknown format must be refused before anything is attempted", path, err, err)
			continue
		}
		if got := exitCodeFor(err); got != taxonomy.ExitUsage {
			t.Errorf("%s: --output sgml exits %d, want %d", path, got, taxonomy.ExitUsage)
		}
	}
}

// TestEveryTaskStartingCommandCanWait is the fourth promise: `--wait` on
// every command that starts a task. Only commands that hand their work to
// an instance carry it — the ones that run the task in this process are
// already blocking, and a flag meaning "yes, really" on some commands and
// nothing on others is worse than no flag. The test asserts both halves:
// the annotated commands have the flag, and at least one command is
// annotated, so deleting the annotation everywhere does not make the
// guard vacuously green.
func TestEveryTaskStartingCommandCanWait(t *testing.T) {
	annotated := 0
	for _, cmd := range runnableCommands() {
		if _, starts := cmd.Annotations[annotationStartsTask]; !starts {
			continue
		}
		annotated++
		for _, name := range []string{"wait", "wait-timeout"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("%s starts a task on an instance but has no --%s (FR-066, R-08)", cmd.CommandPath(), name)
			}
		}
	}
	if annotated == 0 {
		t.Error("no command declares that it starts a task: the --wait guard is checking nothing")
	}
}

// TestNoCommandPromptsWithoutATerminal is the third promise of R-08: no
// command prompts, and none requires a terminal.
//
// The whole tree runs with a pipe on os.Stdin — an input that is not a
// terminal and that nobody ever writes to, which is what a cron entry and
// a CI job hand a process. Two failures are caught, and both were
// observed by removing the guard they protect:
//
//   - a command that asks a question and blocks on the answer never
//     returns, and the deadline names it (proven by forcing the quickstart
//     dialogue interactive: it hangs on the mode question);
//   - a command that prints a prompt and reads whatever comes leaves the
//     prompt on stderr (proven by removing the terminal check of
//     readPassword: `tobby user add` printed "Password:").
//
// Asserting only on the refusal message would catch neither: a build that
// prompts and then fails on EOF produces a perfectly plausible message.
func TestNoCommandPromptsWithoutATerminal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = writer.Close()
		_ = reader.Close()
	}()
	saved := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = saved }()

	for _, cmd := range runnableCommands() {
		path := cmd.CommandPath()
		args, ok := argsFor(t, path)
		if !ok {
			continue // reported by TestEveryRunnableCommandHasAFixture
		}
		// The command's own stdin is left alone: cobra falls back to
		// os.Stdin, which is the pipe. Replacing it with a buffer would
		// hand the command a readable, EOF-terminated input and hide
		// exactly the condition under test.
		done := make(chan string, 1)
		go func() {
			root := New()
			root.SetOut(&strings.Builder{})
			stderr := &strings.Builder{}
			root.SetErr(stderr)
			root.SetArgs(invocation(path, args))
			_ = root.Execute()
			done <- stderr.String()
		}()
		select {
		case stderr := <-done:
			for _, prompt := range []string{"Password:", "Confirm password:"} {
				if strings.Contains(stderr, prompt) {
					t.Errorf("%s printed %q without a terminal", path, prompt)
				}
			}
		case <-time.After(20 * time.Second):
			t.Errorf("%s is still running after 20s with a pipe on standard input: "+
				"it is waiting for input nobody will send. FR-066 (R-08) requires that no "+
				"command prompts and that none requires a terminal — refuse with a message "+
				"naming the non-interactive flag instead.", path)
			// The goroutine is stuck on a read that will never return;
			// the pipe's deferred Close releases it when the test ends.
			return
		}
	}
}

// TestEveryPublishedExitCodeIsProducedByTheCLI closes the exit-code
// contract on the CLI side: the table published by the taxonomy is not a
// list of numbers somebody liked, it is the set of values exitCodeFor can
// return. Each row is produced here from the kind of error that carries
// it, through the mapping the process actually performs.
func TestEveryPublishedExitCodeIsProducedByTheCLI(t *testing.T) {
	produce := map[string]error{
		taxonomy.ExitNameOK:      nil,
		taxonomy.ExitNameFailure: errors.New("the store could not be read"),
		taxonomy.ExitNameUsage: &usageError{
			err: errors.New("unknown flag: --typo"), hint: "see 'tobby --help'",
		},
		taxonomy.ExitNamePolicy:       taxonomy.New(taxonomy.CodeNotAllowlisted, taxonomy.Params{"host": "docker.io"}),
		taxonomy.ExitNameVerification: taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{"recipe": "r", "fingerprints": "f"}),
		taxonomy.ExitNameChangesPlanned: &exitError{
			code: taxonomy.ExitChangesPlanned,
		},
	}
	for _, row := range taxonomy.ExitCodes() {
		err, covered := produce[row.Name]
		if !covered {
			t.Errorf("exit code %d (%s) is published but this test cannot produce it: "+
				"either the CLI cannot return it — and the table must lose the row — "+
				"or the mapping is untested", row.Code, row.Name)
			continue
		}
		if got := exitCodeFor(classifyUsage(err)); got != row.Code {
			t.Errorf("%s: the CLI exits %d, the published table says %d", row.Name, got, row.Code)
		}
	}
	// And the converse: every class of the taxonomy lands on a published
	// row. A class whose exit code is not in the table would be a code an
	// operator can observe and cannot look up.
	for _, class := range taxonomy.Classes() {
		if _, published := taxonomy.ExitCodeName(class.ExitCode()); !published {
			t.Errorf("a taxonomy class exits %d, which the published table does not list", class.ExitCode())
		}
	}
	for _, entry := range taxonomy.All() {
		if _, published := taxonomy.ExitCodeName(entry.Class.ExitCode()); !published {
			t.Errorf("%s exits %d, which the published table does not list", entry.Code, entry.Class.ExitCode())
		}
	}
}
