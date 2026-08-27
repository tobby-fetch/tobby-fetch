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
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
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
	cfg.Files.FileSets = []config.FileSetServe{{
		Name: mediumFileSet, Ref: res.Reference, Version: "1.0.0", Anonymous: true,
	}}
	return res.Repository, res.Digest
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
// So the two rows below are the whole condition, checked both ways: the
// gate arms on the PRESENCE OF THE MANIFEST, and on a destination
// instance. Neither half alone withholds anything.
func TestServeDoesNotWithholdAStoreThatIsNotAMedium(t *testing.T) {
	for _, tc := range []struct {
		name string
		zone string
	}{
		// The m1 shape exactly: mirror mode on a transported store, no
		// zone identity, no media manifest.
		{name: "m1: mirror mode, no zone, no manifest", zone: ""},
		// And the half that proves the manifest is what decides: a fully
		// configured destination instance whose store simply is not a
		// medium withholds nothing either — there is nothing unverified
		// to withhold.
		{name: "destination instance, no manifest", zone: mediumZone},
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
