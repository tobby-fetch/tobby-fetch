// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// dockerManifestMediaType is the Docker schema2 equivalent of the OCI
// image manifest; FileSets imported from Docker registries carry it.
const dockerManifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"

// Whiteout markers (§7.4): OCI image-layer / overlayfs conventions.
const (
	whiteoutPrefix = ".wh."
	opaqueMarker   = ".wh..wh..opq"
)

// extract materializes one FileSet under target following §7.4: layers
// applied in manifest order with whiteouts honored, under the §14.5
// safety rules. The extraction happens in a temporary sibling directory
// and becomes visible only through the final atomic rename, after the
// completion marker is written — a partial extraction is invisible.
func (s *Server) extract(ctx context.Context, set FileSet, target string) error {
	start := time.Now()
	raw, err := s.blobs.Manifest(ctx, set.Repo, set.ManifestDigest)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}
	switch manifest.MediaType {
	case "", ocispec.MediaTypeImageManifest, dockerManifestMediaType:
	default:
		// An index means §7.4 step 1 (platform selection) was skipped by
		// the caller; any other type is not a filesystem image at all.
		return fmt.Errorf("manifest %s is %q, not an image manifest", set.ManifestDigest, manifest.MediaType)
	}

	tmp, err := os.MkdirTemp(s.cacheDir, ".tmp-")
	if err != nil {
		return fmt.Errorf("creating extraction directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // no-op after the successful rename

	st, err := s.extractLayers(ctx, set, manifest.Layers, tmp)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(tmp, markerFile), nil, 0o600); err != nil {
		return fmt.Errorf("writing completion marker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("creating fileset directory: %w", err)
	}
	// A directory without its marker is a leftover partial extraction;
	// clear it so the rename lands cleanly.
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("clearing stale extraction: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("committing extraction: %w", err)
	}

	s.logger.LogAttrs(ctx, slog.LevelInfo, "fileset extracted",
		slog.String("fileset", set.Name),
		slog.String("digest", set.ManifestDigest),
		slog.Int("layers", len(manifest.Layers)),
		slog.Int("files", st.files),
		slog.Int64("bytes", st.bytes),
		slog.Int("ignored", st.ignored),
		slog.Duration("duration", time.Since(start)))
	return nil
}

// extractLayers applies the layers in order into tmp/rootfs. All writes
// go through an os.Root: even a hostile layer that plants an internal
// symlink and then writes through it cannot touch anything outside the
// rootfs (§14.5, NFR-011).
func (s *Server) extractLayers(ctx context.Context, set FileSet, layers []ocispec.Descriptor, tmp string) (*extraction, error) {
	rootfsDir := filepath.Join(tmp, "rootfs")
	if err := os.Mkdir(rootfsDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating rootfs directory: %w", err)
	}
	root, err := os.OpenRoot(rootfsDir)
	if err != nil {
		return nil, fmt.Errorf("opening rootfs root: %w", err)
	}
	defer root.Close() //nolint:errcheck // directory handle; nothing buffered

	ext := &extraction{
		root:   root,
		limits: s.limits,
		logger: s.logger.With(slog.String("fileset", set.Name), slog.String("digest", set.ManifestDigest)),
		// "." carries the rootfs mode when a layer has an explicit "./"
		// entry; directories created implicitly default to 0o755.
		dirModes: map[string]os.FileMode{},
	}
	for i := range layers {
		layer := &layers[i]
		if err := s.applyLayer(ctx, set.Repo, layer, ext); err != nil {
			return nil, fmt.Errorf("layer %s: %w", layer.Digest, err)
		}
	}
	// Directory modes are applied last: a faithful 0o555 directory would
	// otherwise block the extraction of its own children.
	for dir, mode := range ext.dirModes {
		if err := root.Chmod(dir, mode); err != nil {
			return nil, fmt.Errorf("restoring directory mode of %q: %w", dir, err)
		}
	}
	return ext, nil
}

// applyLayer streams one layer blob — never fully in memory — through
// the media-type decompressor and applies its tar entries.
func (s *Server) applyLayer(ctx context.Context, repo string, layer *ocispec.Descriptor, ext *extraction) error {
	rc, err := s.blobs.Blob(ctx, repo, layer.Digest.String())
	if err != nil {
		return fmt.Errorf("opening blob: %w", err)
	}
	defer rc.Close() //nolint:errcheck // read-only stream

	content, closeDec, err := newDecompressor(rc, layer.MediaType)
	if err != nil {
		return err
	}
	defer closeDec()

	tr := tar.NewReader(content)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ext.applyEntry(ctx, hdr, tr); err != nil {
			return err
		}
	}
}

// newDecompressor wraps the blob stream according to the layer media
// type: plain tar, tar+gzip, or tar+zstd (klauspost/compress).
func newDecompressor(r io.Reader, mediaType string) (io.Reader, func(), error) {
	switch {
	case strings.HasSuffix(mediaType, "+gzip"), strings.HasSuffix(mediaType, ".tar.gzip"):
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("opening gzip stream: %w", err)
		}
		return gz, func() { _ = gz.Close() }, nil
	case strings.HasSuffix(mediaType, "+zstd"):
		zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, nil, fmt.Errorf("opening zstd stream: %w", err)
		}
		return zr, zr.Close, nil
	case strings.HasSuffix(mediaType, ".tar"), strings.HasSuffix(mediaType, "+tar"):
		return r, func() {}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported layer media type %q", mediaType)
	}
}

// extraction is the mutable state of one FileSet extraction.
type extraction struct {
	root    *os.Root
	limits  Limits
	logger  *slog.Logger
	files   int
	bytes   int64
	ignored int
	// dirModes defers directory permissions to the end of the extraction
	// (see extractLayers); whiteouts drop the entries they delete.
	dirModes map[string]os.FileMode
}

// reject logs one §14.5 safety rejection — reason and path, never
// silent — and returns the error that aborts the extraction.
func (e *extraction) reject(ctx context.Context, reason, entryPath string) error {
	e.logger.LogAttrs(ctx, slog.LevelWarn, "fileset entry rejected",
		slog.String("reason", reason),
		slog.String("path", entryPath))
	return fmt.Errorf("unsafe layer entry %q: %s", entryPath, reason)
}

// applyEntry materializes one tar entry (or applies its whiteout).
func (e *extraction) applyEntry(ctx context.Context, hdr *tar.Header, content io.Reader) error {
	clean, err := cleanEntryPath(hdr.Name)
	if err != nil {
		return e.reject(ctx, err.Error(), hdr.Name)
	}

	if base := path.Base(clean); base == opaqueMarker || strings.HasPrefix(base, whiteoutPrefix) {
		return e.applyWhiteout(ctx, clean, base)
	}
	if clean == "." {
		// The root directory itself: only its mode is meaningful.
		if hdr.Typeflag == tar.TypeDir {
			e.dirModes["."] = hdr.FileInfo().Mode().Perm()
			return nil
		}
		return e.reject(ctx, "non-directory entry at the rootfs root", hdr.Name)
	}
	if depth := strings.Count(clean, "/") + 1; depth > e.limits.MaxDepth {
		return e.reject(ctx, fmt.Sprintf("path depth %d exceeds limit %d", depth, e.limits.MaxDepth), hdr.Name)
	}
	e.files++
	if e.files > e.limits.MaxFiles {
		return e.reject(ctx, fmt.Sprintf("file count exceeds limit %d", e.limits.MaxFiles), hdr.Name)
	}

	// FileInfo().Mode().Perm() masks to 0o777: setuid/setgid/sticky are
	// stripped by construction (§14.5).
	perm := hdr.FileInfo().Mode().Perm()

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := e.replaceExisting(clean, true); err != nil {
			return err
		}
		if err := e.ensureDir(clean); err != nil {
			return err
		}
		e.dirModes[clean] = perm
		return nil
	case tar.TypeReg:
		return e.writeFile(ctx, clean, perm, content)
	case tar.TypeSymlink:
		return e.writeSymlink(ctx, clean, hdr.Linkname)
	case tar.TypeLink:
		return e.writeHardlink(ctx, clean, hdr.Linkname)
	default:
		// Devices, FIFOs, sockets and unknown types are ignored (§14.5),
		// counted and reported in the extraction log line.
		e.files--
		e.ignored++
		return nil
	}
}

// applyWhiteout handles `.wh.<name>` deletions and the `.wh..wh..opq`
// opaque marker (§7.4): strict semantics, a deleted file cannot
// resurface from a lower layer.
func (e *extraction) applyWhiteout(ctx context.Context, clean, base string) error {
	dir := path.Dir(clean)
	if base == opaqueMarker {
		// The directory restarts empty: drop everything lower layers put
		// in it, keep the directory itself.
		if err := e.ensureDir(dir); err != nil {
			return err
		}
		d, err := e.root.Open(dir)
		if err != nil {
			return fmt.Errorf("opening opaque directory %q: %w", dir, err)
		}
		names, err := d.Readdirnames(-1)
		if cerr := d.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("listing opaque directory %q: %w", dir, err)
		}
		for _, name := range names {
			victim := path.Join(dir, name)
			if err := e.root.RemoveAll(victim); err != nil {
				return fmt.Errorf("emptying opaque directory %q: %w", dir, err)
			}
			e.dropModesUnder(victim)
		}
		return nil
	}
	victim := strings.TrimPrefix(base, whiteoutPrefix)
	if victim == "" || victim == "." || victim == ".." {
		return e.reject(ctx, "invalid whiteout target", clean)
	}
	victimPath := path.Join(dir, victim)
	if err := e.root.RemoveAll(victimPath); err != nil {
		return fmt.Errorf("applying whiteout of %q: %w", victimPath, err)
	}
	e.dropModesUnder(victimPath)
	return nil
}

func (e *extraction) writeFile(ctx context.Context, clean string, perm os.FileMode, content io.Reader) error {
	if err := e.replaceExisting(clean, false); err != nil {
		return err
	}
	if err := e.ensureParents(clean); err != nil {
		return err
	}
	f, err := e.root.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %q: %w", clean, err)
	}
	budget := min(e.limits.MaxBytes-e.bytes, math.MaxInt64-1)
	n, copyErr := io.Copy(f, io.LimitReader(content, budget+1))
	e.bytes += n
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("writing %q: %w", clean, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %q: %w", clean, closeErr)
	}
	if n > budget {
		return e.reject(ctx, fmt.Sprintf("total size exceeds limit %d bytes", e.limits.MaxBytes), clean)
	}
	if err := e.root.Chmod(clean, perm); err != nil {
		return fmt.Errorf("restoring mode of %q: %w", clean, err)
	}
	return nil
}

// writeSymlink materializes a symlink as-is after validating that its
// target resolves lexically inside the rootfs; a violation is an error,
// never a silent skip (§14.5). The serve-time os.Root remains the
// authoritative barrier for resolution chains this lexical check cannot
// see.
func (e *extraction) writeSymlink(ctx context.Context, clean, linkname string) error {
	if linkname == "" || strings.HasPrefix(linkname, "/") {
		return e.reject(ctx, fmt.Sprintf("absolute or empty symlink target %q", linkname), clean)
	}
	resolved := path.Join(path.Dir(clean), linkname)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return e.reject(ctx, fmt.Sprintf("symlink target %q escapes the rootfs", linkname), clean)
	}
	if err := e.replaceExisting(clean, false); err != nil {
		return err
	}
	if err := e.ensureParents(clean); err != nil {
		return err
	}
	if err := e.root.Symlink(linkname, clean); err != nil {
		return fmt.Errorf("creating symlink %q: %w", clean, err)
	}
	return nil
}

// writeHardlink links to an already-extracted, internal regular file;
// anything else is rejected (§14.5).
func (e *extraction) writeHardlink(ctx context.Context, clean, linkname string) error {
	target, err := cleanEntryPath(linkname)
	if err != nil {
		return e.reject(ctx, fmt.Sprintf("hardlink target: %s", err), clean)
	}
	fi, err := e.root.Lstat(target)
	if err != nil || !fi.Mode().IsRegular() {
		return e.reject(ctx, fmt.Sprintf("hardlink target %q is not an extracted regular file", target), clean)
	}
	if err := e.replaceExisting(clean, false); err != nil {
		return err
	}
	if err := e.ensureParents(clean); err != nil {
		return err
	}
	if err := e.root.Link(target, clean); err != nil {
		return fmt.Errorf("creating hardlink %q: %w", clean, err)
	}
	return nil
}

// replaceExisting clears whatever lower layers left at the path so the
// new entry replaces it — except a directory over a directory, which
// merges (§7.4). Removing first also guarantees a file entry never
// writes through a lower layer's symlink.
func (e *extraction) replaceExisting(clean string, isDir bool) error {
	fi, err := e.root.Lstat(clean)
	if errors.Is(err, fs.ErrNotExist) {
		// Nothing there yet — including absent parents, which
		// ensureParents creates right after.
		return nil
	}
	if err != nil {
		return fmt.Errorf("probing %q: %w", clean, err)
	}
	if fi.IsDir() {
		if isDir {
			return nil
		}
		if err := e.root.RemoveAll(clean); err != nil {
			return fmt.Errorf("replacing directory %q: %w", clean, err)
		}
		e.dropModesUnder(clean)
		return nil
	}
	if err := e.root.Remove(clean); err != nil {
		return fmt.Errorf("replacing %q: %w", clean, err)
	}
	return nil
}

// ensureDir creates dir (and parents) inside the rootfs, recording the
// default mode for the segments no layer describes explicitly.
func (e *extraction) ensureDir(dir string) error {
	if dir == "." {
		return nil
	}
	if err := e.root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating directory %q: %w", dir, err)
	}
	prefix := ""
	for _, seg := range strings.Split(dir, "/") {
		prefix = path.Join(prefix, seg)
		if _, ok := e.dirModes[prefix]; !ok {
			e.dirModes[prefix] = 0o755
		}
	}
	return nil
}

func (e *extraction) ensureParents(clean string) error {
	return e.ensureDir(path.Dir(clean))
}

// dropModesUnder forgets recorded directory modes at and under a deleted
// path, so the final chmod pass only touches what still exists.
func (e *extraction) dropModesUnder(victim string) {
	for dir := range e.dirModes {
		if dir == victim || strings.HasPrefix(dir, victim+"/") {
			delete(e.dirModes, dir)
		}
	}
}

// cleanEntryPath validates a tar entry name against §14.5 — no absolute
// paths, no ".." components — and normalizes it to a slash path relative
// to the rootfs ("." for the root itself).
func cleanEntryPath(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(name, 0) {
		return "", errors.New("path contains a NUL byte")
	}
	if strings.HasPrefix(name, "/") {
		return "", errors.New("absolute path")
	}
	// The ".." check runs on the raw components: §14.5 rejects the
	// component itself, not merely names that still escape after Clean.
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", errors.New(`path contains a ".." component`)
		}
	}
	return path.Clean(name), nil
}
