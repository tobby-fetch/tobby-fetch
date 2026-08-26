// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ocilayout

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Reading a layout somebody else produced.
//
// The archive is untrusted data (NFR-011), and the defence is structural
// rather than a list of forbidden strings: no name taken from the
// archive is ever joined onto a filesystem path. An entry is matched
// against the three shapes the image-spec defines — "oci-layout",
// "index.json", "blobs/<algorithm>/<encoded>" — and a blob is thereafter
// addressed by the digest it must hash to. There is no code path in
// which "../../etc/cron.d/x" is a place something gets written, because
// there is no code path in which an archive name becomes a destination.
//
// The archive is also read without inflating anything: exports are
// uncompressed tars (layer blobs are already compressed), so a
// decompression bomb has nothing to expand into, and a compressed
// archive is refused with the one-line fix rather than streamed through
// an unbounded inflater.

// Bounds on what a layout's own metadata may cost before anything of it
// is believed.
const (
	maxLayoutMarkerBytes = 4 << 10
	// maxLayoutEntries bounds a tar's entry count: a scan that never
	// finishes is a denial of service like any other.
	maxLayoutEntries = 1 << 20
)

// gzipMagic is what a gzip stream starts with. An export is
// uncompressed, so meeting it means the operator compressed the medium.
var gzipMagic = []byte{0x1f, 0x8b}

// ErrNotLayout reports a directory or archive that is not an OCI image
// layout.
var ErrNotLayout = errors.New("ocilayout: not an OCI image layout")

// UnsafeEntryError reports an archive entry refused before it was read
// (NFR-011). It names the entry exactly as the archive spelled it: an
// operator handed a hostile medium needs to see what was on it.
type UnsafeEntryError struct {
	Entry  string
	Reason string
}

func (e *UnsafeEntryError) Error() string {
	return fmt.Sprintf("ocilayout: refusing archive entry %q: %s", e.Entry, e.Reason)
}

// The refusal reasons. Stable, untranslated labels.
const (
	reasonAbsolutePath = "absolute paths are not allowed in a layout archive"
	reasonTraversal    = "the path escapes the archive root"
	reasonLink         = "links are not allowed in a layout archive"
	reasonSpecial      = "only regular files and directories are allowed in a layout archive"
	reasonTooManyItems = "the archive declares more entries than a layout can hold"
	reasonCompressed   = "the archive is compressed; an OCI image layout archive is an uncompressed tar (decompress it first)"
)

// layoutReader is a layout opened for reading, whichever shape it has.
type layoutReader interface {
	// marker returns the bytes of a top-level metadata file.
	marker(name string, limit int64) ([]byte, error)
	// has reports whether the layout holds a blob.
	has(d digest.Digest) bool
	// blob opens one blob and reports the size the layout stores it at.
	blob(d digest.Digest) (io.ReadCloser, int64, error)
	// ignored lists the entries that were neither metadata nor blobs.
	ignored() []string
	close() error
}

// openLayout opens a directory or an archive, deciding on what the path
// actually is rather than on its extension: an operator who named their
// archive "payload" and their directory "payload.tar" gets the right
// reader either way.
func openLayout(input string) (layoutReader, error) {
	info, err := os.Stat(input)
	if err != nil {
		return nil, fmt.Errorf("ocilayout: opening %s: %w", input, err)
	}
	if info.IsDir() {
		return &dirReader{root: input}, nil
	}
	return openTarReader(input)
}

// dirReader reads a layout laid out as a directory.
type dirReader struct{ root string }

func (d *dirReader) marker(name string, limit int64) ([]byte, error) {
	return readBoundedFile(filepath.Join(d.root, filepath.FromSlash(name)), name, limit)
}

func (d *dirReader) path(dgst digest.Digest) string {
	return filepath.Join(d.root, filepath.FromSlash(blobPath(dgst)))
}

func (d *dirReader) has(dgst digest.Digest) bool {
	info, err := os.Lstat(d.path(dgst))
	return err == nil && info.Mode().IsRegular()
}

func (d *dirReader) blob(dgst digest.Digest) (io.ReadCloser, int64, error) {
	p := d.path(dgst)
	// Lstat, not Stat: a blob that is a symlink is refused rather than
	// followed. Digest verification would catch a substituted target, but
	// a link to a device or a fifo would first be read — the refusal is
	// cheaper and says what it means (NFR-011).
	info, err := os.Lstat(p)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %s", ErrNotFound, blobPath(dgst))
	}
	if !info.Mode().IsRegular() {
		return nil, 0, &UnsafeEntryError{Entry: blobPath(dgst), Reason: reasonLink}
	}
	f, err := os.Open(p) //nolint:gosec // G304: the path is derived from a digest, never from the archive
	if err != nil {
		return nil, 0, fmt.Errorf("ocilayout: opening %s: %w", blobPath(dgst), err)
	}
	return f, info.Size(), nil
}

func (*dirReader) ignored() []string { return nil }
func (*dirReader) close() error      { return nil }

// tarEntry is where one blob's bytes sit inside the archive.
type tarEntry struct {
	offset int64
	size   int64
}

// tarReader reads a layout laid out as a single uncompressed tar.
//
// One scan records where every blob starts; reads afterwards seek
// straight to it. That is why the archive must be uncompressed — and
// why it is worth insisting: the alternative is either holding the whole
// medium in memory or expanding it to a second copy on a disk that was
// sized for one.
type tarReader struct {
	f       *os.File
	blobs   map[digest.Digest]tarEntry
	markers map[string][]byte
	skipped []string
}

func openTarReader(input string) (*tarReader, error) {
	f, err := os.Open(input) //nolint:gosec // G304: the operator's own archive path
	if err != nil {
		return nil, fmt.Errorf("ocilayout: opening %s: %w", input, err)
	}
	r := &tarReader{f: f, blobs: map[digest.Digest]tarEntry{}, markers: map[string][]byte{}}
	if err := r.scan(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

// scan walks the archive once, refusing what must never be read and
// recording where the rest lives.
func (t *tarReader) scan() error {
	var magic [2]byte
	if _, err := io.ReadFull(t.f, magic[:]); err == nil && bytes.Equal(magic[:], gzipMagic) {
		return &UnsafeEntryError{Entry: filepath.Base(t.f.Name()), Reason: reasonCompressed}
	}
	if _, err := t.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("ocilayout: rewinding the archive: %w", err)
	}

	counter := &countingReader{r: t.f}
	tr := tar.NewReader(counter)
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNotLayout, err)
		}
		entries++
		if entries > maxLayoutEntries {
			return &UnsafeEntryError{Entry: hdr.Name, Reason: reasonTooManyItems}
		}
		// The data of this entry starts exactly where the reader stands:
		// the tar reader consumes header blocks and nothing more.
		offset := counter.n
		name, kind, err := classifyEntry(hdr)
		if err != nil {
			return err
		}
		switch kind {
		case entryIgnored:
			t.skipped = append(t.skipped, hdr.Name)
		case entryDirectory:
		case entryMarker:
			raw, err := readBounded(tr, name, maxManifestBytes)
			if err != nil {
				return err
			}
			t.markers[name] = raw
		case entryBlob:
			d := digest.Digest(name)
			t.blobs[d] = tarEntry{offset: offset, size: hdr.Size}
		}
	}
}

func (t *tarReader) marker(name string, limit int64) ([]byte, error) {
	raw, ok := t.markers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s is missing from %s", ErrNotLayout, name, filepath.Base(t.f.Name()))
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrNotLayout, name, len(raw), limit)
	}
	return raw, nil
}

func (t *tarReader) has(d digest.Digest) bool {
	_, ok := t.blobs[d]
	return ok
}

func (t *tarReader) blob(d digest.Digest) (io.ReadCloser, int64, error) {
	e, ok := t.blobs[d]
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrNotFound, blobPath(d))
	}
	return io.NopCloser(io.NewSectionReader(t.f, e.offset, e.size)), e.size, nil
}

func (t *tarReader) ignored() []string { return t.skipped }

func (t *tarReader) close() error { return t.f.Close() }

// entryKind classifies one archive entry against the layout's shapes.
type entryKind int

const (
	entryIgnored entryKind = iota
	entryDirectory
	entryMarker
	entryBlob
)

// classifyEntry decides what an archive entry is, and refuses outright
// what has no place in a layout. The refusals come first: a symlink is
// rejected on its type, before its target is looked at, and a traversing
// name is rejected on its shape, before anything is done with it.
func classifyEntry(hdr *tar.Header) (name string, kind entryKind, err error) {
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	case tar.TypeSymlink, tar.TypeLink:
		return "", entryIgnored, &UnsafeEntryError{Entry: hdr.Name, Reason: reasonLink}
	case tar.TypeXGlobalHeader:
		// Metadata the reader already applied; it carries no payload.
		return "", entryIgnored, nil
	default:
		return "", entryIgnored, &UnsafeEntryError{Entry: hdr.Name, Reason: reasonSpecial}
	}

	raw := hdr.Name
	if strings.ContainsRune(raw, '\\') || strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return "", entryIgnored, &UnsafeEntryError{Entry: raw, Reason: reasonAbsolutePath}
	}
	clean := path.Clean(strings.TrimPrefix(raw, "./"))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", entryIgnored, &UnsafeEntryError{Entry: raw, Reason: reasonTraversal}
	}
	if hdr.Typeflag == tar.TypeDir {
		return clean, entryDirectory, nil
	}
	switch clean {
	case ocispec.ImageLayoutFile, ocispec.ImageIndexFile:
		return clean, entryMarker, nil
	}
	if d, ok := blobDigestOf(clean); ok {
		return string(d), entryBlob, nil
	}
	// A safe path that is not part of the layout — a stray README, a
	// docker-archive's manifest.json. Ignored and listed in the report:
	// silently dropping content from a transfer medium is how an operator
	// learns too late that half of it never arrived.
	return clean, entryIgnored, nil
}

// blobDigestOf reads the digest a "blobs/<algorithm>/<encoded>" path
// addresses, and reports whether the path is one at all.
func blobDigestOf(clean string) (digest.Digest, bool) {
	parts := strings.Split(clean, "/")
	if len(parts) != 3 || parts[0] != ocispec.ImageBlobsDir {
		return "", false
	}
	d := digest.NewDigestFromEncoded(digest.Algorithm(parts[1]), parts[2])
	if err := d.Validate(); err != nil {
		return "", false
	}
	return d, true
}

// countingReader tracks how many bytes the tar reader has consumed, which
// is where the next entry's data begins.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// readBounded reads at most limit bytes, refusing anything longer rather
// than truncating it: a manifest that does not fit is not a manifest.
func readBounded(r io.Reader, name string, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("ocilayout: reading %s: %w", name, err)
	}
	if n > limit {
		return nil, fmt.Errorf("%w: %s is over the %d-byte limit", ErrNotLayout, name, limit)
	}
	return buf.Bytes(), nil
}

func readBoundedFile(file, name string, limit int64) ([]byte, error) {
	f, err := os.Open(file) //nolint:gosec // G304: a fixed metadata name under the layout root
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s is missing", ErrNotLayout, name)
	}
	if err != nil {
		return nil, fmt.Errorf("ocilayout: opening %s: %w", name, err)
	}
	defer f.Close() //nolint:errcheck // read side
	return readBounded(f, name, limit)
}
