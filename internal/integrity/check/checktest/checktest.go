// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package checktest builds real stores for the tests of the on-demand
// integrity verification (FR-057): made with the real store API, signed
// with real key material, and corrupted on disk the way a failing disk or
// a careless copy does. Corruption tests have to bite on the layout the
// product actually writes, not on a hand-drawn imitation of it — which is
// why the command-line and the instance tests share this one builder with
// the walk's own.
//
// It is a test helper in a regular package, like sigverify/sigtest: nothing
// in the shipped binary imports it.
package checktest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

const (
	mtManifest = "application/vnd.oci.image.manifest.v1+json"
	mtConfig   = "application/vnd.oci.image.config.v1+json"
	mtLayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
	mtIndex    = "application/vnd.oci.image.index.v1+json"

	// Cookbook is the nominal cookbook the fixture recipes come from.
	Cookbook = "registry.example.com/cookbook"
)

// Fixture is one real store with a signing key.
type Fixture struct {
	T    *testing.T
	Root string
	St   *store.Store
	// KP is the key the fixture signs with; Keys its public half as a
	// trust set.
	KP   *sigtest.KeyPair
	Keys *sigverify.Keys
}

// NewKeyPair generates a signing key.
func NewKeyPair(t *testing.T) *sigtest.KeyPair {
	t.Helper()
	kp, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

// KeysOf is the trust set of one key pair's public half.
func KeysOf(t *testing.T, kp *sigtest.KeyPair) *sigverify.Keys {
	t.Helper()
	pem, err := kp.PublicPEM()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := sigverify.ParsePublicKeys(pem)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// New opens a fresh store and generates its signing key.
func New(t *testing.T) *Fixture {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	kp := NewKeyPair(t)
	return &Fixture{T: t, Root: root, St: st, KP: kp, Keys: KeysOf(t, kp)}
}

// Digest is the sha256 digest of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PutBlob writes one blob into a repository and returns its digest.
func (f *Fixture) PutBlob(repo string, payload []byte) string {
	f.T.Helper()
	d := Digest(payload)
	if err := f.St.WriteBlob(context.Background(), repo, digest.Digest(d), bytes.NewReader(payload)); err != nil {
		f.T.Fatalf("writing blob into %s: %v", repo, err)
	}
	return d
}

// PutImage stores a single-platform image (a config and one layer) and
// tags it; it returns the manifest digest.
func (f *Fixture) PutImage(repo, tag string, layer []byte) string {
	f.T.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	cd, ld := f.PutBlob(repo, config), f.PutBlob(repo, layer)
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mtManifest,
		"config": map[string]any{"mediaType": mtConfig, "digest": cd, "size": len(config)},
		"layers": []any{map[string]any{"mediaType": mtLayer, "digest": ld, "size": len(layer)}},
	})
	if err != nil {
		f.T.Fatal(err)
	}
	d, err := f.St.PutManifest(context.Background(), repo, mtManifest, raw, tag)
	if err != nil {
		f.T.Fatal(err)
	}
	return d.String()
}

// PutIndex stores an index over the given child manifests and tags it.
func (f *Fixture) PutIndex(repo, tag string, children ...string) string {
	f.T.Helper()
	descs := make([]any, 0, len(children))
	for _, c := range children {
		descs = append(descs, map[string]any{"mediaType": mtManifest, "digest": c, "size": 100})
	}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mtIndex, "manifests": descs})
	if err != nil {
		f.T.Fatal(err)
	}
	d, err := f.St.PutManifest(context.Background(), repo, mtIndex, raw, tag)
	if err != nil {
		f.T.Fatal(err)
	}
	return d.String()
}

// Sign publishes a cosign attached signature over subject, signed by kp.
func (f *Fixture) Sign(kp *sigtest.KeyPair, repo, subject string) {
	f.T.Helper()
	payload, err := sigtest.SimpleSigningPayload("registry.example.com/"+repo, subject)
	if err != nil {
		f.T.Fatal(err)
	}
	sig, err := kp.Sign(payload)
	if err != nil {
		f.T.Fatal(err)
	}
	config := []byte("{}")
	cd, pd := f.PutBlob(repo, config), f.PutBlob(repo, payload)
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mtManifest,
		"config": map[string]any{"mediaType": mtConfig, "digest": cd, "size": len(config)},
		"layers": []any{map[string]any{
			"mediaType": sigverify.MediaTypeSimpleSigning, "digest": pd, "size": len(payload),
			"annotations": map[string]string{sigverify.AnnotationSignature: sig},
		}},
	})
	if err != nil {
		f.T.Fatal(err)
	}
	if _, err := f.St.PutManifest(context.Background(), repo, mtManifest, raw, "sha256-"+subject[len("sha256:"):]+".sig"); err != nil {
		f.T.Fatal(err)
	}
}

// AddRecipe puts one complete delivery in the store: the ingredient, the
// recipe artifact, its signature (by signer, unless nil) and the graph
// entry. It returns the record and the digest of the ingredient's layer.
func (f *Fixture) AddRecipe(name string, signer *sigtest.KeyPair) (store.RecipeRecord, string) {
	f.T.Helper()
	ingRepo := "docker.io/library/" + name
	layer := []byte("layer bytes of " + name + " — long enough to be a real blob")
	ingDigest := f.PutImage(ingRepo, "1.0", layer)
	artifactRepo := Cookbook + "/" + name
	artifactDigest := f.PutImage(artifactRepo, "1.0.0", []byte("recipe document of "+name))
	if signer != nil {
		f.Sign(signer, artifactRepo, artifactDigest)
	}
	rec := store.RecipeRecord{
		Name: name, Version: "1.0.0",
		CookbookRepo: artifactRepo, ArtifactRepo: artifactRepo, ArtifactTag: "1.0.0",
		Digest: artifactDigest, Zone: "zone-a", ResolvedAt: time.Now().UTC(), Verified: signer != nil,
		Ingredients: []store.IngredientRecord{{Name: name, Kind: "Image", Repo: ingRepo, Tag: "1.0", Digest: ingDigest}},
	}
	if err := f.St.PutRecipeRecord(&rec); err != nil {
		f.T.Fatal(err)
	}
	for _, repo := range []string{ingRepo, artifactRepo} {
		if err := f.St.SetProvenance(repo, &store.Provenance{Class: store.ProvenanceRecipe, Recipe: name, RecipeVersion: "1.0.0"}); err != nil {
			f.T.Fatal(err)
		}
	}
	return rec, Digest(layer)
}

// AdmitUnder rewrites a recipe's graph entry as the engine records one
// admitted unsigned by the declared scope: not verified, naming the scope
// (FR-033, FR-037).
func (f *Fixture) AdmitUnder(rec store.RecipeRecord, scope string) store.RecipeRecord {
	f.T.Helper()
	rec.Verified, rec.TrustScope = false, scope
	if err := f.St.PutRecipeRecord(&rec); err != nil {
		f.T.Fatal(err)
	}
	return rec
}

// BlobPath is where the store keeps the bytes of one digest, relative to
// the store root, in slash form.
func BlobPath(dgst string) string {
	h := dgst[len("sha256:"):]
	return "docker/registry/v2/blobs/sha256/" + h[:2] + "/" + h + "/data"
}

// Truncate lops the last byte off a blob.
func (f *Fixture) Truncate(dgst string) {
	f.T.Helper()
	p := filepath.Join(f.Root, filepath.FromSlash(BlobPath(dgst)))
	info, err := os.Stat(p)
	if err != nil {
		f.T.Fatal(err)
	}
	if err := os.Truncate(p, info.Size()-1); err != nil {
		f.T.Fatal(err)
	}
}

// Remove deletes a blob's data from disk — a referenced blob gone missing.
func (f *Fixture) Remove(dgst string) {
	f.T.Helper()
	if err := os.RemoveAll(filepath.Dir(filepath.Join(f.Root, filepath.FromSlash(BlobPath(dgst))))); err != nil {
		f.T.Fatal(err)
	}
}

// Fingerprint hashes the whole tree under root — every file's name and
// content — so "the store is byte-identical afterwards" is checked on the
// real directory, not on a list of files somebody thought of.
func Fingerprint(t *testing.T, root string) string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			names = append(names, "d "+filepath.ToSlash(rel))
			return nil
		}
		raw, rerr := os.ReadFile(p) //nolint:gosec // G304: the fixture's own tree
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(raw)
		names = append(names, "f "+filepath.ToSlash(rel)+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	h := sha256.New()
	_, _ = io.WriteString(h, strings.Join(names, "\n"))
	return hex.EncodeToString(h.Sum(nil))
}

// AcceptanceStore builds the store of the FR-057 acceptance: an intact
// recipe, one with a truncated blob, one with a manifest missing a
// referenced blob, one signed by a key that is not a trust root, and one
// orphan blob. It returns the digests the assertions name.
type Acceptance struct {
	Truncated, Missing, Orphan string
	MissingRecord              store.RecipeRecord
	RemovedKey                 *sigtest.KeyPair
}

// BuildAcceptance fills the fixture with the acceptance store and corrupts
// it.
func (f *Fixture) BuildAcceptance() Acceptance {
	f.T.Helper()
	f.AddRecipe("alpha", f.KP) // intact: the control
	_, truncated := f.AddRecipe("truncated", f.KP)
	rec, missing := f.AddRecipe("missing", f.KP)
	removed := NewKeyPair(f.T)
	f.AddRecipe("removedkey", removed)
	orphan := f.PutBlob("docker.io/library/orphaned", []byte("a blob nothing references"))
	f.Truncate(truncated)
	f.Remove(missing)
	return Acceptance{Truncated: truncated, Missing: missing, Orphan: orphan, MissingRecord: rec, RemovedKey: removed}
}
