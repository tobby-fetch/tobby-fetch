// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
	"github.com/tobby-fetch/tobby-fetch/internal/server"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The FR-097 acceptance, end to end: an instance is stopped, its state
// directory, configuration and store are copied to a place it has never
// run, and an instance started there behaves as the original — the same
// accounts, the same token, the same certificate fingerprint, the same
// import record, the same runtime override. A state directory that was
// restored with its accounts file made world-readable refuses to start.

// runningInstance is a started serve with the means to stop it.
type runningInstance struct {
	base string
	stop func()
}

func startInstance(t *testing.T, cfg *config.Config) *runningInstance {
	t.Helper()
	srvCh := make(chan *server.Server, 1)
	serveHook = func(s *server.Server) { srvCh <- s }
	t.Cleanup(func() { serveHook = nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, cfg) }()

	var srv *server.Server
	select {
	case srv = <-srvCh:
	case err := <-done:
		cancel()
		t.Fatalf("runServe exited before serving: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("runServe never built its server")
	}
	client := insecureClient()
	deadline := time.Now().Add(15 * time.Second)
	base := ""
	for {
		if addr := srv.Addr(); addr != "" && base == "" {
			base = "https://" + addr
		}
		if base != "" {
			resp, err := client.Get(base + "/readyz")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("instance never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &runningInstance{base: base, stop: func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runServe returned %v after a graceful stop", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("graceful stop timed out")
		}
	}}
}

// insecureClient trusts the instance's self-signed certificate: the test
// compares fingerprints itself, which is the point of the assertion.
func insecureClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // G402: loopback test of a self-signed listener; the fingerprint is asserted explicitly
	}
}

// fingerprint is the SHA-256 of the certificate the listener presents.
func fingerprint(t *testing.T, base string) string {
	t.Helper()
	conn, err := tls.Dial("tcp", strings.TrimPrefix(base, "https://"), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // G402: reading the presented certificate is the test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	sum := sha256.Sum256(conn.ConnectionState().PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:])
}

// status issues an authenticated GET and returns the status code.
func status(t *testing.T, base, path string, set func(*http.Request)) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	set(req)
	resp, err := insecureClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// copyTree copies src to dst, skipping the named top-level entries.
func copyTree(t *testing.T, src, dst string, skip ...string) {
	t.Helper()
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		if skipped[strings.Split(filepath.ToSlash(rel), "/")[0]] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(p) //nolint:gosec // G304,G703: test-owned temp tree
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode().Perm()) //nolint:gosec // G703: test-owned temp tree
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackupAndRebuildGivesTheSameInstance(t *testing.T) {
	const (
		zone     = "zone-rebuild"
		password = "rebuild-test-password"
	)
	// --- The original instance ------------------------------------------
	root := t.TempDir()
	cfg := config.Default()
	cfg.Mode = config.ModePassthrough
	cfg.Zone = zone
	cfg.Storage.Root = filepath.Join(root, "old", "store")
	cfg.State.Root = filepath.Join(root, "old", "state")
	cfg.Server.Addr = "127.0.0.1:0"
	cfg.Server.TLS.Enabled = true // the generated self-signed certificate
	cfg.Shutdown.GracePeriod = config.Duration(2 * time.Second)

	accounts, err := auth.Open(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.AddAccount("admin", auth.RoleAdmin, password, time.Now()); err != nil {
		t.Fatal(err)
	}
	tokenSecret, _, err := accounts.CreateToken("ci", auth.RoleOperator, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The per-zone import record and a runtime interval override.
	imports, err := media.OpenImports(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	resolved := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := imports.Record(zone, &media.ImportRecord{MediaID: "med_rebuild_1", ResolvedAt: resolved, RunID: "run_1"}); err != nil {
		t.Fatal(err)
	}
	override, err := schedule.Open(cfg.State.Root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := override.Set(42*time.Minute, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}

	old := startInstance(t, &cfg)
	if got := status(t, old.base, "/api/v1/accounts", func(r *http.Request) { r.SetBasicAuth("admin", password) }); got != http.StatusOK {
		t.Fatalf("the original admin cannot sign in: %d", got)
	}
	if got := status(t, old.base, "/api/v1/tasks", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tokenSecret) }); got != http.StatusOK {
		t.Fatalf("the original token is refused: %d", got)
	}
	printed := fingerprint(t, old.base)
	// Discardable entries the instance created while running.
	for _, d := range []string{statedir.Partials, statedir.TrustCache} {
		if err := os.MkdirAll(statedir.Path(cfg.State.Root, d), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statedir.Path(cfg.State.Root, d, "left-behind"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old.stop()

	// The documented layout matches what `tobby state inspect` reports on
	// this fully exercised instance: every documented identity entry is
	// present, and only documented entries (plus the transient files
	// listed as such) are there.
	inv, err := statedir.Inspect(cfg.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, e := range statedir.Entries() {
		documented[e.Name] = true
	}
	for _, it := range inv.Items {
		if it.Class == statedir.ClassIdentity && !it.Present {
			t.Errorf("identity entry %s is absent from a fully exercised instance", it.Name)
		}
		if it.Present && !documented[it.Name] {
			t.Errorf("the instance wrote %s, which the layout does not document", it.Name)
		}
	}

	// --- The rebuild: state (identity entries only) + store, fresh paths --
	cfg2 := cfg
	cfg2.Storage.Root = filepath.Join(root, "new", "store")
	cfg2.State.Root = filepath.Join(root, "new", "state")
	copyTree(t, cfg.State.Root, cfg2.State.Root, statedir.Partials, statedir.TrustCache, statedir.Serving)
	copyTree(t, cfg.Storage.Root, cfg2.Storage.Root)

	rebuilt := startInstance(t, &cfg2)
	if got := status(t, rebuilt.base, "/api/v1/accounts", func(r *http.Request) { r.SetBasicAuth("admin", password) }); got != http.StatusOK {
		t.Errorf("the original account cannot sign in on the rebuilt instance: %d", got)
	}
	if got := status(t, rebuilt.base, "/api/v1/accounts", func(r *http.Request) { r.SetBasicAuth("admin", "wrong") }); got != http.StatusUnauthorized {
		t.Errorf("a wrong password was accepted on the rebuilt instance: %d", got)
	}
	if got := status(t, rebuilt.base, "/api/v1/tasks", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tokenSecret) }); got != http.StatusOK {
		t.Errorf("the original token is refused by the rebuilt instance: %d", got)
	}
	if got := fingerprint(t, rebuilt.base); got != printed {
		t.Errorf("certificate fingerprint changed across the rebuild:\n  was %s\n  now %s", printed, got)
	}
	rebuilt.stop()

	// The import record and the override are what they were; the
	// discardable entries were not carried and were not needed.
	imports2, err := media.OpenImports(cfg2.State.Root)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := imports2.Last(zone)
	if !ok || rec.MediaID != "med_rebuild_1" || !rec.ResolvedAt.Equal(resolved) {
		t.Errorf("import record after the rebuild = %+v (found %v)", rec, ok)
	}
	iv, err := schedule.Open(cfg2.State.Root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !iv.Overridden() || iv.Effective() != 42*time.Minute {
		t.Errorf("interval override after the rebuild = %v (overridden %v)", iv.Effective(), iv.Overridden())
	}
	if _, err := os.Stat(statedir.Path(cfg2.State.Root, statedir.Partials, "left-behind")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a discardable entry was carried to the new host")
	}

	// --- A restore that lost its permissions refuses to start -------------
	if runtime.GOOS != "windows" {
		cfg3 := cfg2
		cfg3.State.Root = filepath.Join(root, "loose", "state")
		copyTree(t, cfg2.State.Root, cfg3.State.Root)
		accountsFile := statedir.Path(cfg3.State.Root, statedir.Accounts)
		if err := os.Chmod(accountsFile, 0o644); err != nil { //nolint:gosec // G302: the test loosens the file on purpose
			t.Fatal(err)
		}
		err := runServe(context.Background(), &cfg3)
		var te *taxonomy.Error
		if !errors.As(err, &te) || te.Code() != taxonomy.CodeStatePermissions {
			t.Fatalf("a world-readable accounts file started: %v", err)
		}
		if !strings.Contains(taxonomy.Text("en", te), accountsFile) {
			t.Errorf("the refusal does not name the file: %v", taxonomy.Text("en", te))
		}
		// Group access, which a volume's fsGroup grants, is tolerated.
		if err := os.Chmod(accountsFile, 0o660); err != nil { //nolint:gosec // G302: group access, as a volume grants it
			t.Fatal(err)
		}
		if _, err := statedir.Prepare(cfg3.State.Root, stateExpectations(&cfg3), "test"); err != nil {
			t.Errorf("group access refused: %v", err)
		}
	}

	// --- An incomplete restore is reported, not silent ---------------------
	cfg4 := cfg2
	cfg4.State.Root = filepath.Join(root, "partial", "state")
	copyTree(t, cfg2.State.Root, cfg4.State.Root, statedir.MediaImports, statedir.TLS)
	rep, err := statedir.Prepare(cfg4.State.Root, stateExpectations(&cfg4), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rep.Missing, ","); got != "tls,media-imports.json" {
		t.Errorf("TBY-STA-003 reported %q, want the two missing identity entries", got)
	}
}
