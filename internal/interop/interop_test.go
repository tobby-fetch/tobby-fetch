// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The instance-side behaviour of FR-051 and FR-046, against the REAL
// embedded store: a recipe-scoped export carries what the recipe manages
// and the signatures that attest it, and a reset happens only behind the
// confirmation the requirement asks for — with the audit entry FR-094
// asks for, refusals included.

package interop_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const (
	ingredientRepo = "docker.io/library/alpine"
	cookbookRepo   = "docker.io/cookbook/alpine"
)

// openStore opens an embedded store on a fresh directory.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})
	return st
}

// putBlob stores one blob and returns its descriptor.
func putBlob(t *testing.T, st *store.Store, repo, mediaType string, content []byte) ocispec.Descriptor {
	t.Helper()
	d := digest.FromBytes(content)
	if err := st.WriteBlob(context.Background(), repo, d, bytes.NewReader(content)); err != nil {
		t.Fatalf("writing blob: %v", err)
	}
	return ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(content))}
}

// putManifest stores a manifest document, optionally tagged.
func putManifest(t *testing.T, st *store.Store, repo, mediaType string, doc any, tag string) ocispec.Descriptor {
	t.Helper()
	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(context.Background(), repo, mediaType, payload, tag)
	if err != nil {
		t.Fatalf("storing manifest in %s: %v", repo, err)
	}
	return ocispec.Descriptor{MediaType: mediaType, Digest: d, Size: int64(len(payload))}
}

// putImage stores a one-layer image.
func putImage(t *testing.T, st *store.Store, repo, tag, flavour string) ocispec.Descriptor {
	t.Helper()
	config := putBlob(t, st, repo, ocispec.MediaTypeImageConfig,
		[]byte(`{"architecture":"amd64","os":"linux","flavour":"`+flavour+`"}`))
	layer := putBlob(t, st, repo, ocispec.MediaTypeImageLayerGzip,
		bytes.Repeat([]byte(flavour+"\n"), 32))
	return putManifest(t, st, repo, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispec.Descriptor{layer},
	}, tag)
}

// signAttached publishes the classic cosign layout for a subject.
func signAttached(t *testing.T, st *store.Store, repo string, subject digest.Digest) {
	t.Helper()
	config := putBlob(t, st, repo, ocispec.MediaTypeImageConfig, []byte(`{}`))
	payload := putBlob(t, st, repo, "application/vnd.dev.cosign.simplesigning.v1+json",
		[]byte(`{"critical":{"image":{"docker-manifest-digest":"`+subject.String()+`"}}}`))
	putManifest(t, st, repo, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    config,
		Layers:    []ocispec.Descriptor{payload},
	}, "sha256-"+subject.Encoded()+".sig")
}

// signBundle publishes the cosign 3.x default layout: a referring
// artifact plus the referrers fallback index that is the only way to find
// it on a registry with no Referrers API.
func signBundle(t *testing.T, st *store.Store, repo string, subject *ocispec.Descriptor) (bundle ocispec.Descriptor) {
	t.Helper()
	empty := putBlob(t, st, repo, ocispec.DescriptorEmptyJSON.MediaType, []byte("{}"))
	layer := putBlob(t, st, repo, "application/vnd.dev.sigstore.bundle.v0.3+json", []byte(`{"dsseEnvelope":{}}`))
	bundle = putManifest(t, st, repo, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		Config:       empty,
		Layers:       []ocispec.Descriptor{layer},
		Subject:      subject,
	}, "")
	bundle.ArtifactType = "application/vnd.dev.sigstore.bundle.v0.3+json"
	putManifest(t, st, repo, ocispec.MediaTypeImageIndex, ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{bundle},
	}, "sha256-"+subject.Digest.Encoded())
	return bundle
}

// TestRecipeExportCarriesItsContentAndBothSignatureLayouts is the B-015
// guard at the selection level.
//
// A recipe-scoped export must carry the recipe artifact, its ingredients,
// and the cosign signature artifacts of both — in EITHER of the two
// layouts §12.2 defines, because which one exists is the publisher's
// choice and no exporter can know it. B-015 was exactly the asymmetry of
// knowing one: content that verified upstream and had no signature one
// hop down. An export is one more hop, and a selection that named only
// the ".sig" tag would reproduce the bug on a medium.
func TestRecipeExportCarriesItsContentAndBothSignatureLayouts(t *testing.T) {
	ctx := context.Background()
	src := openStore(t)

	ingredient := putImage(t, src, ingredientRepo, "3.22.1", "alpine")
	signAttached(t, src, ingredientRepo, ingredient.Digest)

	recipe := putImage(t, src, cookbookRepo, "1.0.0", "recipe")
	bundle := signBundle(t, src, cookbookRepo, &recipe)

	// Content that belongs to no recipe: it must NOT ride along on a
	// recipe-scoped export.
	putImage(t, src, "quay.io/other/thing", "9.9.9", "unrelated")

	if err := src.PutRecipeRecord(&store.RecipeRecord{
		Name: "alpine", Version: "1.0.0", CookbookRepo: cookbookRepo,
		Digest: recipe.Digest.String(),
		Ingredients: []store.IngredientRecord{{
			Name: "app", Kind: "ContainerImage", Repo: ingredientRepo,
			Tag: "3.22.1", Digest: ingredient.Digest.String(),
		}},
	}); err != nil {
		t.Fatal(err)
	}

	svc := interop.New(src, nil, "", slog.New(slog.DiscardHandler))
	out := filepath.Join(t.TempDir(), "payload.tar")
	if _, err := svc.Export(ctx, &interop.ExportRequest{
		Selector: interop.Selector{Recipes: []string{"alpine@1.0.0"}},
		Output:   out,
	}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	dst := openStore(t)
	report, err := interop.New(dst, nil, "", slog.New(slog.DiscardHandler)).
		Import(ctx, &interop.ImportRequest{Input: out}, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("importing: %v", err)
	}
	if report.Failed() != 0 {
		t.Fatalf("%d entries failed: %+v", report.Failed(), report.Entries)
	}

	assertTagDigest(t, dst, ingredientRepo, "3.22.1", ingredient.Digest.String())
	assertTagDigest(t, dst, cookbookRepo, "1.0.0", recipe.Digest.String())
	// The classic attached signature of the ingredient.
	assertTagExists(t, dst, ingredientRepo, "sha256-"+ingredient.Digest.Encoded()+".sig")
	// The bundle layout of the recipe: the fallback index AND the
	// referring artifact it names by digest.
	assertTagExists(t, dst, cookbookRepo, "sha256-"+recipe.Digest.Encoded())
	if _, _, _, err := dst.RawManifest(ctx, cookbookRepo, bundle.Digest.String()); err != nil {
		t.Errorf("the referring bundle artifact did not travel: %v", err)
	}

	if _, err := dst.RepoInfo(ctx, "quay.io/other/thing"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a recipe-scoped export carried content the recipe does not manage")
	}
}

// TestResetRefusesAnythingButTheTypedPhrase is FR-046's confirmation, and
// its FR-094 record. A refused reset is audited as denied: somebody
// typing the wrong word into that field is the trail's early warning, and
// a trail that only records successes answers the wrong question.
func TestResetRefusesAnythingButTheTypedPhrase(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	putImage(t, st, ingredientRepo, "3.22.1", "alpine")

	var log bytes.Buffer
	svc := interop.New(st, nil, "", logging.New(&log, slog.LevelInfo))

	for _, wrong := range []string{"", "reset", "RESET please", "réinitialiser"} {
		if _, err := svc.Reset(ctx, "alexis", "127.0.0.1", wrong); err == nil {
			t.Fatalf("the store was reset on confirmation %q", wrong)
		} else if code := codeOf(t, err); code != taxonomy.CodeResetConfirmation {
			t.Errorf("confirmation %q answered %s, want %s", wrong, code, taxonomy.CodeResetConfirmation)
		}
	}
	if repos, err := st.Repositories(ctx); err != nil || len(repos) != 1 {
		t.Fatalf("the refused resets touched the store: %v, %v", repos, err)
	}
	assertAudited(t, log.String(), "denied", "alexis")

	log.Reset()
	if _, err := svc.Reset(ctx, "alexis", "127.0.0.1", interop.ConfirmationPhrase); err != nil {
		t.Fatalf("the exact phrase was refused: %v", err)
	}
	repos, err := st.Repositories(ctx)
	if err != nil || len(repos) != 0 {
		t.Fatalf("the store was not emptied: %v, %v", repos, err)
	}
	assertAudited(t, log.String(), "success", "alexis")
}

// TestResetRecordsTheUnauthenticatedContext: on an instance running with
// the FR-075 authentication override the typed confirmation is kept and
// the audit entry names the unauthenticated context instead of an
// identity — which is what FR-046 asks for in as many words.
func TestResetRecordsTheUnauthenticatedContext(t *testing.T) {
	st := openStore(t)
	putImage(t, st, ingredientRepo, "3.22.1", "alpine")

	var log bytes.Buffer
	svc := interop.New(st, nil, "", logging.New(&log, slog.LevelInfo))

	if _, err := svc.Reset(context.Background(), "anonymous", "127.0.0.1", "nope"); err == nil {
		t.Fatal("the typed confirmation was skipped on an unauthenticated instance")
	}
	assertAudited(t, log.String(), "denied", "anonymous")

	log.Reset()
	if _, err := svc.Reset(context.Background(), "anonymous", "127.0.0.1", interop.ConfirmationPhrase); err != nil {
		t.Fatalf("resetting: %v", err)
	}
	assertAudited(t, log.String(), "success", "anonymous")
}

// TestPlanIsSideEffectFree: FR-055's projection must be computable
// without touching the destination, so a pre-flight can run on a medium
// the operator has not committed to yet.
func TestPlanIsSideEffectFree(t *testing.T) {
	st := openStore(t)
	putImage(t, st, ingredientRepo, "3.22.1", "alpine")
	svc := interop.New(st, nil, "", slog.New(slog.DiscardHandler))

	dir := t.TempDir()
	out := filepath.Join(dir, "payload.tar")
	plan, projection, err := svc.Plan(context.Background(), &interop.ExportRequest{Output: out})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Refs) != 1 {
		t.Errorf("plan resolved %d references, want 1", len(plan.Refs))
	}
	if projection.TotalBytes == 0 || projection.LargestFileBytes == 0 {
		t.Errorf("projection reports nothing to write: %+v", projection)
	}
	if entries := dirEntries(t, dir); len(entries) != 0 {
		t.Errorf("the estimate wrote %v", entries)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func assertTagDigest(t *testing.T, st *store.Store, repo, tag, want string) {
	t.Helper()
	_, _, got, err := st.RawManifest(context.Background(), repo, tag)
	if err != nil {
		t.Errorf("%s:%s did not travel: %v", repo, tag, err)
		return
	}
	if got != want {
		t.Errorf("%s:%s = %s after the transfer, want %s", repo, tag, got, want)
	}
}

func assertTagExists(t *testing.T, st *store.Store, repo, tag string) {
	t.Helper()
	if _, _, _, err := st.RawManifest(context.Background(), repo, tag); err != nil {
		t.Errorf("%s:%s did not travel: %v", repo, tag, err)
	}
}

// auditActionReset is the FR-094 action the reset records under. Spelled
// out here rather than imported: this test is the place that would catch
// the action being renamed out from under a log filter somebody wrote.
const auditActionReset = "store.reset"

// assertAudited finds one FR-094 record in the captured log stream.
func assertAudited(t *testing.T, stream, outcome, actor string) {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["log_type"] != "audit" || rec["action"] != auditActionReset {
			continue
		}
		if rec["outcome"] == outcome && rec["actor"] == actor && rec["target"] != "" && rec["origin"] != "" {
			return
		}
	}
	t.Errorf("no audit record for action=%s outcome=%s actor=%s in:\n%s", auditActionReset, outcome, actor, stream)
}

// codeOf reads the taxonomy code of a service failure.
func codeOf(t *testing.T, err error) taxonomy.Code {
	t.Helper()
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want a taxonomy error", err)
	}
	return te.Code()
}

// The layout package's own error type must reach the surfaces through the
// service, not be flattened into a generic failure: a refused hostile
// archive has its own catalog entry and its own corrective action.
var _ = ocilayout.UnsafeEntryError{}
