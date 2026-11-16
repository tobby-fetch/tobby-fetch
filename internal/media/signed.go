// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// What a delivery is made of comes from its SIGNED recipe document, never
// from the medium's bookkeeping (B-066).
//
// The recipe graph (meta/recipes.json, copied into meta/media.json) says
// which ingredients a recipe has and where they landed, and it is unsigned
// by design (ADR-0006): whoever holds the medium can rewrite it and
// re-inventory. The recipe signature covers the artifact manifest, which
// pins the recipe document, which pins every ingredient by digest
// (RECIPE-SPEC §12.2). So the verification reads that document out of the
// artifact it has just re-hashed, derives the ingredient set from it, and
// refuses the recipe whole when the bookkeeping declares anything else —
// an extra ingredient, another digest, another location. The walk, the
// ingredient signature checks and the served set (B-063) are all built
// from the document's list.

// signedIngredient is one ingredient as the signed recipe document pins
// it, located on the medium.
type signedIngredient struct {
	Ingredient
	// Nominal is the canonical nominal repository (relocate.Path of the
	// document's ref): the pattern space of trust scopes (ADR-0013).
	Nominal string
}

// signedIngredients reads the recipe document of r out of the medium and
// returns its ingredients, located under the producing instance's base
// prefix. It must run after the walk has checked the artifact manifest
// and its layer against the inventory; every byte it parses is hashed
// again here, so a file swapped after that check is caught too.
func (w *walk) signedIngredients(r *Recipe) ([]signedIngredient, *Reason) {
	id := r.Name + "@" + r.Version
	unreadable := func(p string, err error) *Reason {
		return &Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: p, Params: map[string]string{
			"path": p, "detail": "recipe document of " + id + ": " + err.Error(),
		}}
	}
	disagree := func(format string, args ...any) *Reason {
		return &Reason{Code: taxonomy.CodeMediaIngredientsDisagree, Path: r.ArtifactRepo, Params: map[string]string{
			"recipe": id, "detail": fmt.Sprintf(format, args...),
		}}
	}

	h, err := digestHex(r.Digest)
	if err != nil {
		return nil, unreadable(r.ArtifactRepo, err)
	}
	manifestPath := blobPath(h)
	raw, err := w.readBounded(manifestPath, maxManifestBytes, r.Digest)
	if err != nil {
		return nil, unreadable(manifestPath, err)
	}
	layout, err := cookbook.VerifyManifest(raw)
	if err != nil {
		return nil, unreadable(manifestPath, err)
	}
	lh, err := digestHex(layout.Document.Digest)
	if err != nil {
		return nil, unreadable(manifestPath, err)
	}
	docPath := blobPath(lh)
	doc, err := w.readBounded(docPath, cookbook.MaxDocumentBytes, layout.Document.Digest)
	if err != nil {
		return nil, unreadable(docPath, err)
	}
	recipe, err := spec.ParseRecipe(doc)
	if err != nil {
		return nil, unreadable(docPath, err)
	}
	if err := recipe.Validate(spec.ProfileCooked); err != nil {
		return nil, unreadable(docPath, err)
	}
	// §11.3: the document names the recipe it is published as — and the
	// graph entry that points at it names the same one.
	segments := strings.Split(r.CookbookRepo, "/")
	if err := recipe.ValidatePublishLocation(segments[len(segments)-1], r.ArtifactTag); err != nil {
		return nil, disagree("%s", err.Error())
	}
	if recipe.Metadata.Name != r.Name || recipe.Metadata.Version != r.Version {
		return nil, disagree("the signed document is %s@%s", recipe.Metadata.Name, recipe.Metadata.Version)
	}

	prefix, ok := basePrefix(r)
	if !ok {
		return nil, disagree("the recipe artifact sits at %s, which is not its cookbook repository %s under a base prefix",
			r.ArtifactRepo, r.CookbookRepo)
	}
	out := make([]signedIngredient, 0, len(recipe.Spec.Ingredients))
	for i := range recipe.Spec.Ingredients {
		ing := &recipe.Spec.Ingredients[i]
		nominal, err := relocate.Path(ing.Ref)
		if err != nil {
			return nil, unreadable(docPath, err)
		}
		out = append(out, signedIngredient{
			Ingredient: Ingredient{
				Name: ing.Name, Kind: string(ing.Kind),
				Repo: prefix + nominal, Tag: ing.Version, Digest: ing.Digest,
			},
			Nominal: nominal,
		})
	}
	if reason := compareIngredients(r.Ingredients, out); reason != "" {
		return nil, disagree("%s", reason)
	}
	return out, nil
}

// compareIngredients states the first thing the bookkeeping declares that
// the signed list does not, or "" when there is none: every ingredient
// the graph lists must be one the document pins, at the same location,
// tag and digest.
//
// The converse is not a disagreement. An ingredient the document pins and
// the graph omits is what a synchronization records when that ingredient
// failed to arrive (sync drops the rows of failed ingredients), and the
// walk, which follows the document, then finds its content missing
// (TBY-MED-010) — the accurate refusal — or finds it whole.
func compareIngredients(declared []Ingredient, signed []signedIngredient) string {
	byName := make(map[string]*signedIngredient, len(signed))
	for i := range signed {
		byName[signed[i].Name] = &signed[i]
	}
	seen := make(map[string]bool, len(declared))
	for i := range declared {
		d := &declared[i]
		s, ok := byName[d.Name]
		switch {
		case !ok:
			return fmt.Sprintf("ingredient %q (%s@%s) is not in the signed recipe", d.Name, d.Repo, d.Digest)
		case seen[d.Name]:
			return fmt.Sprintf("ingredient %q is listed twice", d.Name)
		case d.Digest != s.Digest:
			return fmt.Sprintf("ingredient %q is recorded at digest %s, the signed recipe pins %s", d.Name, d.Digest, s.Digest)
		case d.Repo != s.Repo:
			return fmt.Sprintf("ingredient %q is recorded in %s, the signed recipe places it in %s", d.Name, d.Repo, s.Repo)
		case d.Tag != s.Tag:
			return fmt.Sprintf("ingredient %q is recorded with tag %q, the signed recipe pins %q", d.Name, d.Tag, s.Tag)
		}
		seen[d.Name] = true
	}
	return ""
}

// basePrefix recovers the producing instance's base prefix (FR-035) from
// where the recipe artifact sits: the medium places the artifact, like
// every ingredient, under that prefix, beside its nominal cookbook path,
// so the prefix is the difference between the two. The prefix is either
// empty or ends with a slash; ok is false on a medium where the two do not
// line up.
func basePrefix(r *Recipe) (prefix string, ok bool) {
	cookbookPath, err := relocate.Path(r.CookbookRepo)
	if err != nil || !strings.HasSuffix(r.ArtifactRepo, cookbookPath) {
		return "", false
	}
	prefix = strings.TrimSuffix(r.ArtifactRepo, cookbookPath)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		return "", false
	}
	return prefix, true
}

// readBounded reads one file of the medium, bounded, and checks that the
// bytes read are the ones the digest names.
func (w *walk) readBounded(p string, limit int64, want string) ([]byte, error) {
	f, err := w.c.root.Open(filepath.FromSlash(p))
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("larger than the %d-byte bound", limit)
	}
	sum := sha256.Sum256(raw)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("its bytes hash to %s, not %s", got, want)
	}
	return raw, nil
}
