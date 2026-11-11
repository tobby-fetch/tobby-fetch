// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Store is what the adapter reads from the instance's store: manifests and
// blobs by digest, and the recipe graph — the authority on which
// tobby-system ingredient is the current one, including on a transported
// medium written under another instance's base prefix (FR-054).
type Store interface {
	RawManifest(ctx context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error)
	BlobReader(ctx context.Context, repo, dgst string) (io.ReadCloser, error)
	RecipeRecords() ([]store.RecipeRecord, error)
}

// Extraction bounds. The database is large and legitimately so; anything
// beyond these is not a database this adapter will unpack.
const (
	maxDBFileBytes     = 16 << 30
	maxBinaryBytes     = 1 << 30
	maxMetadataBytes   = 1 << 20
	maxManifestEntries = 4096
)

// located is one tobby-system ingredient found in the store.
type located struct {
	Repo   string
	Tag    string
	Digest string
	// At is when the recipe that delivered it was resolved.
	At time.Time
}

// locate finds the current copy of a tobby-system ingredient. With an
// explicit repository it looks for that one; otherwise for any repository
// that is a relocation of nominalRef — under this instance's base prefix
// or under the prefix of the instance that wrote the medium, which a
// reader cannot recompute (RECIPE-SPEC §11.5). The most recently resolved
// delivery wins.
func locate(st Store, explicitRepo, nominalRef string) (*located, error) {
	records, err := st.RecipeRecords()
	if err != nil {
		return nil, err
	}
	suffix := ""
	if explicitRepo == "" {
		p, err := relocate.Path(nominalRef)
		if err != nil {
			return nil, err
		}
		suffix = p
	}
	var best *located
	for i := range records {
		rec := &records[i]
		for _, ing := range rec.Ingredients {
			match := ing.Repo == explicitRepo
			if explicitRepo == "" {
				match = ing.Repo == suffix || strings.HasSuffix(ing.Repo, "/"+suffix)
			}
			if !match || ing.Digest == "" {
				continue
			}
			if best == nil || rec.ResolvedAt.After(best.At) {
				best = &located{Repo: ing.Repo, Tag: ing.Tag, Digest: ing.Digest, At: rec.ResolvedAt}
			}
		}
	}
	return best, nil
}

// ociManifest is the subset of an OCI manifest or index the adapter reads.
type ociManifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"config"`
	Layers []ociDescriptor `json:"layers"`
	// Manifests is set on an index.
	Manifests []ociDescriptor `json:"manifests"`
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant,omitempty"`
	} `json:"platform,omitempty"`
}

func (m *ociManifest) isIndex() bool { return len(m.Manifests) > 0 && len(m.Layers) == 0 }

func readManifest(ctx context.Context, st Store, repo, ref string) (m *ociManifest, raw []byte, err error) {
	raw, mediaType, _, err := st.RawManifest(ctx, repo, ref)
	if err != nil {
		return nil, nil, err
	}
	m = &ociManifest{}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, nil, fmt.Errorf("manifest %s@%s is unreadable: %w", repo, ref, err)
	}
	if m.MediaType == "" {
		m.MediaType = mediaType
	}
	if len(m.Layers)+len(m.Manifests) > maxManifestEntries {
		return nil, nil, fmt.Errorf("manifest %s@%s lists more than %d entries", repo, ref, maxManifestEntries)
	}
	return m, raw, nil
}

// verifiedReader hashes what it reads and, at EOF, compares the result with
// the digest the blob is addressed by: what is unpacked is exactly what
// the signed recipe pinned, even on a medium that travelled.
type verifiedReader struct {
	r    io.Reader
	h    hash.Hash
	want string
	n    int64
	max  int64
}

func newVerifiedReader(r io.Reader, want string, maxBytes int64) (*verifiedReader, error) {
	if !strings.HasPrefix(want, "sha256:") {
		return nil, fmt.Errorf("unsupported digest %q", want)
	}
	return &verifiedReader{r: r, h: sha256.New(), want: want, max: maxBytes}, nil
}

func (v *verifiedReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	v.n += int64(n)
	if v.n > v.max {
		return n, fmt.Errorf("blob %s exceeds %d bytes", v.want, v.max)
	}
	if errors.Is(err, io.EOF) {
		if got := "sha256:" + hex.EncodeToString(v.h.Sum(nil)); got != v.want {
			return n, fmt.Errorf("blob %s hashes to %s", v.want, got)
		}
	}
	return n, err
}

// drain reads the rest of a verified reader, so a blob whose interesting
// entry came early is still checked whole.
func drain(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// DBMetadata is the metadata.json the database carries about itself.
type DBMetadata struct {
	Version    int       `json:"Version"`
	NextUpdate time.Time `json:"NextUpdate"`
	UpdatedAt  time.Time `json:"UpdatedAt"`
}

// unpackDB extracts the database layer of the artifact at repo@digest into
// dir/db/ — the layout Trivy's --cache-dir expects — and returns the
// layer digest and the metadata. Only the two expected regular files are
// written; any other entry is skipped, and a path that is not one of the
// two names is never created (RECIPE-SPEC §14.5 spirit: an archive does
// not choose where it lands).
func unpackDB(ctx context.Context, st Store, repo, dgst, dir string) (layer string, meta DBMetadata, err error) {
	m, _, err := readManifest(ctx, st, repo, dgst)
	if err != nil {
		return "", meta, err
	}
	var desc *ociDescriptor
	for i := range m.Layers {
		if m.Layers[i].MediaType == mediaTypeDBLayer {
			desc = &m.Layers[i]
			break
		}
	}
	if desc == nil {
		return "", meta, fmt.Errorf("%s@%s carries no %s layer: it is not a Trivy database", repo, dgst, mediaTypeDBLayer)
	}
	dbDir := filepath.Join(dir, "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return "", meta, err
	}
	rc, err := st.BlobReader(ctx, repo, desc.Digest)
	if err != nil {
		return "", meta, err
	}
	defer rc.Close() //nolint:errcheck // read side
	vr, err := newVerifiedReader(rc, desc.Digest, maxDBFileBytes)
	if err != nil {
		return "", meta, err
	}
	zr, err := gzip.NewReader(vr)
	if err != nil {
		return "", meta, fmt.Errorf("database layer %s: %w", desc.Digest, err)
	}
	tr := tar.NewReader(zr)
	var gotDB, gotMeta bool
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", meta, fmt.Errorf("database layer %s: %w", desc.Digest, err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		switch path.Clean(strings.TrimPrefix(hdr.Name, "./")) {
		case "trivy.db":
			if err := writeFile(filepath.Join(dbDir, "trivy.db"), tr, maxDBFileBytes); err != nil {
				return "", meta, err
			}
			gotDB = true
		case "metadata.json":
			raw, err := io.ReadAll(io.LimitReader(tr, maxMetadataBytes+1))
			if err != nil {
				return "", meta, err
			}
			if len(raw) > maxMetadataBytes {
				return "", meta, errors.New("database metadata.json is too large")
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				return "", meta, fmt.Errorf("database metadata.json is unreadable: %w", err)
			}
			if err := os.WriteFile(filepath.Join(dbDir, "metadata.json"), raw, 0o600); err != nil {
				return "", meta, err
			}
			gotMeta = true
		}
	}
	if err := drain(zr); err != nil {
		return "", meta, err
	}
	if err := drain(vr); err != nil {
		return "", meta, err
	}
	if !gotDB || !gotMeta {
		return "", meta, fmt.Errorf("database layer %s lacks trivy.db or metadata.json", desc.Digest)
	}
	if meta.UpdatedAt.IsZero() {
		return "", meta, errors.New("database metadata.json records no build timestamp (UpdatedAt): its age could not be stated")
	}
	return desc.Digest, meta, nil
}

// binaryCandidates are the paths, inside a FileSet's merged filesystem,
// where the scanner executable may sit: the upstream image's location, and
// the root of a FileSet a site packed itself.
func binaryCandidates(goos string) []string {
	if goos == "windows" {
		return []string{"trivy.exe", "usr/local/bin/trivy.exe", "bin/trivy.exe"}
	}
	return []string{"usr/local/bin/trivy", "trivy", "bin/trivy"}
}

// unpackBinary materializes the scanner executable delivered by the
// tobby-system channel: the platform child of the FileSet at repo@digest
// (RECIPE-SPEC §7.4 extraction: platform selection, layers in order,
// whiteouts honoured for the one path read), written to dst. It returns
// the sha256 of the written file — the pin every later execution is
// verified against.
func unpackBinary(ctx context.Context, st Store, repo, dgst, goos, goarch, dst string) (string, error) {
	m, _, err := readManifest(ctx, st, repo, dgst)
	if err != nil {
		return "", err
	}
	if m.isIndex() {
		child := ""
		for _, d := range m.Manifests {
			if d.Platform != nil && d.Platform.OS == goos && d.Platform.Architecture == goarch {
				child = d.Digest
				break
			}
		}
		if child == "" {
			return "", fmt.Errorf("%s@%s publishes no %s/%s build of the scanner", repo, dgst, goos, goarch)
		}
		if m, _, err = readManifest(ctx, st, repo, child); err != nil {
			return "", err
		}
	}
	candidates := binaryCandidates(goos)
	// Layers apply in order: the last layer that writes (or whites out) a
	// candidate decides it.
	var chosenLayer, chosenPath string
	for _, l := range m.Layers {
		found, removed, err := scanLayer(ctx, st, repo, l.Digest, candidates)
		if err != nil {
			return "", err
		}
		for _, r := range removed {
			if r == chosenPath {
				chosenLayer, chosenPath = "", ""
			}
		}
		if found != "" {
			chosenLayer, chosenPath = l.Digest, found
		}
	}
	if chosenLayer == "" {
		return "", fmt.Errorf("%s@%s holds no scanner executable at %s", repo, dgst, strings.Join(candidates, ", "))
	}
	return extractFrom(ctx, st, repo, chosenLayer, chosenPath, dst)
}

// openLayer opens a filesystem layer as a tar stream, verified against its
// digest, gzip-decoded when it is compressed.
func openLayer(ctx context.Context, st Store, repo, dgst string) (*tar.Reader, func() error, error) {
	rc, err := st.BlobReader(ctx, repo, dgst)
	if err != nil {
		return nil, nil, err
	}
	vr, err := newVerifiedReader(rc, dgst, 4*maxBinaryBytes)
	if err != nil {
		rc.Close() //nolint:errcheck,gosec // read side, already failing
		return nil, nil, err
	}
	br := &peekReader{r: vr}
	var r io.Reader = br
	if br.gzipped() {
		zr, err := gzip.NewReader(br)
		if err != nil {
			rc.Close() //nolint:errcheck,gosec // read side, already failing
			return nil, nil, err
		}
		r = zr
	}
	finish := func() error {
		defer rc.Close() //nolint:errcheck // read side
		if err := drain(r); err != nil {
			return err
		}
		return drain(vr)
	}
	return tar.NewReader(r), finish, nil
}

// scanLayer reports which candidate a layer writes and which it whites out.
func scanLayer(ctx context.Context, st Store, repo, dgst string, candidates []string) (found string, removed []string, err error) {
	tr, finish, err := openLayer(ctx, st, repo, dgst)
	if err != nil {
		return "", nil, err
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		dir, base := path.Split(name)
		if strings.HasPrefix(base, ".wh.") {
			target := path.Join(dir, strings.TrimPrefix(base, ".wh."))
			for _, c := range candidates {
				if c == target || (base == ".wh..wh..opq" && strings.HasPrefix(c, dir)) {
					removed = append(removed, c)
				}
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		for _, c := range candidates {
			if name == c && found == "" {
				found = c
			}
		}
	}
	return found, removed, finish()
}

// extractFrom writes one entry of a layer to dst, atomically, and returns
// its sha256.
func extractFrom(ctx context.Context, st Store, repo, layer, entry, dst string) (string, error) {
	tr, finish, err := openLayer(ctx, st, repo, layer)
	if err != nil {
		return "", err
	}
	var sum string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || path.Clean(strings.TrimPrefix(hdr.Name, "./")) != entry {
			continue
		}
		if sum, err = writeExecutable(dst, tr); err != nil {
			return "", err
		}
	}
	if err := finish(); err != nil {
		return "", err
	}
	if sum == "" {
		return "", fmt.Errorf("layer %s no longer holds %s", layer, entry)
	}
	return sum, nil
}

// writeFile copies r into p through a temporary file and a rename.
func writeFile(p string, r io.Reader, limit int64) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".partial-*")
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("%s exceeds %d bytes", filepath.Base(p), limit)
	}
	if err != nil {
		os.Remove(tmp.Name()) //nolint:errcheck,gosec // best-effort cleanup
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// writeExecutable writes an executable and returns its sha256.
func writeExecutable(p string, r io.Reader) (string, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	h := sha256.New()
	if err := writeFile(p, io.TeeReader(r, h), maxBinaryBytes); err != nil {
		return "", err
	}
	if err := os.Chmod(p, 0o700); err != nil { //nolint:gosec // G302: an executable the instance itself runs
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// peekReader lets the layer opener sniff the gzip magic without losing it.
type peekReader struct {
	r    io.Reader
	head []byte
}

func (p *peekReader) gzipped() bool {
	buf := make([]byte, 2)
	n, _ := io.ReadFull(p.r, buf)
	p.head = buf[:n]
	return n == 2 && buf[0] == 0x1f && buf[1] == 0x8b
}

func (p *peekReader) Read(b []byte) (int, error) {
	if len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		return n, nil
	}
	return p.r.Read(b)
}

// fileDigest hashes a file on disk.
func fileDigest(p string) (string, error) {
	f, err := os.Open(p) //nolint:gosec // G304: the scanner path is the operator's configuration or this adapter's own
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read side
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
