// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// cookedIngredient is a fully pinned ingredient — what a cookbook accepts.
func cookedIngredient() spec.Ingredient {
	return spec.Ingredient{
		Name:    "wordpress",
		Kind:    spec.IngredientContainerImage,
		Ref:     "docker.io/bitnami/wordpress",
		Version: "6.8.2",
		Digest:  "sha256:" + strings.Repeat("ab", 32),
	}
}

// publisherFor builds a Publisher trusting the test registry over plain
// HTTP — the httptest server speaks no TLS.
func publisherFor(t *testing.T, r *registry) *Publisher {
	t.Helper()
	p, err := NewPublisher([]string{r.addr}, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPublishRecipeLayout is the round trip that matters: what the
// publisher writes, the cookbook reader accepts. A layout regression on
// either side fails here rather than in a zone.
func TestPublishRecipeLayout(t *testing.T) {
	r := newRegistry(t)
	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	ref := r.addr + "/cookbook/wordpress:6.8.2"

	res, err := publisherFor(t, r).PublishRecipe(t.Context(), ref, doc)
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if res.Unchanged {
		t.Error("a first publication reported itself unchanged")
	}
	if !strings.HasPrefix(res.Digest, "sha256:") {
		t.Errorf("digest %q is not a sha256 digest", res.Digest)
	}

	// Read it back through the consumer path, which enforces §11.2.
	remotes, err := NewRemotes(nil, []string{r.addr}, "")
	if err != nil {
		t.Fatal(err)
	}
	cb := NewCookbook(remotes, r.addr+"/cookbook")
	fetched, err := cb.FetchArtifact(t.Context(), "wordpress", "6.8.2")
	if err != nil {
		t.Fatalf("the published artifact is refused by the cookbook reader: %v", err)
	}
	if fetched.ManifestDigest != res.Digest {
		t.Errorf("digest round trip: published %s, read back %s", res.Digest, fetched.ManifestDigest)
	}
	if err := cb.LoadDocument(t.Context(), fetched, "wordpress"); err != nil {
		t.Fatalf("loading the published document: %v", err)
	}
	if !bytes.Equal(fetched.YAML, doc) {
		t.Error("the document read back differs from the one published")
	}

	// §11.2 also fixes the layer title, which the reader does not check.
	var man struct {
		Layers []struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(fetched.ManifestBytes, &man); err != nil {
		t.Fatal(err)
	}
	if got := man.Layers[0].Annotations["org.opencontainers.image.title"]; got != recipeLayerTitle {
		t.Errorf("layer title = %q, want %q", got, recipeLayerTitle)
	}
}

// TestPublishRecipeIdempotent: republishing identical bytes is a no-op.
// Re-running a publication pipeline must not be an error — only a
// DIFFERENT content under the same tag is.
func TestPublishRecipeIdempotent(t *testing.T) {
	r := newRegistry(t)
	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	ref := r.addr + "/cookbook/wordpress:6.8.2"
	p := publisherFor(t, r)

	first, err := p.PublishRecipe(t.Context(), ref, doc)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.PublishRecipe(t.Context(), ref, doc)
	if err != nil {
		t.Fatalf("republishing identical content: %v", err)
	}
	if !second.Unchanged {
		t.Error("republishing identical content did not report itself unchanged")
	}
	if second.Digest != first.Digest {
		t.Errorf("digest changed on republication: %s then %s", first.Digest, second.Digest)
	}
}

// TestPublishRecipeImmutability is the guard that makes a cooked recipe
// worth trusting: a version already published cannot come to mean
// something else (§8).
func TestPublishRecipeImmutability(t *testing.T) {
	r := newRegistry(t)
	ref := r.addr + "/cookbook/wordpress:6.8.2"
	p := publisherFor(t, r)

	first := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	if _, err := p.PublishRecipe(t.Context(), ref, first); err != nil {
		t.Fatal(err)
	}

	// Same name, same version, one digest changed.
	altered := cookedIngredient()
	altered.Digest = "sha256:" + strings.Repeat("cd", 32)
	second := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{altered})

	_, err := p.PublishRecipe(t.Context(), ref, second)
	assertTaxonomy(t, err, taxonomy.CodeTagImmutable)
}

// TestPublishRecipeRefusals covers every pre-flight check. Each case is a
// publication that a generic push tool would happily perform.
func TestPublishRecipeRefusals(t *testing.T) {
	r := newRegistry(t)
	p := publisherFor(t, r)

	draft := cookedIngredient()
	draft.Digest = "" // a draft: no digest

	constrained := cookedIngredient()
	constrained.Version = "^6.8.0" // a constraint, not an exact tag

	cases := []struct {
		name string
		ref  string
		doc  []byte
		code taxonomy.Code
	}{{
		name: "not a recipe at all",
		ref:  r.addr + "/cookbook/wordpress:6.8.2",
		doc:  []byte("this: is not\n  valid: yaml: at all\n"),
		code: taxonomy.CodeValidation,
	}, {
		name: "draft: an ingredient carries no digest",
		ref:  r.addr + "/cookbook/wordpress:6.8.2",
		doc:  cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{draft}),
		code: taxonomy.CodeValidation,
	}, {
		name: "draft: an ingredient version is a constraint",
		ref:  r.addr + "/cookbook/wordpress:6.8.2",
		doc:  cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{constrained}),
		code: taxonomy.CodeValidation,
	}, {
		name: "tag contradicts metadata.version",
		ref:  r.addr + "/cookbook/wordpress:6.9.0",
		doc:  cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()}),
		code: taxonomy.CodeValidation,
	}, {
		name: "repository contradicts metadata.name",
		ref:  r.addr + "/cookbook/joomla:6.8.2",
		doc:  cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()}),
		code: taxonomy.CodeValidation,
	}, {
		name: "no tag: would silently mean :latest",
		ref:  r.addr + "/cookbook/wordpress",
		doc:  cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()}),
		code: taxonomy.CodeBadReference,
	}, {
		name: "digest reference: nothing to check the version against",
		ref:  r.addr + "/cookbook/wordpress@sha256:" + strings.Repeat("ef", 32),
		doc:  cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()}),
		code: taxonomy.CodeBadReference,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.PublishRecipe(t.Context(), tc.ref, tc.doc)
			assertTaxonomy(t, err, tc.code)
		})
	}
}

// TestPublishRecipeNeverSubstitutes: source substitution redirects reads,
// never writes. A publication that silently landed on a substitute
// endpoint would put the recipe somewhere the author never named.
func TestPublishRecipeNeverSubstitutes(t *testing.T) {
	author := newRegistry(t)   // where the operator says to publish
	upstream := newRegistry(t) // a substitute, configured for reads
	if _, err := NewRemotes(map[string]string{author.addr: upstream.addr}, nil, ""); err != nil {
		t.Fatal(err)
	}

	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	p, err := NewPublisher([]string{author.addr, upstream.addr}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.PublishRecipe(t.Context(), author.addr+"/cookbook/wordpress:6.8.2", doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Reference, author.addr+"/") {
		t.Errorf("published to %q, want the named registry %q", res.Reference, author.addr)
	}
	if _, ok := upstream.st.ResolveTag(context.Background(), "cookbook/wordpress", "6.8.2"); ok {
		t.Error("the recipe landed on the substitute endpoint: a write followed a read-side substitution")
	}
}

// assertTaxonomy fails unless err carries the expected taxonomy code.
func assertTaxonomy(t *testing.T, err error, want taxonomy.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got no error", want)
	}
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("expected %s, got a plain error: %v", want, err)
	}
	if te.Code() != want {
		t.Fatalf("expected %s, got %s: %v", want, te.Code(), err)
	}
}
