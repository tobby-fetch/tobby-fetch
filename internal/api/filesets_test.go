// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Tests for the FileSet endpoints (FR-047 inventory, FR-048 packing):
// the API half of the /filesets screen, action for action (FR-061).

// fsCatalog is the inventory read surface over a real store, with the
// provenance rule the production adapter applies (internal/cli/serve.go).
type fsCatalog struct{ st *store.Store }

func (c fsCatalog) Repositories(ctx context.Context) ([]string, error) {
	return c.st.Repositories(ctx)
}

func (c fsCatalog) Tags(ctx context.Context, repo string) ([]string, error) {
	return c.st.Tags(ctx, repo)
}

func (c fsCatalog) Provenance(repo string) string {
	p, ok := c.st.ProvenanceOf(repo)
	switch {
	case !ok:
		return fileserve.FromSeed
	case p.Origin == store.OriginLocalPack:
		return fileserve.FromManualImport
	default:
		return fileserve.FromUnitImport
	}
}

// newFileSetsAPI mounts the FileSet endpoints over a real store behind
// real Basic authentication: a viewer and an admin account exist. roots
// is the files.packRoots confinement; passing none is the default
// posture, in which this surface packs nothing (FR-075).
func newFileSetsAPI(t *testing.T, roots ...string) *http.ServeMux {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		name string
		role auth.Role
		pw   string
	}{{"lecteur", auth.RoleViewer, "pw-view"}, {"alexis", auth.RoleAdmin, "pw-admin"}} {
		if err := accounts.AddAccount(a.name, a.role, a.pw, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterFileSets(a, &fileserve.Surface{
		Catalog: fsCatalog{st: st},
		Packer: fileserve.NewPacker(st, "", slog.New(slog.DiscardHandler),
			fileserve.WithPackRoots(roots)),
		Declared: []fileserve.Declared{
			{Name: "site", Ref: "registry.example.com/filesets/site-config", Version: "2.3.1"},
		},
		PackEnabled: len(roots) > 0,
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux
}

// apiPackPost performs a Basic-authenticated POST as the given account.
func apiPackPost(t *testing.T, mux *http.ServeMux, user, pw, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/filesets/pack", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.SetBasicAuth(user, pw)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// fsSourceTree writes a small tree under a root the caller can allow.
func fsSourceTree(t *testing.T) (root, source string) {
	t.Helper()
	root = t.TempDir()
	source = filepath.Join(root, "apt")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "Release"), []byte("Suite: stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, source
}

type fileSetListResponse struct {
	FileSets []struct {
		Name       string   `json:"name"`
		Reference  string   `json:"reference"`
		Repository string   `json:"repository"`
		Versions   []string `json:"versions"`
		Provenance string   `json:"provenance"`
		Signed     bool     `json:"signed"`
		Declared   bool     `json:"declared"`
		Served     bool     `json:"served"`
	} `json:"filesets"`
	PackEnabled bool `json:"packEnabled"`
}

// TestFileSetPackEndpointMarksAManualImport is the FR-048 acceptance path
// through the API: packing answers 201 with the pinned digest and states
// the result is unsigned; the inventory then lists it as a manual import,
// distinguishable from the Recipe-delivered declaration beside it.
func TestFileSetPackEndpointMarksAManualImport(t *testing.T) {
	root, source := fsSourceTree(t)
	mux := newFileSetsAPI(t, root)

	w := apiPackPost(t, mux, "alexis", "pw-admin",
		`{"source":"`+source+`","name":"debs","version":"1.0.0"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("pack = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		FileSet struct {
			Reference string `json:"reference"`
			Digest    string `json:"digest"`
			Files     int    `json:"files"`
			Signed    bool   `json:"signed"`
		} `json:"fileset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.FileSet.Reference != "localhost/filesets/debs" || created.FileSet.Files != 1 {
		t.Fatalf("created = %+v", created.FileSet)
	}
	if !strings.HasPrefix(created.FileSet.Digest, "sha256:") {
		t.Fatalf("digest = %q", created.FileSet.Digest)
	}
	if created.FileSet.Signed {
		t.Fatal(`the endpoint reports "signed": true; Tobby holds no key (ADR-0007)`)
	}

	lw := apiGet(t, mux, "/api/v1/filesets")
	if lw.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", lw.Code, lw.Body.String())
	}
	var list fileSetListResponse
	if err := json.Unmarshal(lw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if !list.PackEnabled {
		t.Error("packEnabled = false on a surface with a configured root")
	}
	byRef := map[string]int{}
	for i, e := range list.FileSets {
		byRef[e.Reference] = i
	}
	packed, ok := byRef["localhost/filesets/debs"]
	if !ok {
		t.Fatalf("the packed FileSet is missing from the inventory: %+v", list.FileSets)
	}
	got := list.FileSets[packed]
	if got.Provenance != "manual-import" || got.Signed || got.Declared || got.Served {
		t.Fatalf("packed entry = %+v, want an unsigned, undeclared manual import", got)
	}
	if len(got.Versions) != 1 || got.Versions[0] != "1.0.0" {
		t.Fatalf("versions = %v", got.Versions)
	}
	declared, ok := byRef["registry.example.com/filesets/site-config"]
	if !ok {
		t.Fatal("the declared FileSet is missing from the inventory")
	}
	if list.FileSets[declared].Provenance == "manual-import" {
		t.Error("a Recipe-delivered declaration is reported as a manual import (FR-048 requires them distinguishable)")
	}
}

// TestFileSetPackEndpointRefusals: a path outside files.packRoots and an
// unsafe tree are both refused with their stable code, and nothing lands.
func TestFileSetPackEndpointRefusals(t *testing.T) {
	root, source := fsSourceTree(t)

	t.Run("no configured root refuses every path", func(t *testing.T) {
		mux := newFileSetsAPI(t)
		w := apiPackPost(t, mux, "alexis", "pw-admin",
			`{"source":"`+source+`","name":"debs","version":"1.0.0"}`)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-FIL-003") {
			t.Fatalf("pack = %d, want 403 TBY-FIL-003: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "problem+json") {
			t.Errorf("Content-Type = %q, want an RFC 9457 problem document", ct)
		}
	})

	t.Run("an escaping symlink is refused", func(t *testing.T) {
		if err := os.Symlink("/etc/passwd", filepath.Join(source, "escape")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(filepath.Join(source, "escape")) })
		mux := newFileSetsAPI(t, root)
		w := apiPackPost(t, mux, "alexis", "pw-admin",
			`{"source":"`+source+`","name":"debs","version":"1.0.0"}`)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-FIL-002") {
			t.Fatalf("pack = %d, want 422 TBY-FIL-002: %s", w.Code, w.Body.String())
		}
	})

	t.Run("an unusable request is refused", func(t *testing.T) {
		mux := newFileSetsAPI(t, root)
		w := apiPackPost(t, mux, "alexis", "pw-admin", `{"source":"`+source+`","name":"Debs","version":""}`)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "TBY-FIL-001") {
			t.Fatalf("pack = %d, want 422 TBY-FIL-001: %s", w.Code, w.Body.String())
		}
	})
}

// TestFileSetPackEndpointIsAdminOnly mirrors the screen's floor: reading
// the inventory is a viewer action, packing is not.
func TestFileSetPackEndpointIsAdminOnly(t *testing.T) {
	root, source := fsSourceTree(t)
	mux := newFileSetsAPI(t, root)

	if w := apiGet(t, mux, "/api/v1/filesets"); w.Code != http.StatusOK {
		t.Fatalf("viewer list = %d", w.Code)
	}
	w := apiPackPost(t, mux, "lecteur", "pw-view",
		`{"source":"`+source+`","name":"debs","version":"1.0.0"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Fatalf("viewer pack = %d, want 403 TBY-AUTH-003: %s", w.Code, w.Body.String())
	}
}
