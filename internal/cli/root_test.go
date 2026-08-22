// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// execute runs the root command with args exactly the way Execute does —
// parse, then usage classification — and returns the classified error.
func execute(t *testing.T, args ...string) error {
	t.Helper()
	root := New()
	root.SetArgs(args)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	return classifyUsage(root.Execute())
}

// TestUsageErrorsExitTwo is the FR-066 contract the package doc promises:
// a command line the parser refuses exits 2, distinct from the 1 of an
// operational failure — scripts branch on that difference. Both parse
// failure shapes are covered, because cobra reports them on different
// paths: a bad flag goes through the flag-error hook, an unknown command
// never reaches any flag parsing at all.
func TestUsageErrorsExitTwo(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		hint string
	}{
		{name: "unknown flag", args: []string{"serve", "--typo"}, hint: "see 'tobby serve --help'"},
		{name: "unknown command", args: []string{"nosuchcmd"}, hint: "see 'tobby --help'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := execute(t, tc.args...)
			if err == nil {
				t.Fatal("expected a parse error")
			}
			if got := exitCodeFor(err); got != taxonomy.ExitUsage {
				t.Errorf("exit code = %d, want %d (usage)", got, taxonomy.ExitUsage)
			}
			var ue *usageError
			if !errors.As(err, &ue) {
				t.Fatalf("error %T is not a usageError", err)
			}
			// SilenceUsage keeps the error terse, so the hint is the only
			// pointer the user gets towards --help.
			if ue.hint != tc.hint {
				t.Errorf("hint = %q, want %q", ue.hint, tc.hint)
			}
		})
	}
}

// TestOperationalErrorsKeepTheirCodes pins the other side of the FR-066
// contract: classifying usage errors must not reroute taxonomy classes or
// plain operational failures.
func TestOperationalErrorsKeepTheirCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, taxonomy.ExitOK},
		{"plain operational", errors.New("dial tcp: connection refused"), taxonomy.ExitFailure},
		{"policy refusal", taxonomy.New(taxonomy.CodeNoAccount, nil), taxonomy.ExitPolicy},
		{"verification failure", taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
			"recipe": "r", "fingerprints": "none",
		}), taxonomy.ExitVerification},
	}
	for _, tc := range cases {
		if got := exitCodeFor(classifyUsage(tc.err)); got != tc.want {
			t.Errorf("%s: exit code = %d, want %d", tc.name, got, tc.want)
		}
	}
	// A RunE failure that merely mentions an unknown command mid-message
	// must not be reclassified: only cobra's own prefix counts.
	err := errors.New("loading config: unknown command in file")
	if got := exitCodeFor(classifyUsage(err)); got != taxonomy.ExitFailure {
		t.Errorf("mid-message match reclassified to %d, want %d", got, taxonomy.ExitFailure)
	}
	// An operational error on a real command still exits 1 end to end:
	// `serve` without storage.root fails in RunE, not in the parser.
	if execErr := execute(t, "config", "dump", "--config", "/nonexistent/tobby.yaml"); execErr != nil {
		if got := exitCodeFor(execErr); got == taxonomy.ExitUsage {
			t.Errorf("operational failure exits %d (usage); parse classification leaked", got)
		}
	}
}
