// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"fmt"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
)

// ResolveVersion resolves a version expression — exact tag or constraint
// (RECIPE-SPEC §9, FR-021) — against the available tags. An exact tag is
// matched literally; a constraint resolves to the highest satisfying
// semver tag; no match is a hard failure, never a silent fallback.
func ResolveVersion(expr string, tags []string) (string, error) {
	c, err := spec.ParseConstraint(expr)
	if err != nil {
		return "", fmt.Errorf("version %q: %w", expr, err)
	}
	if c.IsExact() {
		for _, t := range tags {
			if t == expr {
				return t, nil
			}
		}
		return "", fmt.Errorf("version %q: tag not found", expr)
	}
	tag, err := c.Resolve(tags)
	if err != nil {
		return "", fmt.Errorf("version %q: %w", expr, err)
	}
	return tag, nil
}
