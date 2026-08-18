// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/opencontainers/go-digest"
	"gopkg.in/yaml.v3"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify/sigtest"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// Test infrastructure for the recipe engine (milestone 3).
//
// The source side is ALWAYS a real embedded store served over
// httptest.NewServer(st.APIHandler()) — a real OCI registry speaking the
// real Distribution protocol; the OCI protocol is never mocked (project
// rule). Content is seeded through go-containerregistry as any standard
// client would push it, mirroring internal/ui/content_test.go's
// seedContentStore. The destination is a second, independent store the
// engine writes into directly (ADR-0005).

// openStore opens a fresh embedded store in a temporary directory.
func openStore(t *testing.T) *store.Store {
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

// registry is one real source registry: an embedded store served over the
// real /v2/ API.
type registry struct {
	st   *store.Store
	addr string // "127.0.0.1:<port>"
}

// newRegistry opens a store and serves its OCI Distribution API.
func newRegistry(t *testing.T) *registry {
	t.Helper()
	st := openStore(t)
	srv := httptest.NewServer(st.APIHandler())
	t.Cleanup(srv.Close)
	return &registry{st: st, addr: srv.Listener.Addr().String()}
}

// destRegistry is a real destination registry: an embedded store served
// over the real /v2/ API, with every request recorded.
//
// The recording is the point. FR-028 says an already-synchronized recipe
// transfers zero blobs, and the only way to assert that honestly is to
// count what the destination actually received — a library that promises
// to skip existing blobs is exactly the thing under test, so trusting it
// would make the test agree with the bug.
type destRegistry struct {
	st   *store.Store
	addr string

	mu   sync.Mutex
	reqs []string // "METHOD /path", in order

	// maxDepth, when positive, makes the registry refuse repository names
	// with more path components — the FR-035 fixture for a destination
	// that does not accept nested repositories.
	maxDepth int
}

// newDestRegistry serves a fresh destination.
func newDestRegistry(t *testing.T, opts ...func(*destRegistry)) *destRegistry {
	t.Helper()
	d := &destRegistry{st: openStore(t)}
	for _, o := range opts {
		o(d)
	}
	inner := d.st.APIHandler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.reqs = append(d.reqs, r.Method+" "+r.URL.Path)
		d.mu.Unlock()
		if d.maxDepth > 0 && repoDepth(r.URL.Path) > d.maxDepth {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"code":"NAME_INVALID","message":"repository names accept at most ` +
				strconv.Itoa(d.maxDepth) + ` path components"}]}`))
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	d.addr = srv.Listener.Addr().String()
	return d
}

// refusingNestedNames caps the accepted repository depth.
func refusingNestedNames(depth int) func(*destRegistry) {
	return func(d *destRegistry) { d.maxDepth = depth }
}

// reset drops the recorded requests, so a second cycle is measured on its
// own.
func (d *destRegistry) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = nil
}

// writes returns the recorded requests that could have moved content:
// blob uploads and manifest writes. A HEAD or a GET is how the
// differential decides, so counting those as transfers would make the
// requirement impossible to satisfy rather than merely hard.
func (d *destRegistry) writes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.reqs {
		switch method, _, _ := strings.Cut(r, " "); method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			out = append(out, r)
		}
	}
	return out
}

// repoDepth counts the path components of the repository name in a /v2/
// request path.
func repoDepth(path string) int {
	rest, ok := strings.CutPrefix(path, "/v2/")
	if !ok {
		return 0
	}
	for _, sep := range []string{"/manifests/", "/blobs/", "/tags/", "/referrers/"} {
		if i := strings.Index(rest, sep); i >= 0 {
			rest = rest[:i]
			break
		}
	}
	if rest == "" {
		return 0
	}
	return strings.Count(rest, "/") + 1
}

// seededIndex is a pushed multi-arch image: the pinned index digest and
// the child manifest digest of every platform.
type seededIndex struct {
	digest   string
	children map[string]string // "os/arch[/variant]" → digest
}

// seedIndex pushes a multi-platform image index to repoPath:tag on the
// registry, as a standard client (remote.WriteIndex).
func seedIndex(t *testing.T, r *registry, repoPath, tag string, platforms ...v1.Platform) seededIndex {
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	for i := range platforms {
		img, err := random.Image(512, 1)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &platforms[i]},
		})
	}
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)
	ref, err := name.ParseReference(r.addr + "/" + repoPath + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	d, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	man, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	children := map[string]string{}
	for i := range man.Manifests {
		c := &man.Manifests[i]
		label := c.Platform.OS + "/" + c.Platform.Architecture
		if c.Platform.Variant != "" {
			label += "/" + c.Platform.Variant
		}
		children[label] = c.Digest.String()
	}
	return seededIndex{digest: d.String(), children: children}
}

// seedImage pushes a single-platform random image to repoPath:tag and
// returns its manifest digest.
func seedImage(t *testing.T, r *registry, repoPath, tag string) string {
	t.Helper()
	img, err := random.Image(512, 1)
	if err != nil {
		t.Fatal(err)
	}
	return pushImage(t, r, repoPath, tag, img)
}

// seedChart pushes a minimal Helm chart package (FR-024 fixture): a real
// chart tgz layer under the Helm OCI conventions, with or without its
// declared dependency embedded.
func seedChart(t *testing.T, r *registry, repoPath, tag string, withDep bool) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(path, content string) {
		t.Helper()
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	add("wordpress/Chart.yaml", "name: wordpress\nversion: 24.2.6\ndependencies:\n  - name: mariadb\n    version: 16.3.2\n    repository: oci://docker.io/example-charts\n")
	if withDep {
		add("wordpress/charts/mariadb-16.3.2.tgz", "embedded-subchart-bytes")
	}
	add("wordpress/values.yaml", "replicaCount: 1\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	base, err := random.Image(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	layer := static.NewLayer(buf.Bytes(), types.MediaType("application/vnd.cncf.helm.chart.content.v1.tar+gzip"))
	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		t.Fatal(err)
	}
	return pushImage(t, r, repoPath, tag, mutate.ConfigMediaType(img, "application/vnd.cncf.helm.config.v1+json"))
}

// seedArtifact pushes a generic OCI artifact whose type is carried by the
// config media type (the §7.3 fallback surface of artifactTypeOf).
func seedArtifact(t *testing.T, r *registry, repoPath, tag, configMediaType string) string {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	return pushImage(t, r, repoPath, tag, mutate.ConfigMediaType(img, types.MediaType(configMediaType)))
}

func pushImage(t *testing.T, r *registry, repoPath, tag string, img v1.Image) string {
	t.Helper()
	ref, err := name.ParseReference(r.addr + "/" + repoPath + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// cookedRecipeYAML marshals a cooked Recipe document.
func cookedRecipeYAML(t *testing.T, recipeName, version string, ings []spec.Ingredient) []byte {
	t.Helper()
	r := spec.Recipe{
		APIVersion: spec.APIVersion,
		Kind:       spec.KindRecipe,
		Metadata:   spec.Metadata{Name: recipeName, Version: version},
		Spec:       spec.RecipeSpec{Ingredients: ings},
	}
	raw, err := yaml.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ociImageManifest mirrors the OCI image manifest JSON the §11.2 recipe
// artifact and the cosign signature artifact use.
type ociImageManifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	MediaType     string        `json:"mediaType"`
	ArtifactType  string        `json:"artifactType,omitempty"`
	Config        ociTestDesc   `json:"config"`
	Layers        []ociTestDesc `json:"layers"`
}

type ociTestDesc struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

const mediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"

// publishRecipe publishes a recipe document as the §11.2 artifact into
// storeRepo of the registry's store: empty config (the two bytes "{}"),
// one YAML layer, artifactType application/vnd.tobby.recipe.v1+yaml. It
// returns the recipe artifact manifest digest.
func publishRecipe(t *testing.T, st *store.Store, storeRepo, tag string, recipeYAML []byte) string {
	t.Helper()
	return publishRecipeLayout(t, st, storeRepo, tag, recipeYAML, MediaTypeRecipe, mediaTypeEmptyConfig, MediaTypeRecipe)
}

// publishRecipeLayout is publishRecipe with every §11.2 media type
// overridable, to fabricate layout-violating artifacts.
func publishRecipeLayout(t *testing.T, st *store.Store, storeRepo, tag string, recipeYAML []byte, artifactType, configMT, layerMT string) string {
	t.Helper()
	ctx := context.Background()
	cfg := []byte("{}") // the canonical empty config: exactly these two bytes
	cfgDigest := digest.FromBytes(cfg)
	if err := st.WriteBlob(ctx, storeRepo, cfgDigest, bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	yamlDigest := digest.FromBytes(recipeYAML)
	if err := st.WriteBlob(ctx, storeRepo, yamlDigest, bytes.NewReader(recipeYAML)); err != nil {
		t.Fatal(err)
	}
	man := ociImageManifest{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIManifest,
		ArtifactType:  artifactType,
		Config:        ociTestDesc{MediaType: configMT, Digest: cfgDigest.String(), Size: int64(len(cfg))},
		Layers: []ociTestDesc{{
			MediaType: layerMT, Digest: yamlDigest.String(), Size: int64(len(recipeYAML)),
		}},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(ctx, storeRepo, mediaTypeOCIManifest, raw, tag)
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// signManifest publishes the cosign attached-signature artifact of
// subjectDigest into storeRepo, under the convention tag
// "sha256-<hex>.sig" in the same repository (§12.2), signed by kp.
func signManifest(t *testing.T, st *store.Store, storeRepo, subjectDigest string, kp *sigtest.KeyPair) {
	t.Helper()
	payload, err := sigtest.SimpleSigningPayload("registry.example/"+storeRepo, subjectDigest)
	if err != nil {
		t.Fatal(err)
	}
	sigB64, err := kp.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg := []byte("{}")
	cfgDigest := digest.FromBytes(cfg)
	if err := st.WriteBlob(ctx, storeRepo, cfgDigest, bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	payloadDigest := digest.FromBytes(payload)
	if err := st.WriteBlob(ctx, storeRepo, payloadDigest, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	man := ociImageManifest{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIManifest,
		Config: ociTestDesc{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    cfgDigest.String(), Size: int64(len(cfg)),
		},
		Layers: []ociTestDesc{{
			MediaType: "application/vnd.dev.cosign.simplesigning.v1+json",
			Digest:    payloadDigest.String(), Size: int64(len(payload)),
			Annotations: map[string]string{"dev.cosignproject.cosign/signature": sigB64},
		}},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutManifest(ctx, storeRepo, mediaTypeOCIManifest, raw, SignatureTag(subjectDigest)); err != nil {
		t.Fatal(err)
	}
}

// newKeyPair generates a signing fixture key (cosign default algorithm).
func newKeyPair(t *testing.T) *sigtest.KeyPair {
	t.Helper()
	kp, err := sigtest.GenerateKeyPair(sigtest.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

// trustFor loads a TrustPolicy accepting the given key pairs as roots,
// with the given declared scopes.
func trustFor(t *testing.T, scopes []config.TrustScope, kps ...*sigtest.KeyPair) *TrustPolicy {
	t.Helper()
	cfg := config.Trust{Scopes: scopes}
	for i, kp := range kps {
		pem, err := kp.PublicPEM()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Roots = append(cfg.Roots, config.TrustRoot{Name: rootName(i), Key: string(pem)})
	}
	tp, err := LoadTrust(cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tp
}

func rootName(i int) string { return string(rune('a'+i)) + "-root" }

// retrieverFile writes a Retriever document for one zone to a temp file
// and returns its path (the FR-010 local-file source form).
func retrieverFile(t *testing.T, zone, cookbook string, entries []spec.RecipeSelector) string {
	t.Helper()
	raw := retrieverYAML(t, zone, cookbook, entries)
	path := filepath.Join(t.TempDir(), "retriever.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// retrieverYAML marshals a Retriever document.
func retrieverYAML(t *testing.T, zone, cookbook string, entries []spec.RecipeSelector) []byte {
	t.Helper()
	r := spec.Retriever{
		APIVersion: spec.APIVersion,
		Kind:       spec.KindRetriever,
		Metadata:   spec.Metadata{Name: zone},
		Spec:       spec.RetrieverSpec{Cookbook: cookbook, Recipes: entries},
	}
	raw, err := yaml.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// newRemotes builds a substitution-aware remote access or fails the test.
func newRemotes(t *testing.T, subs map[string]string) *Remotes {
	t.Helper()
	r, err := NewRemotes(config.Registries{Substitutions: subs}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// writeTempFile writes content to a fresh temp file and returns its path.
func writeTempFile(t *testing.T, fileName string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fileName)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// discardLogger returns a logger swallowing everything.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// runSync executes one synchronization run of the engine on a fresh task
// and returns it. The task is fabricated by hand: the engine's contract
// is the Runner signature, not the queue.
func runSync(t *testing.T, e *Engine) (*tasks.Task, error) {
	t.Helper()
	task := &tasks.Task{ID: "tsk_test", RunID: "run_test", Type: tasks.TypeSync, Status: tasks.StatusRunning}
	return task, runSyncTask(t, e, task, slog.New(slog.DiscardHandler))
}

// runSyncTask executes one run on a caller-provided task (FR-029
// resumption fixtures) with a caller-provided logger (log assertions).
func runSyncTask(t *testing.T, e *Engine, task *tasks.Task, logger *slog.Logger) error {
	t.Helper()
	return e.Runner()(context.Background(), task, logger, func() {})
}

// itemByName finds one task item.
func itemByName(t *testing.T, task *tasks.Task, itemName string) *tasks.Item {
	t.Helper()
	for i := range task.Items {
		if task.Items[i].Name == itemName {
			return &task.Items[i]
		}
	}
	t.Fatalf("task has no item %q (items: %s)", itemName, itemNames(task))
	return nil
}

func itemNames(task *tasks.Task) string {
	var names []string
	for _, it := range task.Items {
		names = append(names, it.Name+"="+string(it.Status))
	}
	return "[" + joinComma(names) + "]"
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// resolutionFor finds the Resolution row of one recipe (ingredient "") or
// ingredient.
func resolutionFor(t *testing.T, task *tasks.Task, recipe, ingredient string) *tasks.Resolution {
	t.Helper()
	for i := range task.Resolutions {
		r := &task.Resolutions[i]
		if r.Recipe == recipe && r.Ingredient == ingredient {
			return r
		}
	}
	t.Fatalf("no resolution for recipe %q ingredient %q (rows: %+v)", recipe, ingredient, task.Resolutions)
	return nil
}

// syncCfg is the default engine tuning for tests: parallel enough to
// exercise the semaphore, zero retries so failure scenarios stay fast.
func syncCfg() config.Sync { return config.Sync{Parallelism: 3, Retries: 0} }

// signManifestBundle attaches a Sigstore BUNDLE signature to a manifest —
// the layout cosign 3.x emits by default: an artifact that REFERS to the
// subject, plus the "sha256-<hex>" fallback index that clients publish
// when the registry serves no Referrers API. Tobby's embedded registry is
// one of those, so a store holds the fallback form.
func signManifestBundle(t *testing.T, st *store.Store, storeRepo, subjectDigest string, kp *sigtest.KeyPair) string {
	t.Helper()
	ctx := context.Background()

	bundle, err := sigtest.SignedBundle(subjectDigest, kp)
	if err != nil {
		t.Fatal(err)
	}
	cfg := []byte("{}")
	cfgDigest := digest.FromBytes(cfg)
	if err := st.WriteBlob(ctx, storeRepo, cfgDigest, bytes.NewReader(cfg)); err != nil {
		t.Fatal(err)
	}
	bundleDigest := digest.FromBytes(bundle)
	if err := st.WriteBlob(ctx, storeRepo, bundleDigest, bytes.NewReader(bundle)); err != nil {
		t.Fatal(err)
	}

	man := map[string]any{
		"schemaVersion": 2,
		"mediaType":     mediaTypeOCIManifest,
		"artifactType":  "application/vnd.dev.sigstore.bundle.v0.3+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    cfgDigest.String(), "size": len(cfg),
		},
		"layers": []any{map[string]any{
			"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
			"digest":    bundleDigest.String(), "size": len(bundle),
		}},
		"subject": map[string]any{
			"mediaType": mediaTypeOCIManifest,
			"digest":    subjectDigest, "size": 0,
		},
	}
	raw, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	sigDigest, err := st.PutManifest(ctx, storeRepo, mediaTypeOCIManifest, raw, "")
	if err != nil {
		t.Fatal(err)
	}

	// The fallback referrers index, under "sha256-<hex>" of the subject.
	idx, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []any{map[string]any{
			"mediaType": mediaTypeOCIManifest,
			"digest":    sigDigest.String(), "size": len(raw),
			"artifactType": "application/vnd.dev.sigstore.bundle.v0.3+json",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fallback := strings.Replace(subjectDigest, "sha256:", "sha256-", 1)
	if _, err := st.PutManifest(ctx, storeRepo, "application/vnd.oci.image.index.v1+json", idx, fallback); err != nil {
		t.Fatal(err)
	}
	return sigDigest.String()
}
