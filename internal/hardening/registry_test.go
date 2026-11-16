// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package hardening

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// syncBuffer is a log sink safe for the registry's concurrent handlers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// registryUnder serves the store's /v2/ behind the guard, as an
// administrator — the role the switch must refuse too.
func registryUnder(t *testing.T, st *store.Store, sw Switches, logs *syncBuffer) string {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	guarded := Registry(sw, st, logger, st.APIHandler())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithIdentity(r.Context(), auth.Identity{Name: "root-admin", Role: auth.RoleAdmin})
		guarded.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// TestRegistryPushRefusedForAdministrators locks hardening.disableRegistryPush
// (FR-098): a push by an administrator answers the OCI DENIED error
// carrying TBY-POL-006 and the setting, lands nothing, and is audited as
// hardening.refused; pulls of what the store holds are untouched.
func TestRegistryPushRefusedForAdministrators(t *testing.T) {
	st := openStore(t)
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Seeded while the registry is writable...
	open := registryUnder(t, st, Switches{}, &syncBuffer{})
	seeded, err := name.ParseReference(open + "/zone/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(seeded, img); err != nil {
		t.Fatalf("push with the switch off: %v", err)
	}

	// ...then read-only.
	logs := &syncBuffer{}
	ro := registryUnder(t, st, Switches{DisableRegistryPush: true}, logs)
	ref, err := name.ParseReference(ro + "/zone/other:1.0")
	if err != nil {
		t.Fatal(err)
	}
	err = remote.Write(ref, img)
	if err == nil {
		t.Fatal("an administrator pushed through a read-only registry")
	}
	for _, want := range []string{"DENIED", "TBY-POL-006"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("push error %q misses %s", err, want)
		}
	}
	if _, ok := st.ResolveTag(context.Background(), "zone/other", "1.0"); ok {
		t.Error("a refused push landed a tag")
	}
	audit := logs.String()
	for _, want := range []string{`"action":"hardening.refused"`, `"outcome":"denied"`, `"actor":"root-admin"`,
		`"setting":"hardening.disableRegistryPush"`, `"target":"zone/other"`} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit misses %s:\n%s", want, audit)
		}
	}

	// Every write route, not only the first a client happens to use.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v2/zone/x/blobs/uploads/"},
		{http.MethodPost, "/v2/zone/x/blobs/uploads/?mount=sha256:" + strings.Repeat("a", 64) + "&from=zone/app"},
		{http.MethodPatch, "/v2/zone/x/blobs/uploads/some-session"},
		{http.MethodPut, "/v2/zone/x/blobs/uploads/some-session?digest=sha256:" + strings.Repeat("b", 64)},
		{http.MethodPut, "/v2/zone/app/manifests/2.0"},
		{http.MethodDelete, "/v2/zone/app/manifests/1.0"},
		{http.MethodDelete, "/v2/zone/app/blobs/sha256:" + strings.Repeat("c", 64)},
	} {
		req, _ := http.NewRequest(c.method, "http://"+ro+c.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Errors []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || len(body.Errors) != 1 || body.Errors[0].Code != "DENIED" ||
			!strings.Contains(body.Errors[0].Message, "TBY-POL-006") {
			t.Errorf("%s %s = %d %+v, want 403 DENIED carrying TBY-POL-006", c.method, c.path, resp.StatusCode, body)
		}
	}

	// Pulls are unaffected.
	pull, _ := name.ParseReference(ro + "/zone/app:1.0")
	got, err := remote.Image(pull)
	if err != nil {
		t.Fatalf("pull through a read-only registry: %v", err)
	}
	want, _ := img.Digest()
	if d, _ := got.Digest(); d != want {
		t.Errorf("pulled %s, want %s", d, want)
	}
}

// TestPushRecordsSeedAndLogsProvenance: with the registry writable, a
// pushed repository becomes class seed and the push record carries it.
func TestPushRecordsSeedAndLogsProvenance(t *testing.T) {
	st := openStore(t)
	logs := &syncBuffer{}
	host := registryUnder(t, st, Switches{}, logs)
	img, _ := random.Image(128, 1)
	ref, _ := name.ParseReference(host + "/zone/seed:1.0")
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	if p, ok := st.ProvenanceOf("zone/seed"); !ok || p.Class != store.ProvenanceSeed {
		t.Errorf("pushed repository recorded %+v (%v), want seed", p, ok)
	}
	if !strings.Contains(logs.String(), `"msg":"manifest pushed"`) || !strings.Contains(logs.String(), `"provenance":"seed"`) {
		t.Errorf("push record misses the provenance field:\n%s", logs.String())
	}
}

func TestManifestTarget(t *testing.T) {
	for path, want := range map[string][2]string{
		"/v2/a/b/manifests/1.0":              {"a/b", "1.0"},
		"/v2/a/manifests/b/manifests/sha256": {"a/manifests/b", "sha256"},
		"/v2/a/b/blobs/uploads/":             {"", ""},
		"/v2/a/b/manifests/":                 {"", ""},
		"/v2/manifests/x":                    {"", ""},
	} {
		repo, ref := manifestTarget(path)
		if repo != want[0] || ref != want[1] {
			t.Errorf("manifestTarget(%q) = %q %q, want %q", path, repo, ref, want)
		}
	}
}
