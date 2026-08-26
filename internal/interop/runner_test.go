// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The queue-backed halves of FR-051: what the API and the UI actually
// call. An export and an import are tracked tasks, and their per-item
// outcome is what tells an operator which entry did not make it rather
// than that "the import failed".

package interop_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// newQueued wires a service on a running queue over the store's own root
// (FR-050: the task history travels with the store).
func newQueued(t *testing.T, st *store.Store) (*interop.Service, *tasks.Queue) {
	t.Helper()
	queue, err := tasks.Open(st.Root(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the queue: %v", err)
	}
	svc := interop.New(st, queue, "", slog.New(slog.DiscardHandler))
	svc.Register()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue.Start(ctx)
	return svc, queue
}

// settle waits for a task to stop moving and returns it.
func settle(t *testing.T, queue *tasks.Queue, id string) *tasks.Task {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		task, ok := queue.Get(id)
		if !ok {
			t.Fatalf("task %s vanished", id)
		}
		if !task.Active() {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s never finished (status %s)", id, task.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestExportAndImportRunAsTasks is the surface the API and the UI use:
// enqueue, follow, and read the outcome per item.
func TestExportAndImportRunAsTasks(t *testing.T) {
	src := openStore(t)
	image := putImage(t, src, ingredientRepo, "3.22.1", "alpine")
	svc, queue := newQueued(t, src)

	out := filepath.Join(t.TempDir(), "payload.tar")
	task, err := svc.StartExport("alexis", &interop.ExportRequest{Output: out})
	if err != nil {
		t.Fatalf("enqueuing the export: %v", err)
	}
	if task.Type != tasks.TypeLayoutExport || task.Reference != out || task.Actor != "alexis" {
		t.Errorf("task = %+v, want a layout export of %s by alexis", task, out)
	}
	done := settle(t, queue, task.ID)
	if done.Status != tasks.StatusDone {
		t.Fatalf("export finished %s: %+v", done.Status, done.Items)
	}
	if len(done.Items) != 2 {
		t.Fatalf("export items = %+v, want the reference and the archive", done.Items)
	}
	if archive := done.Items[len(done.Items)-1]; archive.SizeBytes == 0 {
		t.Errorf("the archive item reports no bytes: %+v", archive)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("the export produced nothing: %v", err)
	}

	dst := openStore(t)
	dstSvc, dstQueue := newQueued(t, dst)
	imported, err := dstSvc.StartImport("alexis", &interop.ImportRequest{Input: out})
	if err != nil {
		t.Fatalf("enqueuing the import: %v", err)
	}
	done = settle(t, dstQueue, imported.ID)
	if done.Status != tasks.StatusDone {
		t.Fatalf("import finished %s: %+v", done.Status, done.Items)
	}
	if len(done.Items) != 1 || done.Items[0].Name != ingredientRepo+":3.22.1" {
		t.Fatalf("import items = %+v, want one named entry", done.Items)
	}
	assertTagDigest(t, dst, ingredientRepo, "3.22.1", image.Digest.String())
}

// TestImportTaskFailsPerEntry: an entry the medium lost fails on its own
// line, named, and does not take the task's other entries with it.
func TestImportTaskFailsPerEntry(t *testing.T) {
	src := openStore(t)
	good := putImage(t, src, ingredientRepo, "3.22.1", "alpine")
	putImage(t, src, "quay.io/other/thing", "9.9.9", "other")
	svc, queue := newQueued(t, src)

	out := filepath.Join(t.TempDir(), "payload.tar")
	task, err := svc.StartExport("alexis", &interop.ExportRequest{Output: out})
	if err != nil {
		t.Fatal(err)
	}
	if done := settle(t, queue, task.ID); done.Status != tasks.StatusDone {
		t.Fatalf("export finished %s", done.Status)
	}

	// Damage one blob of the second image, leaving the first intact: the
	// medium survived the trip in part, which is the realistic case.
	damageOneLayer(t, out, "quay.io/other/thing:9.9.9")

	dst := openStore(t)
	dstSvc, dstQueue := newQueued(t, dst)
	imported, err := dstSvc.StartImport("alexis", &interop.ImportRequest{Input: out})
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, dstQueue, imported.ID)
	if done.Status != tasks.StatusFailed {
		t.Fatalf("import finished %s, want failed", done.Status)
	}
	agg := done.Aggregate()
	if agg.Failed != 1 || agg.Done != 1 {
		t.Fatalf("aggregates = %+v, want one failed and one done: %+v", agg, done.Items)
	}
	for _, item := range done.Items {
		if item.Status == tasks.StatusFailed && item.Error == nil {
			t.Errorf("the failed item %s carries no code", item.Name)
		}
	}
	assertTagDigest(t, dst, ingredientRepo, "3.22.1", good.Digest.String())
}

// TestStartRefusesAnIncompleteRequest: the validation an automation
// branches on happens before a task exists, so a script never has to
// poll to learn it forgot an argument.
func TestStartRefusesAnIncompleteRequest(t *testing.T) {
	st := openStore(t)
	svc, _ := newQueued(t, st)

	if _, err := svc.StartExport("alexis", &interop.ExportRequest{}); err == nil {
		t.Error("an export with no destination was enqueued")
	} else if code := codeOf(t, err); code != taxonomy.CodeValidation {
		t.Errorf("code = %s, want %s", code, taxonomy.CodeValidation)
	}
	if _, err := svc.StartExport("alexis", &interop.ExportRequest{
		Output: "/tmp/x", Format: "zip",
	}); err == nil {
		t.Error("an unknown format was enqueued")
	}
	if _, err := svc.StartImport("alexis", &interop.ImportRequest{}); err == nil {
		t.Error("an import with no source was enqueued")
	}
}

// TestExportOfAnUnknownRecipeIsRefusedByName: a selection naming a recipe
// this store never held must say so, rather than quietly export nothing.
func TestExportOfAnUnknownRecipeIsRefusedByName(t *testing.T) {
	st := openStore(t)
	svc := interop.New(st, nil, "", slog.New(slog.DiscardHandler))
	_, _, err := svc.Plan(context.Background(), &interop.ExportRequest{
		Selector: interop.Selector{Recipes: []string{"nowhere@1.0.0"}},
		Output:   "/tmp/x.tar",
	})
	if err == nil {
		t.Fatal("an unknown recipe planned an empty export")
	}
	if code := codeOf(t, err); code != taxonomy.CodeNotFound {
		t.Errorf("code = %s, want %s", code, taxonomy.CodeNotFound)
	}
}

// TestImportTaskRefusesAHostileArchive: the NFR-011 refusal reaches the
// task as its own catalog entry, and the whole archive is rejected — an
// archive shaped like that is not a damaged transfer.
func TestImportTaskRefusesAHostileArchive(t *testing.T) {
	st := openStore(t)
	svc, queue := newQueued(t, st)

	archive := filepath.Join(t.TempDir(), "hostile.tar")
	if err := os.WriteFile(archive, hostileArchiveBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	task, err := svc.StartImport("alexis", &interop.ImportRequest{Input: archive})
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, queue, task.ID)
	if done.Status != tasks.StatusFailed {
		t.Fatalf("import finished %s, want failed", done.Status)
	}
	if done.Error == nil || done.Error.Code != taxonomy.CodeLayoutUnsafe {
		t.Errorf("task error = %+v, want %s", done.Error, taxonomy.CodeLayoutUnsafe)
	}
	repos, err := st.Repositories(context.Background())
	if err != nil || len(repos) != 0 {
		t.Errorf("the refused import wrote %v (%v)", repos, err)
	}
}

// damageOneLayer rewrites an exported archive, replacing the bytes of one
// blob so the manifest that pins it no longer matches — a medium that
// came back with a bad sector, not a hostile one.
func damageOneLayer(t *testing.T, path, ref string) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the test's own export
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	tr := tar.NewReader(bytes.NewReader(raw))
	victim := layerDigestOf(t, raw, ref)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		body, err := readAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "blobs/sha256/"+victim {
			body = bytes.Repeat([]byte("x"), len(body))
		}
		hdr.Size = int64(len(body))
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// layerDigestOf finds the encoded digest of the first layer of the entry
// named ref in an exported archive.
func layerDigestOf(t *testing.T, archive []byte, ref string) string {
	t.Helper()
	blobs := map[string][]byte{}
	var index ocispec.Index
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		body, err := readAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == ocispec.ImageIndexFile {
			if err := unmarshal(body, &index); err != nil {
				t.Fatal(err)
			}
			continue
		}
		blobs[hdr.Name] = body
	}
	for _, desc := range index.Manifests {
		if desc.Annotations[ocispec.AnnotationRefName] != ref {
			continue
		}
		var manifest ocispec.Manifest
		if err := unmarshal(blobs["blobs/sha256/"+desc.Digest.Encoded()], &manifest); err != nil {
			t.Fatal(err)
		}
		return manifest.Layers[0].Digest.Encoded()
	}
	t.Fatalf("no entry named %s in the archive", ref)
	return ""
}

// hostileArchiveBytes builds a layout archive carrying an absolute path.
func hostileArchiveBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct {
		name string
		body []byte
	}{
		{ocispec.ImageLayoutFile, []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{ocispec.ImageIndexFile, []byte(`{"schemaVersion":2,"manifests":[]}`)},
		{"/etc/cron.d/pwn", []byte("* * * * * root sh\n")},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: f.name, Mode: 0o644, Size: int64(len(f.body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readAll drains one archive entry.
func readAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

// unmarshal is json.Unmarshal, named so the archive-surgery helpers read
// as archive surgery rather than as JSON plumbing.
func unmarshal(raw []byte, out any) error { return json.Unmarshal(raw, out) }
