// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Output bounds of one execution. Trivy parses hostile content, so what
// comes back from it is bounded like any other untrusted input: a report
// beyond maxStdout is refused rather than read, and stderr is kept only
// for the failure detail.
const (
	maxStdout = 128 << 20
	maxStderr = 64 << 10
	// waitDelay bounds how long a killed process may keep its pipes open.
	waitDelay = 5 * time.Second
)

// errOutputTooLarge is a report that exceeded maxStdout.
var errOutputTooLarge = errors.New("the scanner's output exceeded the bound Tobby reads")

// boundedBuffer keeps at most limit bytes and records that more came.
type boundedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if len(p) > room {
		b.buf.Write(p[:room])
		b.overflow = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

// minimalEnv is the whole environment the scanner runs with: a private
// home and temporary directory, and outbound HTTP pointed at a closed
// local port. Nothing of Tobby's own environment leaks in — no
// credentials, no proxy, no TRIVY_* override that could re-enable an
// update — and an accidental network attempt fails at once instead of
// reaching anything (NFR-019: zero unconfigured outbound connection; the
// --offline-scan and --skip-*-update flags are the first line, this is
// the second).
func minimalEnv(home string) []string {
	env := []string{
		"HOME=" + home,
		"TMPDIR=" + home,
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"http_proxy=http://127.0.0.1:9",
		"https_proxy=http://127.0.0.1:9",
		"NO_PROXY=",
		"no_proxy=",
		"PATH=",
	}
	if runtime.GOOS == "windows" {
		// The Windows loader and the Go runtime need these two; neither
		// carries anything of the caller's.
		env = append(env, "TMP="+home, "TEMP="+home, "USERPROFILE="+home)
		if root := os.Getenv("SystemRoot"); root != "" {
			env = append(env, "SystemRoot="+root)
		}
	}
	return env
}

// run executes the scanner binary once, out of process, with the minimal
// environment, a deadline, and bounded outputs. The returned stdout is
// complete or the call fails.
func run(ctx context.Context, bin string, args []string, home string, timeout time.Duration) ([]byte, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: bin is the located binary, its digest verified just before; args are this adapter's own
	cmd.Env = minimalEnv(home)
	cmd.Dir = home
	cmd.Stdin = nil
	cmd.WaitDelay = waitDelay
	stdout := &boundedBuffer{limit: maxStdout}
	stderr := &boundedBuffer{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("the scanner did not finish within %s", timeout)
	case err != nil:
		return nil, fmt.Errorf("%w: %s", err, lastLine(stderr.buf.String()))
	case stdout.overflow:
		return nil, errOutputTooLarge
	}
	return stdout.buf.Bytes(), nil
}

// lastLine keeps the end of a diagnostic stream: the line that says why.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 512 {
		s = s[len(s)-512:]
	}
	if s == "" {
		return "no diagnostic on stderr"
	}
	return s
}
