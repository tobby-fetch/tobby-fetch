// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"

	storagedriver "github.com/distribution/distribution/v3/registry/storage/driver"
)

// The two corrections of B-023, locked.
//
// Both defects are invisible on Unix — renaming an open file works there,
// and filepath.Join is path.Join there — so a test that only observed the
// result would prove nothing on the machine most of this suite runs on.
// The ordering test therefore observes the SEQUENCE of driver calls
// through a recording stand-in, which fails on every platform if the
// close moves back below the rename.

// recordingWriter is a FileWriter that remembers whether it was closed.
type recordingWriter struct {
	closed    bool
	committed bool
}

func (w *recordingWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *recordingWriter) Size() int64                 { return 0 }
func (w *recordingWriter) Cancel(context.Context) error {
	w.closed = true
	return nil
}
func (w *recordingWriter) Commit(context.Context) error { w.committed = true; return nil }
func (w *recordingWriter) Close() error                 { w.closed = true; return nil }

// recordingDriver answers Writer/Move/Delete and records the state of the
// writer at the instant Move is called. Every other method of the
// interface is inherited from a nil embedded value: reaching one is a
// panic, which is the right outcome — PutContent must not be calling it.
type recordingDriver struct {
	storagedriver.StorageDriver
	writer *recordingWriter
	// openAtMove records that the temporary object was still open when
	// the rename ran. On Windows that is a sharing violation and the
	// write fails outright.
	openAtMove   bool
	moved        bool
	movedFrom    string
	movedTo      string
	deletedPaths []string
}

func (d *recordingDriver) Writer(context.Context, string, bool) (storagedriver.FileWriter, error) {
	d.writer = &recordingWriter{}
	return d.writer, nil
}

func (d *recordingDriver) Move(_ context.Context, from, to string) error {
	d.openAtMove = !d.writer.closed
	d.moved, d.movedFrom, d.movedTo = true, from, to
	return nil
}

func (d *recordingDriver) Delete(_ context.Context, p string) error {
	d.deletedPaths = append(d.deletedPaths, p)
	return nil
}

// TestPutContentClosesTheTemporaryObjectBeforeRenamingIt is the whole of
// B-023's first half: the library commits and renames with the handle
// still open, which Unix allows and Windows answers with a sharing
// violation on every manifest, every tag and every link.
func TestPutContentClosesTheTemporaryObjectBeforeRenamingIt(t *testing.T) {
	rec := &recordingDriver{}
	d := &storeDriver{StorageDriver: rec, root: t.TempDir()}

	if err := d.PutContent(context.Background(), "/docker/registry/v2/repositories/x/_manifests/tags/1/current/link", []byte("sha256:...")); err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	if !rec.moved {
		t.Fatal("PutContent did not rename anything into place")
	}
	if rec.openAtMove {
		t.Error("the temporary object was still open when it was renamed: " +
			"Windows refuses that rename, and with it every manifest and tag the store records (B-023)")
	}
	if !rec.writer.committed {
		t.Error("the temporary object was renamed without being committed: its bytes may never have reached the disk")
	}
	if !strings.HasSuffix(rec.movedFrom, ".tmp") {
		t.Errorf("renamed from %q, want a temporary name", rec.movedFrom)
	}
	if rec.movedTo != "/docker/registry/v2/repositories/x/_manifests/tags/1/current/link" {
		t.Errorf("renamed to %q, want the requested key", rec.movedTo)
	}
	if len(rec.deletedPaths) != 0 {
		t.Errorf("a successful write deleted %v", rec.deletedPaths)
	}
}

// TestPutContentLeavesNoTemporaryObjectWhenTheRenameFails: a failed write
// must not leave a stray `.tmp` sibling in the store, which the media
// manifest would then inventory as content nothing references (FR-054).
func TestPutContentLeavesNoTemporaryObjectWhenTheRenameFails(t *testing.T) {
	rec := &failingMoveDriver{}
	d := &storeDriver{StorageDriver: rec, root: t.TempDir()}

	err := d.PutContent(context.Background(), "/docker/registry/v2/blobs/x/link", []byte("x"))
	if err == nil {
		t.Fatal("PutContent reported success on a rename that failed")
	}
	if len(rec.deletedPaths) != 1 || !strings.HasSuffix(rec.deletedPaths[0], ".tmp") {
		t.Errorf("cleanup deleted %v, want the one temporary object", rec.deletedPaths)
	}
}

type failingMoveDriver struct{ recordingDriver }

func (d *failingMoveDriver) Move(context.Context, string, string) error {
	return errors.New("sharing violation")
}

// TestListReturnsSlashSeparatedKeys is B-023's second half.
//
// The keys this driver returns are parsed inside the library with the
// slash-only `path` package — the catalog looks for a "_manifests"
// component, the tag store takes path.Base as the tag name. A key joined
// with the platform separator reads as one long component on Windows, so
// nothing is ever recognized: the repository listing comes back empty and
// the garbage collector marks nothing reachable, which is a sweep over
// live content.
//
// On Unix the two joins are the same function and this test cannot fail;
// it is the Windows runner that gives it teeth (NFR-018).
func TestListReturnsSlashSeparatedKeys(t *testing.T) {
	root := t.TempDir()
	drv, err := storeDriverFactory{}.Create(context.Background(), map[string]any{"rootdirectory": root})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const key = "/docker/registry/v2/repositories/docker.io/library/alpine/_manifests/tags/3.22.1/current/link"
	if err := drv.PutContent(ctx, key, []byte("sha256:0")); err != nil {
		t.Fatalf("seeding the store: %v", err)
	}

	const parent = "/docker/registry/v2/repositories/docker.io/library/alpine/_manifests/tags"
	keys, err := drv.List(ctx, parent)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("List = %v, want the one tag directory", keys)
	}
	got := keys[0]
	if strings.ContainsRune(got, '\\') {
		t.Errorf("key %q carries a backslash: the library parses these with path.Split and would read it as one component", got)
	}
	if want := path.Join(parent, "3.22.1"); got != want {
		t.Errorf("key = %q, want %q", got, want)
	}
	if path.Base(got) != "3.22.1" {
		t.Errorf("path.Base(%q) = %q, want the tag name — this is exactly how the tag store reads it", got, path.Base(got))
	}

	// A key that is not there answers the library's own sentinel, because
	// the callers branch on it rather than on the message.
	if _, err := drv.List(ctx, parent+"/absent"); err == nil {
		t.Error("listing a missing key reported success")
	} else {
		var notFound storagedriver.PathNotFoundError
		if !errors.As(err, &notFound) {
			t.Errorf("listing a missing key = %v, want a PathNotFoundError", err)
		}
	}
}
