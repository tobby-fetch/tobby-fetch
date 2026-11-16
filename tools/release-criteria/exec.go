// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// execResult is the outcome of one external command. err is set only when
// the command could not be run at all; a command that ran and failed has
// err == nil and a non-zero exitCode.
type execResult struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func (r execResult) success() bool { return r.err == nil && r.exitCode == 0 }

// output is stdout and stderr together, for failure messages.
func (r execResult) output() string {
	return strings.TrimSpace(r.stdout + "\n" + r.stderr)
}

// commander runs external tools. Tests substitute a fake: no check below
// touches the network or the disk except through it.
type commander interface {
	// has reports whether the tool is on PATH.
	has(name string) bool
	// run executes name in dir with env appended to the process
	// environment.
	run(ctx context.Context, dir string, env []string, name string, args ...string) execResult
}

type osCommander struct{}

func (osCommander) has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (osCommander) run(ctx context.Context, dir string, env []string, name string, args ...string) execResult {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: the tool names are constants of this program and the arguments are built from validated release metadata, never through a shell
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := execResult{stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.exitCode = ee.ExitCode()
	default:
		res.err = err
	}
	return res
}
