// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// emptyConfigJSON is the canonical empty OCI config payload — the two
// bytes "{}", digest sha256:44136fa3…, required by RECIPE-SPEC §11.2.
var emptyConfigJSON = []byte("{}")

// Publisher writes recipe artifacts to a cookbook (R-36).
//
// Deliberately NOT built on Remotes: source substitution answers "where do
// I read this from", and applying it to a write would silently publish to
// an endpoint the author never named. A publication goes exactly where the
// reference says. Credentials and per-host insecure opt-ins are shared with
// the reading side, the endpoint policy is not.
type Publisher struct {
	keychain authn.Keychain
	insecure map[string]bool
}

// NewPublisher builds the publishing side. credentialsFile is the same
// kubernetes.io/dockerconfigjson payload the engine reads with (FR-004);
// pushing simply needs a credential with write scope on the destination.
func NewPublisher(insecureHosts []string, credentialsFile string) (*Publisher, error) {
	kc, err := keychainFor(credentialsFile)
	if err != nil {
		return nil, err
	}
	p := &Publisher{keychain: kc, insecure: map[string]bool{}}
	for _, h := range insecureHosts {
		p.insecure[h] = true
	}
	return p, nil
}

// PublishResult reports what a publication did.
type PublishResult struct {
	// Reference is the fully qualified reference that was written.
	Reference string
	// Digest is the published manifest digest — the argument to give
	// `cosign sign`, which signs a digest, never a tag.
	Digest string
	// Unchanged is true when the tag already pointed at this exact
	// content: publishing twice is a no-op, not a conflict.
	Unchanged bool
}

// PublishRecipe validates a recipe document and publishes it as the OCI
// artifact of RECIPE-SPEC §11.2.
//
// The validation is the point of the command. `oras push` moves bytes
// without knowing what they are; this refuses to publish
//   - a document that is not a valid Recipe,
//   - a recipe that is not fully pinned (§8: a cookbook holds cooked
//     recipes only — every ingredient carries a digest and an exact tag),
//   - a recipe whose name or version contradicts where it is being
//     published (§11.3, anti tag-reuse),
//   - a republication of an existing tag onto different content (§8
//     immutability: any change requires a new metadata.version).
//
// Signing stays outside: Tobby never holds a private key (ADR-0007). The
// returned digest is what `cosign sign` takes.
func (p *Publisher) PublishRecipe(ctx context.Context, ref string, doc []byte) (*PublishResult, error) {
	tagRef, err := p.parseTagRef(ref)
	if err != nil {
		return nil, err
	}

	recipe, err := spec.ParseRecipe(doc)
	if err != nil {
		return nil, validationError(ref, err)
	}
	if err := recipe.Validate(spec.ProfileCooked); err != nil {
		return nil, validationError(ref, err)
	}
	segments := strings.Split(tagRef.Context().RepositoryStr(), "/")
	if err := recipe.ValidatePublishLocation(segments[len(segments)-1], tagRef.TagStr()); err != nil {
		return nil, validationError(ref, err)
	}

	manifest, digest, err := recipeManifest(doc)
	if err != nil {
		return nil, err
	}

	opts := []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(p.keychain)}
	switch existing, err := remote.Head(tagRef, opts...); {
	case err == nil && existing.Digest.String() == digest:
		return &PublishResult{Reference: tagRef.String(), Digest: digest, Unchanged: true}, nil
	case err == nil:
		return nil, taxonomy.New(taxonomy.CodeTagImmutable, taxonomy.Params{
			"reference": tagRef.String(),
			"published": existing.Digest.String(),
			"candidate": digest,
		})
	case !isNotFound(err):
		return nil, err
	}

	repo := tagRef.Context()
	for _, l := range []struct {
		payload   []byte
		mediaType types.MediaType
	}{
		{emptyConfigJSON, mediaTypeEmptyConfig},
		{doc, MediaTypeRecipe},
	} {
		if err := remote.WriteLayer(repo, static.NewLayer(l.payload, l.mediaType), opts...); err != nil {
			return nil, fmt.Errorf("uploading the %s blob: %w", l.mediaType, err)
		}
	}
	if err := remote.Put(tagRef, rawManifest{bytes: manifest}, opts...); err != nil {
		return nil, fmt.Errorf("publishing the manifest: %w", err)
	}
	return &PublishResult{Reference: tagRef.String(), Digest: digest}, nil
}

// parseTagRef requires a tagged reference: §11.3 makes the tag carry
// metadata.version, so a digest reference has nothing to check against and
// a bare repository would silently mean ":latest".
func (p *Publisher) parseTagRef(ref string) (name.Tag, error) {
	bad := func() (name.Tag, error) {
		return name.Tag{}, taxonomy.New(taxonomy.CodeBadReference, taxonomy.Params{"reference": ref})
	}
	if strings.Contains(ref, "@") {
		return bad()
	}
	// The tag is what follows the LAST colon, and only when that colon
	// comes after the last slash — otherwise it is the registry's port
	// ("127.0.0.1:5000/cookbook/wordpress" carries no tag).
	colon := strings.LastIndexByte(ref, ':')
	if colon < 0 || colon < strings.LastIndexByte(ref, '/') || colon == len(ref)-1 {
		return bad()
	}
	opts := []name.Option{}
	if host, _, _ := strings.Cut(ref, "/"); p.insecure[host] {
		opts = append(opts, name.Insecure)
	}
	tagRef, err := name.NewTag(ref, opts...)
	if err != nil {
		return bad()
	}
	return tagRef, nil
}

// recipeManifest builds the §11.2 artifact manifest for a document and
// returns it with its digest. The layout is written out rather than
// assembled through image helpers: it is fixed by the specification, and a
// literal is what a reader can check against §11.2 line by line.
func recipeManifest(doc []byte) (manifest []byte, digest string, err error) {
	docHash, docSize, err := v1.SHA256(bytes.NewReader(doc))
	if err != nil {
		return nil, "", err
	}
	cfgHash, cfgSize, err := v1.SHA256(bytes.NewReader(emptyConfigJSON))
	if err != nil {
		return nil, "", err
	}
	m := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		ArtifactType:  MediaTypeRecipe,
		Config: v1.Descriptor{
			MediaType: mediaTypeEmptyConfig,
			Digest:    cfgHash,
			Size:      cfgSize,
		},
		Layers: []v1.Descriptor{{
			MediaType:   MediaTypeRecipe,
			Digest:      docHash,
			Size:        docSize,
			Annotations: map[string]string{"org.opencontainers.image.title": recipeLayerTitle},
		}},
	}
	manifest, err = json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	manHash, _, err := v1.SHA256(bytes.NewReader(manifest))
	if err != nil {
		return nil, "", err
	}
	return manifest, manHash.String(), nil
}

// recipeLayerTitle is the layer file name of a published recipe. §11.2
// shows it in its example manifest without a normative clause, and the
// consumer side does not check it; writing it keeps published artifacts
// consistent with the publishing guide and with `oras push recipe.yaml`.
const recipeLayerTitle = "recipe.yaml"

// rawManifest is a remote.Taggable over manifest bytes we built ourselves:
// go-containerregistry's image helpers cannot set artifactType, and §11.2
// requires it.
type rawManifest struct{ bytes []byte }

func (r rawManifest) RawManifest() ([]byte, error)        { return r.bytes, nil }
func (r rawManifest) MediaType() (types.MediaType, error) { return types.OCIManifestSchema1, nil }

// validationError wraps an SDK validation failure in the taxonomy, so the
// CLI prints the same what/cause/action shape as every other refusal.
func validationError(ref string, err error) error {
	var list spec.ErrorList
	path := ""
	if errors.As(err, &list) && len(list) > 0 {
		path = list[0].Path
	}
	return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
		"file":       ref,
		"path":       path,
		"constraint": err.Error(),
	})
}
