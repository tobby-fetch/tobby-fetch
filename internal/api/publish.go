// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Recipe publication endpoint (FR-060): the strict mirror of the
// /recipes/publish screen (FR-061, R-40). Same validation, same taxonomy
// codes, same role floor — the validation itself lives in the recipe-spec
// SDK and the transport in engine.Publisher, so neither surface owns a
// rule the other does not have.
//
// The response carries the published digest and nothing else that could
// be mistaken for a signature: Tobby holds no private key (ADR-0007), and
// an API that returned something called a "signature" would be lying about
// the same thing the screen is careful not to imply.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/tobby-fetch/recipe-spec/cookbook"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// publishTimeout bounds one publication, like the screen's: a validation,
// one HEAD, two blob uploads and a manifest PUT, possibly through a
// corporate proxy.
const publishTimeout = 90 * time.Second

// maxPublishRequest bounds the request body. The document is capped by the
// format itself; the slack covers the JSON envelope and the reference.
const maxPublishRequest = cookbook.MaxDocumentBytes + (64 << 10)

// Publisher is the publishing side of the engine as this endpoint needs
// it — one verb, so the surface can do exactly one thing.
type Publisher interface {
	PublishRecipe(ctx context.Context, ref string, doc []byte) (*engine.PublishResult, error)
}

// RegisterPublish mounts POST /api/v1/recipes/publish. Operator, like the
// screen: publishing writes into another zone's registry (ADR-0009).
func RegisterPublish(a *API, p Publisher) {
	pub := &publishAPI{api: a, publisher: p}
	a.Handle("POST /api/v1/recipes/publish", a.RequireRole(auth.RoleOperator, pub.publish))
}

type publishAPI struct {
	api       *API
	publisher Publisher
}

// publishRequest is the submitted body. The document travels as a string
// because it IS text — a recipe is YAML — and because base64 would hide
// the bytes that get published from anyone reading a request log.
type publishRequest struct {
	// Reference is the destination: registry/cookbook/name:version.
	Reference string `json:"reference"`
	// Document is the recipe YAML, published byte for byte.
	Document string `json:"document"`
}

// publishResponse is what a publication reports. The digest is the
// artifact's identity and the argument `cosign sign` takes.
type publishResponse struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	// Unchanged marks the RECIPE-SPEC §8 no-op: the tag already pointed
	// at this exact content. Reported rather than flattened into a plain
	// success — a caller retrying a publication needs to know nothing
	// moved.
	Unchanged bool `json:"unchanged"`
}

// publish serves POST /api/v1/recipes/publish.
func (p *publishAPI) publish(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.IdentityFrom(r.Context())
	if p.publisher == nil {
		p.api.Problem(w, r, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no publisher is wired on this instance")))
		return
	}
	var req publishRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPublishRequest)).Decode(&req); err != nil {
		p.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the request body", "path": "-", "constraint": err.Error(),
		}))
		return
	}
	if req.Reference == "" || req.Document == "" {
		p.auditPublish(r, id.Name, req.Reference, audit.OutcomeFailure)
		p.api.Problem(w, r, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the request body", "path": "reference, document",
			"constraint": "both a destination reference and a recipe document are required",
		}))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), publishTimeout)
	defer cancel()
	res, err := p.publisher.PublishRecipe(ctx, req.Reference, []byte(req.Document))
	if err != nil {
		p.auditPublish(r, id.Name, req.Reference, publishOutcome(err))
		p.api.Problem(w, r, publishProblem(req.Reference, err))
		return
	}
	p.auditPublish(r, id.Name, res.Reference, audit.OutcomeSuccess)
	// 200, not 201: an unchanged republication created nothing, and one
	// status for both outcomes keeps the "unchanged" flag as the single
	// place that distinction is expressed.
	p.api.JSON(w, http.StatusOK, publishResponse{
		Reference: res.Reference, Digest: res.Digest, Unchanged: res.Unchanged,
	})
}

// publishProblem maps a failure onto the catalog. Everything the SDK and
// the publisher refuse is already taxonomized and passes through; what is
// left is a registry that did not answer.
func publishProblem(ref string, err error) *taxonomy.Error {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te
	}
	return taxonomy.New(taxonomy.CodeRegistryUnreachable,
		taxonomy.Params{"host": registryHostOf(ref)}).WithCause(err)
}

// publishOutcome distinguishes a policy barrier (the allowlist, the §8
// immutability rule) from a failure, per the FR-094 outcome set.
func publishOutcome(err error) string {
	var te *taxonomy.Error
	if errors.As(err, &te) && te.Entry().Class == taxonomy.ClassPolicy {
		return audit.OutcomeDenied
	}
	return audit.OutcomeFailure
}

// auditPublish emits the FR-094 record: outbound writing, recorded with
// the authenticated identity and the real network origin.
func (p *publishAPI) auditPublish(r *http.Request, actor, ref, outcome string) {
	audit.Log(r.Context(), p.api.logger, &audit.Event{
		Actor: actor, Action: audit.ActionRecipePublish, Target: ref,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// registryHostOf is the first path segment of a reference — the host a
// failed publication could not reach.
func registryHostOf(ref string) string {
	for i := range len(ref) {
		if ref[i] == '/' {
			return ref[:i]
		}
	}
	return ref
}
