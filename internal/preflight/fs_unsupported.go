// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !linux && !darwin && !windows

package preflight

import "runtime"

// The fallback path, for the operating systems outside the FR-055 work:
// Linux and Windows are the operating scope (NFR-018), macOS is the
// convenience tier (NFR-001), and everything else — a *BSD build, a
// plan9 build, js/wasm — reaches here.
//
// It reports nothing rather than approximating something. Go's standard
// library exposes no portable statfs, and a fallback that returned "free
// space: unknown, filesystem: fine" would turn the one requirement this
// package exists for into a rubber stamp on exactly the platforms nobody
// tested. Reporting the platform by name makes the gap legible in the
// operator's own report instead of hiding it behind a default.

// systemInspector answers with an explicit "not implemented here".
type systemInspector struct{}

func (systemInspector) Inspect(path string) (Filesystem, Space, error) {
	return Filesystem{
			Note:      "this build has no filesystem inspection for " + runtime.GOOS,
			Detection: "none",
		}, Space{}, &unsupportedError{
			goos: runtime.GOOS,
			path: path,
		}
}

// unsupportedError names the platform rather than the call, because the
// corrective action is about the platform.
type unsupportedError struct {
	goos string
	path string
}

func (e *unsupportedError) Error() string {
	return "no filesystem inspection is implemented for " + e.goos +
		": the free space and the file-size capability of " + e.path + " are unknown (FR-055)"
}
