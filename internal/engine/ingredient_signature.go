// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"log/slog"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Ingredient signatures (FR-033 as amended for B-062, RECIPE-SPEC §12.2).
//
// An ingredient's own signature is a consumer policy decision: the
// delivery is authenticated by its recipe's signature, which pins every
// ingredient digest. By default the signature is copied along (§12.2),
// verified for information against the applicable trust roots, recorded
// on the task item, the report row and the log — and never blocks. Under
// trust.requireIngredientSignatures, an ingredient without a signature
// those roots verify blocks its recipe whole (R-19), identically on every
// path: the rule is media.CheckIngredientSignature, here and in `media
// verify`.

// DecideIngredient returns the trust decision for an ingredient's own
// signature: the scope resolution of Decide, on the ingredient's
// canonical nominal repository, plus the requirement. An allowUnsigned
// scope lifts the requirement — the scope is the declared perimeter
// inside which unsigned content is admitted, and the opt-in does not
// override a declaration the operator made on purpose.
func (p *TrustPolicy) DecideIngredient(nominalRepo string) Decision {
	d := p.Decide(nominalRepo)
	d.RequireSignature = p.requireIngredients && !d.AllowUnsigned
	return d
}

// RequiresIngredientSignatures reports trust.requireIngredientSignatures,
// for the surfaces that state the posture (FR-075).
func (p *TrustPolicy) RequiresIngredientSignatures() bool { return p.requireIngredients }

// DecideIngredient implements media.Trust.
func (m MediaTrust) DecideIngredient(nominalRepo string) media.TrustDecision {
	if m.Policy == nil {
		return media.TrustDecision{}
	}
	return mediaDecision(m.Policy.DecideIngredient(nominalRepo))
}

func mediaDecision(d Decision) media.TrustDecision {
	return media.TrustDecision{
		AllowUnsigned: d.AllowUnsigned, Scope: d.Scope, Keys: d.Keys,
		RequireSignature: d.RequireSignature,
	}
}

// ingredientSignature checks one ingredient's own signature in the local
// store, where localRepo holds it, and logs the outcome. The inspection is
// always returned, for the record; the error is the TBY-SIG-001 refusal
// when the requirement applies and nothing verifies.
//
// Trust scopes match the CANONICAL nominal repository, as for the recipe
// (decideStored, ADR-0013); media verification derives the same path from
// the medium (media.ingredientNominal).
func (e *Engine) ingredientSignature(ctx context.Context, logger *slog.Logger, localRepo string, ing *spec.Ingredient, rid string) (sigverify.Inspection, *taxonomy.Error) {
	nominal, err := relocate.Path(ing.Ref)
	if err != nil {
		nominal = ing.Ref
	}
	d := mediaDecision(e.trust.DecideIngredient(nominal))
	in, refusal := media.CheckIngredientSignature(ctx, &storeManifests{src: e.store, repo: localRepo},
		localRepo, ing.Digest, media.IngredientSubject(ing.Name, rid, nominal, ing.Digest), d)

	attrs := []slog.Attr{
		slog.String("ingredient", ing.Name), slog.String("digest", ing.Digest),
		slog.String("signature", string(in.State)), slog.String("detail", in.String()),
		slog.Bool("required", d.RequireSignature), slog.String("requirement", "FR-033"),
	}
	if d.Scope != "" {
		attrs = append(attrs, slog.String("trust_scope", d.Scope))
	}
	if refusal != nil {
		logger.LogAttrs(ctx, slog.LevelWarn, "ingredient signature required and not verified",
			append(attrs, slog.String("code", string(refusal.Code)))...)
		return in, refusal.Error()
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "ingredient signature checked", attrs...)
	return in, nil
}

// checkFetchedSignatures is the fetch side of the rule: once every
// ingredient of a recipe is in the local store with its signatures
// (§12.2), each one's own signature is checked there and recorded on its
// item and its resolution row. It returns the refusals, by ingredient
// name; the caller withholds the recipe whole when there is any (R-19),
// before anything of it is scanned, stored as a cookbook entry, recorded
// or pushed.
//
// records is indexed like the recipe's ingredients; an empty Repo is an
// ingredient that did not land, whose item already says why.
func (e *Engine) checkFetchedSignatures(ctx context.Context, sink *taskSink, logger *slog.Logger, recipe *spec.Recipe, records []store.IngredientRecord, rid string) map[string]*taxonomy.Error {
	refusals := map[string]*taxonomy.Error{}
	for i := range recipe.Spec.Ingredients {
		ing := &recipe.Spec.Ingredients[i]
		if records[i].Repo == "" {
			continue
		}
		in, refusal := e.ingredientSignature(ctx, logger, records[i].Repo, ing, rid)
		itemName := rid + "/" + ing.Name
		sink.update(func(t *tasks.Task) bool {
			item := itemFor(t, itemName)
			item.Signature = &in
			if refusal != nil {
				item.Status = tasks.StatusFailed
				item.Error = tasks.FromTaxonomy(refusal)
			}
			for j := range t.Resolutions {
				r := &t.Resolutions[j]
				if r.Recipe == rid && r.Ingredient == ing.Name && r.Destination == "" {
					r.Signature = &in
				}
			}
			return true
		})
		if refusal != nil {
			refusals[ing.Name] = refusal
		}
	}
	return refusals
}

// checkPromotedSignatures is the push side of the rule, applied again
// over the exact bytes about to leave, before ANY of the recipe crosses:
// a required signature that does not verify withholds the whole delivery
// (RECIPE-SPEC §12.3: the affected item is the recipe), not just the one
// ingredient. It returns the inspections, indexed like the recipe's
// ingredients (nil where the ingredient's location is unknown, which
// promoteIngredient reports), and the refusals by ingredient name.
func (e *Engine) checkPromotedSignatures(ctx context.Context, logger *slog.Logger, f *FetchedRecipe, rid string, org *origin) (sigs []*sigverify.Inspection, refusals map[string]*taxonomy.Error) {
	ings := f.Recipe.Spec.Ingredients
	sigs = make([]*sigverify.Inspection, len(ings))
	refusals = map[string]*taxonomy.Error{}
	for i := range ings {
		ing := &ings[i]
		local, err := org.stored(ing.Ref, ing.Name)
		if err != nil {
			continue
		}
		in, refusal := e.ingredientSignature(ctx, logger, local, ing, rid)
		sigs[i] = &in
		if refusal != nil {
			refusals[ing.Name] = refusal
		}
	}
	return sigs, refusals
}
