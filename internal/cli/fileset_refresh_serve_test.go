// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// B-058, proved on the instance the product actually starts.
//
// /files/ used to resolve and extract its FileSets at startup and after a
// synchronization task, and at no other moment. Every other way content
// reaches the store — an import, packing on this host, a push through the
// embedded registry — left the FileSet unserved until a restart. And on
// the destination side of a physical transfer, the startup extraction
// wrote into the medium's store from blobs nobody had verified yet, then
// kept serving that cache whatever the verification later found (FR-054).
//
// Each test below asks /files/ for real content with a real GET, because
// the crucible step that should have caught this only checked the
// absence of write methods.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// refreshFile is the one file every fixture FileSet of this file carries.
const refreshFile = "etc/tobby/marker.conf"

// scratchFileSet packs a FileSet carrying refreshFile=content into a
// throwaway store and returns that store and the packed result. It is the
// "somewhere else" content comes from: an upstream registry, or a client
// pushing to the instance.
func scratchFileSet(t *testing.T, st *store.Store, version, content string) *fileserve.PackResult {
	t.Helper()
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, filepath.Dir(refreshFile)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(refreshFile)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := fileserve.NewPacker(st, "", slog.New(slog.DiscardHandler), fileserve.WithPackRoots([]string{src})).
		Pack(context.Background(), fileserve.PackRequest{Source: src, Name: "upstream", Version: version})
	if err != nil {
		t.Fatalf("packing the fixture FileSet: %v", err)
	}
	return res
}

// call performs one authenticated request with an optional JSON body.
func call(t *testing.T, method, u string, body any) (status int, raw []byte) {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, u, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth("admin", "test-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	raw, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// servedNow reports whether the inventory says name is served — read
// BEFORE any GET, so the lazy resolution of a miss cannot be what made it
// true.
func servedNow(t *testing.T, base, fileSet string) bool {
	t.Helper()
	status, raw := call(t, http.MethodGet, base+"/api/v1/filesets", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/filesets = %d: %s", status, raw)
	}
	var list struct {
		FileSets []struct {
			Name   string `json:"name"`
			Served bool   `json:"served"`
		} `json:"filesets"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	for _, f := range list.FileSets {
		if f.Name == fileSet {
			return f.Served
		}
	}
	return false
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// copyImage copies repo:tag from one registry to another with a standard
// client over the real protocol.
func copyImage(t *testing.T, from, to string, auth authn.Authenticator) {
	t.Helper()
	src, err := name.ParseReference(from, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := name.ParseReference(to, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	img, err := remote.Image(src)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}
	if err := remote.Write(dst, img, remote.WithAuth(auth)); err != nil {
		t.Fatalf("pushing %s: %v", to, err)
	}
}

var adminAuth = &authn.Basic{Username: "admin", Password: "test-password"}

// TestServeServesAFileSetPackedThroughTheAPI: FR-048 packing through the
// remote surface lands a declared FileSet in the store; it must be served
// the moment the call returns, not after a restart (B-058). The UI form
// goes through the same fileserve.Surface.Pack.
//
// Proved fallible: on the code before B-058 the GET answers 404 and the
// inventory reports the FileSet unserved.
func TestServeServesAFileSetPackedThroughTheAPI(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, filepath.Dir(refreshFile)), 0o750); err != nil {
		t.Fatal(err)
	}
	const content = "packed on this host\n"
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(refreshFile)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Files.PackRoots = []string{src}
	cfg.Files.FileSets = []config.FileSetServe{{
		Name: "packed", Ref: fileserve.PackReference("packed"), Version: "1.0.0", Anonymous: true,
	}}
	base := startServe(t, &cfg)

	status, raw := call(t, http.MethodPost, base+"/api/v1/filesets/pack",
		map[string]string{"source": src, "name": "packed", "version": "1.0.0"})
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/filesets/pack = %d: %s", status, raw)
	}
	if !servedNow(t, base, "packed") {
		t.Error("the inventory does not report the packed FileSet served once packing returned (B-058)")
	}
	status, body := fetch(t, base+fileserve.RoutePrefix+"packed/"+refreshFile)
	if status != http.StatusOK || body != content {
		t.Errorf("GET /files/packed/… after packing = %d %q, want 200 %q (B-058)", status, body, content)
	}
}

// TestServeServesAFileSetPushedThroughTheRegistry: a declared FileSet
// pushed through /v2/ by a standard client is served without a restart —
// on the first GET, and again when a newer version supersedes the served
// one (B-058).
//
// Proved fallible: on the code before B-058 the first GET answers 404.
// With only the lazy resolution of a miss (the registry write hook
// removed), the upgrade sub-check keeps serving the old content.
func TestServeServesAFileSetPushedThroughTheRegistry(t *testing.T) {
	scratch := openFileSetStore(t)
	v1 := scratchFileSet(t, scratch, "1.0.0", "first version\n")
	scratchFileSet(t, scratch, "1.1.0", "second version\n")
	upstream := httptest.NewServer(scratch.APIHandler())
	defer upstream.Close()
	up := upstream.Listener.Addr().String()

	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	cfg.Files.FileSets = []config.FileSetServe{{Name: "pushed", Ref: fileSetRef, Anonymous: true}}
	base := startServe(t, &cfg)
	host := strings.TrimPrefix(base, "http://")

	copyImage(t, up+"/"+v1.Repository+":1.0.0", host+"/"+fileSetRepo+":1.0.0", adminAuth)
	status, body := fetch(t, base+fileserve.RoutePrefix+"pushed/"+refreshFile)
	if status != http.StatusOK || body != "first version\n" {
		t.Fatalf("GET /files/pushed/… after a /v2/ push = %d %q, want 200 %q (B-058)", status, body, "first version\n")
	}

	copyImage(t, up+"/"+v1.Repository+":1.1.0", host+"/"+fileSetRepo+":1.1.0", adminAuth)
	if !eventually(t, 10*time.Second, func() bool {
		_, body := fetch(t, base+fileserve.RoutePrefix+"pushed/"+refreshFile)
		return body == "second version\n"
	}) {
		_, body := fetch(t, base+fileserve.RoutePrefix+"pushed/"+refreshFile)
		t.Errorf("a newer version pushed through /v2/ is never served: still %q (B-058)", body)
	}
}

// TestServeServesAFileSetAfterAUnitImport: a declared FileSet arriving by
// a one-off import (FR-023) is served once the task settles (B-058).
//
// Proved fallible: on the code before B-058 the inventory reports the
// FileSet unserved after the task, and the GET answers 404.
func TestServeServesAFileSetAfterAUnitImport(t *testing.T) {
	scratch := openFileSetStore(t)
	res := scratchFileSet(t, scratch, "1.0.0", "imported once\n")
	upstream := httptest.NewServer(scratch.APIHandler())
	defer upstream.Close()
	up := upstream.Listener.Addr().String()
	ref := up + "/" + res.Repository

	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	cfg.Registries.Insecure = []string{up}
	cfg.Files.FileSets = []config.FileSetServe{{Name: "imported", Ref: ref, Version: "1.0.0", Anonymous: true}}
	base := startServe(t, &cfg)

	status, raw := call(t, http.MethodGet, base+"/api/v1/import/inspect?ref="+url.QueryEscape(ref+":1.0.0"), nil)
	if status != http.StatusOK {
		t.Fatalf("inspect = %d: %s", status, raw)
	}
	var insp struct {
		Platforms []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(raw, &insp); err != nil || len(insp.Platforms) == 0 {
		t.Fatalf("inspect answered no platform: %v %s", err, raw)
	}
	status, raw = call(t, http.MethodPost, base+"/api/v1/import", map[string]any{
		"reference": ref + ":1.0.0", "platforms": insp.Platforms,
	})
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/import = %d: %s", status, raw)
	}
	var created struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	waitTask(t, base, created.Task.ID)

	if !servedNow(t, base, "imported") {
		t.Error("the inventory does not report the imported FileSet served once the import settled (B-058)")
	}
	status, body := fetch(t, base+fileserve.RoutePrefix+"imported/"+refreshFile)
	if status != http.StatusOK || body != "imported once\n" {
		t.Errorf("GET /files/imported/… after a unit import = %d %q, want 200 (B-058)", status, body)
	}
}

// waitTask waits for a task to settle successfully.
func waitTask(t *testing.T, base, id string) {
	t.Helper()
	var last string
	if !eventually(t, 30*time.Second, func() bool {
		_, raw := call(t, http.MethodGet, base+"/api/v1/tasks/"+id, nil)
		var got struct {
			Task struct {
				Status string `json:"status"`
			} `json:"task"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &got)
		last = got.Task.Status + got.Status
		return last == "done" || last == "failed" || last == "cancelled"
	}) {
		t.Fatalf("task %s never settled (last status %q)", id, last)
	}
	if last != "done" {
		_, raw := call(t, http.MethodGet, base+"/api/v1/tasks/"+id, nil)
		t.Fatalf("task %s settled %s: %s", id, last, raw)
	}
}

// waitServing waits until the media gate reports the content surfaces
// open.
func waitServing(t *testing.T, base string) {
	t.Helper()
	if !eventually(t, 30*time.Second, func() bool {
		_, raw := call(t, http.MethodGet, base+"/api/v1/media/verification", nil)
		var st mediagate.Status
		return json.Unmarshal(raw, &st) == nil && st.Serving && !st.Running
	}) {
		_, raw := call(t, http.MethodGet, base+"/api/v1/media/verification", nil)
		t.Fatalf("the medium never cleared: %s", raw)
	}
}

// TestServeDefersFileSetExtractionBehindTheMediaGate is the FR-054 half
// of B-058: on a destination instance holding a transported medium,
// nothing may be written under the medium's fileset cache before a
// verification has cleared it — and once it has, the FileSet is
// extracted and served without a restart.
//
// Proved fallible: on the code before B-058 meta/fileserve already holds
// an extraction made at startup, from blobs no one had verified.
func TestServeDefersFileSetExtractionBehindTheMediaGate(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	seedMedium(t, &cfg)
	base := startServe(t, &cfg)
	cache := filepath.Join(cfg.Storage.Root, "meta", "fileserve")

	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("the fileset cache exists before the medium was verified (stat: %v): "+
			"the startup extraction wrote from unverified blobs (FR-054, B-058)", err)
	}
	if status, _ := fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath); status != http.StatusForbidden {
		t.Errorf("GET /files/… on an unverified medium = %d, want 403", status)
	}
	// A refused request must not be what writes the cache either.
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("a refused /files/ request wrote the fileset cache (stat: %v)", err)
	}

	if status, raw := call(t, http.MethodPost, base+"/api/v1/media/verify", map[string]any{}); status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("POST /api/v1/media/verify = %d: %s", status, raw)
	}
	waitServing(t, base)

	// The gate opening is itself the trigger: the FileSet is extracted
	// without waiting for a client to ask for it.
	if !eventually(t, 10*time.Second, func() bool { return servedNow(t, base, mediumFileSet) }) {
		t.Error("the FileSet is not extracted when the medium clears, only on demand (B-058)")
	}
	status, body := fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
	if status != http.StatusOK || body != mediumFileBytes {
		t.Errorf("GET /files/… once the medium cleared = %d %q, want 200 with the verified content (B-058)", status, body)
	}
}

// TestServeNeverServesACarriedFilesetCache: the medium's store may carry
// an extracted cache — the source side served its FileSets from it — and
// that tree is not what the verification checks: it hashes blobs, and a
// change under meta/ is a finding, never a block. Serving the carried
// tree would serve bytes no verification vouched for. The destination
// re-extracts from the verified blobs instead (FR-054, B-058).
//
// Proved fallible: on the code before B-058 the planted tree carries a
// completion marker under the served digest, is never re-extracted, and
// the GET returns the planted bytes.
func TestServeNeverServesACarriedFilesetCache(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	_, digest := seedMedium(t, &cfg)

	// What the source side left behind, altered after the manifest was
	// written: a complete-looking extraction under the very digest the
	// destination will resolve.
	planted := filepath.Join(cfg.Storage.Root, "meta", "fileserve", mediumFileSet,
		strings.ReplaceAll(digest, ":", "_"))
	if err := os.MkdirAll(filepath.Join(planted, "rootfs", filepath.Dir(mediumFilePath)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, "rootfs", filepath.FromSlash(mediumFilePath)),
		[]byte("planted, never verified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, ".complete"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	base := startServe(t, &cfg)
	if status, raw := call(t, http.MethodPost, base+"/api/v1/media/verify", map[string]any{}); status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("POST /api/v1/media/verify = %d: %s", status, raw)
	}
	waitServing(t, base)

	status, body := fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
	if status != http.StatusOK || body != mediumFileBytes {
		t.Errorf("GET /files/… = %d %q, want 200 with the content of the verified blobs, not the carried cache (FR-054, B-058)", status, body)
	}
}

// TestServeServesAMediumFileSetAfterAMediaImport: the destination journey
// of FR-052 — import the medium as a task — opens the file surface on
// its verdict, and the FileSet is served at once (B-058).
//
// Proved fallible: on the code before B-058 the fileset cache was written
// at startup, before the import verified anything.
func TestServeServesAMediumFileSetAfterAMediaImport(t *testing.T) {
	dest := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer dest.Close()
	destHost := dest.Listener.Addr().String()

	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	seedMedium(t, &cfg)
	cfg.Destination.Registry = destHost
	cfg.Registries.Insecure = []string{destHost}
	base := startServe(t, &cfg)

	if _, err := os.Stat(filepath.Join(cfg.Storage.Root, "meta", "fileserve")); !os.IsNotExist(err) {
		t.Errorf("the fileset cache exists before the medium was imported (stat: %v) (FR-054, B-058)", err)
	}
	status, raw := call(t, http.MethodPost, base+"/api/v1/media/import", map[string]any{})
	if status != http.StatusCreated && status != http.StatusAccepted {
		t.Fatalf("POST /api/v1/media/import = %d: %s", status, raw)
	}
	var created struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.Task.ID == "" {
		t.Fatalf("no task in %s (%v)", raw, err)
	}
	waitTask(t, base, created.Task.ID)

	if !servedNow(t, base, mediumFileSet) {
		t.Error("the inventory does not report the medium's FileSet served once the import settled (B-058)")
	}
	status, body := fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
	if status != http.StatusOK || body != mediumFileBytes {
		t.Errorf("GET /files/… after the media import = %d %q, want 200 (B-058)", status, body)
	}
}

// TestServeStopsServingAFileSetAfterAStoreReset: a reset (FR-046) empties
// the store of its content, and what /files/ serves is that content. The
// extracted cache survived the reset — it lives in meta/, which the reset
// keeps — so a FileSet whose blobs were gone kept being served (B-059).
//
// Proved fallible: on the code before B-059 the GET after the reset still
// answers 200 with the packed bytes, and the extracted tree is on disk.
func TestServeStopsServingAFileSetAfterAStoreReset(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, filepath.Dir(refreshFile)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(refreshFile)), []byte("before the reset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Files.PackRoots = []string{src}
	cfg.Files.FileSets = []config.FileSetServe{{
		Name: "packed", Ref: fileserve.PackReference("packed"), Version: "1.0.0", Anonymous: true,
	}}
	base := startServe(t, &cfg)
	if status, raw := call(t, http.MethodPost, base+"/api/v1/filesets/pack",
		map[string]string{"source": src, "name": "packed", "version": "1.0.0"}); status != http.StatusCreated {
		t.Fatalf("POST /api/v1/filesets/pack = %d: %s", status, raw)
	}
	if status, _ := fetch(t, base+fileserve.RoutePrefix+"packed/"+refreshFile); status != http.StatusOK {
		t.Fatalf("the packed FileSet is not served before the reset (%d)", status)
	}

	if status, raw := call(t, http.MethodPost, base+"/api/v1/store/reset",
		map[string]string{"confirmation": "RESET"}); status/100 != 2 {
		t.Fatalf("POST /api/v1/store/reset = %d: %s", status, raw)
	}
	if servedNow(t, base, "packed") {
		t.Error("the inventory still reports the FileSet served after the store reset (B-059)")
	}
	status, body := fetch(t, base+fileserve.RoutePrefix+"packed/"+refreshFile)
	if status != http.StatusNotFound {
		t.Errorf("GET /files/packed/… after the reset = %d %q, want 404 (B-059)", status, body)
	}
	cache := filepath.Join(cfg.Storage.Root, "meta", "fileserve")
	if entries, err := os.ReadDir(cache); err == nil && len(entries) > 0 {
		t.Errorf("the fileset cache still holds %d entries after the reset (B-059)", len(entries))
	}
}
