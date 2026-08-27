// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"fmt"
	"path"
	"strings"
)

// Path handling for content that arrived on a foreign medium (NFR-011).
//
// Everything below treats its input as hostile. An inventory path, a
// repository name and a tag all come out of an unsigned document written
// by someone else; a manifest crafted to say "../../etc/shadow" must not
// make Tobby read — let alone write — outside the store it was handed.
// Two independent guards apply: these validators, which refuse the shape,
// and the os.Root the verifier opens the store through, which refuses the
// escape even via a symlink planted on the medium.

// TobbyDir is Tobby's own area inside a store, in slash form. Nothing
// under it is covered by the manifest, by construction: the task queue
// writes there while a run is in progress, and the destination side
// writes its return logs there AFTER the inventory was taken (FR-053,
// FR-054). An inventory that covered it would be stale the moment it was
// written.
const TobbyDir = "_tobby"

// LogsDir is where operation logs live on a transport medium (FR-053).
// It is the default parent of the return-log file the destination side
// writes, and it sits under TobbyDir for the reason above.
const LogsDir = TobbyDir + "/logs"

// Covered reports whether a slash-separated path inside a store falls
// under the manifest's inventory.
//
// It is exported for the one caller that must stay OUT of coverage by
// construction: the destination side's return logs (FR-053, FR-054). A
// log file written inside coverage would invalidate the very inventory it
// is meant to accompany — every line appended would make the medium fail
// its own checksum — so the writer asks this function rather than
// re-deriving the rule beside it.
func Covered(slashPath string) bool { return covered(slashPath) }

// Layout constants of the registry filesystem backend, in slash form.
const (
	blobsPrefix = "docker/registry/v2/blobs/sha256"
	reposPrefix = "docker/registry/v2/repositories"
	// metaPrefix is the store bookkeeping; recipesFile is the one file in
	// it whose alteration blocks the whole medium (R-19).
	metaPrefix  = "meta"
	recipesFile = "meta/recipes.json"
)

// checkInventoryPath validates one path from a foreign inventory and
// returns it unchanged when it is acceptable.
//
// Acceptable means: relative, slash-separated, already clean, inside one
// of the covered roots, and free of the characters that turn a path into
// something else on some platform — a backslash (a separator on Windows),
// a colon (a volume or stream separator there), and control bytes.
func checkInventoryPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("an inventory entry has an empty path")
	case strings.ContainsRune(p, '\\'):
		return fmt.Errorf("inventory path %q uses a backslash: manifests carry slash-separated paths on every platform", p)
	case strings.ContainsRune(p, ':'):
		return fmt.Errorf("inventory path %q contains a colon", p)
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("inventory path %q contains a NUL byte", p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("inventory path %q is absolute", p)
	case path.Clean(p) != p:
		// Catches "..", ".", "//" and trailing slashes in one comparison.
		return fmt.Errorf("inventory path %q is not in clean relative form", p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("inventory path %q contains a control character", p)
		}
	}
	if !covered(p) {
		return fmt.Errorf("inventory path %q lies outside the manifest's coverage (%s)",
			p, strings.Join(coveredRoots, ", "))
	}
	return nil
}

// checkRepoName validates a repository name read from a foreign manifest.
// The registry backend turns it straight into directory components, so a
// name that is not made of ordinary path segments is a traversal attempt,
// not a naming style.
func checkRepoName(repo string) error {
	if repo == "" {
		return fmt.Errorf("a repository name is empty")
	}
	if len(repo) > 4096 {
		return fmt.Errorf("repository name is longer than 4096 characters")
	}
	for _, seg := range strings.Split(repo, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("repository name %q has an empty or relative path segment", repo)
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '.', r == '_', r == '-':
			default:
				return fmt.Errorf("repository name %q contains %q, which is not a legal repository character", repo, r)
			}
		}
	}
	return nil
}

// checkTag validates a tag read from a foreign manifest, on the OCI
// distribution grammar.
func checkTag(tag string) error {
	if tag == "" || len(tag) > 128 {
		return fmt.Errorf("tag %q is empty or longer than 128 characters", tag)
	}
	for i, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		case (r == '.' || r == '-') && i > 0:
		default:
			return fmt.Errorf("tag %q contains %q, which is not a legal tag character", tag, r)
		}
	}
	return nil
}

// digestHex validates a "sha256:<64 hex>" digest and returns its hex part.
// Anything else — another algorithm, a short digest, upper case — is
// refused: the store addresses content by sha256 and nothing else, and a
// lenient parser here is a directory name attacker-chosen there.
func digestHex(dgst string) (string, error) {
	hex, ok := strings.CutPrefix(dgst, "sha256:")
	if !ok {
		return "", fmt.Errorf("digest %q is not sha256-prefixed", dgst)
	}
	if len(hex) != 64 {
		return "", fmt.Errorf("digest %q does not carry 64 hex characters", dgst)
	}
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", fmt.Errorf("digest %q is not lower-case hexadecimal", dgst)
		}
	}
	return hex, nil
}

// blobPath is where the backend stores the bytes of one digest.
func blobPath(hex string) string {
	return blobsPrefix + "/" + hex[:2] + "/" + hex + "/data"
}

// manifestLinkPath is the per-repository reference that makes a manifest
// revision servable from that repository.
func manifestLinkPath(repo, hex string) string {
	return reposPrefix + "/" + repo + "/_manifests/revisions/sha256/" + hex + "/link"
}

// layerLinkPath is the per-repository reference to a blob (config layers
// included: the backend links them the same way).
func layerLinkPath(repo, hex string) string {
	return reposPrefix + "/" + repo + "/_layers/sha256/" + hex + "/link"
}

// tagCurrentPath is what a tag currently points at.
func tagCurrentPath(repo, tag string) string {
	return reposPrefix + "/" + repo + "/_manifests/tags/" + tag + "/current/link"
}

// tagIndexDir holds the history of what a tag has pointed at. Entries in
// it are reachable but never required: an older entry left by a re-tag is
// neither missing content nor extraneous content.
func tagIndexDir(repo, tag string) string {
	return reposPrefix + "/" + repo + "/_manifests/tags/" + tag + "/index"
}

// hexOfBlobPath returns the digest a blob path claims for its own content,
// so the verifier can check the content-addressed store against itself.
// The second result is false for any path that is not a blob's data file.
func hexOfBlobPath(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, blobsPrefix+"/")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, "/data")
	if !ok {
		return "", false
	}
	prefix, hex, ok := strings.Cut(rest, "/")
	if !ok || len(hex) != 64 || len(prefix) != 2 || !strings.HasPrefix(hex, prefix) {
		return "", false
	}
	return hex, true
}
