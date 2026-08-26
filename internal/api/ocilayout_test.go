// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The automation surface of FR-051 and FR-046. The behaviour itself is
// tested in internal/interop; what is held here is the contract a script
// depends on — the projection an operator's pre-flight reads, the task an
// export answers with, and the refusal a reset without its phrase gets.

package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// newLayoutAPI seeds a real store with one image and mounts the FR-051
// and FR-046 endpoints behind real Basic authentication.
func newLayoutAPI(t *testing.T) (mux *http.ServeMux, st *store.Store) {
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
	srv := httptest.NewServer(st.APIHandler())
	defer srv.Close()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(srv.Listener.Addr().String() + "/docker.io/library/alpine:3.22.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	if err := accounts.AddAccount("root", auth.RoleAdmin, "pw-admin", now); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store: accounts, Sessions: auth.NewSessions(time.Hour),
		Logger: slog.New(slog.DiscardHandler),
	}
	queue, err := tasks.Open(t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterOCILayout(a, interop.New(st, queue, "", slog.New(slog.DiscardHandler)), queue)
	mux = http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, st
}

// TestLayoutPlanEndpointReportsThePreFlightNumbers: the projection an
// automation compares with free space and with a target filesystem's
// per-file limit, and nothing written to reach it (FR-055).
func TestLayoutPlanEndpointReportsThePreFlightNumbers(t *testing.T) {
	mux, _ := newLayoutAPI(t)
	dir := t.TempDir()

	w := call(t, mux, http.MethodPost, "/api/v1/oci-layout/plan", "root", "pw-admin",
		`{"output":"`+filepath.ToSlash(filepath.Join(dir, "payload.tar"))+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("plan = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Format           string   `json:"format"`
		References       []string `json:"references"`
		Manifests        int      `json:"manifests"`
		Blobs            int      `json:"blobs"`
		TotalBytes       int64    `json:"totalBytes"`
		LargestFileBytes int64    `json:"largestFileBytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Format != "tar" || len(resp.References) != 1 {
		t.Fatalf("plan = %+v, want one reference in a tar", resp)
	}
	if resp.TotalBytes == 0 || resp.LargestFileBytes != resp.TotalBytes {
		t.Errorf("plan = %+v — a single-tar export is one file, so the two sizes match (FR-055)", resp)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "*")); len(entries) != 0 {
		t.Errorf("the plan wrote %v", entries)
	}
}

// TestLayoutExportEndpointAnswersATask: the long operation is a tracked
// task like every other, answered at once so a caller can follow it.
func TestLayoutExportEndpointAnswersATask(t *testing.T) {
	mux, _ := newLayoutAPI(t)
	out := filepath.Join(t.TempDir(), "payload.tar")

	w := call(t, mux, http.MethodPost, "/api/v1/oci-layout/export", "root", "pw-admin",
		`{"output":"`+filepath.ToSlash(out)+`","format":"directory"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("export = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Actor  string `json:"actor"`
			Layout *struct {
				Format string `json:"format"`
			} `json:"layout"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Task.ID == "" || resp.Task.Type != tasks.TypeLayoutExport || resp.Task.Actor != "root" {
		t.Fatalf("task = %+v", resp.Task)
	}
	// The parameters travel on the task, so a resumed run reads them back
	// from the file rather than from a request that is long gone (FR-029).
	if resp.Task.Layout == nil || resp.Task.Layout.Format != "directory" {
		t.Errorf("task.layout = %+v, want the submitted format", resp.Task.Layout)
	}
}

// TestLayoutImportEndpointRefusesAnUnreadableSource: the taxonomized
// refusal reaches the caller as a problem document, not a bare 500.
func TestLayoutImportEndpointRefusesAnUnreadableSource(t *testing.T) {
	mux, _ := newLayoutAPI(t)
	if w := call(t, mux, http.MethodPost, "/api/v1/oci-layout/import", "root", "pw-admin",
		`{"input":""}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty input = %d: %s", w.Code, w.Body.String())
	}
	missing := filepath.Join(t.TempDir(), "nowhere.tar")
	w := call(t, mux, http.MethodPost, "/api/v1/oci-layout/import", "root", "pw-admin",
		`{"input":"`+filepath.ToSlash(missing)+`"}`)
	if w.Code != http.StatusCreated {
		// The path is checked by the runner, not by the endpoint: the
		// task is where a medium that turned out unreadable is reported.
		t.Fatalf("import = %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("the test fixture accidentally exists")
	}
}

// TestStoreResetEndpointKeepsItsTypedConfirmation is FR-046 on the
// automation surface: the phrase is not something a script can omit, and
// the refusal carries its own code.
func TestStoreResetEndpointKeepsItsTypedConfirmation(t *testing.T) {
	mux, st := newLayoutAPI(t)

	w := call(t, mux, http.MethodPost, "/api/v1/store/reset", "root", "pw-admin", `{"confirmation":"yes"}`)
	entry, _ := taxonomy.Lookup(taxonomy.CodeResetConfirmation)
	if w.Code != entry.HTTPStatus {
		t.Fatalf("wrong confirmation = %d, want %d: %s", w.Code, entry.HTTPStatus, w.Body.String())
	}
	repos, err := st.Repositories(context.Background())
	if err != nil || len(repos) != 1 {
		t.Fatalf("the refused reset touched the store: %v, %v", repos, err)
	}

	w = call(t, mux, http.MethodPost, "/api/v1/store/reset", "root", "pw-admin",
		`{"confirmation":"`+interop.ConfirmationPhrase+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reset = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Repositories int   `json:"repositories"`
		Bytes        int64 `json:"bytes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Repositories != 1 || resp.Bytes == 0 {
		t.Errorf("reset reported %+v, want the one repository it discarded", resp)
	}
	repos, err = st.Repositories(context.Background())
	if err != nil || len(repos) != 0 {
		t.Fatalf("the store was not emptied: %v, %v", repos, err)
	}
}
