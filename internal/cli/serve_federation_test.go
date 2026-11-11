// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
)

// TestServeStartsWithIdentityProviderDown is FR-078 at the process level:
// OIDC is enabled against an issuer that does not answer, and the
// instance still starts, becomes ready, mounts the federated routes and
// serves the sign-in page — the provider is never contacted to start.
func TestServeStartsWithIdentityProviderDown(t *testing.T) {
	dead := httptest.NewTLSServer(http.NotFoundHandler())
	issuer := dead.URL + "/realms/gone"
	dead.Close()

	cfg := serveConfig(t)
	cfg.Mode = config.ModePassthrough
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)
	secret := filepath.Join(t.TempDir(), "oidc-secret")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.OIDC = config.OIDC{
		Enabled: true, DisplayName: "Gone directory", Issuer: issuer, ClientID: "tobby",
		ClientSecretFile: secret, RedirectURL: "https://tobby.test/auth/oidc/callback",
		GroupsClaim: "groups", Roles: config.RoleMapping{Viewer: []string{"staff"}},
	}

	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	defer func() { serveHook = nil }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &cfg) }()

	var srv *server.Server
	select {
	case srv = <-srvCh:
	case err := <-done:
		t.Fatalf("the instance refused to start with its identity provider down: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runServe never built its server")
	}
	get := func(path string) (int, string) {
		resp, err := http.Get("http://" + srv.Addr() + path) //nolint:noctx // the test's own loopback listener
		if err != nil {
			return 0, err.Error()
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, string(b)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if srv.Addr() != "" {
			if code, _ := get("/readyz"); code == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the instance never became ready with its identity provider down")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, body := get("/login"); code != http.StatusOK || !strings.Contains(body, "Gone directory") || !strings.Contains(body, `id="password"`) {
		t.Errorf("sign-in page: %d, want the provider button and the local form", code)
	}
	if code, _ := get("/auth/oidc/callback?state=x&code=y"); code != http.StatusUnauthorized {
		t.Errorf("the federated callback is not mounted: %d", code)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("shutdown: %v", err)
	}
}
