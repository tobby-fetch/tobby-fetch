// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
)

// TestStateLockIsExclusiveAndReleasable: a second take of the lock — even
// from this same process, which is how an in-process instance is tested —
// answers ErrStateHeld; released, it is free again; its content is
// readable while held, and emptied on release.
func TestStateLockIsExclusiveAndReleasable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	l, err := LockState(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockState(root); !errors.Is(err, ErrStateHeld) {
		t.Fatalf("second LockState = %v, want ErrStateHeld", err)
	}
	if held, err := StateHeld(root); err != nil || !held {
		t.Fatalf("StateHeld while held = %v, %v; want true", held, err)
	}
	if err := l.Write([]byte(`{"pid":1}`)); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Clean(statedir.Path(root, statedir.Serving))
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != `{"pid":1}` {
		t.Fatalf("marker read while held = %q, %v", raw, err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if held, err := StateHeld(root); err != nil || held {
		t.Fatalf("StateHeld after release = %v, %v; want false", held, err)
	}
	if raw, err := os.ReadFile(marker); err != nil || len(raw) != 0 {
		t.Fatalf("marker after release = %q, %v; want empty", raw, err)
	}
}
