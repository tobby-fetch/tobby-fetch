// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// cookedRecipe is the smallest publishable document: one fully pinned
// ingredient, name and version matching where it will be published.
const cookedRecipe = `apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: wordpress
  version: 6.8.2
spec:
  ingredients:
    - name: wordpress
      kind: ContainerImage
      ref: docker.io/bitnami/wordpress
      version: 6.8.2
      digest: sha256:8acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1
`

// testCookbook serves a real OCI registry and returns its address plus a
// configuration file marking it reachable over plain HTTP.
func testCookbook(t *testing.T) (addr, configPath string) {
	t.Helper()
	st, err := store.Open(t.Context(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(st.APIHandler())
	t.Cleanup(srv.Close)
	addr = srv.Listener.Addr().String()

	configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("registries:\n  insecure: [\""+addr+"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return addr, configPath
}

func writeRecipe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recipe.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRecipePushPrintsDigestOnStdout: the command has to compose into a
// signing pipeline, so the digest — and nothing else — lands on stdout
// (B-010). The cosign hint is guidance, and belongs on stderr.
func TestRecipePushPrintsDigestOnStdout(t *testing.T) {
	addr, cfg := testCookbook(t)
	file := writeRecipe(t, cookedRecipe)

	stdout, stderr, code := runProcess(t, "recipe", "push", file, addr+"/cookbook/wordpress:6.8.2", "--config", cfg)
	if code != taxonomy.ExitOK {
		t.Fatalf("push exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	digest := strings.TrimSpace(stdout)
	if !strings.HasPrefix(digest, "sha256:") || strings.Contains(digest, "\n") {
		t.Errorf("stdout = %q, want the published digest alone", stdout)
	}
	if !strings.Contains(stderr, "cosign sign") {
		t.Errorf("stderr does not point at the signing step: %q", stderr)
	}
	// The hint must target the digest: cosign signs digests, not tags.
	if !strings.Contains(stderr, addr+"/cookbook/wordpress@"+digest) {
		t.Errorf("the cosign hint does not target %s@%s: %q", addr+"/cookbook/wordpress", digest, stderr)
	}
}

// TestRecipePushValidatesBeforePublishing is the whole point of the
// command: a draft never reaches the cookbook, and nothing is written.
func TestRecipePushValidatesBeforePublishing(t *testing.T) {
	addr, cfg := testCookbook(t)
	draft := strings.Replace(cookedRecipe,
		"      digest: sha256:8acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1\n", "", 1)
	file := writeRecipe(t, draft)

	_, stderr, code := runProcess(t, "recipe", "push", file, addr+"/cookbook/wordpress:6.8.2", "--config", cfg)
	if code == taxonomy.ExitOK {
		t.Fatal("a draft was published: the cookbook now holds an unpinned recipe")
	}
	if !strings.Contains(stderr, string(taxonomy.CodeValidation)) {
		t.Errorf("the refusal does not carry %s: %q", taxonomy.CodeValidation, stderr)
	}
	// Nothing must have been written: a later publication of the same
	// version must still be possible.
	good := writeRecipe(t, cookedRecipe)
	if _, stderr, code := runProcess(t, "recipe", "push", good, addr+"/cookbook/wordpress:6.8.2", "--config", cfg); code != taxonomy.ExitOK {
		t.Fatalf("the refused publication left the tag occupied (exit %d): %s", code, stderr)
	}
}

// TestRecipePushRequiresNoMode: publishing is an authoring act; demanding
// a mode would mean a laptop cannot publish without pretending to serve
// (R-34, B-006).
func TestRecipePushRequiresNoMode(t *testing.T) {
	addr, _ := testCookbook(t)
	file := writeRecipe(t, cookedRecipe)
	empty := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(empty, []byte("registries:\n  insecure: [\""+addr+"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runProcess(t, "recipe", "push", file, addr+"/cookbook/wordpress:6.8.2", "--config", empty)
	if code != taxonomy.ExitOK {
		t.Fatalf("push exit = %d without a mode, want 0 (stderr: %s)", code, stderr)
	}
	if strings.Contains(stderr, "mode is required") {
		t.Errorf("publishing demanded an operating mode: %q", stderr)
	}
}
