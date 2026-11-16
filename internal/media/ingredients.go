// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"context"
	"fmt"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Ingredient signatures (FR-033 as amended for B-062, RECIPE-SPEC §12.2).
//
// A delivery's authenticity is its recipe's signature, which pins every
// ingredient digest. An ingredient's OWN signature — typically its
// upstream publisher's — is a consumer policy decision. By default it is
// copied along, verified for information and recorded, and blocks
// nothing: an image its publisher signed with a key this zone does not
// know is exactly as authentic as the recipe pinning it says it is. The
// opt-in trust.requireIngredientSignatures turns the record into a
// requirement.
//
// CheckIngredientSignature below is the ONE rule. The synchronization,
// the promotion before every push, this package's verification and the
// media import all call it, so `media verify` cannot call a delivery
// pushable that the import then refuses (the second half of B-062).

// IngredientVerdict is one ingredient's own-signature verdict, as the
// verification report carries it.
type IngredientVerdict struct {
	Name string `json:"name"`
	// Repo is the canonical nominal repository the trust decision was
	// taken on — the pattern space of trust scopes (ADR-0013).
	Repo   string `json:"repo"`
	Digest string `json:"digest"`
	// Signature is what the ingredient's own signature turned out to be,
	// against THIS instance's applicable trust roots.
	Signature sigverify.Inspection `json:"signature"`
	// TrustScope names the declared scope that decided, when one matched.
	TrustScope string `json:"trustScope,omitempty"`
	// Required is true when trust.requireIngredientSignatures applied to
	// this ingredient (it does not inside an allowUnsigned scope).
	Required bool `json:"required,omitempty"`
}

// CheckIngredientSignature verifies one ingredient's own signature,
// repo@digest read from src, under the ingredient's trust decision d
// (Trust.DecideIngredient). The inspection is always returned, for the
// record. The refusal is non-nil only when d requires a signature and
// none verifies: TBY-SIG-001, naming subject (IngredientSubject) and the
// fingerprints tried.
func CheckIngredientSignature(ctx context.Context, src sigverify.Manifests, repo, digest, subject string, d TrustDecision) (sigverify.Inspection, *Reason) {
	in := sigverify.Inspect(ctx, src, repo, digest, d.Keys)
	if !d.RequireSignature || in.Verified() {
		return in, nil
	}
	return in, &Reason{Code: taxonomy.CodeSignature, Params: map[string]string{
		"recipe": subject, "fingerprints": ingredientFingerprints(in, d), "scope": d.Scope,
	}}
}

// IngredientSubject names an ingredient in a refusal: which one, of which
// recipe, at which pinned digest.
func IngredientSubject(name, recipe, repo, digest string) string {
	return fmt.Sprintf("ingredient %s of %s (%s@%s)", name, recipe, repo, digest)
}

// ingredientFingerprints renders the FR-033 detail of a refused
// ingredient the way the recipe path renders its own.
func ingredientFingerprints(in sigverify.Inspection, d TrustDecision) string {
	switch {
	case d.Keys == nil:
		return "none — no trust root is configured"
	case in.State == sigverify.StateNone:
		return "no signature artifact found"
	case len(in.Tried) > 0:
		return strings.Join(in.Tried, ", ")
	}
	return in.Detail
}

// checkIngredients applies CheckIngredientSignature to every ingredient of
// a delivery whose recipe has just verified, reading the medium. It
// returns the verdicts and the first refusal, in manifest order.
func checkIngredients(ctx context.Context, src Reader, r *Recipe, trust Trust) ([]IngredientVerdict, *Reason) {
	if len(r.Ingredients) == 0 {
		return nil, nil
	}
	id := r.Name + "@" + r.Version
	out := make([]IngredientVerdict, 0, len(r.Ingredients))
	var first *Reason
	for i := range r.Ingredients {
		ing := &r.Ingredients[i]
		nominal := ingredientNominal(r, ing.Repo)
		var d TrustDecision
		if trust != nil {
			d = trust.DecideIngredient(nominal)
		}
		in, refusal := CheckIngredientSignature(ctx, &manifestSource{src: src, repo: ing.Repo},
			ing.Repo, ing.Digest, IngredientSubject(ing.Name, id, nominal, ing.Digest), d)
		out = append(out, IngredientVerdict{
			Name: ing.Name, Repo: nominal, Digest: ing.Digest,
			Signature: in, TrustScope: d.Scope, Required: d.RequireSignature,
		})
		if refusal != nil && first == nil {
			first = refusal
		}
	}
	return out, first
}

// ingredientNominal recovers an ingredient's canonical nominal repository
// from where it sits on the medium.
//
// The medium places every ingredient under the producing instance's base
// prefix (FR-035), and that prefix is written down nowhere — but the
// recipe artifact sits under the same one, beside its nominal cookbook
// repository, so the prefix is the difference between the two. Stripping
// it gives exactly relocate.Path of the ingredient's nominal reference:
// the repository the promotion decides on for the same ingredient. A
// medium on which the two do not line up keeps the path as it is, which
// matches no scope written for the nominal form and gets the strict
// default — and the promotion, deciding on the signed recipe's own
// references, applies the same rule again before any push.
func ingredientNominal(r *Recipe, repo string) string {
	cookbook, err := relocate.Path(r.CookbookRepo)
	if err != nil || !strings.HasSuffix(r.ArtifactRepo, cookbook) {
		return repo
	}
	prefix := strings.TrimSuffix(r.ArtifactRepo, cookbook)
	switch {
	case prefix == "":
		return repo
	case strings.HasSuffix(prefix, "/") && strings.HasPrefix(repo, prefix):
		return strings.TrimPrefix(repo, prefix)
	}
	return repo
}
