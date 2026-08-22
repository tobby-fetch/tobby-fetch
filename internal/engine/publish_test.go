// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	godigest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
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
	p, err := NewPublisher(config.Registries{Insecure: []string{r.addr}}, nil, nil)
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
	remotes, err := NewRemotes(config.Registries{Insecure: []string{r.addr}}, nil, nil)
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
	if got := man.Layers[0].Annotations["org.opencontainers.image.title"]; got != cookbook.LayerTitle {
		t.Errorf("layer title = %q, want %q", got, cookbook.LayerTitle)
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
	if _, err := NewRemotes(config.Registries{Substitutions: map[string]string{author.addr: upstream.addr}}, nil, nil); err != nil {
		t.Fatal(err)
	}

	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	p, err := NewPublisher(config.Registries{Insecure: []string{author.addr, upstream.addr}}, nil, nil)
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

// TestSDKManifestIsByteIdenticalToTheOCILayout is B-018's guard on the
// producing side: the manifest the SDK assembles must be byte-identical
// to what the OCI reference structs — the ones oras-go and crane
// marshal — produce for the same content. The SDK used to match
// go-containerregistry's field order instead, which is neither the
// §11.2 example nor what any other publisher writes, and the digest
// identity DecideRepublication once relied on was false the moment a
// second tool published (the spec now compares document layers; this
// keeps the wrappers from diverging when they don't have to).
func TestSDKManifestIsByteIdenticalToTheOCILayout(t *testing.T) {
	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})

	art, err := cookbook.Build(doc, "wordpress", "6.8.2")
	if err != nil {
		t.Fatalf("cookbook.Build: %v", err)
	}

	docHash, docSize, err := v1.SHA256(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("hashing the document: %v", err)
	}
	emptyConfig := []byte("{}")
	cfgHash, cfgSize, err := v1.SHA256(bytes.NewReader(emptyConfig))
	if err != nil {
		t.Fatalf("hashing the empty config: %v", err)
	}
	fromLibrary, err := json.Marshal(ocispec.Manifest{
		Versioned:    ocispecs.Versioned{SchemaVersion: 2},
		MediaType:    string(types.OCIManifestSchema1),
		ArtifactType: MediaTypeRecipe,
		Config: ocispec.Descriptor{
			MediaType: mediaTypeEmptyConfig,
			Digest:    godigest.Digest(cfgHash.String()),
			Size:      cfgSize,
		},
		Layers: []ocispec.Descriptor{{
			MediaType:   MediaTypeRecipe,
			Digest:      godigest.Digest(docHash.String()),
			Size:        docSize,
			Annotations: map[string]string{"org.opencontainers.image.title": cookbook.LayerTitle},
		}},
	})
	if err != nil {
		t.Fatalf("marshaling the reference manifest: %v", err)
	}

	if !bytes.Equal(art.Manifest.Content, fromLibrary) {
		t.Errorf("the SDK and the OCI reference structs disagree on the manifest bytes.\nSDK:       %s\nreference: %s",
			art.Manifest.Content, fromLibrary)
	}
	manHash, _, err := v1.SHA256(bytes.NewReader(fromLibrary))
	if err != nil {
		t.Fatalf("hashing the reference manifest: %v", err)
	}
	if art.Manifest.Digest != manHash.String() {
		t.Errorf("manifest digest = %s, want %s — a recipe published by oras or crane would wrap differently for nothing",
			art.Manifest.Digest, manHash.String())
	}
}

// TestPublishTransportFailuresAreTaxonomized pins the R-03 contract on
// the publishing path: a registry that is down or refusing the
// credential answers with the same TBY-REG-002/003 block `tobby recipe
// push` prints for any other operation — never a raw "dial tcp" or a
// bare HTTP status. The failure fires on the pre-flight Head, which is
// the first network exchange of a publication and therefore the shape
// every transport failure of this path takes first.
func TestPublishTransportFailuresAreTaxonomized(t *testing.T) {
	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})

	t.Run("unreachable registry is TBY-REG-002 naming the host", func(t *testing.T) {
		// Reserve a loopback port, then close it: nothing listens there.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		p, err := NewPublisher(config.Registries{Insecure: []string{addr}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.PublishRecipe(t.Context(), addr+"/cookbook/wordpress:6.8.2", doc)
		var te *taxonomy.Error
		if !errors.As(err, &te) || te.Code() != taxonomy.CodeRegistryUnreachable {
			t.Fatalf("publishing to a dead port = %v, want %s", err, taxonomy.CodeRegistryUnreachable)
		}
		if got := te.ParamsMap()["host"]; got != addr {
			t.Errorf("host parameter = %v, want %s", got, addr)
		}
	})

	t.Run("refused credential is TBY-REG-003 naming the host", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		addr := strings.TrimPrefix(srv.URL, "http://")

		p, err := NewPublisher(config.Registries{Insecure: []string{addr}}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.PublishRecipe(t.Context(), addr+"/cookbook/wordpress:6.8.2", doc)
		var te *taxonomy.Error
		if !errors.As(err, &te) || te.Code() != taxonomy.CodeRegistryAuth {
			t.Fatalf("publishing with a refused credential = %v, want %s", err, taxonomy.CodeRegistryAuth)
		}
		if got := te.ParamsMap()["host"]; got != addr {
			t.Errorf("host parameter = %v, want %s", got, addr)
		}
	})
}

// TestPublishRecipeUnchangedAcrossManifestWrappers is B-018's guard on the
// publishing side. Manifest bytes are not stable across tools — oras adds
// an org.opencontainers.image.created annotation, and an annotation is all
// it takes — so a tag holding the same document under a foreign wrapper
// must read as already published, and the tag, with whatever signature its
// manifest carries, must stay exactly as it is (§11.2, §8).
func TestPublishRecipeUnchangedAcrossManifestWrappers(t *testing.T) {
	r := newRegistry(t)
	doc := cookedRecipeYAML(t, "wordpress", "6.8.2", []spec.Ingredient{cookedIngredient()})
	ref := r.addr + "/cookbook/wordpress:6.8.2"
	p := publisherFor(t, r)

	first, err := p.PublishRecipe(t.Context(), ref, doc)
	if err != nil {
		t.Fatal(err)
	}

	// Re-wrap the published manifest the way another tool would: same
	// layers, one added annotation, different bytes, different digest.
	payload, mediaType, _, err := r.st.RawManifest(t.Context(), "cookbook/wordpress", "6.8.2")
	if err != nil {
		t.Fatal(err)
	}
	var wrapped map[string]any
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		t.Fatal(err)
	}
	wrapped["annotations"] = map[string]string{"org.opencontainers.image.created": "2026-08-22T00:00:00Z"}
	foreign, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	foreignDigest, err := r.st.PutManifest(t.Context(), "cookbook/wordpress", mediaType, foreign, "6.8.2")
	if err != nil {
		t.Fatal(err)
	}
	if foreignDigest.String() == first.Digest {
		t.Fatal("the rewrap did not change the manifest digest; the fixture proves nothing")
	}

	res, err := p.PublishRecipe(t.Context(), ref, doc)
	if err != nil {
		t.Fatalf("republishing the same document under a foreign wrapper: %v", err)
	}
	if !res.Unchanged {
		t.Error("the same document under a foreign wrapper was not reported unchanged")
	}
	if res.Digest != foreignDigest.String() {
		t.Errorf("reported digest %s; want the published manifest's %s — the tag must not be re-pointed", res.Digest, foreignDigest)
	}
	if _, _, dgst, err := r.st.RawManifest(t.Context(), "cookbook/wordpress", "6.8.2"); err != nil || dgst != foreignDigest.String() {
		t.Errorf("tag moved to %s (err %v); the foreign manifest and its signatures had to stay", dgst, err)
	}
}
