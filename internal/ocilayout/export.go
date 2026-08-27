// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ocilayout

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Tar arithmetic. The projection of FR-055 has to be exact enough to be
// compared with free space and with a filesystem's per-file limit, so the
// framing is counted rather than estimated.
const (
	tarHeaderSize  = 512
	tarBlockSize   = 512
	tarTrailerSize = 1024
	// ustarMaxSize is the largest size a plain USTAR header encodes (11
	// octal digits). Beyond it the writer prepends a PAX extended header,
	// which the projection accounts for rather than ignoring.
	ustarMaxSize = int64(1)<<33 - 1
)

// exportEpoch is the modification time stamped on every entry of an
// exported archive. Fixed, so the same content exported twice produces
// the same bytes: a medium that can be compared byte for byte with the
// one produced last week is worth more, on a transfer procedure, than
// timestamps nobody reads.
var exportEpoch = time.Unix(0, 0).UTC()

// ExportOptions parameterizes one export.
type ExportOptions struct {
	// Output is the destination path: the archive file, or the directory
	// the layout is written into.
	Output string
	// Format is the shape written there.
	Format Format
	// Overwrite allows replacing an existing destination. Off by default:
	// the destination of an export is usually a medium somebody else
	// prepared.
	Overwrite bool
}

// Report is the outcome of an export.
type Report struct {
	// Refs is the number of index entries written.
	Refs int
	// Manifests and Blobs count the files of blobs/.
	Manifests int
	Blobs     int
	// Bytes is what was actually written to the medium.
	Bytes int64
	// LargestFileBytes is the biggest single file written.
	LargestFileBytes int64
	// Missing is what the selection named and the store did not hold.
	Missing []Missing
	// Output is the final destination path.
	Output string
}

// ErrTargetExists reports a destination an export refuses to replace.
var ErrTargetExists = errors.New("ocilayout: destination already exists")

// Write produces the layout described by p.
//
// The write goes to a sibling "<output>.part" and is renamed into place
// once complete: an interrupted export leaves no half-written archive
// that looks like a finished one (NFR-010). That also makes a resumed
// task (FR-029) simply redo the work rather than append to a corpse.
func Write(ctx context.Context, src Source, p *Plan, opts ExportOptions) (*Report, error) {
	if opts.Output == "" {
		return nil, errors.New("ocilayout: an output path is required")
	}
	format, err := ParseFormat(string(opts.Format))
	if err != nil {
		return nil, err
	}
	final := filepath.Clean(opts.Output)
	if _, err := os.Lstat(final); err == nil {
		if !opts.Overwrite {
			return nil, fmt.Errorf("%w: %s", ErrTargetExists, final)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("ocilayout: inspecting %s: %w", final, err)
	}

	staging := final + ".part"
	if err := os.RemoveAll(staging); err != nil {
		return nil, fmt.Errorf("ocilayout: clearing %s: %w", staging, err)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return nil, fmt.Errorf("ocilayout: creating %s: %w", filepath.Dir(final), err)
	}

	w, err := newLayoutWriter(format, staging)
	if err != nil {
		return nil, err
	}
	report, err := writeInto(ctx, src, p, w)
	if cerr := w.close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}

	if opts.Overwrite {
		if err := os.RemoveAll(final); err != nil {
			_ = os.RemoveAll(staging)
			return nil, fmt.Errorf("ocilayout: replacing %s: %w", final, err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("ocilayout: moving the export into place: %w", err)
	}
	report.Output = final
	return report, nil
}

// writeInto streams the planned layout through w.
func writeInto(ctx context.Context, src Source, p *Plan, w layoutWriter) (*Report, error) {
	report := &Report{Refs: len(p.Refs), Missing: p.Missing}

	indexJSON, err := marshalIndex(&p.Index)
	if err != nil {
		return nil, err
	}
	marker := layoutMarkerBytes()
	for _, f := range []struct {
		name string
		data []byte
	}{
		{ocispec.ImageLayoutFile, marker},
		{ocispec.ImageIndexFile, indexJSON},
	} {
		if err := w.file(f.name, f.data); err != nil {
			return nil, err
		}
		report.Bytes += int64(len(f.data))
		report.LargestFileBytes = max(report.LargestFileBytes, int64(len(f.data)))
	}

	// Manifests first: a consumer reading the archive as a stream meets
	// the documents before the layers they describe.
	for i := range p.manifests {
		b := &p.manifests[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := w.blob(b.Digest, b.Size, bytes.NewReader(b.Payload)); err != nil {
			return nil, err
		}
		report.Manifests++
		report.Bytes += b.Size
		report.LargestFileBytes = max(report.LargestFileBytes, b.Size)
	}

	for i := range p.blobs {
		b := &p.blobs[i]
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rc, err := src.BlobReader(ctx, b.Repo, b.Digest.String())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// A blob a manifest references and the store does not
				// hold. Reported, not fatal: the export is honest about
				// being incomplete, and the missing digest is named so an
				// operator can re-fetch it rather than guess.
				report.Missing = append(report.Missing, Missing{
					Ref: Ref{Repo: b.Repo}, Digest: b.Digest.String(), Reason: MissingBlob,
				})
				continue
			}
			return nil, fmt.Errorf("ocilayout: reading blob %s of %s: %w", b.Digest, b.Repo, err)
		}
		err = w.blob(b.Digest, b.Size, rc)
		// Read side: the close verdict is discarded on purpose. The
		// embedded store's blob reader reports its own closure as an
		// error, and nothing about a read that already produced verified
		// bytes is decided by it.
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		report.Blobs++
		report.Bytes += b.Size
		report.LargestFileBytes = max(report.LargestFileBytes, b.Size)
	}
	return report, nil
}

// layoutWriter is the shape of a layout being produced. Two
// implementations, one caller: the difference between a directory and an
// archive is framing, and nothing above this interface knows which is
// running.
type layoutWriter interface {
	file(name string, data []byte) error
	blob(d digest.Digest, size int64, r io.Reader) error
	close() error
}

func newLayoutWriter(format Format, root string) (layoutWriter, error) {
	if format == FormatDirectory {
		return newDirWriter(root)
	}
	return newTarWriter(root)
}

// blobPath is a blob's path inside the layout, derived from its digest —
// never from anything an archive said.
func blobPath(d digest.Digest) string {
	return ocispec.ImageBlobsDir + "/" + d.Algorithm().String() + "/" + d.Encoded()
}

// dirWriter produces the layout as a directory.
type dirWriter struct{ root string }

func newDirWriter(root string) (*dirWriter, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("ocilayout: creating %s: %w", root, err)
	}
	return &dirWriter{root: root}, nil
}

func (d *dirWriter) file(name string, data []byte) error {
	path := filepath.Join(d.root, filepath.FromSlash(name))
	if err := os.WriteFile(path, data, 0o640); err != nil { //nolint:gosec // G306: exported media content, group-readable like the store's own files
		return fmt.Errorf("ocilayout: writing %s: %w", name, err)
	}
	return nil
}

func (d *dirWriter) blob(dgst digest.Digest, size int64, r io.Reader) error {
	rel := blobPath(dgst)
	path := filepath.Join(d.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("ocilayout: creating %s: %w", filepath.Dir(rel), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640) //nolint:gosec // G304: the path is derived from the blob's digest under the operator's own destination
	if err != nil {
		return fmt.Errorf("ocilayout: creating %s: %w", rel, err)
	}
	err = copyVerified(f, r, dgst, size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("ocilayout: writing %s: %w", rel, err)
	}
	return nil
}

func (*dirWriter) close() error { return nil }

// tarWriter produces the layout as a single tar. Uncompressed on
// purpose: layer blobs are already compressed, so a second pass buys
// nothing, and an uncompressed archive is the one that can be read back
// by seeking to a blob instead of inflating everything before it — which
// is also why an import of one cannot be turned into a decompression
// bomb (NFR-011).
type tarWriter struct {
	f  *os.File
	tw *tar.Writer
}

func newTarWriter(path string) (*tarWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640) //nolint:gosec // G304: the operator's own destination path
	if err != nil {
		return nil, fmt.Errorf("ocilayout: creating %s: %w", path, err)
	}
	tw := tar.NewWriter(f)
	for _, dir := range []string{
		ocispec.ImageBlobsDir,
		ocispec.ImageBlobsDir + "/" + string(digest.SHA256),
	} {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755,
			ModTime: exportEpoch, Format: tar.FormatUSTAR,
		}); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("ocilayout: writing %s/: %w", dir, err)
		}
	}
	return &tarWriter{f: f, tw: tw}, nil
}

func (t *tarWriter) file(name string, data []byte) error {
	if err := t.tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data)),
		ModTime: exportEpoch, Format: tar.FormatUSTAR,
	}); err != nil {
		return fmt.Errorf("ocilayout: writing %s: %w", name, err)
	}
	if _, err := t.tw.Write(data); err != nil {
		return fmt.Errorf("ocilayout: writing %s: %w", name, err)
	}
	return nil
}

func (t *tarWriter) blob(dgst digest.Digest, size int64, r io.Reader) error {
	rel := blobPath(dgst)
	hdr := &tar.Header{
		Typeflag: tar.TypeReg, Name: rel, Mode: 0o644, Size: size,
		ModTime: exportEpoch,
	}
	if size <= ustarMaxSize {
		hdr.Format = tar.FormatUSTAR
	}
	if err := t.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("ocilayout: writing %s: %w", rel, err)
	}
	if err := copyVerified(t.tw, r, dgst, size); err != nil {
		return fmt.Errorf("ocilayout: writing %s: %w", rel, err)
	}
	return nil
}

func (t *tarWriter) close() error {
	err := t.tw.Close()
	if cerr := t.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("ocilayout: closing the archive: %w", err)
	}
	return nil
}

// copyVerified writes exactly size bytes and checks they hash to dgst.
//
// The store already verified this content when it landed, so this is not
// a second opinion about the network: it is about the disk the export is
// read from and the medium it is written to. An export is the thing that
// crosses an air gap, and a bit that rotted since the import is caught
// here rather than three zones later, where "identical digests" would
// already have been claimed.
func copyVerified(w io.Writer, r io.Reader, dgst digest.Digest, size int64) error {
	verifier := dgst.Verifier()
	n, err := io.Copy(io.MultiWriter(w, verifier), io.LimitReader(r, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("blob %s: %d bytes read, %d declared", dgst, n, size)
	}
	if !verifier.Verified() {
		return fmt.Errorf("blob %s: content does not hash to its digest", dgst)
	}
	return nil
}

// tarEntrySize is the on-archive cost of one file entry: its header, its
// content padded to a block, and the PAX extended header the writer
// prepends when the size does not fit a USTAR field.
func tarEntrySize(size int64) int64 {
	n := int64(tarHeaderSize) + roundUpBlock(size)
	if size > ustarMaxSize {
		n += tarHeaderSize + tarBlockSize
	}
	return n
}

func roundUpBlock(size int64) int64 {
	if rem := size % tarBlockSize; rem != 0 {
		return size - rem + tarBlockSize
	}
	return size
}
