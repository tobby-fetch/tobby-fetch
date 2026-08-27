// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package config

// canonicalVolume has nothing to add outside Windows: a Unix path has one
// spelling of its root, and EvalSymlinks has already produced it.
func canonicalVolume(path string) string { return path }
