// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The serving half of FR-054, locked.
//
// The central test below is TestUnverifiedMediumServesNeitherRegistryNorFiles,
// and it is written against the REAL handlers an instance mounts — the
// embedded registry's OCI Distribution surface and the FR-047 file
// surface over a genuinely packed FileSet — because the property under
// test is "no byte of this medium leaves the instance", and a stub that
// answers 200 with the word "content" proves a weaker thing.
//
// Fallibility, verified by hand before the guard was written: with Guard
// returning its argument unchanged, both halves serve — the registry
// answers 200 with the manifest and /files/ answers 200 with the file's
// bytes — and the test fails on both. The failure output is recorded in
// the milestone report.

package mediagate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The fixture's coordinates. The FileSet is packed from a real directory
// so the served path and the served bytes are both known exactly: a test
// that cannot say WHAT would have been served cannot prove it was not.
const (
	testZone    = "zone-bravo"
	fileSetName = "site-config"
	servedFile  = "etc/tobby/marker.conf"
	servedBytes = "this content must never leave an unverified medium\n"
	fileSetTag  = "1.0.0"
)

// medium is a store that is also a transport medium: real content, a real
// media manifest, and the two content surfaces mounted exactly as
// internal/cli/serve.go mounts them.
type medium struct {
	root   string
	store  *store.Store
	pack   *fileserve.PackResult
	files  *fileserve.Server
	logger *slog.Logger
}

// newMedium builds the fixture: a directory packed into the store as a
// FileSet (FR-048 — the one import path that yields predictable served
// paths), then inventoried as a medium addressed to testZone (FR-054).
func newMedium(t *testing.T) *medium {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)

	root := t.TempDir()
	st, err := store.Open(ctx, root, logger)
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, filepath.Dir(servedFile)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(servedFile)), []byte(servedBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	packer := fileserve.NewPacker(st, "", logger, fileserve.WithPackRoots([]string{src}))
	res, err := packer.Pack(ctx, fileserve.PackRequest{Source: src, Name: fileSetName, Version: fileSetTag})
	if err != nil {
		t.Fatalf("packing the fixture FileSet: %v", err)
	}

	files := fileserve.NewServer(storeBlobs{st}, t.TempDir(), fileserve.Limits{}, logger)
	// A served FileSet keeps an os.Root open on its extracted tree for the
	// server's whole life, and on Windows an open handle is what stops a
	// directory from being removed (NFR-018). Registering the close after
	// the t.TempDir() call above makes LIFO cleanup release the handle
	// before the temporary directory is torn down.
	t.Cleanup(func() { _ = files.Close() })
	if err := files.Sync(ctx, []fileserve.FileSet{{
		Name: fileSetName, Repo: res.Repository, ManifestDigest: res.Digest,
	}}); err != nil {
		t.Fatalf("enabling the fixture FileSet: %v", err)
	}

	if _, err := media.Write(ctx, st, media.WriteOptions{
		Zone: testZone, RunID: "run_test", ResolvedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("writing the media manifest: %v", err)
	}
	return &medium{root: root, store: st, pack: res, files: files, logger: logger}
}

// storeBlobs adapts the store to the fileserve read surface, exactly as
// internal/cli/serve.go does.
type storeBlobs struct{ st *store.Store }

func (b storeBlobs) Manifest(ctx context.Context, repo, dgst string) ([]byte, error) {
	payload, _, _, err := b.st.RawManifest(ctx, repo, dgst)
	return payload, err
}

func (b storeBlobs) Blob(ctx context.Context, repo, dgst string) (io.ReadCloser, error) {
	return b.st.BlobReader(ctx, repo, dgst)
}

// mount wires the two content surfaces behind the gate, the way the
// instance does.
func (m *medium) mount(g *Gate) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/v2/", g.Guard("/v2/", RegistryRefusal, m.store.APIHandler()))
	mux.Handle(fileserve.RoutePrefix, g.Guard(fileserve.RoutePrefix, FilesRefusal, m.files.Handler()))
	return httptest.NewServer(mux)
}

// registryURL is the manifest of the packed FileSet, by digest: real
// content, addressable, and served by the real registry handler.
func (m *medium) registryURL() string {
	return "/v2/" + m.pack.Repository + "/manifests/" + m.pack.Digest
}

func (m *medium) filesURL() string { return fileserve.RoutePrefix + fileSetName + "/" + servedFile }

// ociAccept is what a standard registry client negotiates. Without it the
// distribution handler answers 404 on an OCI manifest, which would make
// the "and then it serves" half of the guard test pass for the wrong
// reason.
const ociAccept = "application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.oci.image.index.v1+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// get performs one request and returns status and body.
func get(t *testing.T, srv *httptest.Server, path string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", ociAccept)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return resp.StatusCode, string(raw)
}

// clearedReport is a verdict that opens the gate.
func clearedReport() *media.Report {
	return &media.Report{Verdict: media.VerdictPushable}
}

// TestUnverifiedMediumServesNeitherRegistryNorFiles is the FR-054 serving
// guard: "destination-side verification SHALL precede any push, any
// SERVING, and any local write".
//
// Both surfaces are asked for content that genuinely exists in the store
// and that both handlers would genuinely serve. The test fails if either
// one hands out a single byte of it before a verification has cleared the
// medium — which is exactly what this instance did before the gate
// existed.
func TestUnverifiedMediumServesNeitherRegistryNorFiles(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	if !g.Guarded() {
		t.Fatal("the gate does not consider a destination instance holding a media manifest to be guarded")
	}
	srv := m.mount(g)
	defer srv.Close()

	status, body := get(t, srv, m.registryURL())
	if status == http.StatusOK {
		t.Errorf("GET %s served the manifest off an unverified medium (FR-054)", m.registryURL())
	}
	if status != http.StatusForbidden {
		t.Errorf("GET %s = %d, want 403", m.registryURL(), status)
	}
	if !strings.Contains(body, string(taxonomy.CodeMediaUnverified)) {
		t.Errorf("the registry refusal does not name %s; body: %s", taxonomy.CodeMediaUnverified, body)
	}

	status, body = get(t, srv, m.filesURL())
	if strings.Contains(body, servedBytes) {
		t.Errorf("GET %s served the file's content off an unverified medium (FR-054)", m.filesURL())
	}
	if status != http.StatusForbidden {
		t.Errorf("GET %s = %d, want 403", m.filesURL(), status)
	}
	if !strings.Contains(body, string(taxonomy.CodeMediaUnverified)) {
		t.Errorf("the file refusal does not name %s; body: %s", taxonomy.CodeMediaUnverified, body)
	}

	// And the gate opens on a verdict, so the guard is a gate and not a
	// wall: an instance that could never serve would pass every assertion
	// above while being useless.
	g.Observe(clearedReport())
	if status, _ := get(t, srv, m.registryURL()); status != http.StatusOK {
		t.Errorf("GET %s = %d after verification cleared the medium, want 200", m.registryURL(), status)
	}
	status, body = get(t, srv, m.filesURL())
	if status != http.StatusOK || !strings.Contains(body, servedBytes) {
		t.Errorf("GET %s = %d after verification cleared the medium, want 200 with the file's content", m.filesURL(), status)
	}
}

// TestRefusalIsReadableByItsClients: neither surface may answer a 404 or
// a silent 503. An operator who plugs in a disk and gets a blank page
// calls support instead of pressing Verify, so the refusal carries what
// happened, why, and what to do — in the shape each surface's clients
// understand.
func TestRefusalIsReadableByItsClients(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	srv := m.mount(g)
	defer srv.Close()

	// /v2/: the OCI Distribution error envelope, which docker, helm, oras
	// and skopeo all print.
	_, body := get(t, srv, m.registryURL())
	var envelope struct {
		Errors []struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Detail  map[string]string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("the registry refusal is not an OCI error document: %v (%s)", err, body)
	}
	if len(envelope.Errors) != 1 || envelope.Errors[0].Code != "DENIED" {
		t.Fatalf("registry refusal = %+v, want one DENIED entry", envelope.Errors)
	}
	if !strings.Contains(envelope.Errors[0].Message, string(taxonomy.CodeMediaUnverified)) {
		t.Errorf("the message does not carry the code: %q", envelope.Errors[0].Message)
	}
	for _, part := range []string{"cause", "action"} {
		if strings.TrimSpace(envelope.Errors[0].Detail[part]) == "" {
			t.Errorf("the registry refusal carries no %s", part)
		}
	}
	if !strings.Contains(envelope.Errors[0].Detail["action"], Screen) {
		t.Errorf("the action does not say where to verify (%s): %q", Screen, envelope.Errors[0].Detail["action"])
	}

	// /files/: plain text, because its clients are apt, dnf and curl.
	_, body = get(t, srv, m.filesURL())
	for _, want := range []string{string(taxonomy.CodeMediaUnverified), "cause:", "action:", Screen} {
		if !strings.Contains(body, want) {
			t.Errorf("the file refusal does not carry %q; body: %s", want, body)
		}
	}
}

// TestRefusalSpeaksTheClientsLanguage: FR-063 stops at no surface. A
// registry client sending Accept-Language gets the refusal in its own
// language, from the same catalogs the screen renders.
func TestRefusalSpeaksTheClientsLanguage(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	srv := m.mount(g)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+m.filesURL(), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	want := taxonomy.Localize("fr", taxonomy.New(taxonomy.CodeMediaUnverified,
		taxonomy.Params{"surface": fileserve.RoutePrefix, "media": "x", "screen": Screen})).What
	if !strings.Contains(string(body), want) {
		t.Errorf("the refusal is not in French; body: %s", body)
	}
}

// TestOnlyAWholeMediumOpensTheGate: R-19 made the PUSH decision per recipe, so a partially damaged medium
// still delivers its intact recipes. Serving is NOT that decision: /v2/
// and /files/ hand out blobs, and a blob a blocked recipe reaches is
// exactly the byte range that failed. The gate therefore opens on
// "pushable" and on nothing else, and it re-closes if a later
// verification says the medium stopped being whole.
func TestOnlyAWholeMediumOpensTheGate(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	srv := m.mount(g)
	defer srv.Close()

	for _, verdict := range []media.Verdict{media.VerdictPartial, media.VerdictBlocked} {
		g.Observe(&media.Report{Verdict: verdict})
		if g.Serving() {
			t.Errorf("the gate opened on a %q verdict", verdict)
		}
		status, body := get(t, srv, m.filesURL())
		if status != http.StatusForbidden || strings.Contains(body, servedBytes) {
			t.Errorf("a %q medium served %s (%d)", verdict, m.filesURL(), status)
		}
		if !strings.Contains(body, string(taxonomy.CodeMediaNotCleared)) {
			t.Errorf("a %q medium does not answer %s; body: %s", verdict, taxonomy.CodeMediaNotCleared, body)
		}
	}

	g.Observe(clearedReport())
	if !g.Serving() {
		t.Fatal("the gate did not open on a whole medium")
	}
	// Re-closing matters: a re-verification that finds the medium damaged
	// must take the surfaces back down, not leave them open on the
	// strength of an older answer.
	g.Observe(&media.Report{Verdict: media.VerdictBlocked})
	if g.Serving() {
		t.Error("the gate stayed open after a later verification blocked the medium")
	}
}

// TestOnlyADestinationHoldingAMediumIsGuarded: two instances must pay nothing for this feature and must not be
// withheld for a second: a passthrough or destination instance whose
// store is not a medium, and — the one that would be a real regression —
// a mirror instance on the SOURCE side, whose store carries a media
// manifest because it WROTE it. The requirement distinguishes the sides
// by the zone identity: a source-side instance reads its zone from the
// Retriever it resolves and configures none.
func TestOnlyADestinationHoldingAMediumIsGuarded(t *testing.T) {
	m := newMedium(t)

	source := Open(context.Background(), m.root, "", m.logger)
	if source.Guarded() || !source.Serving() {
		t.Error("a source-side mirror instance is withheld by its own medium manifest")
	}
	if source.ReadyDetail() != "" {
		t.Error("a source-side instance advertises a readiness caveat it does not have")
	}
	srv := m.mount(source)
	defer srv.Close()
	if status, body := get(t, srv, m.filesURL()); status != http.StatusOK || !strings.Contains(body, servedBytes) {
		t.Errorf("a source-side instance does not serve its own store: %s = %d", m.filesURL(), status)
	}

	plain := Open(context.Background(), t.TempDir(), testZone, m.logger)
	if plain.Guarded() || !plain.Serving() {
		t.Error("a destination instance whose store is not a medium is withheld")
	}
}

// TestAGuardedInstanceStaysReadyAndSaysWhy (ADR-0012, FR-092): the
// instance is alive and ready — its storage is writable, its
// configuration is valid, its interface is serving — and an operator
// needs all of that to press Verify. What is not ready is the content of
// this medium, and that is what the readiness note says, naming the
// medium and where to act on it.
func TestAGuardedInstanceStaysReadyAndSaysWhy(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)

	detail := g.ReadyDetail()
	for _, want := range []string{"/v2/", fileserve.RoutePrefix, Screen, "FR-054"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the readiness note does not mention %q: %q", want, detail)
		}
	}
	g.Observe(clearedReport())
	if got := g.ReadyDetail(); got != "" {
		t.Errorf("the readiness note survives verification: %q", got)
	}
}

// TestOneVerificationAtATime: re-hashing a disk twice at once halves both
// runs and answers nothing new, so the second caller is told to read the
// verdict of the one already in flight rather than being silently queued
// behind it.
func TestOneVerificationAtATime(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)

	release := make(chan struct{})
	started := make(chan struct{})
	g.SetVerify(func(_ context.Context, _ Options, progress func(media.Progress)) (*media.Report, error) {
		progress(media.Progress{Stage: media.StageRecipes, Bytes: 1, TotalBytes: 2})
		close(started)
		<-release
		return clearedReport(), nil
	})

	if e := g.Start(Options{}); e != nil {
		t.Fatalf("the first verification was refused: %v", e)
	}
	<-started
	e := g.Start(Options{})
	if e == nil {
		t.Fatal("a second verification was accepted while the first was still walking the medium")
	}
	if e.Code() != taxonomy.CodeMediaVerificationRunning {
		t.Errorf("the refusal is %s, want %s", e.Code(), taxonomy.CodeMediaVerificationRunning)
	}

	s := g.Status()
	if !s.Running || s.Progress == nil || s.Progress.Stage != media.StageRecipes {
		t.Errorf("the status does not carry the run in progress: %+v", s)
	}
	if pct := s.Percent(); pct != 50 {
		t.Errorf("progress = %d%%, want 50%%", pct)
	}
	close(release)
}

// TestAFailedRunLeavesTheGateWhereItWas: a verification that could not
// produce a report at all — an unreadable store, no zone identity — says
// nothing about the bytes the gate is withholding, so it must not open
// it, and it must not silently close a gate a good verdict had opened.
func TestAFailedRunLeavesTheGateWhereItWas(t *testing.T) {
	m := newMedium(t)
	g := Open(context.Background(), m.root, testZone, m.logger)
	g.Observe(clearedReport())

	done := make(chan struct{})
	g.SetVerify(func(_ context.Context, _ Options, _ func(media.Progress)) (*media.Report, error) {
		defer close(done)
		return nil, taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": "disk removed"})
	})
	if e := g.Start(Options{}); e != nil {
		t.Fatalf("Start refused: %v", e)
	}
	<-done
	waitIdle(t, g)

	if !g.Serving() {
		t.Error("a run that reached no verdict closed a gate a verdict had opened")
	}
	s := g.Status()
	if s.Failure == nil || s.Failure.Code != taxonomy.CodeStoreRead {
		t.Fatalf("the failure is not reported: %+v", s.Failure)
	}
	if s.Failure.Error().Code() != taxonomy.CodeStoreRead {
		t.Error("the failure does not re-render as its taxonomy entry")
	}
}

// waitIdle blocks until no verification is running.
func waitIdle(t *testing.T, g *Gate) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !g.Status().Running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("a verification never settled")
}
