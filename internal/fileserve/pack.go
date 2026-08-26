// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Operator FileSet packing (FR-048): the other direction of the §7.4
// contract this package already implements for reading. A local file
// tree becomes a single-manifest FileSet image, written into the store
// through the unit-import path (FR-023) — never through an HTTP upload
// surface, which SRS §5.2 keeps closed on purpose — and served, once
// explicitly enabled, by the FR-047 half of this package.
//
// Three properties decide the shape of the code below.
//
//   - Reproducible. Packing the same tree twice produces the same
//     manifest digest: the walk is lexical, timestamps are normalized to
//     the epoch, ownership is dropped, and the layer is an UNCOMPRESSED
//     tar. Uncompressed is the deliberate choice: a compressor's output
//     is a function of its version, so a gzip layer would silently
//     change digest across a Go or library upgrade and re-packing an
//     unchanged tree would land new content in the store. Tar makes the
//     digest a pure function of the file tree.
//   - Safe. What §14.5 forbids at extraction time is refused here, at
//     packing time, where the operator can still see and fix their tree —
//     rather than admitted into a FileSet that fails to extract later.
//     Every refusal names the path and the reason.
//   - Unsigned, and saying so. Tobby holds no key (ADR-0007): a packed
//     FileSet carries no signature, is recorded as a manual import of
//     local origin (store.OriginLocalPack), and is annotated as packed so
//     the marking travels with the content itself.

// Packed FileSet naming (FR-048). Packed content has no source registry,
// so it lands under a reserved nominal reference rather than borrowing a
// registry host it never came from: "localhost" is the one host name the
// relocation convention accepts (ADR-0013) that means "this machine",
// which is exactly the provenance. The resulting reference is the one an
// operator writes in files.filesets[].ref to enable serving (FR-047).
const (
	// PackedHost is the nominal host of every packed FileSet.
	PackedHost = "localhost"
	// PackedNamespace is the repository namespace under that host.
	PackedNamespace = "filesets"
	// AnnotationPacked marks a packed FileSet's image manifest. Unlike
	// the provenance ledger, an annotation is part of the content: it
	// survives an export and reaches any consumer of the image.
	AnnotationPacked = "dev.tobby.fileset/packed"
)

// packedLayerMediaType is the uncompressed OCI filesystem layer. See the
// reproducibility note above for why it is not "+gzip".
const packedLayerMediaType = ocispec.MediaTypeImageLayer

// packedPlatform is what the image config declares. A FileSet is
// platform-independent content published as a single-manifest image
// (§7.4), but the OCI image configuration has no way to say so, and the
// first consumption mode §7.4 names — a Kubernetes image volume — goes
// through runtimes that do read these fields. linux/amd64 is the
// least-surprising answer for a runtime that insists on one; nothing in
// Tobby reads it back.
var packedPlatform = ocispec.Platform{OS: "linux", Architecture: "amd64"}

// PackReference returns the nominal reference of a packed FileSet name —
// the value to put in files.filesets[].ref to serve it (FR-047).
func PackReference(name string) string {
	return PackedHost + "/" + PackedNamespace + "/" + name
}

// Writer is the store write surface packing needs: the direct-to-storage
// path of ADR-0005, digest-verified at commit. It is deliberately not the
// registry HTTP API — FR-048 adds no write surface of any kind.
type Writer interface {
	// HasBlob reports whether repo can already serve the blob.
	HasBlob(ctx context.Context, repo string, dgst digest.Digest) bool
	// WriteBlob streams one blob into repo, verifying dgst at commit.
	WriteBlob(ctx context.Context, repo string, dgst digest.Digest, r io.Reader) error
	// PutManifest stores a manifest payload bit-exactly and tags it.
	PutManifest(ctx context.Context, repo, mediaType string, payload []byte, tag string) (digest.Digest, error)
	// MarkManualImport records repo as a manual import of local origin
	// (FR-048): protected from the FR-045 prune — nothing would bring it
	// back — and individually removable under the FR-044 amendment.
	MarkManualImport(repo string) error
}

// Packer packs local file trees into the store (FR-048).
type Packer struct {
	w      Writer
	base   string
	limits Limits
	// roots confines packing when restricted is set; an empty list then
	// refuses every path. Unrestricted is the local-CLI case, where the
	// operator already holds the host's filesystem rights.
	roots      []string
	restricted bool
	logger     *slog.Logger
}

// PackerOption configures a Packer.
type PackerOption func(*Packer)

// WithPackRoots confines packing to the given directories (and their
// descendants). The remote surfaces — API and UI — always pass this, so
// that reaching an arbitrary host path takes an explicit configuration
// entry rather than an administrator session: an empty list refuses
// every path (FR-075, secure by default). The CLI passes nothing.
func WithPackRoots(roots []string) PackerOption {
	return func(p *Packer) {
		p.restricted = true
		p.roots = roots
	}
}

// WithPackLimits bounds what one packing operation may produce. Zero
// fields pick the documented §14.5 defaults, the same ones extraction
// enforces — a tree that could not be extracted is refused here.
func WithPackLimits(l Limits) PackerOption {
	return func(p *Packer) { p.limits = l.effective() }
}

// NewPacker builds a Packer writing into w, under the instance's
// optional base prefix (FR-035).
func NewPacker(w Writer, basePrefix string, logger *slog.Logger, opts ...PackerOption) *Packer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &Packer{w: w, base: basePrefix, limits: Limits{}.effective(), logger: logger}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// PackRequest is one packing operation: a local directory, the FileSet
// name it lands under, and its version.
type PackRequest struct {
	// Source is the local directory to pack.
	Source string
	// Name is the FileSet name; it becomes the repository path under the
	// reserved namespace and the tag-free half of the reference.
	Name string
	// Version is the tag the packed image carries.
	Version string
}

// PackResult reports what landed in the store.
type PackResult struct {
	Name        string `json:"name"`
	Reference   string `json:"reference"`
	Repository  string `json:"repository"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
	LayerDigest string `json:"layerDigest"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
	Symlinks    int    `json:"symlinks"`
	Bytes       int64  `json:"bytes"`
	// Signed is always false and is reported rather than omitted:
	// ADR-0007 means a packed FileSet can never be anything else, and a
	// missing field reads as an oversight (FR-048).
	Signed bool `json:"signed"`
}

// PackRejection is a local tree refused by the §14.5 safety rules, or a
// name the OCI grammar cannot carry. It names the offending path and the
// reason, so every surface can render the same two facts.
type PackRejection struct {
	// Path is the offending entry, relative to the source root; empty
	// when the refusal is about the request rather than about a file.
	Path string
	// Reason is the refusal, in one clause.
	Reason string
	// Safety marks a refusal that comes from the §14.5 rules rather than
	// from the shape of the request. The two are different answers: a
	// mistyped path is an operational error the caller retypes, an entry
	// that would escape the FileSet root is a policy refusal — and the
	// FR-066 exit codes make scripts able to tell them apart.
	Safety bool
}

func (e *PackRejection) Error() string {
	if e.Path == "" {
		return e.Reason
	}
	return e.Path + ": " + e.Reason
}

// PackRootDenied is a source directory outside the configured pack roots
// (WithPackRoots). Distinct from a PackRejection: nothing is wrong with
// the tree — this surface is simply not allowed to reach it.
type PackRootDenied struct {
	// Source is the resolved absolute path that was refused.
	Source string
}

func (e *PackRootDenied) Error() string {
	return "packing " + e.Source + " is not allowed from this surface"
}

// PackProblem maps a packing failure onto the error taxonomy, once, for
// the three surfaces FR-048 asks for. The CLI, the API and the UI must
// answer the same refusal with the same words and the same class — that
// is what makes the FR-061 parity real rather than declared, and the
// class is also the FR-066 exit code: a policy refusal (the tree is
// unsafe, the path is out of bounds) is not an operational failure (the
// path is wrong).
//
// The contrast with the serving half of this package is deliberate:
// /files/ answers bare HTTP statuses because its clients are package
// managers, while packing is an operator action and gets the full
// what/cause/action treatment.
func PackProblem(err error) *taxonomy.Error {
	var denied *PackRootDenied
	if errors.As(err, &denied) {
		return taxonomy.New(taxonomy.CodePackNotAllowed,
			taxonomy.Params{"path": denied.Source}).WithCause(err)
	}
	var rej *PackRejection
	if errors.As(err, &rej) {
		code := taxonomy.CodePackInput
		if rej.Safety {
			code = taxonomy.CodePackUnsafe
		}
		return taxonomy.New(code, taxonomy.Params{"detail": rej.Error()}).WithCause(err)
	}
	// Anything left is the store refusing the write.
	return taxonomy.New(taxonomy.CodeStoreWrite,
		taxonomy.Params{"detail": err.Error()}).WithCause(err)
}

// Pack packages a local directory as a single-manifest FileSet image and
// writes it into the store (FR-048). The tree is walked twice: once to
// validate it and compute the layer digest — nothing is written while
// anything can still be refused — and once to stream the identical tar
// into the store, where the commit verifies that digest. A tree mutated
// between the two passes therefore cannot land half-described.
func (p *Packer) Pack(ctx context.Context, req PackRequest) (*PackResult, error) {
	if err := validatePackName(req.Name); err != nil {
		return nil, err
	}
	if err := validatePackVersion(req.Version); err != nil {
		return nil, err
	}
	src, err := p.resolveSource(req.Source)
	if err != nil {
		return nil, err
	}

	// Pass 1: validate and measure. The digest of an uncompressed tar is
	// the digest of the layer AND its diff_id — one hash, both roles.
	h := sha256.New()
	size := &countingWriter{}
	stats, err := writeTree(io.MultiWriter(h, size), src, p.limits)
	if err != nil {
		return nil, err
	}
	if stats.total() == 0 {
		return nil, &PackRejection{Reason: "the source directory holds no file to pack"}
	}
	layerDigest := digest.NewDigest(digest.SHA256, h)

	repo, err := relocate.PathWithBase(p.base, PackReference(req.Name))
	if err != nil {
		return nil, &PackRejection{Reason: err.Error()}
	}

	// Pass 2: the same bytes, streamed rather than buffered (NFR-007 —
	// a FileSet is never held whole in memory).
	if err := p.writeLayer(ctx, repo, src, layerDigest, stats); err != nil {
		return nil, err
	}

	cfgRaw, cfgDesc, err := packConfig(layerDigest)
	if err != nil {
		return nil, err
	}
	if !p.w.HasBlob(ctx, repo, cfgDesc.Digest) {
		if err := p.w.WriteBlob(ctx, repo, cfgDesc.Digest, bytes.NewReader(cfgRaw)); err != nil {
			return nil, fmt.Errorf("fileserve: writing the FileSet configuration: %w", err)
		}
	}

	manifestRaw, err := packManifest(&cfgDesc, &ocispec.Descriptor{
		MediaType: packedLayerMediaType,
		Digest:    layerDigest,
		Size:      size.n,
	})
	if err != nil {
		return nil, err
	}
	dgst, err := p.w.PutManifest(ctx, repo, ocispec.MediaTypeImageManifest, manifestRaw, req.Version)
	if err != nil {
		return nil, fmt.Errorf("fileserve: storing the FileSet manifest: %w", err)
	}
	if err := p.w.MarkManualImport(repo); err != nil {
		return nil, fmt.Errorf("fileserve: recording the manual import: %w", err)
	}

	p.logger.LogAttrs(ctx, slog.LevelInfo, "fileset packed",
		slog.String("fileset", req.Name),
		slog.String("repository", repo),
		slog.String("version", req.Version),
		slog.String("digest", dgst.String()),
		slog.Int("files", stats.files),
		slog.Int("directories", stats.dirs),
		slog.Int("symlinks", stats.symlinks),
		slog.Int64("bytes", stats.bytes),
		slog.String("requirement", "FR-048"))

	return &PackResult{
		Name:        req.Name,
		Reference:   PackReference(req.Name),
		Repository:  repo,
		Version:     req.Version,
		Digest:      dgst.String(),
		LayerDigest: layerDigest.String(),
		Files:       stats.files,
		Directories: stats.dirs,
		Symlinks:    stats.symlinks,
		Bytes:       stats.bytes,
		Signed:      false,
	}, nil
}

// writeLayer streams the second pass into the store. The tar is produced
// by a goroutine writing into a pipe; both ends are closed on every exit
// path, and the walk's own error wins over the store's — a tree that
// changed under the packing explains a digest mismatch better than
// "commit failed" does.
func (p *Packer) writeLayer(ctx context.Context, repo, src string, layerDigest digest.Digest, want packStats) error {
	if p.w.HasBlob(ctx, repo, layerDigest) {
		// Re-packing an unchanged tree transfers nothing (NFR-009,
		// FR-026) — which is what reproducible digests are for. Skipping
		// the second walk is also a correctness matter: the store's
		// WriteBlob returns without reading when the blob is already
		// there, and a producer left writing into a closed pipe would
		// report a failure where nothing failed.
		return nil
	}
	pr, pw := io.Pipe()
	type outcome struct {
		stats packStats
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		st, err := writeTree(pw, src, p.limits)
		_ = pw.CloseWithError(err)
		done <- outcome{st, err}
	}()
	werr := p.w.WriteBlob(ctx, repo, layerDigest, pr)
	_ = pr.CloseWithError(werr)
	run := <-done

	if run.err != nil {
		return run.err
	}
	if run.stats != want {
		return &PackRejection{Reason: "the source directory changed while it was being packed"}
	}
	if werr != nil {
		return fmt.Errorf("fileserve: writing the FileSet layer: %w", werr)
	}
	return nil
}

// resolveSource turns the request's path into the absolute, symlink-free
// directory that will be walked, and applies the WithPackRoots
// confinement to that resolved form — a symlink pointing out of an
// allowed root must not smuggle the walk elsewhere.
func (p *Packer) resolveSource(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", &PackRejection{Reason: "a source directory is required"}
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", &PackRejection{Path: source, Reason: err.Error()}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", &PackRejection{Path: source, Reason: "the source directory cannot be read"}
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", &PackRejection{Path: source, Reason: "the source directory cannot be read"}
	}
	if !fi.IsDir() {
		return "", &PackRejection{Path: source, Reason: "the source is not a directory"}
	}
	if !p.allowed(resolved) {
		return "", &PackRootDenied{Source: resolved}
	}
	return resolved, nil
}

// allowed reports whether resolved sits inside a configured pack root.
func (p *Packer) allowed(resolved string) bool {
	if !p.restricted {
		return true
	}
	for _, root := range p.roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		allowedRoot, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue
		}
		if resolved == allowedRoot || strings.HasPrefix(resolved, allowedRoot+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// packStats counts one walk. Comparing two walks compares these values,
// so the struct stays comparable on purpose.
type packStats struct {
	files    int
	dirs     int
	symlinks int
	bytes    int64
}

func (s packStats) total() int { return s.files + s.dirs + s.symlinks }

// countingWriter measures the layer without holding it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(b []byte) (int, error) {
	c.n += int64(len(b))
	return len(b), nil
}

// normalizedModTime is the timestamp every packed entry carries. A file
// tree's modification times are an accident of how it was copied onto
// the host; embedding them would make the digest depend on that accident
// instead of on the content (reproducibility, FR-048).
var normalizedModTime = time.Unix(0, 0).UTC()

// writeTree walks src and writes the FileSet layer tar into w.
//
// Determinism comes from four decisions: filepath.WalkDir orders every
// directory lexically, timestamps are normalized, ownership is dropped
// (a uid is meaningless in content served over read-only HTTP, and
// embedding it would make the digest depend on who ran the command), and
// the PAX format is pinned so a long path does not change the encoding
// of every other entry.
//
// Everything the §14.5 rules forbid at extraction time is refused here
// instead of being silently dropped: the operator asked for these files
// to be served, and a FileSet that quietly holds fewer files than the
// directory it was made from is worse than a refusal that names why.
func writeTree(w io.Writer, src string, lim Limits) (packStats, error) {
	var st packStats
	tw := tar.NewWriter(w)
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return &PackRejection{Path: relOf(src, p), Reason: "cannot be read"}
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return &PackRejection{Path: p, Reason: "is outside the source directory"}
		}
		name := filepath.ToSlash(rel)
		if name == "." {
			// The source directory itself: its own mode is not part of
			// what gets served, and omitting it keeps the tar free of the
			// "." entry the extractor treats specially.
			return nil
		}
		if err := checkEntryName(name, lim); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return &PackRejection{Path: name, Reason: "cannot be read"}
		}
		mode := info.Mode()
		if mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
			// §14.5 forbids applying these at extraction, so a FileSet
			// carrying them promises something no consumer will honor.
			return &PackRejection{Path: name, Reason: "carries a setuid or setgid bit", Safety: true}
		}

		switch {
		case mode.IsDir():
			st.dirs++
			if err := tw.WriteHeader(packHeader(name+"/", tar.TypeDir, mode, 0, "")); err != nil {
				return err
			}
		case mode.IsRegular():
			st.files++
			st.bytes += info.Size()
			if st.bytes > lim.MaxBytes {
				return &PackRejection{Path: name, Reason: fmt.Sprintf("total size exceeds the limit of %d bytes", lim.MaxBytes), Safety: true}
			}
			if err := tw.WriteHeader(packHeader(name, tar.TypeReg, mode, info.Size(), "")); err != nil {
				return err
			}
			if err := copyRegular(tw, p, info.Size(), name); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			st.symlinks++
			target, err := os.Readlink(p)
			if err != nil {
				return &PackRejection{Path: name, Reason: "cannot be read"}
			}
			if err := checkLinkTarget(name, target); err != nil {
				return err
			}
			if err := tw.WriteHeader(packHeader(name, tar.TypeSymlink, mode, 0, target)); err != nil {
				return err
			}
		default:
			// Device nodes, FIFOs, sockets: §14.5 makes extraction ignore
			// them, so packing them would produce a FileSet that is not
			// the tree the operator pointed at.
			return &PackRejection{Path: name, Reason: "is neither a regular file, a directory nor a symbolic link", Safety: true}
		}
		if st.total() > lim.MaxFiles {
			return &PackRejection{Path: name, Reason: fmt.Sprintf("entry count exceeds the limit of %d", lim.MaxFiles), Safety: true}
		}
		return nil
	})
	if walkErr != nil {
		return st, walkErr
	}
	if err := tw.Close(); err != nil {
		return st, fmt.Errorf("fileserve: closing the FileSet layer: %w", err)
	}
	return st, nil
}

// copyRegular streams one file into the tar, refusing a file whose size
// changed since the walk stat'ed it: the tar header is already written
// and a short or long body would corrupt the archive silently.
func copyRegular(tw *tar.Writer, p string, size int64, name string) error {
	f, err := os.Open(p) //nolint:gosec // G304: packing the operator's own directory is the feature
	if err != nil {
		return &PackRejection{Path: name, Reason: "cannot be read"}
	}
	defer f.Close() //nolint:errcheck // read-only file
	n, err := io.Copy(tw, io.LimitReader(f, size))
	if err != nil {
		return &PackRejection{Path: name, Reason: "cannot be read"}
	}
	if n != size {
		return &PackRejection{Path: name, Reason: "changed size while it was being packed"}
	}
	return nil
}

// packHeader builds one normalized tar header. Only the permission bits
// survive from the local mode (§7.4 preserves modes; §14.5 strips
// setuid/setgid/sticky, and packing has already refused the first two).
func packHeader(name string, typeflag byte, mode os.FileMode, size int64, linkname string) *tar.Header {
	return &tar.Header{
		Typeflag: typeflag,
		Name:     name,
		Linkname: linkname,
		Size:     size,
		Mode:     int64(mode.Perm()),
		ModTime:  normalizedModTime,
		Format:   tar.FormatPAX,
	}
}

// checkEntryName applies the path rules of §14.5 to a name produced by
// the walk. WalkDir cannot emit ".." or an absolute path, so what is
// left is what a legitimate local file name can still mean to a tar
// consumer: a whiteout marker, a separator on another platform, or a
// depth no extraction will accept.
func checkEntryName(name string, lim Limits) error {
	if strings.ContainsRune(name, 0) {
		return &PackRejection{Path: name, Reason: "contains a NUL byte", Safety: true}
	}
	if strings.ContainsRune(name, '\\') {
		// A backslash is a path separator on the platforms this FileSet
		// may be extracted on (NFR-018), so it is a traversal primitive
		// there even though it is an ordinary character here.
		return &PackRejection{Path: name, Reason: "contains a backslash, a path separator on other platforms", Safety: true}
	}
	if hasDriveLetter(name) {
		// The same argument one spelling further: a directory literally
		// named "C:" is legal here and packs into an entry named
		// "C:/something", which on Windows is an absolute path onto
		// another volume. §14.5 requires packing to refuse what
		// extraction would refuse, and the extractor refuses this
		// (extract.go, cleanEntryPath) — so refusing it here is what
		// keeps the two ends of the rule the same rule (B-025).
		return &PackRejection{Path: name, Reason: "names a volume, which is an absolute path on other platforms", Safety: true}
	}
	if base := path.Base(name); base == opaqueMarker || strings.HasPrefix(base, whiteoutPrefix) {
		// §7.4 reads these as layer deletions: a real file so named would
		// delete its neighbour instead of being served.
		return &PackRejection{Path: name, Reason: "uses the " + whiteoutPrefix + " whiteout prefix reserved by the image layer format", Safety: true}
	}
	if depth := strings.Count(name, "/") + 1; depth > lim.MaxDepth {
		return &PackRejection{Path: name, Reason: fmt.Sprintf("path depth %d exceeds the limit of %d", depth, lim.MaxDepth), Safety: true}
	}
	return nil
}

// checkLinkTarget refuses a symbolic link that extraction would have to
// refuse too (§14.5): absolute targets, and relative targets that
// resolve outside the FileSet root. The check is lexical, exactly like
// the extractor's — the two must agree, or packing would produce a
// FileSet that cannot be served.
func checkLinkTarget(name, target string) error {
	if target == "" {
		return &PackRejection{Path: name, Reason: "is a symbolic link with an empty target", Safety: true}
	}
	if strings.ContainsRune(target, 0) || strings.ContainsRune(target, '\\') {
		return &PackRejection{Path: name, Reason: "is a symbolic link whose target contains a NUL byte or a backslash", Safety: true}
	}
	if strings.HasPrefix(target, "/") || filepath.IsAbs(target) || hasDriveLetter(target) {
		// filepath.IsAbs alone is not enough and cannot be: it answers
		// with the rules of the platform this binary was BUILT for, and
		// "C:/Windows/System32/config/SAM" is a relative path on Linux
		// and an absolute one on Windows. A FileSet packed on the
		// connected side of a mirror is extracted on the isolated side,
		// which may not be the same operating system — so the lexical
		// rule has to be the same on both, and hasDriveLetter is what
		// makes it so (B-025).
		return &PackRejection{Path: name, Reason: "is a symbolic link to the absolute path " + target + ", which points outside the FileSet", Safety: true}
	}
	resolved := path.Join(path.Dir(name), target)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return &PackRejection{Path: name, Reason: "is a symbolic link to " + target + ", which escapes the FileSet root", Safety: true}
	}
	return nil
}

// relOf renders a walk path relative to the source for a message, and
// falls back to the raw path when it cannot.
func relOf(src, p string) string {
	rel, err := filepath.Rel(src, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

// packConfig builds the OCI image configuration of a packed FileSet.
// "created" is deliberately absent: a timestamp there would change the
// digest of an unchanged tree at every packing. When the packing
// happened is recorded where it belongs — the provenance ledger.
func packConfig(layerDigest digest.Digest) ([]byte, ocispec.Descriptor, error) {
	cfg := ocispec.Image{
		Platform: packedPlatform,
		RootFS: ocispec.RootFS{
			Type: "layers",
			// An uncompressed layer is its own diff: digest and diff_id
			// are the same value.
			DiffIDs: []digest.Digest{layerDigest},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, ocispec.Descriptor{}, fmt.Errorf("fileserve: encoding the FileSet configuration: %w", err)
	}
	return raw, ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(raw),
		Size:      int64(len(raw)),
	}, nil
}

// packManifest builds the single-manifest image of a packed FileSet
// (§7.4: platform-independent content SHOULD be published as one). The
// ocispec structs are marshalled directly rather than a local mirror,
// because field order decides the digest and a hand-written struct is
// how B-018 got the cookbook manifest wrong.
func packManifest(config, layer *ocispec.Descriptor) ([]byte, error) {
	m := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    *config,
		Layers:    []ocispec.Descriptor{*layer},
		Annotations: map[string]string{
			AnnotationPacked: "true",
		},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("fileserve: encoding the FileSet manifest: %w", err)
	}
	return raw, nil
}

// validatePackName checks the FileSet name against the OCI repository
// grammar, so the reference it builds is one a registry client can
// actually pull.
func validatePackName(name string) error {
	if name == "" {
		return &PackRejection{Reason: "a FileSet name is required"}
	}
	if len(name) > 100 {
		return &PackRejection{Reason: "the FileSet name is longer than 100 characters"}
	}
	// One path component, not a path: the name is both the last segment
	// of the reference and the /files/<name>/ URL segment an operator
	// writes to serve it (FR-047), and those two must be the same word.
	if !validPathComponent(name) {
		return &PackRejection{Reason: "the FileSet name must be lowercase alphanumerics, optionally separated by a dot, a dash or an underscore"}
	}
	return nil
}

// validPathComponent implements one component of the OCI repository name
// grammar: lowercase alphanumerics, separated by periods, underscores or
// dashes. It is also, deliberately, a safe URL path segment.
func validPathComponent(s string) bool {
	if s == "" {
		return false
	}
	alnum := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
	}
	if !alnum(s[0]) || !alnum(s[len(s)-1]) {
		return false
	}
	for i := 1; i < len(s)-1; i++ {
		switch c := s[i]; {
		case alnum(c), c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// validatePackVersion checks the version against the OCI tag grammar.
func validatePackVersion(version string) error {
	if version == "" {
		return &PackRejection{Reason: "a version is required"}
	}
	if len(version) > 128 {
		return &PackRejection{Reason: "the version is longer than 128 characters"}
	}
	for i := range len(version) {
		c := version[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', i > 0 && (c == '.' || c == '-'):
		default:
			return &PackRejection{Reason: "the version must be alphanumerics, dots, dashes or underscores, starting with an alphanumeric or an underscore"}
		}
	}
	return nil
}
