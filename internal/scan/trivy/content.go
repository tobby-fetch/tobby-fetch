// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/detect"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Content inspection (ADR-0017): an ingredient declared as something other
// than a container image is scanned when its content is one. The rule is
// package detect's; this file walks the store with it, and materializes
// an image archive found inside a layer as the one-image OCI layout the
// scanner is always given.

// Bounds of one materialized image archive.
const (
	maxArchiveBytes   = maxDBFileBytes
	maxArchiveEntries = 1 << 16
	maxIndexDepth     = 3
	// dockerManifestType is the Docker schema 2 manifest a docker-archive
	// image is described with once materialized.
	dockerManifestType = "application/vnd.docker.distribution.manifest.v2+json"
)

// Inspect judges an ingredient by its content (scan.Scanner). It needs
// neither the binary nor the database. An error is a TBY-SCN-007 taxonomy
// error: the content could not be judged.
func (s *Scanner) Inspect(ctx context.Context, t scan.Target) (*scan.Content, error) {
	key := cacheKey{target: t.Digest, platform: t.Platform, db: "inspect", bin: t.Kind}
	s.stateMu.RLock()
	cached := s.inspected[key]
	s.stateMu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	c, err := inspectContent(ctx, s.opts.Store, t)
	if err != nil {
		ref := t.Repository + "@" + t.Digest
		return nil, taxonomy.New(taxonomy.CodeScanFailed, taxonomy.Params{
			"reference": ref, "detail": "its content could not be inspected: " + err.Error(),
		}).WithCause(err)
	}
	s.stateMu.Lock()
	if len(s.inspected) >= cacheSize {
		clear(s.inspected)
	}
	s.inspected[key] = c
	s.stateMu.Unlock()
	return c, nil
}

// inspectContent applies the rule to the manifest at t and, for an index,
// to every child the store holds (a sparse index carries the selected
// platforms only), nested indexes included.
func inspectContent(ctx context.Context, st Store, t scan.Target) (*scan.Content, error) {
	c := &scan.Content{Declared: t.Kind}
	seen := map[string]bool{}
	var visit func(dgst string, depth int) error
	visit = func(dgst string, depth int) error {
		if seen[dgst] {
			return nil
		}
		seen[dgst] = true
		m, _, err := readManifest(ctx, st, t.Repository, dgst)
		if err != nil {
			if depth > 0 && errors.Is(err, store.ErrNotFound) {
				return nil // not selected, not in the store
			}
			return err
		}
		if !m.isIndex() {
			return inspectManifest(ctx, st, t.Repository, dgst, m, c)
		}
		if depth >= maxIndexDepth {
			return fmt.Errorf("%s nests indexes deeper than %d", dgst, maxIndexDepth)
		}
		for i := range m.Manifests {
			d := &m.Manifests[i]
			if !platformSelected(d, t.Platform) {
				continue
			}
			if child, _, err := readManifest(ctx, st, t.Repository, d.Digest); err == nil && isAttestation(d, child) {
				continue
			}
			if err := visit(d.Digest, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(t.Digest, 0); err != nil {
		return nil, err
	}
	return c, nil
}

// platformSelected applies the narrowing of targetsOf: a child without a
// platform may be selected for any.
func platformSelected(d *ociDescriptor, platform string) bool {
	if platform == "" || d.Platform == nil {
		return true
	}
	label := d.Platform.OS + "/" + d.Platform.Architecture
	if d.Platform.Variant != "" {
		label += "/" + d.Platform.Variant
	}
	return label == platform || strings.HasPrefix(label, platform+"/")
}

// inspectManifest applies the rule to one manifest: an image config, or a
// filesystem layer that really is a tar, makes it an image; every layer
// that is an image archive, or holds files that are, adds those.
func inspectManifest(ctx context.Context, st Store, repo, dgst string, m *ociManifest, c *scan.Content) error {
	fsTar := false
	var archives []scan.DetectedImage
	for _, l := range m.Layers {
		rep, err := inspectBlob(ctx, st, repo, l.Digest)
		if err != nil {
			return fmt.Errorf("layer %s: %w", l.Digest, err)
		}
		if rep.Archive != "" {
			archives = append(archives, scan.DetectedImage{Digest: l.Digest, Evidence: string(rep.Archive)})
		}
		for _, n := range rep.Nested {
			archives = append(archives, scan.DetectedImage{Digest: l.Digest, Path: n.Path, Evidence: string(n.Evidence)})
		}
		if detect.IsFilesystemLayer(l.MediaType) && rep.Tar {
			fsTar = true
		}
	}
	switch {
	case detect.IsImageConfig(m.Config.MediaType):
		c.Images = append(c.Images, scan.DetectedImage{Digest: dgst, Evidence: scan.EvidenceImageConfig})
	case fsTar:
		c.Images = append(c.Images, scan.DetectedImage{Digest: dgst, Evidence: scan.EvidenceFilesystemLayers})
	}
	c.Images = append(c.Images, archives...)
	return nil
}

// inspectBlob reads one blob through the detector. A blob read to its end
// is verified against its digest; one recognized as not being a tar from
// its first bytes is not read further — its content address is the
// store's guarantee, checked by `tobby store verify`.
func inspectBlob(ctx context.Context, st Store, repo, dgst string) (*detect.Report, error) {
	rc, err := st.BlobReader(ctx, repo, dgst)
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	vr, err := newVerifiedReader(rc, dgst, maxDBFileBytes)
	if err != nil {
		return nil, err
	}
	rep, err := detect.Inspect(ctx, vr, detect.DefaultLimits)
	if err != nil {
		return nil, err
	}
	if rep.Tar {
		if err := drain(vr); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// contentKey identifies what a content scan covers, for the result cache:
// empty for a declared image, a digest of the detected images otherwise.
func contentKey(c *scan.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, im := range c.Images {
		b.WriteString(im.Digest + "\x00" + im.Path + "\x00" + im.Evidence + "\x01")
	}
	return "#" + digestBytes([]byte(b.String()))
}

// scanContent scans every image Inspect found: a manifest of the store
// as it is, an image archive once materialized as a one-image OCI layout.
func (s *Scanner) scanContent(ctx context.Context, bin *binary, db *database, t scan.Target) (string, []scan.Finding, error) {
	var (
		version  string
		findings []scan.Finding
	)
	add := func(v string, fs []scan.Finding) {
		if v != "" {
			version = v
		}
		findings = append(findings, fs...)
	}
	for _, im := range t.Content.Images {
		switch im.Evidence {
		case scan.EvidenceImageConfig, scan.EvidenceFilesystemLayers:
			m, _, err := readManifest(ctx, s.opts.Store, t.Repository, im.Digest)
			if err != nil {
				return "", nil, err
			}
			v, fs, err := s.scanImage(ctx, bin, db, s.opts.Store, t.Repository, imageTarget{Digest: im.Digest, MediaType: m.MediaType})
			if err != nil {
				return "", nil, err
			}
			add(v, fs)
		default:
			v, fs, err := s.scanArchive(ctx, bin, db, t.Repository, im)
			if err != nil {
				return "", nil, err
			}
			add(v, fs)
		}
	}
	return version, findings, nil
}

// scanArchive materializes one image archive in a private directory and
// scans every image it holds.
func (s *Scanner) scanArchive(ctx context.Context, bin *binary, db *database, repo string, im scan.DetectedImage) (string, []scan.Finding, error) {
	if err := os.MkdirAll(s.opts.WorkDir, 0o700); err != nil {
		return "", nil, err
	}
	scratch, err := os.MkdirTemp(s.opts.WorkDir, "archive-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(scratch) //nolint:errcheck // best-effort cleanup of a private directory
	fs, targets, err := materialize(ctx, s.opts.Store, repo, im, scratch)
	if err != nil {
		where := im.Digest
		if im.Path != "" {
			where += ":" + im.Path
		}
		return "", nil, fmt.Errorf("the %s at %s: %w", im.Evidence, where, err)
	}
	var (
		version  string
		findings []scan.Finding
	)
	for _, tg := range targets {
		v, f, err := s.scanImage(ctx, bin, db, fs, "", tg)
		if err != nil {
			return "", nil, err
		}
		if v != "" {
			version = v
		}
		findings = append(findings, f...)
	}
	return version, findings, nil
}

// materialize extracts the archive im names — the layer blob itself, or
// one file inside it — into dir, and returns a store over its blobs and
// the image manifests to scan. Only regular files are written, under
// names this function chooses, never under the archive's own: an entry
// name is a lookup key, not a path.
func materialize(ctx context.Context, st Store, repo string, im scan.DetectedImage, dir string) (*fileStore, []imageTarget, error) {
	rc, err := st.BlobReader(ctx, repo, im.Digest)
	if err != nil {
		return nil, nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	vr, err := newVerifiedReader(rc, im.Digest, maxDBFileBytes)
	if err != nil {
		return nil, nil, err
	}
	var src io.Reader = vr
	if im.Path != "" {
		dr, closeFn, err := detect.Decompress(vr)
		if err != nil {
			return nil, nil, err
		}
		defer closeFn()
		tr := tar.NewReader(dr)
		found := false
		for !found {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil, nil, fmt.Errorf("no file %s in the layer", im.Path)
			}
			if err != nil {
				return nil, nil, err
			}
			found = hdr.Typeflag == tar.TypeReg && cleanEntry(hdr.Name) == im.Path
		}
		src = tr
	}
	files, err := extractArchive(ctx, src, filepath.Join(dir, "files"))
	if err != nil {
		return nil, nil, err
	}
	if im.Path == "" {
		if err := drain(vr); err != nil {
			return nil, nil, err
		}
	}
	fs := &fileStore{files: map[string]string{}, manifests: map[string][]byte{}}
	if p, ok := files["manifest.json"]; ok && im.Evidence == scan.EvidenceDockerArchive {
		targets, err := fs.dockerImages(p, files)
		return fs, targets, err
	}
	targets, err := fs.layoutImages(ctx, files)
	return fs, targets, err
}

// extractArchive writes the regular files of a (possibly compressed) tar
// stream under dir, bounded, and returns entry name -> written path; a
// symbolic or hard link resolving to a written file inside the archive is
// an alias of it (docker save links layers shared between images).
func extractArchive(ctx context.Context, r io.Reader, dir string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dr, closeFn, err := detect.Decompress(r)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	files := map[string]string{}
	links := map[string]string{}
	var total int64
	tr := tar.NewReader(dr)
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if n >= maxArchiveEntries {
			return nil, fmt.Errorf("the archive has more than %d entries", maxArchiveEntries)
		}
		name := cleanEntry(hdr.Name)
		if name == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeSymlink:
			// A lookup key among the archive's own names, never a path
			// written to: nothing is created under the link's name.
			links[name] = cleanEntry(path.Dir(name) + "/" + hdr.Linkname)
		case tar.TypeLink:
			links[name] = cleanEntry(hdr.Linkname)
		case tar.TypeReg:
			p := filepath.Join(dir, strconv.Itoa(n))
			left := maxArchiveBytes - total
			if err := writeFile(p, tr, left); err != nil {
				return nil, err
			}
			fi, err := os.Stat(p)
			if err != nil {
				return nil, err
			}
			total += fi.Size()
			files[name] = p
		}
	}
	for name, target := range links {
		if p, ok := files[target]; ok && target != "" {
			files[name] = p
		}
	}
	return files, nil
}

// cleanEntry normalizes an archive entry name; empty for one that has no
// place in an archive.
func cleanEntry(n string) string {
	n = path.Clean(strings.TrimPrefix(strings.ReplaceAll(n, `\`, "/"), "./"))
	if n == "." || strings.HasPrefix(n, "/") || n == ".." || strings.HasPrefix(n, "../") {
		return ""
	}
	return n
}

// fileStore is a Store over the files of a materialized archive: what
// writeLayout copies from, each blob verified against its digest again.
type fileStore struct {
	files     map[string]string // digest -> path
	manifests map[string][]byte // synthesized manifests, by digest
}

var _ Store = (*fileStore)(nil)

func (f *fileStore) RawManifest(_ context.Context, _, ref string) (payload []byte, mediaType, dgst string, err error) {
	if raw, ok := f.manifests[ref]; ok {
		return raw, "", ref, nil
	}
	p, ok := f.files[ref]
	if !ok {
		return nil, "", "", store.ErrNotFound
	}
	raw, err := readSmall(p, maxMetadataBytes*4)
	if err != nil {
		return nil, "", "", err
	}
	if digestBytes(raw) != ref {
		return nil, "", "", fmt.Errorf("manifest %s does not match its digest", ref)
	}
	return raw, "", ref, nil
}

func (f *fileStore) BlobReader(_ context.Context, _, dgst string) (io.ReadCloser, error) {
	p, ok := f.files[dgst]
	if !ok {
		return nil, store.ErrNotFound
	}
	return os.Open(p) //nolint:gosec // G304: a file this adapter wrote under its own private directory
}

func (f *fileStore) RecipeRecords() ([]store.RecipeRecord, error) { return nil, nil }

// dockerImages describes each image of a `docker save` archive as a
// Docker schema 2 manifest over its config and layer files.
func (f *fileStore) dockerImages(manifestPath string, files map[string]string) ([]imageTarget, error) {
	meta, err := readSmall(manifestPath, maxMetadataBytes)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Config string   `json:"Config"`
		Layers []string `json:"Layers"`
	}
	if err := json.Unmarshal(meta, &entries); err != nil {
		return nil, fmt.Errorf("manifest.json is unreadable: %w", err)
	}
	var out []imageTarget
	for _, e := range entries {
		cfgPath, ok := files[cleanEntry(e.Config)]
		if !ok || len(e.Layers) == 0 {
			continue
		}
		cfg, err := f.add(cfgPath, "application/vnd.docker.container.image.v1+json")
		if err != nil {
			return nil, err
		}
		layers := make([]map[string]any, 0, len(e.Layers))
		for _, l := range e.Layers {
			p, ok := files[cleanEntry(l)]
			if !ok {
				return nil, fmt.Errorf("manifest.json names %s, which the archive does not hold", l)
			}
			mt, err := layerType(p)
			if err != nil {
				return nil, err
			}
			d, err := f.add(p, mt)
			if err != nil {
				return nil, err
			}
			layers = append(layers, d)
		}
		m, err := json.Marshal(map[string]any{
			"schemaVersion": 2, "mediaType": dockerManifestType, "config": cfg, "layers": layers,
		})
		if err != nil {
			return nil, err
		}
		d := digestBytes(m)
		f.manifests[d] = m
		out = append(out, imageTarget{Digest: d, MediaType: dockerManifestType})
	}
	if len(out) == 0 {
		return nil, errors.New("the docker archive describes no image")
	}
	return out, nil
}

// add registers a file as a blob and returns its descriptor.
func (f *fileStore) add(p, mediaType string) (map[string]any, error) {
	d, size, err := hashFile(p)
	if err != nil {
		return nil, err
	}
	f.files[d] = p
	return map[string]any{"mediaType": mediaType, "digest": d, "size": size}, nil
}

// layoutImages lists the image manifests an OCI image layout's index
// reaches, by the same rule as the detection.
func (f *fileStore) layoutImages(ctx context.Context, files map[string]string) ([]imageTarget, error) {
	for name, p := range files {
		if hexDigest, ok := strings.CutPrefix(name, "blobs/sha256/"); ok && !strings.Contains(hexDigest, "/") {
			f.files["sha256:"+hexDigest] = p
		}
	}
	idxPath, ok := files["index.json"]
	if !ok {
		return nil, errors.New("the OCI layout has no index.json")
	}
	raw, err := readSmall(idxPath, maxMetadataBytes)
	if err != nil {
		return nil, err
	}
	var idx ociManifest
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("index.json is unreadable: %w", err)
	}
	var out []imageTarget
	for _, d := range idx.Manifests {
		ts, err := targetsOf(ctx, f, "", d.Digest, "")
		if err != nil {
			return nil, err
		}
		for _, tg := range ts {
			m, _, err := readManifest(ctx, f, "", tg.Digest)
			if err != nil {
				return nil, err
			}
			types := make([]string, 0, len(m.Layers))
			for _, l := range m.Layers {
				types = append(types, l.MediaType)
			}
			if detect.ManifestIsImage(m.Config.MediaType, types) {
				out = append(out, tg)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the OCI layout holds no image manifest")
	}
	return out, nil
}

// layerType names a docker-archive layer by its content: uncompressed,
// gzip or zstd.
func layerType(p string) (string, error) {
	fh, err := os.Open(p) //nolint:gosec // G304: a file this adapter wrote under its own private directory
	if err != nil {
		return "", err
	}
	defer fh.Close() //nolint:errcheck // read side
	magic, err := bufio.NewReader(fh).Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		return "application/vnd.docker.image.rootfs.diff.tar.gzip", nil
	case len(magic) == 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		return "application/vnd.oci.image.layer.v1.tar+zstd", nil
	}
	return "application/vnd.docker.image.rootfs.diff.tar", nil
}

func readSmall(p string, limit int64) ([]byte, error) {
	fh, err := os.Open(p) //nolint:gosec // G304: a file this adapter wrote under its own private directory
	if err != nil {
		return nil, err
	}
	defer fh.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(p), limit)
	}
	return raw, nil
}

func hashFile(p string) (dgst string, size int64, err error) {
	fh, err := os.Open(p) //nolint:gosec // G304: a file this adapter wrote under its own private directory
	if err != nil {
		return "", 0, err
	}
	defer fh.Close() //nolint:errcheck // read side
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func digestBytes(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}
