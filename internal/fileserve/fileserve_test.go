// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Tests for the FR-047 read-only FileSet file service: §7.4 extraction
// semantics (layer order, whiteouts), the §14.5 safety corpus (NFR-011
// traversal resistance), the HTTP surface, and Sync lifecycle. All tar
// fixtures are built in memory over a map-backed Blobs implementation.
package fileserve

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// --- fixtures -----------------------------------------------------------

type tarEntry struct {
	name string
	typ  byte
	data string
	link string
	mode int64
}

func file(name, data string) tarEntry {
	return tarEntry{name: name, typ: tar.TypeReg, data: data, mode: 0o644}
}

func fileMode(name, data string, mode int64) tarEntry {
	return tarEntry{name: name, typ: tar.TypeReg, data: data, mode: mode}
}

func dirEntry(name string) tarEntry {
	return tarEntry{name: name, typ: tar.TypeDir, mode: 0o755}
}

func dirMode(name string, mode int64) tarEntry {
	return tarEntry{name: name, typ: tar.TypeDir, mode: mode}
}

func symlink(name, target string) tarEntry {
	return tarEntry{name: name, typ: tar.TypeSymlink, link: target, mode: 0o777}
}

func hardlink(name, target string) tarEntry {
	return tarEntry{name: name, typ: tar.TypeLink, link: target, mode: 0o644}
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header %q: %v", e.name, err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatalf("writing tar data %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	return buf.Bytes()
}

func gzipCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func zstdCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// fakeBlobs is the map-backed Blobs implementation of the store surface.
type fakeBlobs struct {
	manifests map[string][]byte
	blobs     map[string][]byte
}

func newFakeBlobs() *fakeBlobs {
	return &fakeBlobs{manifests: map[string][]byte{}, blobs: map[string][]byte{}}
}

func (f *fakeBlobs) Manifest(_ context.Context, repo, dgst string) ([]byte, error) {
	b, ok := f.manifests[repo+"@"+dgst]
	if !ok {
		return nil, fmt.Errorf("manifest %s@%s not found", repo, dgst)
	}
	return b, nil
}

func (f *fakeBlobs) Blob(_ context.Context, repo, dgst string) (io.ReadCloser, error) {
	b, ok := f.blobs[repo+"@"+dgst]
	if !ok {
		return nil, fmt.Errorf("blob %s@%s not found", repo, dgst)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// addLayer registers one layer blob and returns its descriptor.
// compression is "gzip", "zstd" or "none".
func (f *fakeBlobs) addLayer(t *testing.T, repo, compression string, entries []tarEntry) ocispec.Descriptor {
	t.Helper()
	raw := buildTar(t, entries)
	var body []byte
	var mediaType string
	switch compression {
	case "gzip":
		body = gzipCompress(t, raw)
		mediaType = ocispec.MediaTypeImageLayerGzip
	case "zstd":
		body = zstdCompress(t, raw)
		mediaType = ocispec.MediaTypeImageLayerZstd
	case "none":
		body = raw
		mediaType = ocispec.MediaTypeImageLayer
	default:
		t.Fatalf("unknown compression %q", compression)
	}
	d := digest.FromBytes(body)
	f.blobs[repo+"@"+d.String()] = body
	return ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(body))}
}

// addImage registers an image manifest over the given layers and returns
// its digest.
func (f *fakeBlobs) addImage(t *testing.T, repo string, layers ...ocispec.Descriptor) string {
	t.Helper()
	cfgRaw := []byte("{}")
	cfgDigest := digest.FromBytes(cfgRaw)
	f.blobs[repo+"@"+cfgDigest.String()] = cfgRaw
	m := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    cfgDigest,
			Size:      int64(len(cfgRaw)),
		},
		Layers: layers,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshaling manifest: %v", err)
	}
	d := digest.FromBytes(raw)
	f.manifests[repo+"@"+d.String()] = raw
	return d.String()
}

// singleLayerSet builds a one-gzip-layer FileSet named name.
func singleLayerSet(t *testing.T, b *fakeBlobs, name, repo string, entries ...tarEntry) FileSet {
	t.Helper()
	layer := b.addLayer(t, repo, "gzip", entries)
	return FileSet{Name: name, Repo: repo, ManifestDigest: b.addImage(t, repo, layer)}
}

// --- harness ------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newTestServer(t *testing.T, blobs Blobs, limits Limits) (srv *Server, cacheDir string) {
	t.Helper()
	dir := t.TempDir()
	return NewServer(blobs, dir, limits, discardLogger()), dir
}

func mustSync(t *testing.T, s *Server, sets ...FileSet) {
	t.Helper()
	if err := s.Sync(context.Background(), sets); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

func doRequest(t *testing.T, s *Server, method, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	return doRequest(t, s, http.MethodGet, target, nil)
}

func wantBody(t *testing.T, rec *httptest.ResponseRecorder, code int, body string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, code, rec.Body.String())
	}
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
}

func setDir(cacheDir string, set FileSet) string {
	return filepath.Join(cacheDir, sanitizeName(set.Name),
		strings.ReplaceAll(set.ManifestDigest, ":", "_"))
}

func noTmpLeftovers(t *testing.T, cacheDir string) {
	t.Helper()
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return // cache directory may not exist at all
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("leftover temporary directory %q in cache", e.Name())
		}
	}
}

// --- extraction semantics (§7.4) ---------------------------------------

func TestSyncExtractsAndServes(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "assets", "example.com/assets",
		dirEntry("docs"),
		file("hello.txt", "hello world\n"),
		file("docs/guide.qz9", "\x00\x01binary"),
	)
	set.Anonymous = true
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	rec := get(t, s, "/files/assets/hello.txt")
	wantBody(t, rec, http.StatusOK, "hello world\n")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}

	rec = get(t, s, "/files/assets/docs/guide.qz9")
	wantBody(t, rec, http.StatusOK, "\x00\x01binary")
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}

	if rec := get(t, s, "/files/assets/missing.txt"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing file status = %d, want 404", rec.Code)
	}

	enabled := s.Enabled()
	if len(enabled) != 1 || enabled[0].Name != "assets" || !enabled[0].Anonymous {
		t.Fatalf("Enabled() = %+v, want the one anonymous fileset", enabled)
	}
}

func TestLayerMergeOverwriteAndWhiteouts(t *testing.T) {
	b := newFakeBlobs()
	repo := "example.com/merged"
	l1 := b.addLayer(t, repo, "gzip", []tarEntry{
		dirEntry("a"),
		file("a/file1", "v1"),
		file("a/file2", "doomed"),
		dirEntry("b"),
		file("b/keep", "stay"),
	})
	l2 := b.addLayer(t, repo, "gzip", []tarEntry{
		file("a/file1", "v2"),
		{name: "a/.wh.file2", typ: tar.TypeReg},
		{name: "a/.wh.ghost", typ: tar.TypeReg}, // whiteout of a never-extracted path is a no-op
	})
	l3 := b.addLayer(t, repo, "gzip", []tarEntry{
		dirEntry("c"),
		file("c/one", "1"),
		file("c/two", "2"),
	})
	l4 := b.addLayer(t, repo, "gzip", []tarEntry{
		dirEntry("c"),
		{name: "c/.wh..wh..opq", typ: tar.TypeReg},
		file("c/new", "n"),
	})
	set := FileSet{Name: "merged", Repo: repo, ManifestDigest: b.addImage(t, repo, l1, l2, l3, l4)}
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	wantBody(t, get(t, s, "/files/merged/a/file1"), http.StatusOK, "v2")
	wantBody(t, get(t, s, "/files/merged/b/keep"), http.StatusOK, "stay")
	wantBody(t, get(t, s, "/files/merged/c/new"), http.StatusOK, "n")
	// Strict whiteouts: a deleted file does not resurface, and an opaque
	// directory restarts empty.
	for _, gone := range []string{"a/file2", "a/.wh.file2", "c/one", "c/two", "c/.wh..wh..opq"} {
		if rec := get(t, s, "/files/merged/"+gone); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", gone, rec.Code)
		}
	}
}

func TestPlainAndZstdLayers(t *testing.T) {
	b := newFakeBlobs()
	repo := "example.com/mixed"
	l1 := b.addLayer(t, repo, "none", []tarEntry{file("plain.txt", "plain")})
	l2 := b.addLayer(t, repo, "zstd", []tarEntry{file("z.txt", "zzz")})
	set := FileSet{Name: "mixed", Repo: repo, ManifestDigest: b.addImage(t, repo, l1, l2)}
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	wantBody(t, get(t, s, "/files/mixed/plain.txt"), http.StatusOK, "plain")
	wantBody(t, get(t, s, "/files/mixed/z.txt"), http.StatusOK, "zzz")
}

func TestModesPreservedWithoutSetuidSetgid(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "modes", "example.com/modes",
		fileMode("suid", "x", 0o4755),
		fileMode("sgid", "x", 0o2755),
		fileMode("plain", "x", 0o640),
		dirMode("d", 0o750),
		file("d/inner.txt", "in"),
	)
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	rootfs := filepath.Join(setDir(cacheDir, set), "rootfs")
	for name, wantPerm := range map[string]os.FileMode{
		"suid": 0o755, "sgid": 0o755, "plain": 0o640, "d": 0o750,
	} {
		fi, err := os.Stat(filepath.Join(rootfs, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if fi.Mode().Perm() != wantPerm {
			t.Fatalf("%s perm = %o, want %o", name, fi.Mode().Perm(), wantPerm)
		}
		if fi.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			t.Fatalf("%s kept setuid/setgid: %v", name, fi.Mode())
		}
	}
	wantBody(t, get(t, s, "/files/modes/d/inner.txt"), http.StatusOK, "in")
}

func TestRejectsIndexManifest(t *testing.T) {
	b := newFakeBlobs()
	repo := "example.com/idx"
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     ocispec.MediaTypeImageIndex,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := digest.FromBytes(raw).String()
	b.manifests[repo+"@"+d] = raw
	s, _ := newTestServer(t, b, Limits{})
	err = s.Sync(context.Background(), []FileSet{{Name: "idx", Repo: repo, ManifestDigest: d}})
	if err == nil || !strings.Contains(err.Error(), "not an image manifest") {
		t.Fatalf("Sync error = %v, want the index rejection", err)
	}
}

func TestRejectsUnsupportedLayerMediaType(t *testing.T) {
	b := newFakeBlobs()
	repo := "example.com/badmt"
	layer := b.addLayer(t, repo, "gzip", []tarEntry{file("f", "x")})
	layer.MediaType = "application/x-not-a-layer"
	set := FileSet{Name: "badmt", Repo: repo, ManifestDigest: b.addImage(t, repo, layer)}
	s, _ := newTestServer(t, b, Limits{})
	err := s.Sync(context.Background(), []FileSet{set})
	if err == nil || !strings.Contains(err.Error(), "unsupported layer media type") {
		t.Fatalf("Sync error = %v, want unsupported media type", err)
	}
}

// --- §14.5 attack corpus (NFR-011) --------------------------------------

func TestRejectsTraversalEntries(t *testing.T) {
	cases := map[string]tarEntry{
		"relative dotdot": file("../evil", "x"),
		"absolute path":   file("/etc/passwd", "x"),
		"interior dotdot": file("a/../../evil", "x"),
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			b := newFakeBlobs()
			set := singleLayerSet(t, b, "hostile", "example.com/hostile", file("ok", "ok"), entry)
			s, cacheDir := newTestServer(t, b, Limits{})
			err := s.Sync(context.Background(), []FileSet{set})
			if err == nil || !strings.Contains(err.Error(), "unsafe layer entry") {
				t.Fatalf("Sync error = %v, want unsafe layer entry", err)
			}
			if len(s.Enabled()) != 0 {
				t.Fatal("hostile fileset ended up enabled")
			}
			if rec := get(t, s, "/files/hostile/ok"); rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 for a failed fileset", rec.Code)
			}
			// Nothing may have escaped next to or above the cache.
			for _, p := range []string{
				filepath.Join(cacheDir, "evil"),
				filepath.Join(filepath.Dir(cacheDir), "evil"),
			} {
				if _, err := os.Stat(p); err == nil {
					t.Fatalf("traversal artifact %q exists", p)
				}
			}
			noTmpLeftovers(t, cacheDir)
		})
	}
}

func TestRejectsEscapingSymlink(t *testing.T) {
	cases := map[string]tarEntry{
		"relative escape": symlink("escape", "../../outside"),
		"absolute target": symlink("abs", "/etc/passwd"),
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			b := newFakeBlobs()
			set := singleLayerSet(t, b, "sym", "example.com/sym", entry)
			s, cacheDir := newTestServer(t, b, Limits{})
			err := s.Sync(context.Background(), []FileSet{set})
			if err == nil || !strings.Contains(err.Error(), "unsafe layer entry") {
				t.Fatalf("Sync error = %v, want the symlink rejection", err)
			}
			noTmpLeftovers(t, cacheDir)
		})
	}
}

func TestInternalSymlinkServedButNeverOutOfRoot(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "links", "example.com/links",
		dirEntry("data"),
		file("data/target.txt", "target-content"),
		symlink("data/link.txt", "target.txt"),
		symlink("top-link", "data/target.txt"),
	)
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	wantBody(t, get(t, s, "/files/links/data/link.txt"), http.StatusOK, "target-content")
	wantBody(t, get(t, s, "/files/links/top-link"), http.StatusOK, "target-content")

	// Serve-time barrier: even a symlink planted directly inside the
	// extracted cache (bypassing extraction validation) must never
	// resolve outside the rootfs — os.Root refuses it, the client sees
	// only a 404.
	rootfs := filepath.Join(setDir(cacheDir, set), "rootfs")
	if err := os.Symlink("/etc/passwd", filepath.Join(rootfs, "planted-abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strings.Repeat("../", 10)+"etc/passwd", filepath.Join(rootfs, "planted-rel")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"planted-abs", "planted-rel"} {
		if rec := get(t, s, "/files/links/"+p); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", p, rec.Code)
		}
	}
}

func TestHardlinks(t *testing.T) {
	t.Run("internal target already extracted", func(t *testing.T) {
		b := newFakeBlobs()
		set := singleLayerSet(t, b, "hard", "example.com/hard",
			file("real.txt", "data"),
			hardlink("alias.txt", "real.txt"),
		)
		s, _ := newTestServer(t, b, Limits{})
		mustSync(t, s, set)
		wantBody(t, get(t, s, "/files/hard/alias.txt"), http.StatusOK, "data")
	})
	rejected := map[string]tarEntry{
		"external target":      hardlink("h", "../outside"),
		"absolute target":      hardlink("h", "/etc/passwd"),
		"not-extracted target": hardlink("h", "missing.txt"),
	}
	for name, entry := range rejected {
		t.Run(name, func(t *testing.T) {
			b := newFakeBlobs()
			set := singleLayerSet(t, b, "hard", "example.com/hard", entry)
			s, _ := newTestServer(t, b, Limits{})
			err := s.Sync(context.Background(), []FileSet{set})
			if err == nil || !strings.Contains(err.Error(), "unsafe layer entry") {
				t.Fatalf("Sync error = %v, want the hardlink rejection", err)
			}
		})
	}
}

func TestIgnoresSpecialEntriesAndCountsThem(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "special", "example.com/special",
		file("ok.txt", "ok"),
		tarEntry{name: "dev-node", typ: tar.TypeChar, mode: 0o644},
		tarEntry{name: "fifo", typ: tar.TypeFifo, mode: 0o644},
	)
	var logBuf bytes.Buffer
	s := NewServer(b, t.TempDir(), Limits{}, slog.New(slog.NewJSONHandler(&logBuf, nil)))
	mustSync(t, s, set)

	wantBody(t, get(t, s, "/files/special/ok.txt"), http.StatusOK, "ok")
	for _, gone := range []string{"dev-node", "fifo"} {
		if rec := get(t, s, "/files/special/"+gone); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", gone, rec.Code)
		}
	}
	if !strings.Contains(logBuf.String(), `"ignored":2`) {
		t.Fatalf("extraction log does not count the 2 ignored entries: %s", logBuf.String())
	}
}

func TestExtractionLimits(t *testing.T) {
	t.Run("max files", func(t *testing.T) {
		b := newFakeBlobs()
		set := singleLayerSet(t, b, "bomb", "example.com/bomb",
			file("f1", "x"), file("f2", "x"), file("f3", "x"))
		s, cacheDir := newTestServer(t, b, Limits{MaxFiles: 2})
		err := s.Sync(context.Background(), []FileSet{set})
		if err == nil || !strings.Contains(err.Error(), "file count exceeds limit") {
			t.Fatalf("Sync error = %v, want the file-count limit", err)
		}
		noTmpLeftovers(t, cacheDir)
	})
	t.Run("max bytes across files", func(t *testing.T) {
		b := newFakeBlobs()
		set := singleLayerSet(t, b, "bomb", "example.com/bomb",
			file("f1", "12345678"), file("f2", "12345678"))
		s, cacheDir := newTestServer(t, b, Limits{MaxBytes: 10})
		err := s.Sync(context.Background(), []FileSet{set})
		if err == nil || !strings.Contains(err.Error(), "total size exceeds limit") {
			t.Fatalf("Sync error = %v, want the total-size limit", err)
		}
		noTmpLeftovers(t, cacheDir)
	})
	t.Run("max depth", func(t *testing.T) {
		b := newFakeBlobs()
		set := singleLayerSet(t, b, "bomb", "example.com/bomb",
			file("a/b/c/d.txt", "deep"))
		s, _ := newTestServer(t, b, Limits{MaxDepth: 3})
		err := s.Sync(context.Background(), []FileSet{set})
		if err == nil || !strings.Contains(err.Error(), "path depth") {
			t.Fatalf("Sync error = %v, want the depth limit", err)
		}
	})
	t.Run("cache stays usable after a failure", func(t *testing.T) {
		b := newFakeBlobs()
		bomb := singleLayerSet(t, b, "app", "example.com/bombthengood",
			file("f1", "x"), file("f2", "x"), file("f3", "x"))
		s, cacheDir := newTestServer(t, b, Limits{MaxFiles: 2})
		if err := s.Sync(context.Background(), []FileSet{bomb}); err == nil {
			t.Fatal("Sync of the bomb succeeded")
		}
		good := singleLayerSet(t, b, "app", "example.com/good", file("ok.txt", "ok"))
		mustSync(t, s, good)
		wantBody(t, get(t, s, "/files/app/ok.txt"), http.StatusOK, "ok")
		noTmpLeftovers(t, cacheDir)
	})
}

// --- HTTP surface -------------------------------------------------------

func TestHandlerMethodsAndRanges(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "assets", "example.com/assets",
		file("hello.txt", "hello world\n"))
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	t.Run("HEAD", func(t *testing.T) {
		rec := doRequest(t, s, http.MethodHead, "/files/assets/hello.txt", nil)
		wantBody(t, rec, http.StatusOK, "")
		if cl := rec.Header().Get("Content-Length"); cl != "12" {
			t.Fatalf("Content-Length = %q, want 12", cl)
		}
	})
	t.Run("range", func(t *testing.T) {
		rec := doRequest(t, s, http.MethodGet, "/files/assets/hello.txt",
			map[string]string{"Range": "bytes=6-10"})
		wantBody(t, rec, http.StatusPartialContent, "world")
		if cr := rec.Header().Get("Content-Range"); cr != "bytes 6-10/12" {
			t.Fatalf("Content-Range = %q, want bytes 6-10/12", cr)
		}
	})
	t.Run("write methods refused", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			rec := doRequest(t, s, method, "/files/assets/hello.txt", nil)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s status = %d, want 405", method, rec.Code)
			}
			if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Fatalf("%s Allow = %q, want GET, HEAD", method, allow)
			}
		}
	})
}

func TestHandlerHostilePaths(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "assets", "example.com/assets",
		dirEntry("docs"),
		file("hello.txt", "hello world\n"),
		file("docs/inner.txt", "inner"),
	)
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	cases := map[string]int{
		"/files/assets/../assets/hello.txt": http.StatusBadRequest,
		"/files/assets/%2e%2e/hello.txt":    http.StatusBadRequest,
		"/files/assets//hello.txt":          http.StatusBadRequest,
		"/files/assets/./hello.txt":         http.StatusBadRequest,
		"/files/assets/hello%00.txt":        http.StatusBadRequest,
		"/files/unknown/hello.txt":          http.StatusNotFound,
		"/files/%2e%2e/hello.txt":           http.StatusNotFound,
		"/files/assets":                     http.StatusNotFound,
		"/files/assets/":                    http.StatusNotFound,
		"/files/assets/docs":                http.StatusNotFound,
		"/files/assets/docs/":               http.StatusNotFound,
		"/files/":                           http.StatusNotFound,
		"/elsewhere/assets/hello.txt":       http.StatusNotFound,
		"/files/assets/hello.txt/":          http.StatusNotFound,
	}
	for target, want := range cases {
		if rec := get(t, s, target); rec.Code != want {
			t.Fatalf("GET %s status = %d, want %d", target, rec.Code, want)
		}
	}
}

func TestHostileFileSetNames(t *testing.T) {
	t.Run("slash and NUL rejected by Sync", func(t *testing.T) {
		b := newFakeBlobs()
		s, _ := newTestServer(t, b, Limits{})
		for _, name := range []string{"a/b", "a\x00b", "", ".", ".."} {
			err := s.Sync(context.Background(), []FileSet{{
				Name: name, Repo: "r",
				ManifestDigest: digest.FromString("x").String(),
			}})
			if err == nil || !strings.Contains(err.Error(), "invalid fileset name") {
				t.Fatalf("Sync(%q) error = %v, want invalid fileset name", name, err)
			}
		}
	})
	t.Run("odd but legal name served from a hashed directory", func(t *testing.T) {
		b := newFakeBlobs()
		name := "wei rd\x01name"
		set := singleLayerSet(t, b, name, "example.com/weird", file("f.txt", "weird-ok"))
		s, cacheDir := newTestServer(t, b, Limits{})
		mustSync(t, s, set)

		rec := get(t, s, "/files/"+url.PathEscape(name)+"/f.txt")
		wantBody(t, rec, http.StatusOK, "weird-ok")
		if san := sanitizeName(name); !strings.HasPrefix(san, "_") {
			t.Fatalf("sanitizeName(%q) = %q, want a hashed (_-prefixed) form", name, san)
		}
		if _, err := os.Stat(setDir(cacheDir, set)); err != nil {
			t.Fatalf("hashed cache directory missing: %v", err)
		}
	})
}

// --- Sync lifecycle -----------------------------------------------------

func TestSyncIdempotent(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v1"))
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	target := setDir(cacheDir, set)
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	mustSync(t, s, set)
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged digest was re-extracted (directory inode changed)")
	}
	wantBody(t, get(t, s, "/files/app/f.txt"), http.StatusOK, "v1")
}

func TestSyncDigestChangeExtractsAndPurges(t *testing.T) {
	b := newFakeBlobs()
	v1 := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v1"))
	v2 := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v2"))
	if v1.ManifestDigest == v2.ManifestDigest {
		t.Fatal("fixture digests collide")
	}
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, v1)
	wantBody(t, get(t, s, "/files/app/f.txt"), http.StatusOK, "v1")

	mustSync(t, s, v2)
	wantBody(t, get(t, s, "/files/app/f.txt"), http.StatusOK, "v2")
	if _, err := os.Stat(setDir(cacheDir, v1)); !os.IsNotExist(err) {
		t.Fatalf("superseded digest still cached (err = %v)", err)
	}
	if _, err := os.Stat(setDir(cacheDir, v2)); err != nil {
		t.Fatalf("new digest not cached: %v", err)
	}
}

func TestSyncRemovesDisabledFileSet(t *testing.T) {
	b := newFakeBlobs()
	a := singleLayerSet(t, b, "aaa", "example.com/a", file("a.txt", "a"))
	c := singleLayerSet(t, b, "ccc", "example.com/c", file("c.txt", "c"))
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, a, c)
	wantBody(t, get(t, s, "/files/ccc/c.txt"), http.StatusOK, "c")

	mustSync(t, s, a)
	if rec := get(t, s, "/files/ccc/c.txt"); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled fileset status = %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "ccc")); !os.IsNotExist(err) {
		t.Fatalf("disabled fileset still cached (err = %v)", err)
	}
	if enabled := s.Enabled(); len(enabled) != 1 || enabled[0].Name != "aaa" {
		t.Fatalf("Enabled() = %+v, want only aaa", enabled)
	}
	wantBody(t, get(t, s, "/files/aaa/a.txt"), http.StatusOK, "a")
}

func TestSyncRedoesInterruptedExtraction(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v1"))
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, set)

	target := setDir(cacheDir, set)
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted extraction: the completion marker is gone.
	if err := os.Remove(filepath.Join(target, markerFile)); err != nil {
		t.Fatal(err)
	}
	mustSync(t, s, set)
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("marker-less extraction was not redone")
	}
	if _, err := os.Stat(filepath.Join(target, markerFile)); err != nil {
		t.Fatalf("completion marker missing after re-extraction: %v", err)
	}
	wantBody(t, get(t, s, "/files/app/f.txt"), http.StatusOK, "v1")
}

func TestSyncEmptyClearsEverything(t *testing.T) {
	b := newFakeBlobs()
	set := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v1"))
	s, cacheDir := newTestServer(t, b, Limits{})
	mustSync(t, s, set)
	mustSync(t, s)

	if enabled := s.Enabled(); len(enabled) != 0 {
		t.Fatalf("Enabled() = %+v, want none", enabled)
	}
	if rec := get(t, s, "/files/app/f.txt"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 after clearing", rec.Code)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache not empty after Sync(nil): %v", entries)
	}
}

func TestConcurrentServeAndSync(t *testing.T) {
	b := newFakeBlobs()
	v1 := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v1"))
	v2 := singleLayerSet(t, b, "app", "example.com/app", file("f.txt", "v2"))
	s, _ := newTestServer(t, b, Limits{})
	mustSync(t, s, v1)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := get(t, s, "/files/app/f.txt")
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200 during sync churn", rec.Code)
					return
				}
				if body := rec.Body.String(); body != "v1" && body != "v2" {
					t.Errorf("body = %q, want v1 or v2", body)
					return
				}
			}
		}()
	}
	for i := range 6 {
		next := v1
		if i%2 == 0 {
			next = v2
		}
		mustSync(t, s, next)
	}
	close(stop)
	wg.Wait()
}
