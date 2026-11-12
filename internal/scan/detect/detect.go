// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package detect tells, from content alone, whether stored bytes are a
// container image — whatever kind the recipe declared them as (ADR-0017).
// It is scanner-neutral and reads nothing by itself: the caller hands it
// media types and byte streams.
//
// Two questions are answered here:
//
//   - whether an OCI manifest describes an image: its config is an image
//     configuration (IsImageConfig), or one of its layers is declared as a
//     filesystem layer (IsFilesystemLayer) and really is a tar archive;
//   - whether a byte stream — a layer blob, or a file inside a filesystem
//     layer — is an image archive: the output of `docker save`
//     (docker-archive) or an OCI image layout packed as a tar.
//
// Every read is streamed and bounded (Limits): no archive is extracted,
// nothing beyond small metadata documents is held in memory, and a stream
// beyond a bound is an error — never a silent "not an image".
package detect

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Image configuration media types (OCI image spec, Docker image spec v1.2).
const (
	MediaTypeOCIConfig    = "application/vnd.oci.image.config.v1+json"
	MediaTypeDockerConfig = "application/vnd.docker.container.image.v1+json"
)

// filesystemLayers is the closed set of layer media types a container
// runtime unpacks as a root filesystem: OCI tar, tar+gzip, tar+zstd, their
// non-distributable variants, and the Docker schema 2 rootfs layers. A
// type merely ENDING in "tar+gzip" (a Trivy database layer, a Helm chart)
// is not one: no runtime unpacks it, and it is judged by its content.
var filesystemLayers = map[string]bool{
	"application/vnd.oci.image.layer.v1.tar":                       true,
	"application/vnd.oci.image.layer.v1.tar+gzip":                  true,
	"application/vnd.oci.image.layer.v1.tar+zstd":                  true,
	"application/vnd.oci.image.layer.nondistributable.v1.tar":      true,
	"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip": true,
	"application/vnd.oci.image.layer.nondistributable.v1.tar+zstd": true,
	"application/vnd.docker.image.rootfs.diff.tar":                 true,
	"application/vnd.docker.image.rootfs.diff.tar.gzip":            true,
	"application/vnd.docker.image.rootfs.foreign.diff.tar":         true,
	"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip":    true,
	"application/vnd.docker.image.rootfs.diff.tar.zstd":            true,
	"application/vnd.docker.image.rootfs.foreign.diff.tar.zstd":    true,
}

// IsImageConfig reports whether a config media type is an image
// configuration.
func IsImageConfig(mediaType string) bool {
	return mediaType == MediaTypeOCIConfig || mediaType == MediaTypeDockerConfig
}

// IsFilesystemLayer reports whether a layer media type is one a container
// runtime unpacks as a root filesystem.
func IsFilesystemLayer(mediaType string) bool { return filesystemLayers[mediaType] }

// Evidence is what made content an image.
type Evidence string

// The evidences, in the report's vocabulary (scan.Content).
const (
	// EvidenceImageConfig: the manifest's config is an image configuration.
	EvidenceImageConfig Evidence = "image-config"
	// EvidenceFilesystemLayers: the manifest declares filesystem layers,
	// and they are tar archives.
	EvidenceFilesystemLayers Evidence = "filesystem-layers"
	// EvidenceDockerArchive: the bytes are a `docker save` archive.
	EvidenceDockerArchive Evidence = "docker-archive"
	// EvidenceOCILayout: the bytes are an OCI image layout packed as a tar,
	// holding at least one image manifest.
	EvidenceOCILayout Evidence = "oci-layout"
)

// Limits bound one inspection.
type Limits struct {
	// MaxBytes bounds the decompressed bytes read from one stream.
	MaxBytes int64
	// MaxEntries bounds the tar entries of one stream.
	MaxEntries int
	// MaxMetaBytes bounds one metadata document read into memory
	// (manifest.json, index.json, oci-layout, a manifest blob).
	MaxMetaBytes int64
	// MaxHeldBytes bounds the manifest blobs of an OCI layout held in
	// memory, all together, until its index can be judged.
	MaxHeldBytes int64
}

// DefaultLimits are generous for real content and finite for hostile one.
var DefaultLimits = Limits{
	MaxBytes:     64 << 30,
	MaxEntries:   1 << 20,
	MaxMetaBytes: 1 << 20,
	MaxHeldBytes: 16 << 20,
}

// ErrLimit is returned when a stream exceeds a bound: its content could
// not be judged, which a blocking policy treats as not scannable.
var ErrLimit = errors.New("content exceeds an inspection bound")

// Found is a file inside a tar stream that is an image archive.
type Found struct {
	Path     string
	Evidence Evidence
}

// Report is what Inspect learnt about one stream.
type Report struct {
	// Tar is true when the (decompressed) stream is a tar archive.
	Tar bool
	// Archive is set when the stream itself is an image archive.
	Archive Evidence
	// Nested lists the regular files of the tar stream that are image
	// archives themselves (one level deep: an archive inside one of those
	// is not looked into).
	Nested []Found
}

// Inspect reads a stream — plain, gzip or zstd, recognized by its magic
// bytes, never by a name or a media type — and reports whether it is a
// tar archive, an image archive, and which of its files are image
// archives. A stream that is not a tar is recognized from its first 512
// decompressed bytes and is not read further.
func Inspect(ctx context.Context, r io.Reader, lim Limits) (*Report, error) {
	budget := lim.MaxBytes
	return inspect(ctx, r, lim, &budget, true)
}

// inspect reads one stream; budget is shared with the streams nested in
// it, so that many small compressed files cannot each expand to the bound.
func inspect(ctx context.Context, r io.Reader, lim Limits, budget *int64, nested bool) (*Report, error) {
	dr, closeFn, err := Decompress(r)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	br := bufio.NewReaderSize(&bounded{r: dr, left: budget}, 4096)
	head, err := br.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	rep := &Report{}
	if !IsTarHeader(head) {
		return rep, nil
	}
	rep.Tar = true
	w := newWalk(lim)
	tr := tar.NewReader(br)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the tar stream: %w", err)
		}
		w.entries++
		if w.entries > lim.MaxEntries {
			return nil, fmt.Errorf("%w: more than %d tar entries", ErrLimit, lim.MaxEntries)
		}
		name := cleanName(hdr.Name)
		if name == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeSymlink, tar.TypeLink:
			// docker save links layers shared between images.
			w.name(name)
			continue
		default:
			continue
		}
		w.name(name)
		var content io.Reader = tr
		if meta, ok, err := w.metadata(name, hdr.Size, tr); err != nil {
			return nil, err
		} else if ok {
			content = bytes.NewReader(meta)
		}
		if !nested {
			continue
		}
		sub, err := inspect(ctx, content, lim, budget, false)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if sub.Archive != "" {
			rep.Nested = append(rep.Nested, Found{Path: name, Evidence: sub.Archive})
		}
	}
	// What the tar reader did not consume stays bounded and is drained,
	// so a stream past the bound is never judged on its prefix alone.
	if _, err := io.Copy(io.Discard, br); err != nil {
		return nil, err
	}
	rep.Archive = w.verdict()
	return rep, nil
}

// walk accumulates what one tar stream says about itself.
type walk struct {
	lim      Limits
	entries  int
	names    map[string]bool
	docker   []dockerEntry
	layout   bool
	index    []byte
	blobs    map[string][]byte
	heldSize int64
	// oversized names layout blobs too large to hold: unknown content.
	oversized map[string]bool
}

type dockerEntry struct {
	Config string   `json:"Config"`
	Layers []string `json:"Layers"`
}

func newWalk(lim Limits) *walk {
	return &walk{lim: lim, names: map[string]bool{}, blobs: map[string][]byte{}, oversized: map[string]bool{}}
}

// name remembers the entries a docker-archive manifest may reference —
// shallow ones only ("<config>", "<id>/layer.tar", "blobs/sha256/<hex>"),
// so a deep filesystem does not fill memory with paths.
func (w *walk) name(n string) {
	if strings.Count(n, "/") <= 2 {
		w.names[n] = true
	}
}

// metadata reads the small documents that make a tar an image archive.
// It returns their bytes when it consumed the entry.
func (w *walk) metadata(name string, size int64, r io.Reader) (meta []byte, consumed bool, err error) {
	isBlob := strings.HasPrefix(name, "blobs/sha256/") && strings.Count(name, "/") == 2
	if name != "manifest.json" && name != "index.json" && name != "oci-layout" && !isBlob {
		return nil, false, nil
	}
	if size > w.lim.MaxMetaBytes || (isBlob && w.heldSize+size > w.lim.MaxHeldBytes) {
		if isBlob {
			w.oversized["sha256:"+path.Base(name)] = true
		}
		return nil, false, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r, size))
	if err != nil {
		return nil, false, err
	}
	switch name {
	case "manifest.json":
		var entries []dockerEntry
		if json.Unmarshal(raw, &entries) == nil {
			w.docker = entries
		}
	case "index.json":
		w.index = raw
	case "oci-layout":
		var v struct {
			ImageLayoutVersion string `json:"imageLayoutVersion"`
		}
		w.layout = json.Unmarshal(raw, &v) == nil && v.ImageLayoutVersion != ""
	default:
		// Held only when it hashes to its name: a tar may carry the same
		// path twice, and only the content-addressed copy is the one a
		// reader of the layout resolves.
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) == path.Base(name) {
			w.blobs["sha256:"+path.Base(name)] = raw
			w.heldSize += int64(len(raw))
		}
	}
	return raw, true, nil
}

// verdict judges the stream once it has been read whole.
func (w *walk) verdict() Evidence {
	for _, e := range w.docker {
		if e.Config == "" || len(e.Layers) == 0 || !w.names[cleanName(e.Config)] {
			continue
		}
		complete := true
		for _, l := range e.Layers {
			if !w.names[cleanName(l)] {
				complete = false
				break
			}
		}
		if complete {
			return EvidenceDockerArchive
		}
	}
	if w.layout && w.index != nil && w.layoutHoldsImage() {
		return EvidenceOCILayout
	}
	return ""
}

// layoutHoldsImage applies the manifest rule to every manifest the
// layout's index reaches. A manifest too large to have been held is
// counted as an image: its content is unknown, so it is handed to the
// scanner, which refuses it if it is not one.
func (w *walk) layoutHoldsImage() bool {
	var idx manifest
	if json.Unmarshal(w.index, &idx) != nil {
		return false
	}
	seen := map[string]bool{}
	var visit func(ds []descriptor, depth int) bool
	visit = func(ds []descriptor, depth int) bool {
		if depth > 4 {
			return false
		}
		for _, d := range ds {
			if seen[d.Digest] {
				continue
			}
			seen[d.Digest] = true
			if w.oversized[d.Digest] {
				return true
			}
			raw, ok := w.blobs[d.Digest]
			if !ok {
				continue
			}
			var m manifest
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			if len(m.Manifests) > 0 && visit(m.Manifests, depth+1) {
				return true
			}
			if ManifestIsImage(m.Config.MediaType, m.layerTypes()) {
				return true
			}
		}
		return false
	}
	return visit(idx.Manifests, 0)
}

// ManifestIsImage is the manifest rule on media types alone: an image
// config, or at least one filesystem layer. Where the layers can be read,
// the caller also checks that a filesystem layer really is a tar (an ORAS
// push labels plain files with the OCI tar type).
func ManifestIsImage(configType string, layerTypes []string) bool {
	if IsImageConfig(configType) {
		return true
	}
	for _, t := range layerTypes {
		if IsFilesystemLayer(t) {
			return true
		}
	}
	return false
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
}

type manifest struct {
	Config    descriptor   `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []descriptor `json:"manifests"`
}

func (m *manifest) layerTypes() []string {
	out := make([]string, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.MediaType)
	}
	return out
}

// Decompress recognizes gzip and zstd by their magic bytes and returns a
// decoding reader; anything else is returned as it is.
func Decompress(r io.Reader) (io.Reader, func(), error) {
	br := bufio.NewReader(r)
	magic, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, fmt.Errorf("opening a gzip stream: %w", err)
		}
		return zr, func() { _ = zr.Close() }, nil
	case len(magic) == 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<30))
		if err != nil {
			return nil, nil, fmt.Errorf("opening a zstd stream: %w", err)
		}
		return zr, zr.Close, nil
	}
	return br, func() {}, nil
}

// IsTarHeader reports whether block is a tar header: 512 bytes whose
// checksum field matches the header's own sum, the way every tar reader
// validates one. A zero block (an empty archive) is not a header.
func IsTarHeader(block []byte) bool {
	if len(block) < 512 {
		return false
	}
	block = block[:512]
	field := strings.TrimRight(strings.TrimSpace(string(bytes.TrimRight(block[148:156], "\x00 "))), "\x00")
	if field == "" {
		return false
	}
	want, err := strconv.ParseInt(field, 8, 64)
	if err != nil {
		return false
	}
	var unsigned, signed int64
	for i, b := range block {
		if i >= 148 && i < 156 {
			b = ' '
		}
		unsigned += int64(b)
		signed += int64(int8(b))
	}
	return want == unsigned || want == signed
}

// cleanName normalizes an entry name; empty for one that has no place in
// an archive (absolute, or escaping its root).
func cleanName(n string) string {
	n = path.Clean(strings.TrimPrefix(strings.ReplaceAll(n, `\`, "/"), "./"))
	if n == "." || strings.HasPrefix(n, "/") || n == ".." || strings.HasPrefix(n, "../") {
		return ""
	}
	return n
}

// bounded fails a read past its budget instead of truncating it.
type bounded struct {
	r    io.Reader
	left *int64
}

func (b *bounded) Read(p []byte) (int, error) {
	if *b.left <= 0 {
		// One byte more tells a stream that ends exactly at the bound from
		// one that goes past it.
		var one [1]byte
		if n, _ := b.r.Read(one[:]); n > 0 {
			return 0, fmt.Errorf("%w: more than the inspected-bytes bound", ErrLimit)
		}
		return 0, io.EOF
	}
	if int64(len(p)) > *b.left {
		p = p[:*b.left]
	}
	n, err := b.r.Read(p)
	*b.left -= int64(n)
	return n, err
}
