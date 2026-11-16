// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/provenance"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// historyOf is a task lister holding a fixed history.
type historyOf []*tasks.Task

func (h historyOf) List(tasks.Status, string, string) []*tasks.Task { return h }

// lockedLog is a log sink safe for concurrent handlers.
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// provenanceUI is the browser half of the FR-098 acceptance store: the
// seeded content store plus its FR-044 provenance (a recipe-managed image,
// a unit import, a chart pushed through /v2/), a FileSet packed by the real
// packer, and a scan of the recipe-managed tag.
func provenanceUI(t *testing.T, sw hardening.Switches, logs *lockedLog) (*UI, *store.Store) {
	t.Helper()
	st := seedContentStore(t)
	seedProvenance(t, st)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "readme.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fileserve.NewPacker(st, "", nil).Pack(context.Background(),
		fileserve.PackRequest{Source: src, Name: "docs", Version: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	info, err := st.RepoInfo(context.Background(), "docker.io/bitnami/wordpress")
	if err != nil {
		t.Fatal(err)
	}
	scanned := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	history := &provenance.History{Graph: st, Tasks: historyOf{{
		ID: "tsk_sync", Type: tasks.TypeSync, Created: scanned,
		Items: []tasks.Item{{
			Name: "wordpress@6.4.2/wordpress", Digest: info.Tags[0].Digest, Status: tasks.StatusDone,
			Scan: &scan.Report{
				ScannedAt: scanned,
				Database:  &scan.Database{BuiltAt: scanned.Add(-30 * time.Hour), AgeSeconds: 30 * 3600},
				Policy:    scan.Evaluation{Verdict: scan.VerdictWarn},
			},
		}},
	}}}
	var logw io.Writer
	if logs != nil {
		logw = logs
	}
	u := newTestUIWithOptions(t, &Options{Store: st, Hardening: sw, ScanHistory: history}, logw)
	root := t.TempDir()
	u.fileSets = &fileserve.Surface{
		Catalog:            fileSetsCatalog{st: st},
		Packer:             fileserve.NewPacker(st, "", nil, fileserve.WithPackRoots([]string{root})),
		PackEnabled:        true,
		UnitImportDisabled: sw.DisableUnitImport,
	}
	return u, st
}

// TestContentBrowserShowsProvenance is the browser half of the FR-098
// acceptance: each repository with its class in the listing, the
// repository page with its class, its managing recipe, and the scanned
// tag's date with the database age.
func TestContentBrowserShowsProvenance(t *testing.T) {
	u, _ := provenanceUI(t, hardening.Switches{}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/content", nil).Body.String()
	for _, want := range []string{
		`<th scope="col">Provenance</th>`,
		`data-provenance="recipe">Managed by a verified recipe`,
		`data-provenance="unit-import">Unit import`,
		`data-provenance="unit-import">Packed on this instance`,
		`data-provenance="seed">Pushed directly`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("listing misses %q", want)
		}
	}
	if strings.Contains(body, `data-hardening=`) {
		t.Error("a default instance states a hardening switch")
	}

	body = get(t, mux, c, "/content/docker.io/bitnami/wordpress", nil).Body.String()
	for _, want := range []string{
		`data-provenance="recipe"`, "Managed by a verified recipe", "wordpress@6.4.2",
		`data-scan="warn"`, "Last scanned 2026-11-10", "database 1d6h old at scan time",
		`data-last-scan="warn"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("recipe repository page misses %q", want)
		}
	}
	body = get(t, mux, c, "/content/localhost/filesets/docs", nil).Body.String()
	for _, want := range []string{`data-provenance="unit-import"`, "Packed on this instance", "Not scanned on this instance"} {
		if !strings.Contains(body, want) {
			t.Errorf("packed FileSet page misses %q", want)
		}
	}
	body = get(t, mux, c, "/content/docker.io/bitnamicharts/wordpress", nil).Body.String()
	if !strings.Contains(body, `data-provenance="seed"`) || !strings.Contains(body, "Pushed directly") {
		t.Error("seeded repository page misses its class")
	}
}

// TestImportScreenDisabledWhenHardened locks the screen half of
// hardening.disableUnitImport (FR-098): the action shown disabled naming
// the setting, never hidden; the inspection and the submission refused
// with TBY-POL-005 and audited as hardening.refused; no task created.
func TestImportScreenDisabledWhenHardened(t *testing.T) {
	logs := &lockedLog{}
	u, _ := provenanceUI(t, hardening.Switches{DisableUnitImport: true}, logs)
	q, err := tasks.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	u.queue = q
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := get(t, mux, c, "/import?ref=docker.io/library/redis:8", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("GET /import = %d", w.Code)
	}
	for _, want := range []string{`data-hardening="disableUnitImport"`, "hardening.disableUnitImport", `disabled aria-describedby="import-hardened"`} {
		if !strings.Contains(body, want) {
			t.Errorf("import page misses %q", want)
		}
	}
	if strings.Contains(body, `id="inspect-zone" hx-get=`) {
		t.Error("a hardened import page still arms the load-time inspection")
	}

	w = get(t, mux, c, "/import?ref=docker.io/library/redis:8", map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-005") {
		t.Errorf("inspection fragment = %d, want 403 TBY-POL-005:\n%s", w.Code, w.Body)
	}

	w = postImportForm(t, u, mux, c, url.Values{"ref": {"docker.io/library/redis:8"}, "platform": {"linux/amd64"}}, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-005") {
		t.Errorf("POST /import = %d, want 403 TBY-POL-005", w.Code)
	}
	if n := len(q.List("", "", "")); n != 0 {
		t.Errorf("%d task(s) created under the switch", n)
	}
	if n := strings.Count(logs.String(), `"action":"hardening.refused"`); n != 2 {
		t.Errorf("%d hardening.refused records, want 2:\n%s", n, logs)
	}
	if !strings.Contains(logs.String(), `"setting":"hardening.disableUnitImport"`) {
		t.Error("the audit record does not name the setting")
	}
}

// TestFileSetsPackDisabledWhenHardened: the packing form shown disabled
// naming the setting, and the submission refused and audited (FR-098).
func TestFileSetsPackDisabledWhenHardened(t *testing.T) {
	logs := &lockedLog{}
	u, st := provenanceUI(t, hardening.Switches{DisableUnitImport: true}, logs)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/filesets", nil).Body.String()
	for _, want := range []string{`data-hardening="disableUnitImport"`, `disabled aria-describedby="pack-hardened"`} {
		if !strings.Contains(body, want) {
			t.Errorf("filesets page misses %q", want)
		}
	}
	w := postPack(t, u, mux, c, url.Values{"source": {"/tmp"}, "name": {"docs"}, "version": {"2.0.0"}})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-POL-005") {
		t.Errorf("POST /filesets/pack = %d, want 403 TBY-POL-005", w.Code)
	}
	if !strings.Contains(logs.String(), `"action":"hardening.refused"`) || strings.Contains(logs.String(), `"action":"fileset.pack"`) {
		t.Errorf("want exactly the hardening record:\n%s", logs)
	}
	if _, ok := st.ResolveTag(context.Background(), "localhost/filesets/docs", "2.0.0"); ok {
		t.Error("a refused packing landed content")
	}
}

// TestContentBrowserStatesReadOnlyRegistry: under
// hardening.disableRegistryPush the content listing and a seeded
// repository's page say the registry is read-only, naming the setting.
func TestContentBrowserStatesReadOnlyRegistry(t *testing.T) {
	u, _ := provenanceUI(t, hardening.Switches{DisableRegistryPush: true}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	for _, target := range []string{"/content", "/content/docker.io/bitnamicharts/wordpress"} {
		body := get(t, mux, c, target, nil).Body.String()
		if !strings.Contains(body, `data-hardening="disableRegistryPush"`) || !strings.Contains(body, "hardening.disableRegistryPush") {
			t.Errorf("%s does not state the read-only registry", target)
		}
	}
}
