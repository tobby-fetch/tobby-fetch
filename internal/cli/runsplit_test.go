// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"testing"
)

// runSplit executes the CLI keeping the two streams apart, which is the
// contract every reporting command relies on: the machine report goes to
// stdout so it can be piped into a tool, and human narration, structured
// logs and the audit record go to stderr (B-010, R-08). The shared run()
// helper merges them, and a merged stream is not machine-readable — so a
// test that used it would not be checking the thing that matters.
//
// It lives here rather than beside any one command because three
// milestone-5 lots each wrote their own copy of it, which is what a
// helper asking to be shared looks like.
func runSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := New()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}
