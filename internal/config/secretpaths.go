// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Secrets never travel (NFR-020, R-16).
//
// The store is handed to a courier and plugged into a machine in another
// zone: everything under it is assumed to be read by someone else. So no
// file holding a secret may live there — registry credentials, TLS private
// keys, the local user database and the static tokens beside it, and the
// state directory that holds all three by construction.
//
// The check below is deliberately a filesystem resolution rather than a
// string comparison. disjointRoots already refuses the obvious spelling at
// load time, and it is the wrong instrument for this one: a relative path,
// a "..", or a symlink whose name reads like an escape and whose target
// lands back inside the store all defeat prefix matching, and each of them
// is a way to put an account database on a medium that leaves the site.
// Resolving through the real filesystem is what makes "looks outside" and
// "is outside" the same statement.

// caseFoldPaths reports whether the platform compares paths without
// regard to case (Windows, NFR-018): there, "C:\Store\creds.json" and
// "c:\store\CREDS.JSON" are one file, and a containment test that says
// otherwise is a hole. A variable rather than a runtime.GOOS call at the
// point of use so the Windows behaviour is exercised on every runner
// instead of only on the one that has it.
var caseFoldPaths = runtime.GOOS == "windows"

// volumeSyntax reports whether the platform spells the same directory in
// more than one way at the volume level (Windows, NFR-018). A variable
// for the same reason as caseFoldPaths: the rewriting below is exercised
// on every runner, not only on the one where it matters.
var volumeSyntax = runtime.GOOS == "windows"

// ordinaryVolume rewrites the extended-length and device spellings of a
// Windows path onto the ordinary one.
//
// This matters because the containment test underneath is filepath.Rel,
// and Rel refuses to relate two paths whose VOLUME NAMES differ — it
// returns an error, which pathUnder reads as "not under". So
// `\\?\C:\store\creds.json` was not under `C:\store`, and the NFR-020
// refusal that exists to keep a credentials file off a medium handed to a
// courier simply did not fire. The same for `\\.\C:\…` and for
// `\\?\UNC\server\share\…`, which is the extended spelling of
// `\\server\share\…` (B-027).
//
// Every one of these is an ordinary thing to find in a configuration
// file: they are what Windows tooling produces when a path might exceed
// MAX_PATH. This is a rewriting of spellings, not a security boundary —
// see canonicalVolume for the spellings no amount of string work can
// reconcile.
func ordinaryVolume(p string) string {
	if !volumeSyntax {
		return p
	}
	for _, uncPrefix := range []string{`\\?\UNC\`, `\\.\UNC\`} {
		if strings.HasPrefix(p, uncPrefix) {
			return `\\` + p[len(uncPrefix):]
		}
	}
	for _, prefix := range []string{`\\?\`, `\\.\`} {
		if strings.HasPrefix(p, prefix) {
			return p[len(prefix):]
		}
	}
	return p
}

// SecretPath is one configured location holding secret material, with the
// configuration key an operator has to go and change.
type SecretPath struct {
	// Key is the configuration key ("registries.credentialsFile").
	Key string
	// Path is the value as configured, echoed back so the operator
	// recognizes what they wrote.
	Path string
	// Resolved is Path after filesystem resolution — the answer to "but
	// it does not look like it is inside the store".
	Resolved string
}

// SecretPaths lists every configured path holding, or containing, secret
// material (NFR-020). Unset paths are omitted: there is nothing to place
// anywhere.
//
// The proxy password is absent on purpose — it has no file form, it is a
// Secret value in this very structure (FR-080). So are trust roots and CA
// bundles: public keys are configuration, not secrets.
func (c *Config) SecretPaths() []SecretPath {
	var out []SecretPath
	add := func(key, path string) {
		if path != "" {
			out = append(out, SecretPath{Key: key, Path: path})
		}
	}
	// The state directory first: it is the one that holds the local user
	// database, the static token digests, the generated TLS key and the
	// resume spool, so it is the one whose misplacement costs the most.
	add("state.root", c.State.Root)
	add("registries.credentialsFile", c.Registries.CredentialsFile)
	add("server.tls.keyFile", c.Server.TLS.KeyFile)
	return out
}

// SecretsInStore returns the configured secret paths that resolve under
// the store root — the NFR-020 startup refusal, empty on a healthy
// instance. Each entry carries both the configured spelling and what it
// resolves to.
//
// An instance without a store root has no transportable medium yet and
// nothing to refuse.
func (c *Config) SecretsInStore() []SecretPath {
	if c.Storage.Root == "" {
		return nil
	}
	root := resolvePath(c.Storage.Root)
	var found []SecretPath
	for _, sp := range c.SecretPaths() {
		resolved := resolvePath(sp.Path)
		if !pathUnder(root, resolved) {
			continue
		}
		sp.Resolved = resolved
		found = append(found, sp)
	}
	return found
}

// StoreRootResolved reports the store root after the same resolution the
// secret check applies, so a refusal names the directory the check
// actually compared against rather than the string that was configured.
func (c *Config) StoreRootResolved() string { return resolvePath(c.Storage.Root) }

// resolvePath turns a configured path into the absolute, symlink-free
// path the filesystem will actually use.
//
// A secret file need not exist yet — the listener's key file is written on
// first start, and the state directory is created on demand — so
// EvalSymlinks cannot be applied to the leaf. It is applied to the deepest
// ancestor that DOES exist and the remainder is re-appended: a
// "…/outside/creds.json" whose "outside" is a symlink into the store
// resolves into the store, which is the whole point.
func resolvePath(p string) string {
	if p == "" {
		return ""
	}
	p = ordinaryVolume(p)
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	rest := ""
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			// canonicalVolume finishes what ordinaryVolume cannot: a
			// substituted drive and an administrative share are the same
			// directory under another volume name, and only the operating
			// system knows it.
			return filepath.Join(canonicalVolume(ordinaryVolume(resolved)), rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the volume root without finding anything that
			// exists: nothing to resolve, the absolute form is the best
			// answer available.
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// pathUnder reports whether path is the root itself or lives beneath it.
// Both arguments are already resolved.
func pathUnder(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	if caseFoldPaths {
		root = strings.ToLower(root)
		path = strings.ToLower(path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		// Different volumes on Windows: not under, and not a question the
		// separator rules can answer differently.
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// FormatSecretPaths renders the offending paths for the refusal message:
// one "key = configured (resolves to …)" per line, the resolved form
// omitted when it adds nothing. It is a message helper, not a data
// accessor — the caller that has to name them all has one way to do it.
func FormatSecretPaths(paths []SecretPath) string {
	var b strings.Builder
	for i, sp := range paths {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(sp.Key)
		b.WriteString(" = ")
		b.WriteString(sp.Path)
		if sp.Resolved != "" && sp.Resolved != sp.Path {
			b.WriteString(" (resolves to ")
			b.WriteString(sp.Resolved)
			b.WriteString(")")
		}
	}
	return b.String()
}
