// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

//go:build !windows

package trivy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestForeignWorkDirIsRefused: the scanner reuses the database unpacked in
// its work directory and runs the channel binary from it, so a work
// directory others can write into — or a symbolic link to one — is
// refused, and the degraded mode says why (B-046). A database planted
// there is never used.
func TestForeignWorkDirIsRefused(t *testing.T) {
	for _, c := range []struct {
		name    string
		prepare func(t *testing.T, dir string)
		detail  string
	}{
		{"writable by others", func(t *testing.T, dir string) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // G302: the defect under test
				t.Fatal(err)
			}
		}, "lets other users write"},
		{"a symbolic link", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
		}, "symbolic link"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, fakeConf{})
			c.prepare(t, f.opts.WorkDir)
			// A database planted where the adapter reuses an unpacked one.
			planted := filepath.Join(f.opts.WorkDir, "db", strings.TrimPrefix(f.db, "sha256:"), "db")
			if err := os.MkdirAll(planted, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				"trivy.db":      "planted",
				"metadata.json": `{"Version":2,"UpdatedAt":"2026-11-11T00:00:00Z"}`,
			} {
				if err := os.WriteFile(filepath.Join(planted, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s := New(&f.opts)
			st := s.Refresh(context.Background())
			if st.Available || st.Database != nil {
				t.Fatalf("a foreign work directory was used: %+v", st)
			}
			cause := s.Cause()
			if codeOf(cause) != taxonomy.CodeScannerUnavailable || !strings.Contains(cause.Error(), c.detail) {
				t.Fatalf("cause = %v; want %s naming %q", cause, taxonomy.CodeScannerUnavailable, c.detail)
			}
		})
	}
}
