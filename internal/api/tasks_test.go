// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
)

// newTasksAPI wires the task endpoints over one store root shared by the
// embedded store and a live queue (the serve wiring), plus a real
// in-memory upstream registry seeded with a multi-arch index.
func newTasksAPI(t *testing.T) (*http.ServeMux, *tasks.Queue, string) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	q, err := tasks.Open(root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	q.Register(tasks.TypeUnitImport, importer.NewRunner(st))

	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var idx v1.ImageIndex = empty.Index
	for _, p := range []v1.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64"}} {
		img, err := random.Image(256, 1)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add: img, Descriptor: v1.Descriptor{Platform: &p},
		})
	}
	ref := host.Host + "/library/redis:7.2"
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(parsed, idx); err != nil {
		t.Fatal(err)
	}

	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("lecteur", auth.RoleViewer, "pw-view", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("op", auth.RoleOperator, "pw-op", time.Now()); err != nil {
		t.Fatal(err)
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.DiscardHandler))
	api.RegisterTasks(a, q, st, 10*time.Second, nil)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, q, ref
}

// call performs a Basic-authenticated request with an optional JSON body.
func call(t *testing.T, mux *http.ServeMux, method, target, user, pass, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.SetBasicAuth(user, pass)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// apiWaitSettled polls the queue until the task settles.
func apiWaitSettled(t *testing.T, q *tasks.Queue, id string) *tasks.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		task, ok := q.Get(id)
		if ok && !task.Active() {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s never settled", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// inspectResp mirrors the endpoint payload the tests decode.
type inspectResp struct {
	Reference   string `json:"reference"`
	Repository  string `json:"repository"`
	Tag         string `json:"tag"`
	IndexDigest string `json:"index_digest"`
	MultiArch   bool   `json:"multi_arch"`
	Platforms   []struct {
		Name      string `json:"name"`
		Digest    string `json:"digest"`
		SizeBytes int64  `json:"size_bytes"`
		Status    string `json:"status"`
	} `json:"platforms"`
}

// TestInspectEndpoint covers GET /api/v1/import/inspect?ref= — the mirror
// of the screen's step 2 (FR-061): operator-gated, per-digest FR-026
// statuses, raw bytes only.
func TestInspectEndpoint(t *testing.T) {
	mux, _, ref := newTasksAPI(t)

	// Reads of the inspection are operator-gated like the screen.
	w := call(t, mux, http.MethodGet, "/api/v1/import/inspect?ref="+url.QueryEscape(ref), "lecteur", "pw-view", "")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("viewer inspect = %d, want taxonomized 403", w.Code)
	}

	// Missing reference: 400 problem document.
	w = call(t, mux, http.MethodGet, "/api/v1/import/inspect", "op", "pw-op", "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "TBY-REG-001") {
		t.Errorf("missing ref = %d, want TBY-REG-001", w.Code)
	}

	w = call(t, mux, http.MethodGet, "/api/v1/import/inspect?ref="+url.QueryEscape(ref), "op", "pw-op", "")
	if w.Code != http.StatusOK {
		t.Fatalf("inspect = %d: %s", w.Code, w.Body.String())
	}
	var resp inspectResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.MultiArch || resp.Tag != "7.2" || !strings.HasPrefix(resp.IndexDigest, "sha256:") {
		t.Errorf("report = %+v", resp)
	}
	if len(resp.Platforms) != 2 {
		t.Fatalf("platforms = %+v", resp.Platforms)
	}
	for _, p := range resp.Platforms {
		if p.Status != "new" || !strings.HasPrefix(p.Digest, "sha256:") || p.SizeBytes <= 0 {
			t.Errorf("platform %+v", p)
		}
	}
}

// TestImportCreateEndpoint covers POST /api/v1/import: operator-gated,
// 201 with the created task and its computed aggregates.
func TestImportCreateEndpoint(t *testing.T) {
	mux, q, ref := newTasksAPI(t)

	w := call(t, mux, http.MethodPost, "/api/v1/import", "lecteur", "pw-view",
		`{"reference":"`+ref+`","platforms":[{"name":"linux/amd64","digest":"sha256:00"}]}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", w.Code)
	}

	for _, body := range []string{"{not json", `{"reference":"","platforms":[]}`, `{"reference":"` + ref + `","platforms":[]}`} {
		w = call(t, mux, http.MethodPost, "/api/v1/import", "op", "pw-op", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, w.Code)
		}
	}

	w = call(t, mux, http.MethodPost, "/api/v1/import", "op", "pw-op",
		`{"reference":"`+ref+`","platforms":[{"name":"linux/amd64","digest":"sha256:0011"},{"name":"linux/arm64","digest":"sha256:2233"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task struct {
			ID         string `json:"id"`
			RunID      string `json:"run_id"`
			Status     string `json:"status"`
			Actor      string `json:"actor"`
			ItemsTotal int    `json:"items_total"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Task.ID, "tsk_") || resp.Task.RunID == "" ||
		resp.Task.Status != "pending" || resp.Task.Actor != "op" || resp.Task.ItemsTotal != 2 {
		t.Errorf("created task = %+v", resp.Task)
	}
	if _, ok := q.Get(resp.Task.ID); !ok {
		t.Error("created task absent from the queue")
	}
}

// TestImportCreateVendorFlag covers the FR-025 opt-in on POST
// /api/v1/import: vendorDependencies lands on the created task — and the
// default stays off.
func TestImportCreateVendorFlag(t *testing.T) {
	mux, q, ref := newTasksAPI(t)

	w := call(t, mux, http.MethodPost, "/api/v1/import", "op", "pw-op",
		`{"reference":"`+ref+`","vendorDependencies":true,"platforms":[{"name":"artifact","digest":"sha256:0011"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Task struct {
			ID                 string `json:"id"`
			VendorDependencies bool   `json:"vendor_dependencies"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Task.VendorDependencies {
		t.Errorf("task JSON misses the vendoring opt-in: %s", w.Body.String())
	}
	task, ok := q.Get(resp.Task.ID)
	if !ok || !task.VendorDependencies {
		t.Errorf("queued task = %+v, want VendorDependencies", task)
	}

	// Without the field: default off (TBY-CHT-001 keeps refusing).
	w = call(t, mux, http.MethodPost, "/api/v1/import", "op", "pw-op",
		`{"reference":"`+ref+`","platforms":[{"name":"artifact","digest":"sha256:2233"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("default create = %d: %s", w.Code, w.Body.String())
	}
	var dflt struct {
		Task struct {
			ID                 string `json:"id"`
			VendorDependencies bool   `json:"vendor_dependencies"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dflt); err != nil {
		t.Fatal(err)
	}
	dtask, ok := q.Get(dflt.Task.ID)
	if dflt.Task.VendorDependencies || !ok || dtask.VendorDependencies {
		t.Error("vendoring must stay an explicit opt-in")
	}
}

// TestTasksEndpoints covers the list with the screen's exact parameters
// (?q=&status=&type=), the detail, and the taxonomized 404.
func TestTasksEndpoints(t *testing.T) {
	mux, q, _ := newTasksAPI(t)
	t1, err := q.Create(tasks.TypeUnitImport, "docker.io/library/redis:7.2", "op",
		[]tasks.Item{{Name: "linux/amd64", Digest: "sha256:00"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Create(tasks.TypeUnitImport, "quay.io/argoproj/argocd:v2.11.3", "op",
		[]tasks.Item{{Name: "linux/arm64", Digest: "sha256:11"}}); err != nil {
		t.Fatal(err)
	}

	w := call(t, mux, http.MethodGet, "/api/v1/tasks", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Tasks []struct {
			ID          string `json:"id"`
			Reference   string `json:"reference"`
			Status      string `json:"status"`
			ItemsTotal  int    `json:"items_total"`
			ItemsDone   int    `json:"items_done"`
			ItemsFailed int    `json:"items_failed"`
			Created     string `json:"created"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Tasks) != 2 {
		t.Fatalf("tasks = %+v", resp.Tasks)
	}
	if _, err := time.Parse(time.RFC3339, resp.Tasks[0].Created); err != nil {
		t.Errorf("created %q is not RFC 3339: %v", resp.Tasks[0].Created, err)
	}

	// Same filters as the screen (FR-061).
	w = call(t, mux, http.MethodGet, "/api/v1/tasks?q=redis&status=pending&type=unit-import", "lecteur", "pw-view", "")
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Tasks) != 1 || resp.Tasks[0].ID != t1.ID {
		t.Errorf("filtered = %+v", resp.Tasks)
	}
	w = call(t, mux, http.MethodGet, "/api/v1/tasks?status=done", "lecteur", "pw-view", "")
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Tasks) != 0 {
		t.Errorf("status=done leaks pending tasks: %+v", resp.Tasks)
	}

	w = call(t, mux, http.MethodGet, "/api/v1/tasks/"+t1.ID, "lecteur", "pw-view", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), t1.ID) {
		t.Errorf("detail = %d", w.Code)
	}
	w = call(t, mux, http.MethodGet, "/api/v1/tasks/tsk_ghost", "lecteur", "pw-view", "")
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "TBY-TSK-001") {
		t.Errorf("unknown task = %d, want problem TBY-TSK-001", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("problem content-type = %q", ct)
	}
}

// TestTaskLogsEndpoint runs a real import end to end through the API and
// locks the two log contracts: raw download with its attachment name, and
// the incremental {chunk, next} cursor (UI-SPEC §8).
func TestTaskLogsEndpoint(t *testing.T) {
	mux, q, ref := newTasksAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	q.Start(ctx)

	// Pin the digests through the mirror's own inspection.
	w := call(t, mux, http.MethodGet, "/api/v1/import/inspect?ref="+url.QueryEscape(ref), "op", "pw-op", "")
	var rep inspectResp
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	platforms := make([]string, 0, len(rep.Platforms))
	for _, p := range rep.Platforms {
		platforms = append(platforms, `{"name":"`+p.Name+`","digest":"`+p.Digest+`"}`)
	}
	w = call(t, mux, http.MethodPost, "/api/v1/import", "op", "pw-op",
		`{"reference":"`+ref+`","platforms":[`+strings.Join(platforms, ",")+`]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Task struct {
			ID    string `json:"id"`
			RunID string `json:"run_id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	task := apiWaitSettled(t, q, created.Task.ID)
	if task.Status != tasks.StatusDone {
		t.Fatalf("import settled as %s: %+v", task.Status, task)
	}

	// Raw download: text/plain attachment named with task and run ids.
	w = call(t, mux, http.MethodGet, "/api/v1/tasks/"+task.ID+"/logs?format=raw", "lecteur", "pw-view", "")
	if w.Code != http.StatusOK {
		t.Fatalf("raw logs = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("raw content-type = %q", ct)
	}
	wantName := `attachment; filename="tobby-` + task.ID + `-` + task.RunID + `.log"`
	if cd := w.Header().Get("Content-Disposition"); cd != wantName {
		t.Errorf("content-disposition = %q, want %q", cd, wantName)
	}
	if !strings.Contains(w.Body.String(), task.ID) {
		t.Error("raw log misses the task correlation")
	}
	rawLen := w.Body.Len()

	// Incremental cursor: full read then an empty tail at the cursor.
	w = call(t, mux, http.MethodGet, "/api/v1/tasks/"+task.ID+"/logs?after=0", "lecteur", "pw-view", "")
	var chunk struct {
		Chunk string `json:"chunk"`
		Next  int64  `json:"next"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &chunk); err != nil {
		t.Fatal(err)
	}
	if len(chunk.Chunk) != rawLen || chunk.Next != int64(rawLen) {
		t.Errorf("chunk len=%d next=%d, want raw len %d", len(chunk.Chunk), chunk.Next, rawLen)
	}
	w = call(t, mux, http.MethodGet,
		"/api/v1/tasks/"+task.ID+"/logs?after="+strconv.FormatInt(chunk.Next, 10),
		"lecteur", "pw-view", "")
	if err := json.Unmarshal(w.Body.Bytes(), &chunk); err != nil {
		t.Fatal(err)
	}
	if chunk.Chunk != "" {
		t.Errorf("tail chunk = %q, want empty", chunk.Chunk)
	}
}
