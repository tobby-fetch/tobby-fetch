// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
)

// The recipe artifact layout (RECIPE-SPEC §11.2) is the format's, not
// ours: the SDK holds it, and these names exist so the rest of the engine
// reads the same values without importing it everywhere.
const (
	// MediaTypeRecipe is the artifactType and single-layer media type of a
	// published recipe.
	MediaTypeRecipe = cookbook.ArtifactType
	// mediaTypeEmptyConfig is the OCI empty config of an artifact manifest.
	mediaTypeEmptyConfig = cookbook.ConfigMediaType
	// maxRecipeBytes bounds a recipe document read (a recipe is a small
	// YAML file; anything larger is hostile or wrong). Retrievers are read
	// under the same bound: they are the same kind of document.
	maxRecipeBytes = cookbook.MaxDocumentBytes
	// layoutPath names the §11.2 layout in taxonomy error paths.
	layoutPath = "artifact layout (RECIPE-SPEC §11.2)"
)

// Cookbook reads recipes from one cookbook — an ordinary OCI repository
// namespace (ADR-0002) — through the substitution-aware remote access.
type Cookbook struct {
	remotes *Remotes
	// nominal is the cookbook's nominal reference from the Retriever
	// ("registry.example.com/cookbook") — the trust-scope space.
	nominal string
}

// NewCookbook opens a cookbook by its nominal reference.
func NewCookbook(remotes *Remotes, nominal string) *Cookbook {
	return &Cookbook{remotes: remotes, nominal: nominal}
}

// Versions lists the version tags of one recipe name (§11.4 discovery:
// GET /v2/<path>/<name>/tags/list). Signature tags (sha256-….sig) are
// filtered out — they are artifacts, not versions.
func (c *Cookbook) Versions(ctx context.Context, name string) ([]string, error) {
	tags, err := c.remotes.ListTags(ctx, c.nominal+"/"+name)
	if err != nil {
		return nil, err
	}
	out := tags[:0]
	for _, t := range tags {
		if strings.HasPrefix(t, "sha256-") {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// FetchedRecipe is one recipe artifact pulled from a cookbook, with
// everything needed to verify, validate, and copy it — manifest bytes are
// the exact signed bytes (§12.2: one signature attests the exact bytes of
// the whole delivery).
type FetchedRecipe struct {
	// NominalRepo is the recipe's nominal cookbook repository
	// ("registry.example.com/cookbook/wordpress") — the trust-scope key
	// and the relocation input.
	NominalRepo string
	Tag         string
	// ManifestBytes/MediaType/Digest are the recipe artifact manifest,
	// bit-exact.
	ManifestBytes  []byte
	MediaType      string
	ManifestDigest string
	// ConfigDigest and LayerDigest identify the two blobs of the §11.2
	// layout; YAML is the recipe document (the single layer's content).
	ConfigDigest string
	LayerDigest  string
	YAML         []byte
	// Recipe is the parsed, cooked-validated document.
	Recipe *spec.Recipe
}

// FetchArtifact pulls one recipe artifact's MANIFEST by tag and enforces
// the §11.2 layout — without touching the document yet. The caller
// verifies the signature over the manifest digest first (§12.3 point 1:
// verify before acting), then calls LoadDocument.
func (c *Cookbook) FetchArtifact(ctx context.Context, recipeName, tag string) (*FetchedRecipe, error) {
	nominalRepo := c.nominal + "/" + recipeName
	desc, err := c.remotes.Get(ctx, nominalRepo, tag)
	if err != nil {
		return nil, err
	}

	layout, err := cookbook.VerifyManifest(desc.Manifest)
	if err != nil {
		return nil, err
	}

	return &FetchedRecipe{
		NominalRepo:    nominalRepo,
		Tag:            tag,
		ManifestBytes:  desc.Manifest,
		MediaType:      string(desc.MediaType),
		ManifestDigest: desc.Digest.String(),
		ConfigDigest:   layout.Config.Digest,
		LayerDigest:    layout.Document.Digest,
	}, nil
}

// LoadDocument pulls the recipe document layer of a fetched — and by now
// verified — artifact, parses it, and validates it under the cooked
// profile (§8: a cookbook only ever holds cooked recipes), including the
// §11.3 name/tag coherence check (anti tag-reuse).
func (c *Cookbook) LoadDocument(ctx context.Context, f *FetchedRecipe, recipeName string) error {
	repo, _, err := c.remotes.Repository(f.NominalRepo)
	if err != nil {
		return err
	}
	layer, err := remote.Layer(repo.Digest(f.LayerDigest), c.remotes.options(ctx)...)
	if err != nil {
		return err
	}
	rc, err := layer.Compressed()
	if err != nil {
		return err
	}
	defer rc.Close() //nolint:errcheck // read side
	yaml, err := readBounded(rc, maxRecipeBytes)
	if err != nil {
		return err
	}

	recipe, err := spec.ParseRecipe(yaml)
	if err != nil {
		return err
	}
	if err := recipe.Validate(spec.ProfileCooked); err != nil {
		return err
	}
	// §11.3: an artifact disagreeing with its publication location MUST be
	// rejected (anti tag-reuse).
	if err := recipe.ValidatePublishLocation(recipeName, f.Tag); err != nil {
		return err
	}
	f.YAML = yaml
	f.Recipe = recipe
	return nil
}

// SignatureTag is the cosign attached-signature tag of a manifest digest
// (ADR-0007: sha256-<hex>.sig in the same repository).
func SignatureTag(manifestDigest string) string {
	return strings.Replace(manifestDigest, "sha256:", "sha256-", 1) + ".sig"
}

// readBounded reads r fully, refusing inputs larger than limit.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("content exceeds the %d-byte bound", limit)
	}
	return data, nil
}

// isNotFound reports a registry 404 (missing manifest/tag/repository).
func isNotFound(err error) bool {
	var terr *transport.Error
	if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
		return true
	}
	return strings.Contains(err.Error(), "MANIFEST_UNKNOWN") ||
		strings.Contains(err.Error(), "NAME_UNKNOWN") ||
		errors.Is(err, os.ErrNotExist)
}
