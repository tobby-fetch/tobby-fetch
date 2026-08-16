// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Tests for the FR-047 FileSet resolution: mapping the files.filesets
// configuration onto concrete store content — pinned version or highest
// local semver tag, platform selection on an index, and the refusals that
// keep an ambiguous or absent FileSet from being served silently.

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// fileSetRef is the nominal ingredient reference of the fixture FileSet;
// relocation (ADR-0013) turns it into the repository path below.
const (
	fileSetRef  = "registry.example.com/filesets/site-config"
	fileSetRepo = "registry.example.com/filesets/site-config"
)

// openFileSetStore opens an empty store for one test.
func openFileSetStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	return st
}

// pushTo runs push against the store's own OCI Distribution surface: the
// fixtures are written by a standard registry client over the real
// protocol, never by a mock of it.
func pushTo(t *testing.T, st *store.Store, push func(addr string)) {
	t.Helper()
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	push(srv.Listener.Addr().String())
}

// pushImages writes one random single-platform image per tag and returns
// the tag → manifest digest mapping the resolution must reproduce.
func pushImages(t *testing.T, st *store.Store, tags ...string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	pushTo(t, st, func(addr string) {
		for _, tag := range tags {
			img, err := random.Image(256, 1)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := name.ParseReference(addr + "/" + fileSetRepo + ":" + tag)
			if err != nil {
				t.Fatal(err)
			}
			if err := remote.Write(ref, img); err != nil {
				t.Fatalf("pushing %s: %v", tag, err)
			}
			dgst, err := img.Digest()
			if err != nil {
				t.Fatal(err)
			}
			digests[tag] = dgst.String()
		}
	})
	return digests
}

// pushIndex writes one index tagged tag over the given platforms and
// returns the platform label → child manifest digest mapping.
func pushIndex(t *testing.T, st *store.Store, tag string, platforms ...v1.Platform) map[string]string {
	t.Helper()
	children := map[string]string{}
	pushTo(t, st, func(addr string) {
		idx := v1.ImageIndex(empty.Index)
		for _, p := range platforms {
			img, err := random.Image(256, 1)
			if err != nil {
				t.Fatal(err)
			}
			dgst, err := img.Digest()
			if err != nil {
				t.Fatal(err)
			}
			children[p.OS+"/"+p.Architecture] = dgst.String()
			idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
				Add:        img,
				Descriptor: v1.Descriptor{Platform: &p},
			})
		}
		idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
		ref, err := name.ParseReference(addr + "/" + fileSetRepo + ":" + tag)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.WriteIndex(ref, idx); err != nil {
			t.Fatal(err)
		}
	})
	return children
}

// indexPlatform is the platform block of an index entry.
type indexPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"architecture"`
}

// putSparseIndex stores, byte for byte, an index whose platform manifests
// are deliberately absent: the sparse index a partially mirrored store
// legitimately holds (FR-022). The store keeps it as received, so the
// resolution has to notice the missing content itself.
func putSparseIndex(t *testing.T, st *store.Store, tag string, platforms ...indexPlatform) {
	t.Helper()
	type descriptor struct {
		MediaType string        `json:"mediaType"`
		Digest    string        `json:"digest"`
		Size      int64         `json:"size"`
		Platform  indexPlatform `json:"platform"`
	}
	idx := struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Manifests     []descriptor `json:"manifests"`
	}{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex}
	for _, p := range platforms {
		sum := sha256.Sum256([]byte(p.OS + "/" + p.Arch))
		idx.Manifests = append(idx.Manifests, descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    "sha256:" + hexDigest(sum),
			Size:      512,
			Platform:  p,
		})
	}
	payload, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutManifest(context.Background(), fileSetRepo, ocispec.MediaTypeImageIndex, payload, tag); err != nil {
		t.Fatalf("storing sparse index: %v", err)
	}
}

func hexDigest(sum [sha256.Size]byte) string { return hex.EncodeToString(sum[:]) }

// fileSetConfig builds the serving configuration around the given FileSets.
func fileSetConfig(sets ...config.FileSetServe) *config.Config {
	cfg := config.Default()
	cfg.Mode = config.ModeMirror
	cfg.Files.FileSets = sets
	return &cfg
}

// resolveOne resolves a single FileSet declaration and returns it.
func resolveOne(t *testing.T, st *store.Store, f config.FileSetServe) (fileSet, error) {
	t.Helper()
	sets, err := resolveFileSets(context.Background(), st, fileSetConfig(f))
	if err != nil {
		return fileSet{}, err
	}
	if len(sets) != 1 {
		t.Fatalf("resolved %d FileSets, want 1", len(sets))
	}
	return fileSet{name: sets[0].Name, repo: sets[0].Repo, digest: sets[0].ManifestDigest, anonymous: sets[0].Anonymous}, nil
}

// fileSet is the resolution result under assertion.
type fileSet struct {
	name      string
	repo      string
	digest    string
	anonymous bool
}

// TestResolveFileSetPicksHighestLocalSemver: without a pinned version the
// served content is the highest semver tag actually present locally — 1.10.0
// beats 1.2.0 (semver order, never string order) — and the cosign
// attachment tags ("sha256-…") are not versions and must be ignored.
func TestResolveFileSetPicksHighestLocalSemver(t *testing.T) {
	st := openFileSetStore(t)
	digests := pushImages(t, st, "1.0.0", "1.2.0", "1.10.0", "sha256-c0ffee.sig")

	got, err := resolveOne(t, st, config.FileSetServe{Name: "debs", Ref: fileSetRef, Anonymous: true})
	if err != nil {
		t.Fatalf("resolveFileSets: %v", err)
	}
	if got.digest != digests["1.10.0"] {
		t.Errorf("served digest = %s, want the 1.10.0 manifest %s (semver order, not string order)", got.digest, digests["1.10.0"])
	}
	if got.repo != fileSetRepo {
		t.Errorf("repo = %q, want the relocated path %q", got.repo, fileSetRepo)
	}
	if got.name != "debs" || !got.anonymous {
		t.Errorf("FileSet identity/anonymity lost: %+v", got)
	}
}

// TestResolveFileSetHonorsBasePrefix: the instance base prefix (FR-035)
// applies to the FileSet lookup exactly as it applies to every other
// ingredient — the configuration names the nominal ref, never the
// relocated path.
func TestResolveFileSetHonorsBasePrefix(t *testing.T) {
	st := openFileSetStore(t)
	const prefixed = "zone-b/" + fileSetRepo
	pushTo(t, st, func(addr string) {
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := name.ParseReference(addr + "/" + prefixed + ":1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
	})

	cfg := fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef})
	cfg.Storage.BasePrefix = "zone-b"
	sets, err := resolveFileSets(context.Background(), st, cfg)
	if err != nil {
		t.Fatalf("resolveFileSets: %v", err)
	}
	if len(sets) != 1 || sets[0].Repo != prefixed {
		t.Fatalf("resolved %+v, want one FileSet on %q", sets, prefixed)
	}
}

// TestResolveFileSetPinnedVersion: a pinned version serves exactly that
// tag, and a version absent from the store is a named failure — never a
// silent fallback to another version.
func TestResolveFileSetPinnedVersion(t *testing.T) {
	st := openFileSetStore(t)
	digests := pushImages(t, st, "1.0.0", "2.0.0")

	got, err := resolveOne(t, st, config.FileSetServe{Name: "debs", Ref: fileSetRef, Version: "1.0.0"})
	if err != nil {
		t.Fatalf("resolveFileSets: %v", err)
	}
	if got.digest != digests["1.0.0"] {
		t.Errorf("pinned 1.0.0 served %s, want %s", got.digest, digests["1.0.0"])
	}

	_, err = resolveFileSets(context.Background(), st,
		fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef, Version: "9.9.9"}))
	if err == nil || !strings.Contains(err.Error(), "fileset debs") {
		t.Errorf("pinning an absent version = %v, want a failure naming the FileSet", err)
	}
}

// TestResolveFileSetNoUsableTag: with no semver tag to choose from there is
// no "highest version" to serve. The refusal names the FileSet instead of
// falling back to whatever tag happens to be there.
func TestResolveFileSetNoUsableTag(t *testing.T) {
	st := openFileSetStore(t)
	// Only a non-semver tag: nothing the "highest version" rule can pick.
	pushImages(t, st, "latest")

	_, err := resolveFileSets(context.Background(), st,
		fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef}))
	if err == nil || !strings.Contains(err.Error(), "fileset debs") {
		t.Errorf("resolution over non-semver tags = %v, want a failure naming the FileSet", err)
	}
}

// TestSelectFileSetManifestOnIndex covers the §7.4 step 1 platform
// selection: the configured platform wins, an unknown one is a named
// failure, and a multi-platform index without a configured platform is
// refused with the setting to add — never an arbitrary pick.
func TestSelectFileSetManifestOnIndex(t *testing.T) {
	st := openFileSetStore(t)
	children := pushIndex(t, st, "1.0.0",
		v1.Platform{OS: "linux", Architecture: "amd64"},
		v1.Platform{OS: "linux", Architecture: "arm64"})

	got, err := resolveOne(t, st, config.FileSetServe{Name: "debs", Ref: fileSetRef, Platform: "linux/arm64"})
	if err != nil {
		t.Fatalf("resolveFileSets: %v", err)
	}
	if got.digest != children["linux/arm64"] {
		t.Errorf("selected %s, want the linux/arm64 child %s", got.digest, children["linux/arm64"])
	}

	_, err = resolveFileSets(context.Background(), st,
		fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef, Platform: "windows/amd64"}))
	if err == nil || !strings.Contains(err.Error(), "platform windows/amd64 not found in the index") {
		t.Errorf("unknown platform = %v, want an explicit not-found-in-index error", err)
	}

	_, err = resolveFileSets(context.Background(), st,
		fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef}))
	if err == nil || !strings.Contains(err.Error(), "files.filesets[].platform") {
		t.Errorf("ambiguous index = %v, want a refusal naming the setting to add", err)
	}
}

// TestSelectFileSetManifestSinglePlatformIndex: an index carrying one real
// platform plus buildx-style attestation entries ("unknown/unknown") is not
// ambiguous — the attestations are not platforms, so no configuration is
// needed to serve it.
func TestSelectFileSetManifestSinglePlatformIndex(t *testing.T) {
	st := openFileSetStore(t)
	children := pushIndex(t, st, "1.0.0",
		v1.Platform{OS: "linux", Architecture: "amd64"},
		v1.Platform{OS: "unknown", Architecture: "unknown"})

	got, err := resolveOne(t, st, config.FileSetServe{Name: "debs", Ref: fileSetRef})
	if err != nil {
		t.Fatalf("resolveFileSets: %v", err)
	}
	if got.digest != children["linux/amd64"] {
		t.Errorf("selected %s, want the only real platform %s", got.digest, children["linux/amd64"])
	}
}

// TestSelectFileSetManifestSparseIndex: a partially mirrored index keeps
// its pinned digest (FR-022), so the selected platform may simply not be in
// the store. Serving must refuse and say so, both when the platform is
// configured and when the index carries a single one.
func TestSelectFileSetManifestSparseIndex(t *testing.T) {
	t.Run("configured platform absent locally", func(t *testing.T) {
		st := openFileSetStore(t)
		putSparseIndex(t, st, "1.0.0",
			indexPlatform{OS: "linux", Arch: "amd64"},
			indexPlatform{OS: "linux", Arch: "arm64"})

		_, err := resolveFileSets(context.Background(), st,
			fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef, Platform: "linux/amd64"}))
		if err == nil || !strings.Contains(err.Error(), "not present locally (sparse index)") {
			t.Errorf("sparse index = %v, want an explicit sparse-index refusal", err)
		}
	})

	t.Run("single platform absent locally", func(t *testing.T) {
		st := openFileSetStore(t)
		putSparseIndex(t, st, "1.0.0", indexPlatform{OS: "linux", Arch: "amd64"})

		_, err := resolveFileSets(context.Background(), st,
			fileSetConfig(config.FileSetServe{Name: "debs", Ref: fileSetRef}))
		if err == nil || !strings.Contains(err.Error(), "single platform of") {
			t.Errorf("sparse single-platform index = %v, want an explicit absent-content refusal", err)
		}
	})
}

// TestSelectFileSetManifestRejectsCorruptIndex: an index payload that does
// not parse is reported as such, not treated as a plain manifest.
func TestSelectFileSetManifestRejectsCorruptIndex(t *testing.T) {
	_, err := selectFileSetManifest(context.Background(), openFileSetStore(t), fileSetRepo,
		[]byte("{not json"), ocispec.MediaTypeImageIndex, "sha256:deadbeef", "linux/amd64")
	if err == nil || !strings.Contains(err.Error(), "parsing index") {
		t.Errorf("corrupt index payload = %v, want a parsing-index error", err)
	}
}

// TestResolveFileSetsReportsEveryFailureAndKeepsTheRest is the FR-047
// degradation rule: one unresolvable FileSet — a malformed ref, or content
// that has not arrived yet — must not take the instance down. The healthy
// FileSets are still served, and every failure is reported by name so the
// operator can act.
func TestResolveFileSetsReportsEveryFailureAndKeepsTheRest(t *testing.T) {
	st := openFileSetStore(t)
	digests := pushImages(t, st, "1.0.0")

	sets, err := resolveFileSets(context.Background(), st, fileSetConfig(
		config.FileSetServe{Name: "malformed", Ref: "no-registry-host/filesets/x"},
		config.FileSetServe{Name: "not-synced-yet", Ref: "registry.example.com/filesets/absent"},
		config.FileSetServe{Name: "debs", Ref: fileSetRef},
	))

	if len(sets) != 1 || sets[0].Name != "debs" || sets[0].ManifestDigest != digests["1.0.0"] {
		t.Fatalf("resolved %+v, want only the healthy FileSet on %s", sets, digests["1.0.0"])
	}
	if err == nil {
		t.Fatal("failures were swallowed; the operator would never learn about them")
	}
	for _, want := range []string{"fileset malformed", "explicit registry host", "fileset not-synced-yet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("joined error = %v\n  misses %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "fileset debs") {
		t.Errorf("the healthy FileSet was reported as failing: %v", err)
	}
}

// TestStoreBlobsReadsThroughTheStore: the fileserve read surface is backed
// by the store accessors (ADR-0005), not by the HTTP loopback. Manifests
// come back byte-exact — their digest must still verify — and blobs stream.
func TestStoreBlobsReadsThroughTheStore(t *testing.T) {
	st := openFileSetStore(t)
	digests := pushImages(t, st, "1.0.0")
	ctx := context.Background()
	blobs := storeBlobs{st: st}

	payload, err := blobs.Manifest(ctx, fileSetRepo, digests["1.0.0"])
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	sum := sha256.Sum256(payload)
	if got := "sha256:" + hexDigest(sum); got != digests["1.0.0"] {
		t.Errorf("manifest bytes hash to %s, want %s (the read must be bit-exact)", got, digests["1.0.0"])
	}

	// The layer named by that manifest streams back with its own digest
	// intact — the FileSet extractor depends on exactly that.
	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Layers) == 0 {
		t.Fatal("fixture image has no layer")
	}
	rc, err := blobs.Blob(ctx, fileSetRepo, manifest.Layers[0].Digest)
	if err != nil {
		t.Fatalf("Blob: %v", err)
	}
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	// The storage library's reader reports its own "closed" sentinel from
	// Close; the read result above is what carries the outcome.
	_ = rc.Close()
	blobSum := sha256.Sum256(body)
	if got := "sha256:" + hexDigest(blobSum); got != manifest.Layers[0].Digest {
		t.Errorf("blob bytes hash to %s, want %s", got, manifest.Layers[0].Digest)
	}

	if _, err := blobs.Manifest(ctx, fileSetRepo, "sha256:"+strings.Repeat("00", 32)); err == nil {
		t.Error("an unknown manifest digest must fail, never return empty bytes")
	}
	if _, err := blobs.Blob(ctx, fileSetRepo, "sha256:"+strings.Repeat("00", 32)); err == nil {
		t.Error("an unknown blob digest must fail, never return an empty reader")
	}
}
