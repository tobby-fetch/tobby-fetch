// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// TestServeHardeningSwitches drives FR-098 through the real serve wiring —
// the listener, its authentication, the guarded /v2/, the API and the
// metrics — on one store, first open, then hardened:
//
//   - open: an administrator's push lands and the repository counts as
//     seeded content in the unmanaged-content metric; both switches read 0;
//   - hardened: the same administrator's push answers DENIED carrying
//     TBY-POL-006, the pull of what is there still works, the import API
//     answers TBY-POL-005, and both switches read 1.
func TestServeHardeningSwitches(t *testing.T) {
	cfg := serveConfig(t)
	cfg.Mode = config.ModeMirror
	admin := remote.WithAuth(&authn.Basic{Username: "admin", Password: "test-password"})
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}

	scrape := func(base string) string {
		resp, err := http.Get(base + "/metrics") //nolint:noctx // the test server's own URL
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close() //nolint:errcheck // test
		raw, _ := io.ReadAll(resp.Body)
		return string(raw)
	}
	run := func(t *testing.T, c config.Config) string {
		srv := startServeForHeaders(t, &c)
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		return ts.URL
	}

	t.Run("open", func(t *testing.T) {
		base := run(t, cfg)
		ref, _ := name.ParseReference(strings.TrimPrefix(base, "http://") + "/zone/seeded/app:1.0")
		if err := remote.Write(ref, img, admin); err != nil {
			t.Fatalf("push on an open instance: %v", err)
		}
		m := scrape(base)
		for _, want := range []string{
			`tobby_hardening_enabled{setting="disableUnitImport"} 0`,
			`tobby_hardening_enabled{setting="disableRegistryPush"} 0`,
			`tobby_store_unmanaged_repositories{class="seed"} 1`,
			`tobby_store_unmanaged_repositories{class="unit-import"} 0`,
		} {
			if !strings.Contains(m, want) {
				t.Errorf("metrics miss %s", want)
			}
		}
	})

	t.Run("hardened", func(t *testing.T) {
		c := cfg
		c.Hardening = config.Hardening{DisableUnitImport: true, DisableRegistryPush: true}
		base := run(t, c)
		host := strings.TrimPrefix(base, "http://")

		ref, _ := name.ParseReference(host + "/zone/other/app:1.0")
		err := remote.Write(ref, img, admin)
		if err == nil || !strings.Contains(err.Error(), "DENIED") || !strings.Contains(err.Error(), "TBY-POL-006") {
			t.Fatalf("administrator push = %v, want DENIED carrying TBY-POL-006", err)
		}
		pull, _ := name.ParseReference(host + "/zone/seeded/app:1.0")
		if _, err := remote.Image(pull, admin); err != nil {
			t.Errorf("pull on a read-only registry: %v", err)
		}

		req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/import", //nolint:noctx // test
			strings.NewReader(`{"reference":"docker.io/library/redis:8","platforms":[{"name":"linux/amd64","digest":"sha256:`+strings.Repeat("0", 64)+`"}]}`))
		req.SetBasicAuth("admin", "test-password")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "TBY-POL-005") {
			t.Errorf("POST /api/v1/import = %d %s, want 403 TBY-POL-005", resp.StatusCode, body)
		}

		m := scrape(base)
		for _, want := range []string{
			`tobby_hardening_enabled{setting="disableUnitImport"} 1`,
			`tobby_hardening_enabled{setting="disableRegistryPush"} 1`,
			`tobby_store_unmanaged_repositories{class="seed"} 1`,
		} {
			if !strings.Contains(m, want) {
				t.Errorf("metrics miss %s", want)
			}
		}
	})
}
