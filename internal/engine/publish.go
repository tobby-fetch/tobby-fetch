// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Publisher writes recipe artifacts to a cookbook (R-36).
//
// Deliberately NOT built on Remotes: source substitution answers "where do
// I read this from", and applying it to a write would silently publish to
// an endpoint the author never named. A publication goes exactly where the
// reference says. Credentials and per-host insecure opt-ins are shared with
// the reading side, the endpoint policy is not.
type Publisher struct {
	keychain  authn.Keychain
	insecure  map[string]bool
	allowlist *policy.Allowlist
	egress    *netx.Egress
}

// NewPublisher builds the publishing side from the same registry
// configuration the reading side uses: credentials (FR-004 — pushing
// simply needs one with write scope on the destination), per-host
// insecure opt-ins, and the instance's allowlist (FR-030, which covers
// destinations as well as sources). eg is the same shared outbound
// transport the reading side uses (FR-080): a zone that reaches its
// registries through a proxy reaches them through it in both directions.
func NewPublisher(cfg config.Registries, allow *policy.Allowlist, eg *netx.Egress) (*Publisher, error) {
	kc, err := keychainFor(cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	p := &Publisher{keychain: kc, insecure: map[string]bool{}, allowlist: allow, egress: netx.Or(eg)}
	for _, h := range cfg.Insecure {
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
// The validation is the point of the command — `oras push` moves bytes
// without knowing what they are — and it is not implemented here: what a
// recipe artifact must look like belongs to the format, so the recipe-spec
// SDK owns it and every implementation refuses the same documents. This
// function owns the other half, the one the SDK deliberately has no
// business knowing: how to talk to a registry.
//
// Signing stays outside both: Tobby never holds a private key (ADR-0007).
// The returned digest is what `cosign sign` takes.
func (p *Publisher) PublishRecipe(ctx context.Context, ref string, doc []byte) (*PublishResult, error) {
	tagRef, err := p.parseTagRef(ref)
	if err != nil {
		return nil, err
	}

	segments := strings.Split(tagRef.Context().RepositoryStr(), "/")
	art, err := cookbook.Build(doc, segments[len(segments)-1], tagRef.TagStr())
	if err != nil {
		return nil, validationError(ref, err)
	}

	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(p.keychain),
		remote.WithTransport(p.egress.RoundTripper()),
	}
	switch existing, headErr := remote.Head(tagRef, opts...); {
	case headErr == nil:
		// The tag exists: §8 says what writing to it would mean.
		if cookbook.DecideRepublication(existing.Digest.String(), art.Manifest.Digest) == cookbook.RepublicationIdentical {
			return &PublishResult{Reference: tagRef.String(), Digest: art.Manifest.Digest, Unchanged: true}, nil
		}
		return nil, taxonomy.New(taxonomy.CodeTagImmutable, taxonomy.Params{
			"reference": tagRef.String(),
			"published": existing.Digest.String(),
			"candidate": art.Manifest.Digest,
		})
	case !isNotFound(headErr):
		// Through the same mapping as the reading side (R-03): an
		// unreachable registry or a refused credential must answer with
		// its TBY-REG block here too, not with a raw transport error the
		// CLI prints dry while the UI taxonomizes the same failure.
		return nil, mapEngineError(headErr, tagRef.Context().RegistryStr())
	}

	repo := tagRef.Context()
	for _, b := range []cookbook.Blob{art.Config, art.Document} {
		layer := static.NewLayer(b.Content, types.MediaType(b.MediaType))
		if err := remote.WriteLayer(repo, layer, opts...); err != nil {
			// The wrap names the step for logs; the mapping decides the
			// user-visible code, with the destination host as parameter.
			return nil, mapEngineError(fmt.Errorf("uploading the %s blob: %w", b.MediaType, err), repo.RegistryStr())
		}
	}
	if err := remote.Put(tagRef, rawManifest{bytes: art.Manifest.Content, mediaType: types.MediaType(art.Manifest.MediaType)}, opts...); err != nil {
		return nil, mapEngineError(fmt.Errorf("publishing the manifest: %w", err), repo.RegistryStr())
	}
	return &PublishResult{Reference: tagRef.String(), Digest: art.Manifest.Digest}, nil
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
	// FR-030 applies to destinations too, and applies here rather than
	// later: a publication that is going to be refused must be refused
	// before the document is even read, let alone uploaded.
	if err := p.allowlist.Check(tagRef.RegistryStr()); err != nil {
		return name.Tag{}, err
	}
	return tagRef, nil
}

// rawManifest is a remote.Taggable over manifest bytes somebody else
// produced: go-containerregistry's image helpers cannot set artifactType,
// which §11.2 requires, and the promotion path (FR-028) must write back
// the exact bytes it verified rather than a re-serialization of them.
// The media type travels alongside because a promoted manifest may be an
// index or a Docker-schema image, not only the OCI manifest a publication
// builds.
type rawManifest struct {
	bytes     []byte
	mediaType types.MediaType
}

func (r rawManifest) RawManifest() ([]byte, error)        { return r.bytes, nil }
func (r rawManifest) MediaType() (types.MediaType, error) { return r.mediaType, nil }

// validationError wraps an SDK refusal in the taxonomy, so the CLI prints
// the same what/cause/action shape as every other refusal. Both refusal
// shapes the SDK returns land here: a document that is not a publishable
// recipe (an ErrorList over its fields), and one that is not shaped like a
// recipe artifact at all (a LayoutError).
func validationError(ref string, err error) error {
	path := ""
	var list spec.ErrorList
	var layout *cookbook.LayoutError
	switch {
	case errors.As(err, &list) && len(list) > 0:
		path = list[0].Path
	case errors.As(err, &layout):
		path = layoutPath
	}
	return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
		"file":       ref,
		"path":       path,
		"constraint": err.Error(),
	})
}
