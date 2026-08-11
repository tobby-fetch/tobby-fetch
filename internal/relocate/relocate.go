// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package relocate computes where an ingredient lands: the relocation
// convention of ADR-0013 (FR-035, RECIPE-SPEC §11.5).
//
// Every ingredient is stored under its nominal, canonicalized source host —
// in the embedded store and in every destination registry alike:
//
//	<destination>[/<base-prefix>]/<canonical-source-host>[_<port>]/<repository-path>
//
// The mapping is a pure function of the recipe's `ref` and the optional base
// prefix. Source substitution (FR-036) changes only the network endpoint
// contacted, never the computed path, so the relocated path is invariant
// across hops in cascaded topologies.
package relocate

import (
	"fmt"
	"strings"
)

// dockerHubAliases is the closed canonicalization list of ADR-0013: the
// Docker Hub aliases collapse to "docker.io"; no other implicit
// normalization exists.
var dockerHubAliases = map[string]string{
	"index.docker.io":      "docker.io",
	"registry-1.docker.io": "docker.io",
}

// Path returns the relocated repository path for a nominal ingredient
// reference — host plus repository, the `ref` syntax of the recipe format
// (no tag, no digest).
//
//	Path("docker.io/bitnami/wordpress")        → "docker.io/bitnami/wordpress"
//	Path("index.docker.io/library/nginx")      → "docker.io/library/nginx"
//	Path("registry.example.com:5000/a/b")      → "registry.example.com_5000/a/b"
func Path(nominalRef string) (string, error) {
	host, repo, err := splitRef(nominalRef)
	if err != nil {
		return "", err
	}
	return host + "/" + repo, nil
}

// PathWithBase is Path with the instance's optional base prefix (FR-035)
// prepended. An empty base is the default and adds nothing.
func PathWithBase(base, nominalRef string) (string, error) {
	p, err := Path(nominalRef)
	if err != nil {
		return "", err
	}
	base = strings.Trim(base, "/")
	if base == "" {
		return p, nil
	}
	return base + "/" + p, nil
}

// splitRef separates and canonicalizes the host of a nominal reference and
// validates the shape the recipe format guarantees (RECIPE-SPEC §13.1:
// references carry an explicit host and no tag or digest).
func splitRef(ref string) (host, repo string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("empty reference")
	}
	if strings.Contains(ref, "://") {
		return "", "", fmt.Errorf("reference %q must not carry a URL scheme", ref)
	}
	if strings.HasPrefix(ref, "[") {
		// ADR-0013: IPv6 literal hosts are not relocatable — the bracket
		// syntax cannot round-trip through a repository path component.
		return "", "", fmt.Errorf("reference %q: IPv6 literal hosts are not relocatable, use a hostname", ref)
	}
	if i := strings.IndexAny(ref, "@"); i >= 0 {
		return "", "", fmt.Errorf("reference %q must not pin a digest: digests live in the ingredient's digest field", ref)
	}

	slash := strings.IndexByte(ref, '/')
	if slash <= 0 {
		return "", "", fmt.Errorf("reference %q must include an explicit registry host (e.g. docker.io/library/nginx)", ref)
	}
	host, repo = ref[:slash], ref[slash+1:]

	// A tag separator after the host boundary is a colon in the repository
	// part; the recipe format keeps versions out of references.
	if strings.Contains(repo, ":") {
		return "", "", fmt.Errorf("reference %q must not carry a tag: versions live in the ingredient's version field", ref)
	}
	if repo == "" || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") || strings.Contains(repo, "//") {
		return "", "", fmt.Errorf("reference %q has an empty repository path component", ref)
	}

	host = strings.ToLower(host)
	// The host must be recognizable as one: a dot, a port, or "localhost".
	// A bare first component would silently become a Docker Hub namespace
	// on some clients — exactly the ambiguity the format forbids.
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "", "", fmt.Errorf("reference %q must include an explicit registry host (e.g. docker.io/library/nginx)", ref)
	}

	if colon := strings.IndexByte(host, ':'); colon >= 0 {
		port := host[colon+1:]
		if port == "" || strings.Contains(port, ":") {
			return "", "", fmt.Errorf("reference %q has a malformed host port", ref)
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return "", "", fmt.Errorf("reference %q has a malformed host port", ref)
			}
		}
		// ADR-0013: ":" is invalid in repository names; "_" is unambiguous
		// because a valid hostname cannot contain it.
		host = host[:colon] + "_" + port
	}

	if canonical, ok := dockerHubAliases[host]; ok {
		host = canonical
	}
	return host, repo, nil
}
