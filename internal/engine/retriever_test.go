// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
)

// TestLoadRetrieverSources locks FR-010: the desired-state document loads
// from a local file, an HTTP(S) URL, and an OCI reference (with and
// without the oci:// prefix); a wrong source yields an actionable error.
func TestLoadRetrieverSources(t *testing.T) {
	ctx := context.Background()
	remotes := newRemotes(t, nil)
	entries := []spec.RecipeSelector{{Name: "wordpress", Version: "6.8.2"}}
	raw := retrieverYAML(t, "zone-src", "registry.example.com/cookbook", entries)

	t.Run("local file", func(t *testing.T) {
		path := writeTempFile(t, "retriever.yaml", raw)
		r, err := LoadRetriever(ctx, remotes, path)
		if err != nil {
			t.Fatal(err)
		}
		if r.Metadata.Name != "zone-src" || len(r.Spec.Recipes) != 1 {
			t.Errorf("loaded retriever = %+v", r)
		}
	})

	t.Run("http url", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(raw)
		}))
		t.Cleanup(srv.Close)
		r, err := LoadRetriever(ctx, remotes, srv.URL+"/retriever.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if r.Metadata.Name != "zone-src" {
			t.Errorf("loaded retriever = %+v", r)
		}
	})

	t.Run("http error is actionable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(srv.Close)
		_, err := LoadRetriever(ctx, remotes, srv.URL+"/missing.yaml")
		if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("error = %v, want the HTTP status named", err)
		}
	})

	t.Run("oci reference", func(t *testing.T) {
		reg := newRegistry(t)
		// The retriever artifact follows the single-YAML-layer envelope
		// (§11.2 applied to a Retriever document).
		publishRecipe(t, reg.st, "retrievers/lab", "current", raw)
		for _, source := range []string{
			reg.addr + "/retrievers/lab:current",
			"oci://" + reg.addr + "/retrievers/lab:current",
		} {
			r, err := LoadRetriever(ctx, remotes, source)
			if err != nil {
				t.Errorf("LoadRetriever(%q): %v", source, err)
				continue
			}
			if r.Metadata.Name != "zone-src" {
				t.Errorf("LoadRetriever(%q) = %+v", source, r)
			}
		}
	})

	t.Run("oci reference to a missing repo", func(t *testing.T) {
		reg := newRegistry(t)
		_, err := LoadRetriever(ctx, remotes, "oci://"+reg.addr+"/retrievers/ghost:current")
		if err == nil {
			t.Error("LoadRetriever succeeded on a missing OCI reference")
		}
	})

	t.Run("missing file is actionable", func(t *testing.T) {
		_, err := LoadRetriever(ctx, remotes, "/nonexistent/dir/retriever.yaml")
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("error = %v, want a does-not-exist explanation (FR-010)", err)
		}
	})

	// A Windows drive designator is a file path, not a registry host. The
	// case is checked on every platform because the classification is
	// lexical by design: a configuration file is read on the machine it is
	// deployed to, so the answer must not depend on the build target
	// (NFR-018).
	t.Run("missing windows path is actionable, not a registry guess", func(t *testing.T) {
		for _, source := range []string{
			`C:/config/retriever.yaml`,
			`C:\config\retriever.yaml`,
			`c:config/retriever.yaml`,
		} {
			_, err := LoadRetriever(ctx, remotes, source)
			if err == nil || !strings.Contains(err.Error(), "does not exist") {
				t.Errorf("LoadRetriever(%q) error = %v, want a does-not-exist explanation and no registry attempt (FR-010)", source, err)
			}
		}
	})

	t.Run("empty source is actionable", func(t *testing.T) {
		_, err := LoadRetriever(ctx, remotes, "")
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Errorf("error = %v, want the not-configured explanation", err)
		}
	})

	t.Run("invalid document is rejected", func(t *testing.T) {
		path := writeTempFile(t, "invalid.yaml", []byte("apiVersion: recipe.tobby.dev/v1alpha1\nkind: Recipe\n"))
		if _, err := LoadRetriever(ctx, remotes, path); err == nil {
			t.Error("LoadRetriever accepted a non-Retriever document")
		}
	})
}
