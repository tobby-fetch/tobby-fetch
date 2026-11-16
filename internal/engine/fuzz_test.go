// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
)

// Recipes and Retrievers arrive from cookbooks and from operators, and a
// recipe is parsed BEFORE its signature has been trusted with anything:
// the parser is the first code a hostile cookbook reaches. Any bytes must
// produce a document or an error; a document that parses must parse to the
// same verdict again.
//
//	go test -run '^$' -fuzz FuzzParseRecipe ./internal/engine/
//	go test -run '^$' -fuzz FuzzParseRetriever ./internal/engine/
func FuzzParseRecipe(f *testing.F) {
	for _, seed := range []string{
		"apiVersion: recipe.tobby.dev/v1alpha1\nkind: Recipe\nmetadata:\n  name: a\n  version: 1.0.0\nspec:\n  ingredients:\n    - name: x\n      kind: ContainerImage\n      ref: r.example/a/b\n      version: 1.0.0\n      digest: sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n      platforms: [linux/amd64]\n",
		"apiVersion: recipe.tobby.dev/v1alpha1\nkind: Recipe\nmetadata: {}\nspec: {ingredients: []}\n",
		"apiVersion: recipe.tobby.dev/v9\nkind: Recipe\n",
		"kind: Recipe\nspec:\n  ingredients:\n    - &a {name: x}\n    - *a\n",
		"{", "", "[]", "\x00",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		r, err := spec.ParseRecipe(data)
		_, err2 := spec.ParseRecipe(data)
		if (err == nil) != (err2 == nil) {
			t.Fatal("two parses of the same bytes disagree")
		}
		if err == nil && r == nil {
			t.Fatal("no error and no recipe")
		}
	})
}

func FuzzParseRetriever(f *testing.F) {
	for _, seed := range []string{
		"apiVersion: recipe.tobby.dev/v1alpha1\nkind: Retriever\nmetadata:\n  name: zone-b\nspec:\n  cookbook: r.example/cookbook\n  recipes:\n    - name: a\n      version: \"1.0.0\"\n",
		"apiVersion: recipe.tobby.dev/v1alpha1\nkind: Retriever\nspec:\n  recipes:\n    - name: a\n      version: \">=1.0.0 <2\"\n",
		"kind: Retriever\n", "{", "", "- 1\n- 2\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		r, err := spec.ParseRetriever(data)
		if err == nil && r == nil {
			t.Fatal("no error and no retriever")
		}
	})
}
