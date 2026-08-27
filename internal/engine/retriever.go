// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"
)

// retrieverTimeout bounds one desired-state fetch. The document is a few
// kilobytes of YAML; a minute is generous even across a slow proxy.
const retrieverTimeout = 60 * time.Second

// LoadRetriever fetches and validates the desired-state document from the
// configured source (FR-010): an HTTP(S) URL, an OCI reference (with or
// without the helm-style oci:// prefix), or a local file path. The
// configured source string is what the UI and the API report.
func LoadRetriever(ctx context.Context, remotes *Remotes, source string) (*spec.Retriever, error) {
	raw, err := readRetrieverSource(ctx, remotes, source)
	if err != nil {
		return nil, err
	}
	return spec.ParseRetriever(raw)
}

// readRetrieverSource resolves the three source forms. Disambiguation:
// an explicit scheme decides; otherwise an existing file wins, and a
// host-qualified reference is tried as OCI last — so a mistyped file path
// yields the file-not-found error, not a confusing registry one.
func readRetrieverSource(ctx context.Context, remotes *Remotes, source string) ([]byte, error) {
	switch {
	case source == "":
		return nil, fmt.Errorf("retriever.source is not configured: set an HTTP(S) URL, an OCI reference, or a file path (FR-010)")
	case strings.HasPrefix(source, "http://"), strings.HasPrefix(source, "https://"):
		return fetchRetrieverURL(ctx, remotes, source)
	case strings.HasPrefix(source, "oci://"):
		return fetchRetrieverOCI(ctx, remotes, strings.TrimPrefix(source, "oci://"))
	}
	if _, err := os.Stat(source); err == nil {
		raw, err := os.ReadFile(source) //nolint:gosec // G304: operator-configured retriever path (FR-010)
		if err != nil {
			return nil, fmt.Errorf("retriever file %s: %w", source, err)
		}
		return raw, nil
	}
	if host, _, ok := strings.Cut(source, "/"); ok && !hasDriveDesignator(source) && (strings.ContainsAny(host, ".:") || host == "localhost") {
		return fetchRetrieverOCI(ctx, remotes, source)
	}
	return nil, fmt.Errorf("retriever file %s does not exist (and the value is not an URL or an OCI reference)", source)
}

// hasDriveDesignator reports whether source opens with a Windows drive
// designator, which makes it a file path and never a registry reference.
//
// Without it "C:/config/retriever.yaml" — a perfectly ordinary value, as
// forward slashes are legal on Windows and usual in YAML — cuts to the
// host "C:", whose colon reads as a port and sends a local file to a
// registry called "C:". That is precisely the confusion the
// disambiguation order above exists to prevent (NFR-018).
//
// The check is spelled out lexically rather than delegated to
// filepath.VolumeName because that function returns "" unless the binary
// was built for Windows. A configuration file is read on the machine it
// is deployed to, so how its source is classified must not depend on
// where the binary was compiled — nor on which platform ran the tests.
func hasDriveDesignator(source string) bool {
	if len(source) < 2 || source[1] != ':' {
		return false
	}
	c := source[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// fetchRetrieverURL reads the document over HTTP(S) through the
// instance's shared outbound transport (FR-080, FR-081): the desired
// state is fetched from wherever the operator publishes it, which in a
// segmented zone is reachable only through the proxy — and is exactly
// the kind of path that gets forgotten because it is not a registry.
func fetchRetrieverURL(ctx context.Context, remotes *Remotes, url string) ([]byte, error) {
	client := remotes.Egress().Client(retrieverTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching retriever %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching retriever %s: HTTP %d", url, resp.StatusCode)
	}
	return readBounded(resp.Body, maxRecipeBytes)
}

// fetchRetrieverOCI pulls the document published as an OCI artifact: a
// reference "host/repo[:tag]", single YAML layer (the retriever follows
// the recipe envelope layout, §11.2 applied to a Retriever document).
func fetchRetrieverOCI(ctx context.Context, remotes *Remotes, reference string) ([]byte, error) {
	repoPath, tag := reference, "latest"
	if i := strings.LastIndexByte(reference, ':'); i > strings.LastIndexByte(reference, '/') {
		repoPath, tag = reference[:i], reference[i+1:]
	}
	desc, err := remotes.Get(ctx, repoPath, tag)
	if err != nil {
		return nil, fmt.Errorf("fetching retriever %s: %w", reference, err)
	}
	var man artifactManifest
	if err := json.Unmarshal(desc.Manifest, &man); err != nil {
		return nil, fmt.Errorf("retriever artifact %s: unparseable manifest: %w", reference, err)
	}
	if len(man.Layers) != 1 {
		return nil, fmt.Errorf("retriever artifact %s: %d layers, want exactly one document layer", reference, len(man.Layers))
	}
	repo, _, err := remotes.Repository(repoPath)
	if err != nil {
		return nil, err
	}
	layer, err := remote.Layer(repo.Digest(man.Layers[0].Digest), remotes.options(ctx)...)
	if err != nil {
		return nil, err
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, err
	}
	defer func(c io.Closer) { _ = c.Close() }(rc)
	return readBounded(rc, maxRecipeBytes)
}
