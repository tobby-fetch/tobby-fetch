// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// seedRecipeDocument publishes a real recipe artifact into the store
// through the publishing path (R-36), so the view under test reads what
// the command actually writes — not a hand-built fixture that could drift
// from it. Returns the store repository and the document bytes.
func seedRecipeDocument(t *testing.T, st *store.Store) (repo, tag string, doc []byte) {
	t.Helper()
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	doc = []byte(`apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: wordpress
  version: 6.8.2
spec:
  ingredients:
    - name: wordpress
      kind: ContainerImage
      ref: docker.io/bitnami/wordpress
      version: 6.8.2
      digest: sha256:8acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1
`)
	p, err := engine.NewPublisher(config.Registries{Insecure: []string{addr}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PublishRecipe(t.Context(), addr+"/cookbook/wordpress:6.8.2", doc); err != nil {
		t.Fatal(err)
	}
	return "cookbook/wordpress", "6.8.2", doc
}

// TestRecipeDocumentShownOnManifestPage: a recipe held locally shows its
// YAML. Reading what a zone actually received should not require leaving
// the tool for an oras pull (R-37).
func TestRecipeDocumentShownOnManifestPage(t *testing.T) {
	st := openTestStore(t)
	repo, tag, _ := seedRecipeDocument(t, st)
	u := newTestUIWithStore(t, false, st)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/content/"+repo+"/-/tags/"+tag, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest page = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// A distinctive line of the document, HTML-escaped as the template
	// renders it.
	if !strings.Contains(body, "recipe.tobby.dev/v1alpha1") {
		t.Error("the manifest page of a recipe does not show its document")
	}
	if !strings.Contains(body, "sha256:8acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1") {
		t.Error("the shown document is missing its pinned ingredient digest")
	}
	// The download must be offered: copying is for a snippet, deriving the
	// next version starts from the file.
	if !strings.Contains(body, "/content/"+repo+"/-/tags/"+tag+"/recipe.yaml") {
		t.Error("no download link for the document")
	}
	// The framing matters as much as the bytes: what is shown is the
	// locally verified copy, and a published version is immutable.
	if !strings.Contains(body, "immutable") && !strings.Contains(body, "immuable") {
		t.Error("the document is shown without saying a published version is immutable")
	}
}

// TestRecipeDocumentDownload serves the exact stored bytes, as a file.
func TestRecipeDocumentDownload(t *testing.T) {
	st := openTestStore(t)
	repo, tag, doc := seedRecipeDocument(t, st)
	u := newTestUIWithStore(t, false, st)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/content/"+repo+"/-/tags/"+tag+"/recipe.yaml", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("download = %d, want 200", w.Code)
	}
	if w.Body.String() != string(doc) {
		t.Error("the downloaded document is not byte-identical to the published one")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "wordpress-6.8.2.yaml") {
		t.Errorf("Content-Disposition = %q, want a wordpress-6.8.2.yaml attachment", cd)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the document is served without nosniff: a YAML the browser sniffs as HTML would run in our origin")
	}
}

// TestRecipeDocumentOnlyForRecipes: the section is absent for every other
// kind, and the download route 404s rather than serving an image layer.
func TestRecipeDocumentOnlyForRecipes(t *testing.T) {
	st := seedContentStore(t)
	u := newTestUIWithStore(t, false, st)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	const imageRepo = "docker.io/bitnami/wordpress"
	w := get(t, mux, c, "/content/"+imageRepo+"/-/tags/6.4.2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("manifest page = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "/recipe.yaml") {
		t.Error("an image manifest page offers a recipe document")
	}

	w = get(t, mux, c, "/content/"+imageRepo+"/-/tags/6.4.2/recipe.yaml", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("document download of an image = %d, want 404", w.Code)
	}
}
