// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// A real store, built with the real store API and signed with real
// key material: the corruption tests below have to bite on the layout the
// product actually writes, not on a hand-drawn imitation of it.

const (
	mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIConfig   = "application/vnd.oci.image.config.v1+json"
	mediaTypeOCILayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
	mediaTypeOCIIndex    = "application/vnd.oci.image.index.v1+json"
)

type fixture struct {
	t    *testing.T
	root string
	st   *store.Store
	kp   *sigtest.KeyPair
	keys *sigverify.Keys
}

// trustFor is a Trust implementation over one key set: the DESTINATION
// instance's roots (FR-054 — roots present on the medium are ignored, and
// nothing here ever reads any).
type trustFor struct {
	keys          *sigverify.Keys
	allowUnsigned bool
	scope         string
}

func (t trustFor) Decide(string) media.TrustDecision {
	return media.TrustDecision{Keys: t.keys, AllowUnsigned: t.allowUnsigned, Scope: t.scope}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, logging.New(io.Discard, slog.LevelError))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	kp, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
	if err != nil {
		t.Fatalf("generating key pair: %v", err)
	}
	pem, err := kp.PublicPEM()
	if err != nil {
		t.Fatalf("encoding public key: %v", err)
	}
	keys, err := sigverify.ParsePublicKeys(pem)
	if err != nil {
		t.Fatalf("parsing public key: %v", err)
	}
	return &fixture{t: t, root: root, st: st, kp: kp, keys: keys}
}

func dgstOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// putBlob writes one blob into a repository.
func (f *fixture) putBlob(repo string, payload []byte) string {
	f.t.Helper()
	d := dgstOf(payload)
	parsed, err := digest.Parse(d)
	if err != nil {
		f.t.Fatalf("parsing digest: %v", err)
	}
	if err := f.st.WriteBlob(context.Background(), repo, parsed, bytes.NewReader(payload)); err != nil {
		f.t.Fatalf("writing blob into %s: %v", repo, err)
	}
	return d
}

// putImage stores a single-platform image (config plus one layer) and tags
// it. It returns the manifest digest.
func (f *fixture) putImage(repo, tag string, layer []byte) string {
	f.t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	configDigest := f.putBlob(repo, config)
	layerDigest := f.putBlob(repo, layer)
	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeOCIManifest,
		"config":        map[string]any{"mediaType": mediaTypeOCIConfig, "digest": configDigest, "size": len(config)},
		"layers": []any{
			map[string]any{"mediaType": mediaTypeOCILayer, "digest": layerDigest, "size": len(layer)},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		f.t.Fatal(err)
	}
	d, err := f.st.PutManifest(context.Background(), repo, mediaTypeOCIManifest, raw, tag)
	if err != nil {
		f.t.Fatalf("storing manifest in %s: %v", repo, err)
	}
	return d.String()
}

// putIndex stores a multi-platform index over the given child manifests.
func (f *fixture) putIndex(repo, tag string, children []string) string {
	f.t.Helper()
	descs := make([]any, 0, len(children))
	for _, c := range children {
		descs = append(descs, map[string]any{
			"mediaType": mediaTypeOCIManifest, "digest": c, "size": 100,
			"platform": map[string]any{"architecture": "amd64", "os": "linux"},
		})
	}
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mediaTypeOCIIndex, "manifests": descs,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	d, err := f.st.PutManifest(context.Background(), repo, mediaTypeOCIIndex, raw, tag)
	if err != nil {
		f.t.Fatalf("storing index in %s: %v", repo, err)
	}
	return d.String()
}

// sign publishes a cosign attached signature over subject in repo
// (RECIPE-SPEC §12.2), as the fetch side copies it onto the medium.
func (f *fixture) sign(repo, subject string) {
	f.t.Helper()
	payload, err := sigtest.SimpleSigningPayload("registry.example.com/"+repo, subject)
	if err != nil {
		f.t.Fatal(err)
	}
	sig, err := f.kp.Sign(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	config := []byte("{}")
	configDigest := f.putBlob(repo, config)
	payloadDigest := f.putBlob(repo, payload)
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeOCIManifest,
		"config":        map[string]any{"mediaType": mediaTypeOCIConfig, "digest": configDigest, "size": len(config)},
		"layers": []any{map[string]any{
			"mediaType":   sigverify.MediaTypeSimpleSigning,
			"digest":      payloadDigest,
			"size":        len(payload),
			"annotations": map[string]string{sigverify.AnnotationSignature: sig},
		}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	hexPart := subject[len("sha256:"):]
	if _, err := f.st.PutManifest(context.Background(), repo, mediaTypeOCIManifest, raw, "sha256-"+hexPart+".sig"); err != nil {
		f.t.Fatalf("publishing signature in %s: %v", repo, err)
	}
}

// addRecipe puts one complete delivery on the medium: the ingredient, the
// recipe artifact, its signature, and the store's recipe graph entry.
func (f *fixture) addRecipe(name, version string, signIt bool) store.RecipeRecord {
	f.t.Helper()
	ingredientRepo := "docker.io/library/" + name
	ingredientDigest := f.putImage(ingredientRepo, "1.0", []byte("layer bytes of "+name))

	cookbook := "registry.example.com/cookbook"
	artifactRepo := cookbook + "/" + name
	artifactDigest := f.putImage(artifactRepo, version, []byte("recipe document of "+name))
	if signIt {
		f.sign(artifactRepo, artifactDigest)
	}

	rec := store.RecipeRecord{
		Name: name, Version: version,
		CookbookRepo: artifactRepo, ArtifactRepo: artifactRepo, ArtifactTag: version,
		Digest: artifactDigest, Zone: "zone-a", ResolvedAt: time.Now().UTC(), Verified: signIt,
		Ingredients: []store.IngredientRecord{{
			Name: name, Kind: "Image", Repo: ingredientRepo, Tag: "1.0", Digest: ingredientDigest,
		}},
	}
	if err := f.st.PutRecipeRecord(&rec); err != nil {
		f.t.Fatalf("recording the recipe graph: %v", err)
	}
	// The provenance ledger the fetch path also writes (FR-045): it is
	// covered bookkeeping, so the medium carries it and verification has
	// something to check beyond the recipe graph.
	if err := f.st.SetProvenance(ingredientRepo, &store.Provenance{
		Class: store.ProvenanceRecipe, Recipe: name, RecipeVersion: version,
	}); err != nil {
		f.t.Fatalf("recording provenance: %v", err)
	}
	return rec
}

// write writes the media manifest, as the end of a mirror synchronization
// does (FR-054).
func (f *fixture) write(resolvedAt time.Time) *media.Manifest {
	f.t.Helper()
	m, err := media.Write(context.Background(), f.st, media.WriteOptions{
		Zone: zoneA, RunID: "20260826T101010Z-abcdabcd", ResolvedAt: resolvedAt,
	})
	if err != nil {
		f.t.Fatalf("writing the media manifest: %v", err)
	}
	return m
}

// verify runs destination-side verification with this instance's roots.
func (f *fixture) verify(opts media.VerifyOptions) *media.Report {
	f.t.Helper()
	if opts.Trust == nil {
		opts.Trust = trustFor{keys: f.keys}
	}
	rep, err := media.Verify(context.Background(), f.st, opts)
	if err != nil {
		f.t.Fatalf("verifying the medium: %v", err)
	}
	return rep
}

// path resolves one store-relative slash path on disk.
func (f *fixture) path(slash string) string {
	return filepath.Join(f.root, filepath.FromSlash(slash))
}

// truncate lops the last byte off a file — the corruption FR-054 names
// first ("truncating or corrupting any covered file").
func (f *fixture) truncate(slash string) {
	f.t.Helper()
	p := f.path(slash)
	info, err := os.Stat(p)
	if err != nil {
		f.t.Fatalf("stat %s: %v", slash, err)
	}
	if err := os.Truncate(p, info.Size()-1); err != nil {
		f.t.Fatalf("truncating %s: %v", slash, err)
	}
}

// flip rewrites a file with different bytes of the SAME length, so the
// size check passes and the digest check is the one that has to catch it.
func (f *fixture) flip(slash string) {
	f.t.Helper()
	p := f.path(slash)
	raw, err := os.ReadFile(p) //nolint:gosec // G304: a path this test just built
	if err != nil {
		f.t.Fatalf("reading %s: %v", slash, err)
	}
	if len(raw) == 0 {
		f.t.Fatalf("%s is empty, nothing to flip", slash)
	}
	raw[len(raw)-1] ^= 0xff
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		f.t.Fatalf("writing %s: %v", slash, err)
	}
}

// blobPathOf is where the store keeps the bytes of one digest.
func blobPathOf(dgst string) string {
	h := dgst[len("sha256:"):]
	return "docker/registry/v2/blobs/sha256/" + h[:2] + "/" + h + "/data"
}

// layerDigestOf reads the layer digest out of a stored image manifest.
func (f *fixture) layerDigestOf(repo string) string {
	const reference = "1.0"
	f.t.Helper()
	raw, _, _, err := f.st.RawManifest(context.Background(), repo, reference)
	if err != nil {
		f.t.Fatalf("reading manifest %s@%s: %v", repo, reference, err)
	}
	var doc struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		f.t.Fatal(err)
	}
	if len(doc.Layers) == 0 {
		f.t.Fatalf("manifest %s@%s carries no layer", repo, reference)
	}
	return doc.Layers[0].Digest
}

// storeIngredient builds one recipe-graph ingredient row.
func storeIngredient(repo, tag, dgst string) store.IngredientRecord {
	return store.IngredientRecord{Name: repo, Kind: "Image", Repo: repo, Tag: tag, Digest: dgst}
}
