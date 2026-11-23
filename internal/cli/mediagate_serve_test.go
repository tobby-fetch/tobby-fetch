// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The FR-054 serving guard, proved on the instance the product actually
// starts.
//
// internal/mediagate proves the middleware refuses. This file proves the
// middleware is WIRED: it runs the real `serve` path, with the real
// mounting order, against a store that is a real transported medium, and
// asks the two content surfaces for content that is really there. The two
// tests are not redundant — the gap between "the guard works" and "the
// guard is installed" is exactly where a serving hole lived until now.

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	godigest "github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The fixture: a FileSet packed from a real directory, so the served path
// and the served bytes are both known exactly.
const (
	mediumZone      = "zone-destination"
	mediumFileSet   = "site-config"
	mediumFilePath  = "etc/tobby/marker.conf"
	mediumFileBytes = "this content must never leave an unverified medium\n"
)

// seedMedium turns cfg.Storage.Root into a transported medium.
func seedMedium(t *testing.T, cfg *config.Config) (repo, digest string) {
	t.Helper()
	return seedTransportStore(t, cfg, true)
}

// seedTransportStore fills cfg.Storage.Root with content served on both
// surfaces — a packed FileSet enabled for /files/, and its image manifest
// reachable through /v2/ — and, when asked, writes the media manifest that
// turns the directory into a delivery addressed to mediumZone (FR-050,
// FR-054).
//
// The manifest goes in LAST, so the store it inventories is the store the
// instance will open — which is what a mirror synchronization does at the
// end of its run.
//
// Whether it goes in at all is the whole point of the pair of tests below:
// a store that carries one changed hands, and a store that does not is an
// ordinary working store, however the instance holding it is configured.
func seedTransportStore(t *testing.T, cfg *config.Config, manifest bool) (repo, digest string) {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)

	if err := os.MkdirAll(cfg.Storage.Root, 0o750); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, cfg.Storage.Root, logger)
	if err != nil {
		t.Fatalf("opening the medium's store: %v", err)
	}

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, filepath.Dir(mediumFilePath)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(mediumFilePath)),
		[]byte(mediumFileBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := fileserve.NewPacker(st, cfg.Storage.BasePrefix, logger,
		fileserve.WithPackRoots([]string{src})).
		Pack(ctx, fileserve.PackRequest{Source: src, Name: mediumFileSet, Version: "1.0.0"})
	if err != nil {
		t.Fatalf("packing the fixture FileSet: %v", err)
	}
	if manifest {
		// One delivery reaching the FileSet: its artifact and its only
		// ingredient are the packed image, unsigned, admitted below by an
		// allowUnsigned trust scope. The fixture used to carry an EMPTY
		// recipe graph, which a verification called pushable — zero
		// blocked out of zero — and the gate then served the whole store
		// (B-063). A medium clears only what its pushable recipes reach.
		artifact := publishFixtureRecipe(t, st, res)
		if err := st.PutRecipeRecord(&store.RecipeRecord{
			Name: "site", Version: "1.0.0",
			CookbookRepo: fixtureCookbook, ArtifactRepo: fixtureCookbook, ArtifactTag: "1.0.0",
			Digest: artifact, Zone: mediumZone, ResolvedAt: time.Now().UTC(),
			Ingredients: []store.IngredientRecord{{
				Name: mediumFileSet, Kind: "FileSet", Repo: res.Repository, Tag: "1.0.0", Digest: res.Digest,
			}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := media.Write(ctx, st, media.WriteOptions{
			Zone: mediumZone, RunID: "run_fixture", ResolvedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("writing the media manifest: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("closing the medium's store: %v", err)
	}

	cfg.Zone = mediumZone
	cfg.Trust.Scopes = []config.TrustScope{{
		Name: "lab", Repositories: []string{fixtureCookbook}, AllowUnsigned: true,
	}}
	cfg.Files.FileSets = []config.FileSetServe{{
		Name: mediumFileSet, Ref: res.Reference, Version: "1.0.0", Anonymous: true,
	}}
	return res.Repository, res.Digest
}

// fixtureCookbook is where the fixture's recipe artifact is published.
const fixtureCookbook = "localhost/cookbook/site"

// publishFixtureRecipe publishes, as the RECIPE-SPEC §11.2 artifact, a
// recipe delivering the packed FileSet pinned by digest, and returns the
// artifact's manifest digest.
func publishFixtureRecipe(t *testing.T, st *store.Store, res *fileserve.PackResult) string {
	t.Helper()
	ctx := context.Background()
	ref, _, _ := strings.Cut(res.Reference, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	doc := []byte("apiVersion: recipe.tobbyfetch.dev/v1alpha1\nkind: Recipe\nmetadata:\n  name: site\n  version: 1.0.0\n" +
		"spec:\n  ingredients:\n    - name: " + mediumFileSet + "\n      kind: FileSet\n      ref: " + ref +
		"\n      version: 1.0.0\n      digest: " + res.Digest + "\n")
	cfg := []byte("{}")
	for _, b := range [][]byte{cfg, doc} {
		if err := st.WriteBlob(ctx, fixtureCookbook, godigest.FromBytes(b), bytes.NewReader(b)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"artifactType": engine.MediaTypeRecipe,
		"config":       map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": godigest.FromBytes(cfg), "size": len(cfg)},
		"layers":       []any{map[string]any{"mediaType": engine.MediaTypeRecipe, "digest": godigest.FromBytes(doc), "size": len(doc)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.PutManifest(ctx, fixtureCookbook, "application/vnd.oci.image.manifest.v1+json", raw, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	return d.String()
}

// startServe runs the real serve path and returns its base URL.
func startServe(t *testing.T, cfg *config.Config) string {
	t.Helper()
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)

	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	t.Cleanup(func() { serveHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg) }()
	t.Cleanup(func() {
		// Hang up before asking for a clean drain, so the drain usually
		// ends at once. Not every client can be reached from here (the
		// registry pushes go through go-containerregistry's own
		// transport), and a spare connection one of them dialed is only
		// counted idle by net/http after five seconds: the server then
		// closes it when the two-second grace period expires, and the
		// drain must still return nil (FR-093, TestDrainClosesASilentConnection).
		http.DefaultClient.CloseIdleConnections()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("runServe returned %v, want nil", err)
		}
	})

	var srv *server.Server
	select {
	case srv = <-srvCh:
	case err := <-done:
		t.Fatalf("runServe exited before serving: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runServe never built its server")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if addr := srv.Addr(); addr != "" {
			base := "http://" + addr
			resp, err := http.Get(base + "/readyz") //nolint:noctx // probe
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return base
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the instance never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fetch performs one authenticated request and returns status and body.
func fetch(t *testing.T, url string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json,"+
		"application/vnd.oci.image.index.v1+json,"+
		"application/vnd.docker.distribution.manifest.v2+json")
	req.SetBasicAuth("admin", "test-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// TestServeWithholdsAnUnverifiedMedium is the FR-054 serving guard on the
// real instance: `tobby serve` pointed at a transported store mounts /v2/
// and /files/ at startup, and until a verification has cleared the medium
// neither of them may hand out a byte of it.
//
// It also pins the two things that make the refusal usable rather than
// merely correct: the instance stays LIVE and READY — an operator needs
// its interface to press Verify — and both refusals name the taxonomy
// code and the screen that opens the gate.
//
// Proved fallible: with the two mediaGate.Guard wrappers removed from
// runServe, /v2/ answers 200 with the manifest and /files/ answers 200
// with the file's bytes, and this test fails on both.
func TestServeWithholdsAnUnverifiedMedium(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	repo, digest := seedMedium(t, &cfg)
	base := startServe(t, &cfg)

	registryURL := base + "/v2/" + repo + "/manifests/" + digest
	status, body := fetch(t, registryURL)
	if status == http.StatusOK {
		t.Errorf("the embedded registry served a manifest off an unverified medium (FR-054)")
	}
	if status != http.StatusForbidden {
		t.Errorf("GET /v2/…/manifests/… = %d, want 403", status)
	}
	if !strings.Contains(body, string(taxonomy.CodeMediaUnverified)) {
		t.Errorf("the registry refusal does not name %s; body: %s", taxonomy.CodeMediaUnverified, body)
	}

	filesURL := base + fileserve.RoutePrefix + mediumFileSet + "/" + mediumFilePath
	status, body = fetch(t, filesURL)
	if strings.Contains(body, mediumFileBytes) {
		t.Errorf("the file surface served content off an unverified medium (FR-054)")
	}
	if status != http.StatusForbidden {
		t.Errorf("GET /files/… = %d, want 403", status)
	}
	for _, want := range []string{string(taxonomy.CodeMediaUnverified), mediagate.Screen} {
		if !strings.Contains(body, want) {
			t.Errorf("the file refusal does not carry %q; body: %s", want, body)
		}
	}

	// Alive and ready, and saying what it is nevertheless withholding
	// (ADR-0012): a 503 would take the instance out of rotation and
	// remove the very screen that opens the gate.
	if status, body := fetch(t, base+"/healthz"); status != http.StatusOK {
		t.Errorf("/healthz = %d (%s), want 200: the instance is alive", status, body)
	}
	status, body = fetch(t, base+"/readyz")
	if status != http.StatusOK {
		t.Errorf("/readyz = %d, want 200: the instance is ready, its medium is not verified", status)
	}
	for _, want := range []string{"/v2/", fileserve.RoutePrefix, mediagate.Screen} {
		if !strings.Contains(body, want) {
			t.Errorf("/readyz does not say %q is closed; body: %q", want, body)
		}
	}

	// The Media screen itself must stay reachable — it is the way out.
	if status, _ := fetch(t, base+"/api/v1/media/verification"); status != http.StatusOK {
		t.Errorf("GET /api/v1/media/verification = %d on a guarded instance, want 200", status)
	}
}

// TestServeDoesNotWithholdASourceSideMirror: a mirror instance on the SOURCE side carries a media manifest too — it
// wrote it at the end of its own synchronization — and it must keep
// serving: its store is its output, not something that changed hands. The
// requirement distinguishes the two sides by the zone identity, which a
// source-side instance reads from its Retriever and never configures.
//
// This is the regression that would turn the guard from a fix into an
// outage, so it is pinned beside it.
func TestServeDoesNotWithholdASourceSideMirror(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	repo, digest := seedMedium(t, &cfg)
	cfg.Zone = "" // source side: the zone comes from the Retriever, not from here
	base := startServe(t, &cfg)

	if status, _ := fetch(t, base+"/v2/"+repo+"/manifests/"+digest); status != http.StatusOK {
		t.Errorf("a source-side mirror does not serve its own store: /v2/ = %d, want 200", status)
	}
	status, body := fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
	if status != http.StatusOK || !strings.Contains(body, mediumFileBytes) {
		t.Errorf("a source-side mirror does not serve /files/: %d (%s)", status, body)
	}
	if status, body := fetch(t, base+"/readyz"); status != http.StatusOK || strings.Contains(body, mediagate.Screen) {
		t.Errorf("a source-side mirror advertises a media caveat it does not have: %d (%s)", status, body)
	}
}

// TestServeDoesNotWithholdAStoreThatIsNotAMedium is the counter-test of
// the guard above, and it exists because a guard can be too WIDE without
// anything saying so.
//
// The shape it pins is a real one, not a hypothetical: crucible scenario
// m1 fills a store through /v2/ from a passthrough instance, carries the
// physical medium across the gap, starts `tobby serve --mode=mirror` on
// it, and requires the content to be servable on the other side (FR-050 —
// the store is self-contained and relocatable). That store never went
// through a mirror synchronization, so it carries no media manifest, and
// nothing about it changed hands in the sense FR-054 means. A guard armed
// on the operating mode, on the storage root having travelled, or on the
// zone identity alone would break that scenario — and the break would
// only surface on a paid crucible node.
//
// The gate arms on a DESTINATION instance (one told its zone) whose store
// holds content: with no zone, nothing is withheld, manifest or not. The
// destination half without a manifest is TestServeWithholdsADestination
// StoreWithoutManifest: since R9 of the milestone-7 review it is guarded.
func TestServeDoesNotWithholdAStoreThatIsNotAMedium(t *testing.T) {
	for _, tc := range []struct {
		name string
		zone string
	}{
		// The m1 shape exactly: mirror mode on a transported store, no
		// zone identity, no media manifest.
		{name: "m1: mirror mode, no zone, no manifest", zone: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := serveConfig(t)
			cfg.Mode = config.ModeMirror
			repo, digest := seedTransportStore(t, &cfg, false)
			cfg.Zone = tc.zone
			base := startServe(t, &cfg)

			if status, _ := fetch(t, base+"/v2/"+repo+"/manifests/"+digest); status != http.StatusOK {
				t.Errorf("/v2/…/manifests/… = %d on a store that is not a medium, want 200", status)
			}
			status, body := fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
			if status != http.StatusOK || !strings.Contains(body, mediumFileBytes) {
				t.Errorf("/files/… = %d on a store that is not a medium, want 200 with its content", status)
			}

			// The crucible's wait_ready polls /readyz and only looks at
			// the status, so a 200 is what keeps every finished
			// milestone's suite replayable. The body must also stay free
			// of a caveat this instance does not have.
			status, body = fetch(t, base+"/readyz")
			if status != http.StatusOK {
				t.Errorf("/readyz = %d, want 200: the crucible waits on it", status)
			}
			if strings.Contains(body, mediagate.Screen) {
				t.Errorf("/readyz advertises a media caveat this instance does not have: %q", body)
			}
		})
	}
}

// TestServeWithholdsADestinationStoreWithoutManifest (R9 of the milestone-7
// review): a destination instance — configured with its zone — whose store
// holds content but no meta/media.json is a medium whose manifest went
// missing, and FR-054 (R-19) makes an absent manifest a global block. It
// used to be served whole, carried FileSet cache included.
//
// Proved fallible: before the fix both surfaces answered 200 here.
func TestServeWithholdsADestinationStoreWithoutManifest(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	repo, digest := seedTransportStore(t, &cfg, false)
	base := startServe(t, &cfg)

	status, body := fetch(t, base+"/v2/"+repo+"/manifests/"+digest)
	if status != http.StatusForbidden || !strings.Contains(body, string(taxonomy.CodeMediaUnverified)) {
		t.Errorf("/v2/…/manifests/… = %d (%s), want 403 %s", status, body, taxonomy.CodeMediaUnverified)
	}
	status, body = fetch(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
	if status != http.StatusForbidden || strings.Contains(body, mediumFileBytes) {
		t.Errorf("/files/… = %d (%s), want 403 and none of the content", status, body)
	}

	// Verifying does not open it: the manifest is missing (TBY-MED-001).
	if status, raw := call(t, http.MethodPost, base+"/api/v1/media/verify", map[string]any{}); status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("verify = %d: %s", status, raw)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, raw := call(t, http.MethodGet, base+"/api/v1/media/verification", nil)
		if strings.Contains(string(raw), string(taxonomy.CodeMediaManifestMissing)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the verification never reported %s: %s", taxonomy.CodeMediaManifestMissing, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status, _ := fetch(t, base+"/v2/"+repo+"/manifests/"+digest); status != http.StatusForbidden {
		t.Errorf("/v2/ = %d after a verification that blocked, want 403", status)
	}
}

// TestServeDoesNotServeAVersionAddedAfterTheManifest (B-063, milestone-7
// review proof F1): a FileSet version stored on the medium after its media
// manifest was written is uninventoried, reached by no recipe and never
// hashed. Once a verification opens the gate, the "highest local tag"
// resolution must not pick it, and /v2/ must not hand it out.
//
// Proved fallible: before the fix /files/ answered 200 "SMUGGLED".
func TestServeDoesNotServeAVersionAddedAfterTheManifest(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	repo, _ := seedMedium(t, &cfg)
	cfg.Files.FileSets[0].Version = "" // highest local semver tag

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Storage.Root, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, filepath.Dir(mediumFilePath)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, filepath.FromSlash(mediumFilePath)), []byte("SMUGGLED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	smuggled, err := fileserve.NewPacker(st, cfg.Storage.BasePrefix, slog.New(slog.DiscardHandler),
		fileserve.WithPackRoots([]string{src})).
		Pack(ctx, fileserve.PackRequest{Source: src, Name: mediumFileSet, Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	base := startServe(t, &cfg)
	if status, raw := call(t, http.MethodPost, base+"/api/v1/media/verify", map[string]any{}); status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("verify = %d: %s", status, raw)
	}
	waitServing(t, base)

	filesURL := base + fileserve.RoutePrefix + mediumFileSet + "/" + mediumFilePath
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, body := fetch(t, filesURL)
		if body == "SMUGGLED\n" {
			t.Fatalf("/files/ serves the version added after the manifest (status %d)", status)
		}
		if status == http.StatusOK && body == mediumFileBytes {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/files/ never served the verified version: %d %q", status, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, ref := range []string{smuggled.Digest, "9.9.9"} {
		status, body := fetch(t, base+"/v2/"+repo+"/manifests/"+ref)
		if status != http.StatusForbidden || !strings.Contains(body, string(taxonomy.CodeMediaOutsideVerified)) {
			t.Errorf("/v2/…/manifests/%s = %d (%s), want 403 %s", ref, status, body, taxonomy.CodeMediaOutsideVerified)
		}
	}
}

// anonymousGet performs one request without credentials.
func anonymousGet(t *testing.T, url string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// TestServeGateSitsInsideAuthentication (B-067, second adversarial review
// of milestone 7): the media gate answers /v2/_catalog and tags/list
// itself, from the verified set. Mounted outside authentication, an open
// gate listed every verified repository and tag to an anonymous client,
// although registry reads need viewer (ADR-0009), and its closed-gate
// refusals reached clients nobody had authenticated.
//
// Proved fallible: with the gate outside authn.Registry, the anonymous
// catalog and tag list answer 200.
func TestServeGateSitsInsideAuthentication(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	repo, digest := seedMedium(t, &cfg)
	base := startServe(t, &cfg)

	// Closed: an anonymous client is asked to authenticate, not told
	// about the medium.
	if status, body := anonymousGet(t, base+"/v2/"+repo+"/manifests/"+digest); status != http.StatusUnauthorized {
		t.Errorf("anonymous GET /v2/…/manifests/… on a closed gate = %d (%s), want 401", status, body)
	}

	if status, raw := call(t, http.MethodPost, base+"/api/v1/media/verify", map[string]any{}); status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("verify = %d: %s", status, raw)
	}
	waitServing(t, base)

	for _, p := range []string{"/v2/_catalog", "/v2/" + repo + "/tags/list", "/v2/" + repo + "/manifests/" + digest} {
		if status, body := anonymousGet(t, base+p); status != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s on an open gate = %d (%s), want 401", p, status, strings.TrimSpace(body))
		}
		if status, body := fetch(t, base+p); status != http.StatusOK {
			t.Errorf("authenticated GET %s on an open gate = %d (%s), want 200", p, status, strings.TrimSpace(body))
		}
	}
	// The anonymous FileSet stays anonymous (FR-047).
	if status, body := anonymousGet(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath); status != http.StatusOK || body != mediumFileBytes {
		t.Errorf("anonymous GET of the anonymous FileSet = %d (%q), want 200 with its bytes", status, body)
	}
}

// TestServeGateRefusalDoesNotNameTheStore (B-067): a FileSet declared
// anonymous reaches the gate without credentials, so the gate's refusal
// reaches an anonymous client. On a medium whose manifest gives no media
// ID it used to name the medium by the store's path on the host.
func TestServeGateRefusalDoesNotNameTheStore(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	seedTransportStore(t, &cfg, false) // no manifest: no media ID
	base := startServe(t, &cfg)

	status, body := anonymousGet(t, base+fileserve.RoutePrefix+mediumFileSet+"/"+mediumFilePath)
	if status != http.StatusForbidden || !strings.Contains(body, string(taxonomy.CodeMediaUnverified)) {
		t.Fatalf("anonymous GET of the anonymous FileSet = %d (%s), want 403 %s", status, body, taxonomy.CodeMediaUnverified)
	}
	if strings.Contains(body, cfg.Storage.Root) {
		t.Errorf("the refusal names the store's path %s to an anonymous client: %s", cfg.Storage.Root, body)
	}
}
