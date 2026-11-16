// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"context"
	"fmt"
	"strings"

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
// fingerprints tried — and, like a recipe refusal (B-057), the reserved
// root the signature does verify against, when one does.
func CheckIngredientSignature(ctx context.Context, src sigverify.Manifests, repo, digest, subject string, d TrustDecision) (sigverify.Inspection, *Reason) {
	in := sigverify.Inspect(ctx, src, repo, digest, d.Keys)
	if !d.RequireSignature || in.Verified() {
		return in, nil
	}
	params := map[string]string{
		"recipe": subject, "fingerprints": ingredientFingerprints(in, d), "scope": d.Scope,
	}
	if in.State != sigverify.StateNone {
		if reserved, reservedTo := SignatureElsewhere(ctx, src, repo, digest, d); reserved != "" {
			params["reserved"], params["reservedto"] = reserved, reservedTo
		}
	}
	return in, &Reason{Code: taxonomy.CodeSignature, Params: params}
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
		// B-068: roots may well be configured, every one reserved to
		// another scope — say which.
		return NoKeyDetail(d)
	case in.State == sigverify.StateNone:
		return "no signature artifact found"
	case len(in.Tried) > 0:
		return strings.Join(in.Tried, ", ")
	}
	return in.Detail
}

// checkIngredients applies CheckIngredientSignature to every ingredient of
// a delivery whose recipe has just verified, reading the medium. The list
// is the one the signed recipe document pins (B-066), each decided on its
// canonical nominal repository. It returns the verdicts and the first
// refusal, in document order.
func checkIngredients(ctx context.Context, src Reader, id string, signed []signedIngredient, trust Trust) ([]IngredientVerdict, *Reason) {
	if len(signed) == 0 {
		return nil, nil
	}
	out := make([]IngredientVerdict, 0, len(signed))
	var first *Reason
	for i := range signed {
		ing := &signed[i]
		var d TrustDecision
		if trust != nil {
			d = trust.DecideIngredient(ing.Nominal)
		}
		in, refusal := CheckIngredientSignature(ctx, &manifestSource{src: src, repo: ing.Repo},
			ing.Repo, ing.Digest, IngredientSubject(ing.Name, id, ing.Nominal, ing.Digest), d)
		out = append(out, IngredientVerdict{
			Name: ing.Name, Repo: ing.Nominal, Digest: ing.Digest,
			Signature: in, TrustScope: d.Scope, Required: d.RequireSignature,
		})
		if refusal != nil && first == nil {
			first = refusal
		}
	}
	return out, first
}
