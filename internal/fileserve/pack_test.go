// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Tests for FR-048 operator FileSet packing: the round trip through the
// FR-047 server (a packed tree is a servable FileSet), the digest
// reproducibility the feature promises, the hostile-tree corpus packing
// refuses under RECIPE-SPEC §14.5, the manual-import marking, and the
// confinement that keeps the remote surfaces off arbitrary host paths.

package fileserve

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// --- harness ------------------------------------------------------------

// fakeStore is fakeBlobs plus the Writer half: the same map-backed store
// the extraction tests read from, so a packed FileSet can be served by a
// real Server without a registry anywhere in the test.
type fakeStore struct {
	*fakeBlobs
	tags   map[string]string // repo:tag → digest
	manual []string          // repositories marked as manual imports
	writes int               // blob writes actually performed
}

func newFakeStore() *fakeStore {
	return &fakeStore{fakeBlobs: newFakeBlobs(), tags: map[string]string{}}
}

func (f *fakeStore) HasBlob(_ context.Context, repo string, dgst digest.Digest) bool {
	_, ok := f.blobs[repo+"@"+dgst.String()]
	return ok
}

func (f *fakeStore) WriteBlob(_ context.Context, repo string, dgst digest.Digest, r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if got := digest.FromBytes(raw); got != dgst {
		return errors.New("digest mismatch on commit: " + got.String() + " != " + dgst.String())
	}
	f.blobs[repo+"@"+dgst.String()] = raw
	f.writes++
	return nil
}

func (f *fakeStore) PutManifest(_ context.Context, repo, _ string, payload []byte, tag string) (digest.Digest, error) {
	d := digest.FromBytes(payload)
	f.manifests[repo+"@"+d.String()] = payload
	if tag != "" {
		f.tags[repo+":"+tag] = d.String()
	}
	return d, nil
}

func (f *fakeStore) MarkManualImport(repo string) error {
	f.manual = append(f.manual, repo)
	return nil
}

// packInto packs src as name:version into st and fails the test on error.
func packInto(t *testing.T, st *fakeStore, src, name, version string, opts ...PackerOption) *PackResult {
	t.Helper()
	res, err := NewPacker(st, "", discardLogger(), opts...).
		Pack(context.Background(), PackRequest{Source: src, Name: name, Version: version})
	if err != nil {
		t.Fatalf("Pack(%s): %v", src, err)
	}
	return res
}

// tree materializes a directory from a path → content map. A value
// ending in "/" is a directory; "-> target" declares a symbolic link.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			mustMkdirAll(t, full)
			continue
		}
		mustMkdirAll(t, filepath.Dir(full))
		if target, ok := strings.CutPrefix(content, "-> "); ok {
			// Creating a symbolic link needs a privilege a Windows
			// runner may not hold (NFR-018). Skip as the packing-root
			// case at the bottom of this file already does, rather than
			// fail a fixture the platform refuses to build.
			if err := os.Symlink(filepath.FromSlash(target), full); err != nil {
				t.Skipf("symlinks unavailable, the symlink cases of this tree are not covered: %v", err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// servePacked wires a Server over the store and syncs the packed result,
// so the assertions run against the real FR-047 surface.
func servePacked(t *testing.T, st *fakeStore, res *PackResult) *Server {
	t.Helper()
	s, _ := newTestServer(t, st, Limits{})
	mustSync(t, s, FileSet{Name: res.Name, Repo: res.Repository, ManifestDigest: res.Digest})
	return s
}

// --- round trip ---------------------------------------------------------

// TestPackedTreeIsServedBackByTheFileSetServer is the FR-048 acceptance
// criterion end to end: packing a directory yields a FileSet whose
// extraction reproduces the tree — content, modes and symlinks per §7.4 —
// and whose content is served under /files/<name>/….
func TestPackedTreeIsServedBackByTheFileSetServer(t *testing.T) {
	src := tree(t, map[string]string{
		"index.html":        "<h1>hello</h1>\n",
		"pool/main/a.deb":   "deb-bytes",
		"dists/stable/":     "",
		"link.html":         "-> index.html",
		"pool/deep/nested/": "",
	})
	if err := os.Chmod(filepath.Join(src, "pool", "main", "a.deb"), 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	st := newFakeStore()
	res := packInto(t, st, src, "repo", "1.0.0")
	if res.Reference != "localhost/filesets/repo" || res.Repository != "localhost/filesets/repo" {
		t.Fatalf("reference/repository = %q/%q", res.Reference, res.Repository)
	}
	if res.Signed {
		t.Fatal("a packed FileSet reports Signed = true; Tobby holds no key (ADR-0007)")
	}
	if res.Files != 2 || res.Symlinks != 1 {
		t.Fatalf("counts = %d files, %d symlinks, want 2 and 1", res.Files, res.Symlinks)
	}

	s := servePacked(t, st, res)
	wantBody(t, get(t, s, "/files/repo/index.html"), http.StatusOK, "<h1>hello</h1>\n")
	wantBody(t, get(t, s, "/files/repo/pool/main/a.deb"), http.StatusOK, "deb-bytes")
	// The symlink resolves inside the rootfs and serves its target.
	wantBody(t, get(t, s, "/files/repo/link.html"), http.StatusOK, "<h1>hello</h1>\n")

	// §7.4 step 3: modes survive the round trip.
	rootfs := filepath.Join(setDir(cacheDirOf(t, s), FileSet{Name: res.Name, ManifestDigest: res.Digest}), "rootfs")
	fi, err := os.Lstat(filepath.Join(rootfs, "pool", "main", "a.deb"))
	if err != nil {
		t.Fatalf("lstat extracted file: %v", err)
	}
	// Windows has no POSIX permission bits: Chmod(0o400) there only sets
	// the read-only attribute, Stat reports a synthetic 0444, and the
	// packed header records that instead of 0400. The exact mode bits of
	// a packed and re-extracted tree are therefore not observable on
	// Windows (NFR-018); the rest of the round trip still is.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o400 {
		t.Fatalf("extracted mode = %o, want 400", fi.Mode().Perm())
	}
}

// cacheDirOf reads back the cache directory of a test server.
func cacheDirOf(t *testing.T, s *Server) string {
	t.Helper()
	return s.cacheDir
}

// TestPackedManifestIsASingleManifestImage locks the shape §7.4 asks for:
// one image manifest (never an index), one uncompressed layer whose
// digest is also its diff_id, and the annotation that makes a packed
// FileSet recognizable from the content itself.
func TestPackedManifestIsASingleManifestImage(t *testing.T) {
	st := newFakeStore()
	res := packInto(t, st, tree(t, map[string]string{"a.txt": "a"}), "docs", "2.1.0")

	raw, err := st.Manifest(context.Background(), res.Repository, res.Digest)
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.MediaType != ocispec.MediaTypeImageManifest {
		t.Fatalf("media type = %q, want an image manifest", m.MediaType)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != ocispec.MediaTypeImageLayer {
		t.Fatalf("layers = %+v, want one uncompressed tar layer", m.Layers)
	}
	if m.Annotations[AnnotationPacked] != "true" {
		t.Fatalf("annotations = %v, want %s=true", m.Annotations, AnnotationPacked)
	}

	cfgRaw, err := st.Blob(context.Background(), res.Repository, m.Config.Digest.String())
	if err != nil {
		t.Fatalf("config blob: %v", err)
	}
	var cfg ocispec.Image
	if err := json.NewDecoder(cfgRaw).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if len(cfg.RootFS.DiffIDs) != 1 || cfg.RootFS.DiffIDs[0] != m.Layers[0].Digest {
		t.Fatalf("diff_ids = %v, want the layer digest %s", cfg.RootFS.DiffIDs, m.Layers[0].Digest)
	}
	if cfg.Created != nil {
		t.Fatal("the image configuration carries a creation timestamp; the digest would then change at every packing")
	}
}

// --- reproducibility ----------------------------------------------------

// TestPackingTheSameTreeTwiceYieldsTheSameDigest is the reproducibility
// guarantee of FR-048. The second tree is a separate copy at a different
// path, written later and stamped with different modification times: what
// must not leak into the digest is exactly what differs here.
func TestPackingTheSameTreeTwiceYieldsTheSameDigest(t *testing.T) {
	files := map[string]string{
		"b.txt":       "second",
		"a.txt":       "first",
		"sub/c.txt":   "third",
		"sub/empty/":  "",
		"sub/link":    "-> c.txt",
		"z/y/x/w.txt": "deep",
	}
	first := tree(t, files)
	second := tree(t, files)
	stamp := time.Date(2031, 7, 4, 11, 22, 33, 0, time.UTC)
	if err := filepath.WalkDir(second, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, stamp, stamp)
	}); err != nil {
		t.Fatalf("restamping the copy: %v", err)
	}

	a := packInto(t, newFakeStore(), first, "docs", "1.0.0")
	b := packInto(t, newFakeStore(), second, "docs", "1.0.0")
	if a.Digest != b.Digest {
		t.Fatalf("manifest digests differ across two packings of the same tree:\n%s\n%s", a.Digest, b.Digest)
	}
	if a.LayerDigest != b.LayerDigest {
		t.Fatalf("layer digests differ:\n%s\n%s", a.LayerDigest, b.LayerDigest)
	}

	// And a real difference must still move it.
	if err := os.WriteFile(filepath.Join(second, "a.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c := packInto(t, newFakeStore(), second, "docs", "1.0.0")
	if c.Digest == a.Digest {
		t.Fatal("changing a file did not change the digest")
	}
}

// TestRepackingAnUnchangedTreeTransfersNothing: reproducible digests are
// only useful if the second packing is a no-op (NFR-009, FR-026). The
// store must see no second layer write, and the command must still
// succeed with the same result.
func TestRepackingAnUnchangedTreeTransfersNothing(t *testing.T) {
	st := newFakeStore()
	src := tree(t, map[string]string{"a.txt": "a", "sub/b.txt": "b"})

	first := packInto(t, st, src, "docs", "1.0.0")
	writes := st.writes
	second := packInto(t, st, src, "docs", "1.0.0")

	if second.Digest != first.Digest {
		t.Fatalf("second packing digest = %s, want %s", second.Digest, first.Digest)
	}
	if st.writes != writes {
		t.Fatalf("re-packing wrote %d blobs, want none", st.writes-writes)
	}
}

// --- the hostile corpus (§14.5) -----------------------------------------

// TestPackRefusesUnsafeTrees walks the corpus of local trees packing must
// refuse rather than pass on to an extraction that would refuse them
// later (§14.5, NFR-011). Each case asserts the refusal names the reason.
func TestPackRefusesUnsafeTrees(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
		// windowsSkip, when set, is the reason the case cannot be built
		// on Windows at all — the fixture the corpus needs is not a tree
		// the platform can represent (NFR-018).
		windowsSkip string
	}{
		{
			name:  "absolute symlink",
			files: map[string]string{"ok.txt": "x", "escape": "-> /etc/shadow"},
			want:  "absolute path",
		},
		{
			name:  "symlink climbing out of the root",
			files: map[string]string{"ok.txt": "x", "sub/escape": "-> ../../outside"},
			want:  "escapes the FileSet root",
		},
		{
			name:  "whiteout-looking file name",
			files: map[string]string{".wh.victim": "x"},
			want:  "whiteout prefix",
		},
		{
			name:  "opaque marker",
			files: map[string]string{"dir/.wh..wh..opq": "x"},
			want:  "whiteout prefix",
		},
		{
			// A directory named "C:" is legal on Linux and packs into an
			// entry named "C:/…", which is an absolute path onto another
			// volume on Windows. The extraction side refuses it
			// (TestRejectsTraversalEntries "drive forward"); §14.5 wants
			// the packer to refuse it first (B-025).
			name:  "volume designator in a name",
			files: map[string]string{"ok.txt": "x", "C:/evil.txt": "y"},
			want:  "names a volume",
			windowsSkip: "a colon cannot appear in a Windows path component, so this fixture cannot be built here; " +
				"the same rule is exercised from the extraction side on every platform by " +
				"TestRejectsTraversalEntries (\"drive forward\") in fileserve_test.go",
		},
		{
			// filepath.IsAbs answers with the rules of the platform this
			// binary was built for, so on Linux this target reads as an
			// ordinary relative path. It is not one where the FileSet is
			// extracted.
			name:  "symlink to a Windows volume",
			files: map[string]string{"ok.txt": "x", "escape": `-> C:/Windows/System32/config/SAM`},
			want:  "absolute path",
		},
		{
			name:  "symlink to a UNC share",
			files: map[string]string{"ok.txt": "x", "escape": `-> \\attacker\share\payload`},
			want:  "backslash",
		},
		{
			name:  "backslash in a name",
			files: map[string]string{`a\b.txt`: "x"},
			want:  "backslash",
			windowsSkip: "a backslash cannot appear in a Windows file name, so this fixture becomes the legal tree a/b.txt " +
				"and checkEntryName's backslash rule is unreachable from the packing side here; " +
				"the same rule is exercised from the extraction side on every platform by " +
				"TestRejectsTraversalEntries (\"windows dotdot\" and \"unc share\") in fileserve_test.go",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.windowsSkip != "" {
				t.Skip(tc.windowsSkip)
			}
			st := newFakeStore()
			_, err := NewPacker(st, "", discardLogger()).Pack(context.Background(),
				PackRequest{Source: tree(t, tc.files), Name: "hostile", Version: "1.0.0"})
			var rej *PackRejection
			if !errors.As(err, &rej) {
				t.Fatalf("Pack error = %v, want a *PackRejection", err)
			}
			if !strings.Contains(rej.Reason, tc.want) {
				t.Fatalf("reason = %q, want it to mention %q", rej.Reason, tc.want)
			}
			if len(st.blobs) != 0 || len(st.manifests) != 0 || len(st.manual) != 0 {
				t.Fatalf("a refused tree still wrote to the store: %d blobs, %d manifests, %d marks",
					len(st.blobs), len(st.manifests), len(st.manual))
			}
		})
	}
}

// TestPackRefusesSetuidEntries: §14.5 forbids applying setuid/setgid at
// extraction, so a FileSet carrying them promises something no consumer
// honors — the refusal happens where the operator can still see it.
func TestPackRefusesSetuidEntries(t *testing.T) {
	src := tree(t, map[string]string{"tool": "binary"})
	// This case needs the setuid bit to be SET on the fixture before Pack
	// can be asked to refuse it. Windows never records or reports it, so
	// the guard below always fires there (NFR-018) and the refusal cannot
	// be exercised; the same is true of any filesystem mounted nosuid.
	if err := os.Chmod(filepath.Join(src, "tool"), 0o755|os.ModeSetuid); err != nil {
		t.Skipf("the setuid bit cannot be set here, the §14.5 setuid refusal in Pack is not covered: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(src, "tool")); err != nil || fi.Mode()&os.ModeSetuid == 0 {
		t.Skip("the setuid bit is not reported here, the §14.5 setuid refusal in Pack is not covered")
	}
	_, err := NewPacker(newFakeStore(), "", discardLogger()).Pack(context.Background(),
		PackRequest{Source: src, Name: "tools", Version: "1.0.0"})
	var rej *PackRejection
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "setuid") {
		t.Fatalf("Pack error = %v, want a setuid refusal", err)
	}
}

// TestPackRefusesSpecialFiles: a socket, a FIFO or a device node is
// ignored by extraction (§14.5), so packing one would produce a FileSet
// that is not the tree the operator pointed at.
func TestPackRefusesSpecialFiles(t *testing.T) {
	src := tree(t, map[string]string{"ok.txt": "x"})
	sock := filepath.Join(src, "sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix socket available here, the §14.5 special-file refusal in Pack is not covered: %v", err)
	}
	defer ln.Close() //nolint:errcheck // test listener
	// Windows reports an AF_UNIX socket as an ordinary file rather than a
	// special one (NFR-018), so the fixture this case needs does not exist
	// there and the refusal cannot be exercised.
	if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Skip("this platform does not expose the socket as a special file, the §14.5 special-file refusal in Pack is not covered")
	}

	_, err = NewPacker(newFakeStore(), "", discardLogger()).Pack(context.Background(),
		PackRequest{Source: src, Name: "sockets", Version: "1.0.0"})
	var rej *PackRejection
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, "neither a regular file") {
		t.Fatalf("Pack error = %v, want a special-file refusal", err)
	}
}

// TestPackEnforcesTheExtractionLimits: the anti-decompression-bomb bounds
// of §14.5 apply at packing too, so a tree that could never be extracted
// is refused before it lands.
func TestPackEnforcesTheExtractionLimits(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		limits Limits
		want   string
	}{
		{
			name:   "total size",
			files:  map[string]string{"a": "0123456789", "b": "0123456789"},
			limits: Limits{MaxBytes: 15},
			want:   "total size exceeds",
		},
		{
			name:   "entry count",
			files:  map[string]string{"a": "1", "b": "2", "c": "3"},
			limits: Limits{MaxFiles: 2},
			want:   "entry count exceeds",
		},
		{
			name:   "path depth",
			files:  map[string]string{"a/b/c/d.txt": "deep"},
			limits: Limits{MaxDepth: 3},
			want:   "path depth",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPacker(newFakeStore(), "", discardLogger(), WithPackLimits(tc.limits)).
				Pack(context.Background(), PackRequest{Source: tree(t, tc.files), Name: "big", Version: "1.0.0"})
			var rej *PackRejection
			if !errors.As(err, &rej) || !strings.Contains(rej.Reason, tc.want) {
				t.Fatalf("Pack error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestPackRefusesUnusableSources: an empty directory, a file, and a path
// that does not exist are refused by name rather than producing an empty
// FileSet nobody asked for.
func TestPackRefusesUnusableSources(t *testing.T) {
	empty := t.TempDir()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cases := []struct{ name, source, want string }{
		{"empty directory", empty, "no file to pack"},
		{"a file", file, "not a directory"},
		{"missing path", filepath.Join(empty, "nope"), "cannot be read"},
		{"no path at all", "", "source directory is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPacker(newFakeStore(), "", discardLogger()).Pack(context.Background(),
				PackRequest{Source: tc.source, Name: "x", Version: "1.0.0"})
			var rej *PackRejection
			if !errors.As(err, &rej) || !strings.Contains(rej.Reason, tc.want) {
				t.Fatalf("Pack error = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestPackRefusesUnusableNames keeps the produced reference one a
// registry client can pull.
func TestPackRefusesUnusableNames(t *testing.T) {
	src := tree(t, map[string]string{"a.txt": "a"})
	cases := []struct{ label, name, version string }{
		{"empty name", "", "1.0.0"},
		{"uppercase name", "Docs", "1.0.0"},
		{"traversal in the name", "../escape", "1.0.0"},
		{"empty version", "docs", ""},
		{"tag starting with a dot", "docs", ".1.0.0"},
		{"slash in the version", "docs", "1/0"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, err := NewPacker(newFakeStore(), "", discardLogger()).Pack(context.Background(),
				PackRequest{Source: src, Name: tc.name, Version: tc.version})
			var rej *PackRejection
			if !errors.As(err, &rej) {
				t.Fatalf("Pack error = %v, want a *PackRejection", err)
			}
		})
	}
}

// --- manual-import marking (FR-048) -------------------------------------

// TestPackedFileSetIsRecordedAsAManualImport: the packed repository is
// marked, so FR-045 never prunes it — nothing would bring it back — and
// listings can tell it apart from a Recipe-delivered FileSet.
func TestPackedFileSetIsRecordedAsAManualImport(t *testing.T) {
	st := newFakeStore()
	res := packInto(t, st, tree(t, map[string]string{"a.txt": "a"}), "docs", "1.0.0")
	if len(st.manual) != 1 || st.manual[0] != res.Repository {
		t.Fatalf("manual imports = %v, want [%s]", st.manual, res.Repository)
	}
}

// --- confinement of the remote surfaces (FR-075) ------------------------

// TestPackRootsConfineTheRemoteSurfaces: the API and the UI always pass
// WithPackRoots, so reaching an arbitrary host path takes a configuration
// entry instead of an administrator session. No configured root refuses
// everything — security-reducing settings are opt-in, never a default.
func TestPackRootsConfineTheRemoteSurfaces(t *testing.T) {
	allowed := tree(t, map[string]string{"sub/a.txt": "a"})
	outside := tree(t, map[string]string{"b.txt": "b"})

	denied := func(t *testing.T, roots []string, source string) {
		t.Helper()
		_, err := NewPacker(newFakeStore(), "", discardLogger(), WithPackRoots(roots)).
			Pack(context.Background(), PackRequest{Source: source, Name: "docs", Version: "1.0.0"})
		var d *PackRootDenied
		if !errors.As(err, &d) {
			t.Fatalf("Pack error = %v, want a *PackRootDenied", err)
		}
	}

	t.Run("no configured root refuses every path", func(t *testing.T) {
		denied(t, nil, allowed)
	})
	t.Run("a path outside the roots is refused", func(t *testing.T) {
		denied(t, []string{allowed}, outside)
	})
	t.Run("the root itself and its descendants are allowed", func(t *testing.T) {
		st := newFakeStore()
		packInto(t, st, allowed, "docs", "1.0.0", WithPackRoots([]string{allowed}))
		packInto(t, st, filepath.Join(allowed, "sub"), "sub", "1.0.0", WithPackRoots([]string{allowed}))
	})
	t.Run("a symlink into an allowed root does not smuggle the walk out", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "shortcut")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		denied(t, []string{allowed}, link)
	})
	t.Run("the CLI packer is unconfined", func(t *testing.T) {
		packInto(t, newFakeStore(), outside, "docs", "1.0.0")
	})
}

// --- the closed write surface (FR-047, SRS §5.2) ------------------------

// TestFilesSurfaceAcceptsNoWriteMethod: FR-048 adds a way IN to the store
// through OCI import, and must not reopen one over HTTP. /files/ answers
// 405 to every write method, before and after this feature.
func TestFilesSurfaceAcceptsNoWriteMethod(t *testing.T) {
	st := newFakeStore()
	res := packInto(t, st, tree(t, map[string]string{"a.txt": "a"}), "docs", "1.0.0")
	s := servePacked(t, st, res)

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		"PROPFIND", "MKCOL", "COPY", "MOVE",
	} {
		for _, target := range []string{"/files/", "/files/docs/a.txt", "/files/docs/new.txt"} {
			rec := doRequest(t, s, method, target, nil)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, target, rec.Code)
			}
			if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Errorf("%s %s: Allow = %q, want \"GET, HEAD\"", method, target, allow)
			}
		}
	}
	// The refused write created nothing.
	if rec := get(t, s, "/files/docs/new.txt"); rec.Code != http.StatusNotFound {
		t.Fatalf("after the refused writes, /files/docs/new.txt = %d, want 404", rec.Code)
	}
}
