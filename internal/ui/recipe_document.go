// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxRecipeDocument bounds a document read. The same bound the cookbook
// reader applies on the way in (RECIPE-SPEC artifact layout): a recipe is
// a small YAML file, anything larger never entered the store.
const maxRecipeDocument = 4 << 20

// recipeDocument is the YAML a recipe artifact carries, read back from the
// local store (R-37).
//
// What is shown is the byte sequence THIS instance holds and verified on
// entry — not a fresh read of the upstream cookbook. The digest travels
// with it so the distinction stays checkable rather than asserted.
type recipeDocument struct {
	YAML   string
	Digest string
	// Href downloads the same bytes as a file.
	Href string
}

// recipeDocumentOf reads the document layer of a recipe artifact held at
// name:tag. It returns nil — never an error — when the artifact is not a
// recipe: the manifest page renders for every kind, and the document
// section simply does not apply to an image.
func (u *UI) recipeDocumentOf(r *http.Request, name, tag string) *recipeDocument {
	raw, _, dgst, err := u.store.RawManifest(r.Context(), name, tag)
	if err != nil {
		return nil
	}
	var man struct {
		ArtifactType string `json:"artifactType"`
		Layers       []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		return nil
	}
	if man.ArtifactType != engine.MediaTypeRecipe || len(man.Layers) != 1 ||
		man.Layers[0].MediaType != engine.MediaTypeRecipe {
		return nil
	}
	doc, err := u.readBlob(r, name, man.Layers[0].Digest)
	if err != nil {
		return nil
	}
	return &recipeDocument{
		YAML:   string(doc),
		Digest: dgst,
		Href:   "/content/" + name + "/-/tags/" + tag + "/" + recipeFileName,
	}
}

// recipeFileName is the download name, and the last path segment that
// routes to it. It matches the layer title a published recipe carries.
const recipeFileName = "recipe.yaml"

// readBlob reads one bounded blob of a repository.
func (u *UI) readBlob(r *http.Request, name, dgst string) ([]byte, error) {
	rc, err := u.store.BlobReader(r.Context(), name, dgst)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(io.LimitReader(rc, maxRecipeDocument))
}

// contentRecipeDocument serves GET /content/{repo}/-/tags/{tag}/recipe.yaml:
// the document as a file, so an operator can start from a published recipe
// to derive the next version (R-37).
//
// A cooked recipe is immutable, so this is deliberately a download and not
// an editor: the next version is a new document, published under a new
// metadata.version — never an edit of the artifact already resolved by
// zones.
func (u *UI) contentRecipeDocument(w http.ResponseWriter, r *http.Request, name, tag string) {
	doc := u.recipeDocumentOf(r, name, tag)
	if doc == nil {
		u.render.Error(w, r, taxonomy.New(taxonomy.CodeNotFound, nil))
		return
	}
	short := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		short = name[i+1:]
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+short+"-"+tag+`.yaml"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(doc.YAML))
}
