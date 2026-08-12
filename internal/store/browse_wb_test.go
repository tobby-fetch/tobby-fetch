// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// TestRepositoriesPaginationLoop shrinks the internal catalog page to one
// entry: the full list must still come back — the 1000-entry ceiling is
// the HTTP catalog's, never the accessors' (FR-062).
func TestRepositoriesPaginationLoop(t *testing.T) {
	old := catalogPageSize
	catalogPageSize = 1
	t.Cleanup(func() { catalogPageSize = old })

	st, err := Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatal(err)
	}
	repos := []string{"a.example.com/one", "b.example.com/two", "c.example.com/three/nested"}
	for _, repo := range repos {
		ref, err := name.ParseReference(addr + "/" + repo + ":1")
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.Repositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.example.com/one", "b.example.com/two", "c.example.com/three/nested"}
	if len(got) != len(want) {
		t.Fatalf("repositories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("repositories[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Empty store: no error, no repositories.
	st2, err := Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st2.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	}()
	empty, err := st2.Repositories(context.Background())
	if err != nil || len(empty) != 0 {
		t.Errorf("empty store: repos=%v err=%v", empty, err)
	}
}

// TestPaginate pins the page-window math shared by the UI and the API.
func TestPaginate(t *testing.T) {
	cases := []struct {
		total, page, size      int
		start, end, totalPages int
	}{
		{0, 1, 25, 0, 0, 1},
		{10, 1, 25, 0, 10, 1},
		{25, 1, 25, 0, 25, 1},
		{26, 1, 25, 0, 25, 2},
		{26, 2, 25, 25, 26, 2},
		{26, 3, 25, 26, 26, 2}, // out of range: empty window, math intact
		{26, 0, 25, 0, 25, 2},  // clamped to page 1
	}
	for _, c := range cases {
		start, end, totalPages := paginate(c.total, c.page, c.size)
		if start != c.start || end != c.end || totalPages != c.totalPages {
			t.Errorf("paginate(%d, %d, %d) = (%d, %d, %d), want (%d, %d, %d)",
				c.total, c.page, c.size, start, end, totalPages, c.start, c.end, c.totalPages)
		}
	}
}

// TestKindDetection pins the media-type table of the badge component
// (UI-SPEC §7), including the RECIPE-SPEC §11.2 artifactType path that the
// integration seeds cannot produce with a config media type alone.
func TestKindDetection(t *testing.T) {
	cases := []struct {
		artifactType, configType string
		want                     Kind
	}{
		{"", "application/vnd.oci.image.config.v1+json", KindContainerImage},
		{"", "application/vnd.docker.container.image.v1+json", KindContainerImage},
		{"", "application/vnd.cncf.helm.config.v1+json", KindHelmChart},
		{"application/vnd.tobby.recipe.v1+yaml", "application/vnd.oci.empty.v1+json", KindRecipe},
		{"", "application/vnd.tobby.recipe.v1+yaml", KindRecipe},
		{"application/vnd.example.thing.v1", "application/vnd.oci.empty.v1+json", KindOCIArtifact},
		{"", "application/vnd.example.custom.config.v1+json", KindOCIArtifact},
		// TODO(FileSet): reads as OCIArtifact until RECIPE-SPEC defines its
		// artifact media type.
	}
	for _, c := range cases {
		p := &manifestProbe{ArtifactType: c.artifactType}
		p.Config.MediaType = c.configType
		if got := p.kind(); got != c.want {
			t.Errorf("kind(artifactType=%q, config=%q) = %s, want %s",
				c.artifactType, c.configType, got, c.want)
		}
	}
}
