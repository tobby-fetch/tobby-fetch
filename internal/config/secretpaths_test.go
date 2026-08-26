// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// plantedCorpus writes one file of every secret kind NFR-020 names INSIDE
// the store, and returns the store root plus a configuration pointing at
// each of them. It is the acceptance corpus: real files, in the place the
// requirement forbids, reached through the settings an operator actually
// writes.
func plantedCorpus(t *testing.T) (root string, cfg Config) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "store")
	mkdir(t, filepath.Join(root, "secrets", "tls"))
	mkdir(t, filepath.Join(root, "instance-state"))

	creds := filepath.Join(root, "secrets", ".dockerconfigjson")
	// Shape, not substance: the check is about WHERE the file is, and a
	// fixture carrying a credential-looking blob only teaches the secret
	// scanner to distrust this repository.
	write(t, creds, `{"auths":{"registry.example.com":{}}}`)
	key := filepath.Join(root, "secrets", "tls", "server.key")
	write(t, key, "-----BEGIN PRIVATE KEY-----\nplanted\n-----END PRIVATE KEY-----\n")
	accounts := filepath.Join(root, "instance-state", "accounts.yaml")
	write(t, accounts, "version: 1\naccounts:\n  - name: admin\n")

	cfg = Default()
	cfg.Storage.Root = root
	cfg.State.Root = filepath.Join(root, "instance-state")
	cfg.Registries.CredentialsFile = creds
	cfg.Server.TLS.KeyFile = key
	return root, cfg
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSecretsInStoreDetectsPlantedCorpus is the NFR-020 acceptance: every
// planted secret is reported, each named by the configuration key an
// operator has to go and change.
func TestSecretsInStoreDetectsPlantedCorpus(t *testing.T) {
	_, cfg := plantedCorpus(t)

	found := cfg.SecretsInStore()
	got := map[string]bool{}
	for _, sp := range found {
		got[sp.Key] = true
		if sp.Resolved == "" {
			t.Errorf("%s reported without a resolved path", sp.Key)
		}
	}
	for _, want := range []string{"state.root", "registries.credentialsFile", "server.tls.keyFile"} {
		if !got[want] {
			t.Errorf("%s is inside the store and was not reported (reported: %v)", want, got)
		}
	}

	// The message names the offending paths, not just their count: an
	// operator who cannot find the file cannot move it.
	rendered := FormatSecretPaths(found)
	// The state directory is named as a directory: the local user database
	// and the static tokens live in it, and moving the directory is the
	// corrective action — moving accounts.yaml alone would leave the rest.
	for _, want := range []string{"registries.credentialsFile", ".dockerconfigjson", "server.key", "instance-state"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("refusal text does not name %q: %s", want, rendered)
		}
	}
}

// TestSecretsOutsideStoreArePermitted locks the other half: the same
// corpus placed beside the store is not a refusal. Without it the check
// could pass by refusing everything.
func TestSecretsOutsideStoreArePermitted(t *testing.T) {
	base := t.TempDir()
	cfg := Default()
	cfg.Storage.Root = filepath.Join(base, "store")
	cfg.State.Root = filepath.Join(base, "state")
	cfg.Registries.CredentialsFile = filepath.Join(base, "creds.json")
	cfg.Server.TLS.KeyFile = filepath.Join(base, "tls", "server.key")
	mkdir(t, cfg.Storage.Root)
	mkdir(t, cfg.State.Root)

	if found := cfg.SecretsInStore(); len(found) > 0 {
		t.Errorf("secrets beside the store were refused: %s", FormatSecretPaths(found))
	}
}

// TestSecretsInStoreThroughSymlink is the reason this check resolves
// through the filesystem instead of comparing strings: the configured path
// is outside the store by every textual measure and lands inside it.
func TestSecretsInStoreThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs a privilege Windows does not grant by default")
	}
	base := t.TempDir()
	root := filepath.Join(base, "store")
	mkdir(t, filepath.Join(root, "hidden"))
	write(t, filepath.Join(root, "hidden", "creds.json"), "{}")

	// "elsewhere" reads like a directory beside the store; it is the
	// store's own subdirectory.
	link := filepath.Join(base, "elsewhere")
	if err := os.Symlink(filepath.Join(root, "hidden"), link); err != nil {
		t.Fatal(err)
	}

	cfg := Default()
	cfg.Storage.Root = root
	cfg.Registries.CredentialsFile = filepath.Join(link, "creds.json")

	found := cfg.SecretsInStore()
	if len(found) != 1 {
		t.Fatalf("symlinked credentials file was not caught: %v", found)
	}
	if !strings.Contains(found[0].Resolved, filepath.Join("store", "hidden")) {
		t.Errorf("resolved path %q does not show where the link lands", found[0].Resolved)
	}
}

// TestSecretsInStoreThroughRelativeAndDotDot covers the two spellings that
// defeat prefix matching: a relative path, and one that leaves the store
// only to come back.
func TestSecretsInStoreThroughRelativeAndDotDot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "store")
	mkdir(t, filepath.Join(root, "sub"))

	t.Run("dot dot returns inside", func(t *testing.T) {
		cfg := Default()
		cfg.Storage.Root = root
		cfg.Registries.CredentialsFile = filepath.Join(root, "sub", "..", "creds.json")
		if found := cfg.SecretsInStore(); len(found) != 1 {
			t.Fatalf("a path returning into the store was not caught: %v", found)
		}
	})

	t.Run("relative to the working directory", func(t *testing.T) {
		t.Chdir(root)
		cfg := Default()
		cfg.Storage.Root = "."
		cfg.Registries.CredentialsFile = filepath.Join("sub", "creds.json")
		if found := cfg.SecretsInStore(); len(found) != 1 {
			t.Fatalf("a relative path inside the store was not caught: %v", found)
		}
	})
}

// TestSecretsInStoreFoldsCaseOnWindows exercises the NFR-018 half of the
// containment test on every runner: where paths compare case-insensitively
// a differently-cased spelling is the same file, and a case-sensitive
// comparison would let it through.
func TestSecretsInStoreFoldsCaseOnWindows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "Store")
	mkdir(t, root)

	cfg := Default()
	cfg.Storage.Root = root
	cfg.Registries.CredentialsFile = filepath.Join(base, "STORE", "creds.json")

	folded := caseFoldPaths
	t.Cleanup(func() { caseFoldPaths = folded })

	caseFoldPaths = false
	if found := cfg.SecretsInStore(); len(found) != 0 {
		// On a case-insensitive filesystem the resolution alone already
		// normalizes the spelling; the fold is then belt and braces.
		t.Logf("the filesystem itself folded the case: %s", FormatSecretPaths(found))
	}

	caseFoldPaths = true
	if found := cfg.SecretsInStore(); len(found) != 1 {
		t.Fatalf("a differently-cased path inside the store was not caught: %v", found)
	}
}

// TestSecretsInStoreWithoutStoreRoot: a command with no store configured
// has no transportable medium and nothing to refuse.
func TestSecretsInStoreWithoutStoreRoot(t *testing.T) {
	cfg := Default()
	cfg.State.Root = t.TempDir()
	if found := cfg.SecretsInStore(); found != nil {
		t.Errorf("refused %s without any store root", FormatSecretPaths(found))
	}
}

// TestSecretPathsOmitsNonSecrets locks the inventory itself: the proxy
// password has no file form, and CA bundles and trust roots are public
// keys. A path listed here becomes a startup refusal, so the list is a
// contract, not a convenience.
func TestSecretPathsOmitsNonSecrets(t *testing.T) {
	cfg := Default()
	cfg.Network.TLS.CAFiles = []string{"/etc/ssl/private-ca.pem"}
	cfg.Server.TLS.CertFile = "/etc/tobby/server.crt"
	cfg.UI.ThemeOverride = "/etc/tobby/theme.css"
	cfg.Network.Proxy.Password = NewSecret("hunter2")

	for _, sp := range cfg.SecretPaths() {
		switch sp.Key {
		case "state.root", "registries.credentialsFile", "server.tls.keyFile":
		default:
			t.Errorf("unexpected secret path %s = %s", sp.Key, sp.Path)
		}
	}
}
