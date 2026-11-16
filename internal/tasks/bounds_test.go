// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package tasks

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestReadLogIsChunked (NFR-023): one follow call returns at most one
// bounded chunk; the cursor carries the rest.
func TestReadLogIsChunked(t *testing.T) {
	root := t.TempDir()
	q, err := Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), 3<<20)
	if err := os.WriteFile(q.LogPath("tsk_big"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	var total int64
	for from, calls := int64(0), 0; ; calls++ {
		chunk, next, err := q.ReadLog("tsk_big", from)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk) > 1<<20 {
			t.Fatalf("one ReadLog call returned %d bytes, want at most 1 MiB", len(chunk))
		}
		if len(chunk) == 0 {
			break
		}
		total += int64(len(chunk))
		from = next
		if calls > 10 {
			t.Fatal("the cursor does not advance")
		}
	}
	if total != int64(len(big)) {
		t.Errorf("followed %d bytes, want %d", total, len(big))
	}
}

// TestTaskFileIdentifierMustMatchItsName (NFR-023): the identifier read
// from a task file is joined into the log path, so a tampered one — on a
// store that travelled — is refused, never believed.
func TestTaskFileIdentifierMustMatchItsName(t *testing.T) {
	root := t.TempDir()
	q, err := Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(q.LogPath("x"))
	for name, body := range map[string]string{
		"tsk_a.json": `{"id":"../../../escape","type":"sync","status":"done"}`,
		"tsk_b.json": `{"id":"tsk_other","type":"sync","status":"done"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	q2, err := Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../../../escape", "tsk_other", "tsk_a", "tsk_b"} {
		if _, ok := q2.Get(id); ok {
			t.Errorf("task %q loaded from a file that names another identifier", id)
		}
	}
	all, err := ReadAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("ReadAll believed %d tampered task files", len(all))
	}
}
