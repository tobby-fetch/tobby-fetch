// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/provenance"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// fixedTasks is a task lister holding a fixed history.
type fixedTasks []*tasks.Task

func (f fixedTasks) List(tasks.Status, string, string) []*tasks.Task { return f }

// lockedBuffer is a log sink safe for concurrent handlers.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// provenanceStore builds the FR-098 acceptance store through the real
// write paths: a recipe-managed repository (its tag scanned), a unit
// import, a FileSet packed by the real packer, and a repository pushed by
// a standard client through the guarded /v2/ — which is what records it
// as seeded content.
type provenanceStore struct {
	st        *store.Store
	scans     *provenance.History
	recipeTag string // digest of the recipe-managed tag
	scannedAt time.Time
	builtAt   time.Time
}

func newProvenanceStore(t *testing.T) *provenanceStore {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	guarded := httptest.NewServer(hardening.Registry(hardening.Switches{}, st, slog.New(slog.DiscardHandler), st.APIHandler()))
	defer guarded.Close()
	raw := httptest.NewServer(st.APIHandler())
	defer raw.Close()
	push := func(host, repo string) string {
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := name.ParseReference(strings.TrimPrefix(host, "http://") + "/" + repo + ":1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatal(err)
		}
		d, _ := img.Digest()
		return d.String()
	}
	// Recipe-managed and unit-imported content arrive through the store
	// directly in production; the raw handler stands in for those paths
	// here and the ledger is written the way they write it.
	p := &provenanceStore{st: st}
	p.recipeTag = push(raw.URL, "docker.io/library/nginx")
	if err := st.PutRecipeRecord(&store.RecipeRecord{
		Name: "web", Version: "1.0.0", Verified: true,
		Ingredients: []store.IngredientRecord{{Name: "nginx", Repo: "docker.io/library/nginx", Tag: "1.0.0", Digest: p.recipeTag}},
	}); err != nil {
		t.Fatal(err)
	}
	push(raw.URL, "ghcr.io/team/tool")
	if _, err := st.RecordUnitImport("ghcr.io/team/tool"); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fileserve.NewPacker(st, "", nil).Pack(context.Background(),
		fileserve.PackRequest{Source: src, Name: "docs", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	push(guarded.URL, "zone/seeded/app")

	p.scannedAt = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	p.builtAt = p.scannedAt.Add(-30 * time.Hour)
	p.scans = &provenance.History{Graph: st, Tasks: fixedTasks{{
		ID: "tsk_sync", Type: tasks.TypeSync, Created: p.scannedAt,
		Items: []tasks.Item{{
			Name: "web@1.0.0/nginx", Digest: p.recipeTag, Status: tasks.StatusDone,
			Scan: &scan.Report{
				ScannedAt: p.scannedAt,
				Database:  &scan.Database{BuiltAt: p.builtAt, AgeSeconds: 30 * 3600},
				Policy:    scan.Evaluation{Verdict: scan.VerdictPass},
			},
		}},
	}}}
	return p
}

// provenanceAPI mounts the content, import and FileSet endpoints over the
// fixture, behind real Basic authentication, as an administrator.
func provenanceAPI(t *testing.T, p *provenanceStore, sw hardening.Switches, logs *lockedBuffer) *http.ServeMux {
	t.Helper()
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("chef", auth.RoleAdmin, "pw-admin", time.Now()); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	authn := &auth.Authenticator{Store: accounts, Sessions: auth.NewSessions(time.Hour), Logger: logger}
	a := api.New(authn, logger)
	a.SetControls(api.Controls{Hardening: sw, Scans: p.scans})
	api.RegisterContent(a, p.st, nil)
	q, err := tasks.Open(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	api.RegisterTasks(a, q, p.st, time.Second, nil)
	layouts := interop.New(p.st, q, "", logger)
	layouts.SetUnitImportDisabled(sw.DisableUnitImport)
	api.RegisterOCILayout(a, layouts, q)
	root := t.TempDir()
	api.RegisterFileSets(a, &fileserve.Surface{
		Catalog:            catalogOf{p.st},
		Packer:             fileserve.NewPacker(p.st, "", logger, fileserve.WithPackRoots([]string{root})),
		PackEnabled:        true,
		UnitImportDisabled: sw.DisableUnitImport,
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux
}

// catalogOf adapts the store to the FileSet inventory.
type catalogOf struct{ st *store.Store }

func (c catalogOf) Repositories(ctx context.Context) ([]string, error) { return c.st.Repositories(ctx) }
func (c catalogOf) Tags(ctx context.Context, repo string) ([]string, error) {
	return c.st.Tags(ctx, repo)
}
func (c catalogOf) Provenance(string) string { return fileserve.FromSeed }

func chefDo(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.SetBasicAuth("chef", "pw-admin")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestContentAPIExposesProvenance is the API half of the FR-098
// acceptance: each repository carries its class in the listing and its
// provenance on its detail, and the scanned tag its scan date with the
// database age.
func TestContentAPIExposesProvenance(t *testing.T) {
	p := newProvenanceStore(t)
	mux := provenanceAPI(t, p, hardening.Switches{}, &lockedBuffer{})

	w := chefDo(t, mux, http.MethodGet, "/api/v1/content", "")
	var list struct {
		Repositories []struct {
			Name       string `json:"name"`
			Provenance string `json:"provenance"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	want := map[string]string{
		"docker.io/library/nginx": "recipe", "ghcr.io/team/tool": "unit-import",
		"localhost/filesets/docs": "unit-import", "zone/seeded/app": "seed",
	}
	got := map[string]string{}
	for _, r := range list.Repositories {
		got[r.Name] = r.Provenance
	}
	for repo, class := range want {
		if got[repo] != class {
			t.Errorf("listing: %s = %q, want %q (all: %v)", repo, got[repo], class, got)
		}
	}

	type lastScan struct {
		Verdict            string    `json:"verdict"`
		ScannedAt          time.Time `json:"scannedAt"`
		DatabaseBuiltAt    time.Time `json:"databaseBuiltAt"`
		DatabaseAgeSeconds int64     `json:"databaseAgeSeconds"`
	}
	type detail struct {
		Provenance *struct {
			Class    string    `json:"class"`
			Origin   string    `json:"origin"`
			Recipes  []string  `json:"recipes"`
			LastScan *lastScan `json:"lastScan"`
		} `json:"provenance"`
		Tags []struct {
			Digest   string    `json:"digest"`
			LastScan *lastScan `json:"lastScan"`
		} `json:"tags"`
	}
	read := func(repo string) detail {
		w := chefDo(t, mux, http.MethodGet, "/api/v1/content/"+repo, "")
		var d detail
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || d.Provenance == nil {
			t.Fatalf("%s: %d %s", repo, w.Code, w.Body)
		}
		return d
	}
	rec := read("docker.io/library/nginx")
	if rec.Provenance.Class != "recipe" || len(rec.Provenance.Recipes) != 1 || rec.Provenance.Recipes[0] != "web@1.0.0" {
		t.Errorf("recipe repository provenance = %+v", rec.Provenance)
	}
	if len(rec.Tags) != 1 || rec.Tags[0].LastScan == nil {
		t.Fatalf("the scanned tag carries no last scan: %+v", rec.Tags)
	}
	if s := rec.Tags[0].LastScan; s.Verdict != "pass" || !s.ScannedAt.Equal(p.scannedAt) ||
		!s.DatabaseBuiltAt.Equal(p.builtAt) || s.DatabaseAgeSeconds != 30*3600 {
		t.Errorf("tag last scan = %+v", s)
	}
	if rec.Provenance.LastScan == nil || !rec.Provenance.LastScan.ScannedAt.Equal(p.scannedAt) {
		t.Errorf("repository last scan = %+v", rec.Provenance.LastScan)
	}
	if d := read("localhost/filesets/docs"); d.Provenance.Class != "unit-import" || d.Provenance.Origin != "local-pack" {
		t.Errorf("packed FileSet provenance = %+v", d.Provenance)
	}
	if d := read("ghcr.io/team/tool"); d.Provenance.Class != "unit-import" || d.Provenance.Origin != "" || d.Tags[0].LastScan != nil {
		t.Errorf("unit import provenance = %+v, tags %+v", d.Provenance, d.Tags)
	}
	if d := read("zone/seeded/app"); d.Provenance.Class != "seed" {
		t.Errorf("seeded provenance = %+v", d.Provenance)
	}
}

// TestImportAndPackEndpointsRefusedWhenHardened locks the API half of
// hardening.disableUnitImport (FR-098): the import, its inspection and the
// FileSet packing answer TBY-POL-005 naming the setting, with the
// catalog's status, for an administrator; each refusal is one
// hardening.refused audit record; and the FileSet listing says why its
// action is off.
func TestImportAndPackEndpointsRefusedWhenHardened(t *testing.T) {
	p := newProvenanceStore(t)
	logs := &lockedBuffer{}
	mux := provenanceAPI(t, p, hardening.Switches{DisableUnitImport: true}, logs)

	for _, c := range []struct{ method, target, body string }{
		{http.MethodPost, "/api/v1/import", `{"reference":"docker.io/library/redis:8","platforms":[{"name":"linux/amd64","digest":"sha256:` + strings.Repeat("0", 64) + `"}]}`},
		{http.MethodGet, "/api/v1/import/inspect?ref=docker.io/library/redis:8", ""},
		{http.MethodPost, "/api/v1/filesets/pack", `{"source":"/tmp","name":"docs","version":"2.0.0"}`},
		// An OCI layout import is a unit import too (B-071).
		{http.MethodPost, "/api/v1/oci-layout/import", `{"input":"/tmp/payload.tar"}`},
	} {
		w := chefDo(t, mux, c.method, c.target, c.body)
		var prob struct {
			Code   string         `json:"code"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &prob)
		if w.Code != http.StatusForbidden || prob.Code != "TBY-POL-005" || !strings.Contains(w.Body.String(), "hardening.disableUnitImport") {
			t.Errorf("%s %s = %d %s, want 403 TBY-POL-005 naming the setting", c.method, c.target, w.Code, w.Body)
		}
	}
	if n := strings.Count(logs.String(), `"action":"hardening.refused"`); n != 4 {
		t.Errorf("%d hardening.refused records, want one per refusal:\n%s", n, logs)
	}
	if !strings.Contains(logs.String(), `"setting":"hardening.disableUnitImport"`) {
		t.Errorf("the audit record does not name the setting:\n%s", logs)
	}
	if strings.Contains(logs.String(), `"action":"import.create"`) || strings.Contains(logs.String(), `"action":"fileset.pack"`) ||
		strings.Contains(logs.String(), `"action":"layout.import"`) {
		t.Errorf("a refused operation was also audited as performed:\n%s", logs)
	}

	w := chefDo(t, mux, http.MethodGet, "/api/v1/filesets", "")
	var list struct {
		PackEnabled    bool   `json:"packEnabled"`
		PackDisabledBy string `json:"packDisabledBy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.PackEnabled || list.PackDisabledBy != "hardening.disableUnitImport" {
		t.Errorf("filesets listing = %+v (%v), want packing off naming the setting", list, err)
	}
	if _, ok := p.st.ResolveTag(context.Background(), "localhost/filesets/docs", "2.0.0"); ok {
		t.Error("a refused packing landed content")
	}
}
