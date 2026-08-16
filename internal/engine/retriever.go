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
		return fetchRetrieverURL(ctx, source)
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
	if host, _, ok := strings.Cut(source, "/"); ok && (strings.ContainsAny(host, ".:") || host == "localhost") {
		return fetchRetrieverOCI(ctx, remotes, source)
	}
	return nil, fmt.Errorf("retriever file %s does not exist (and the value is not an URL or an OCI reference)", source)
}

// fetchRetrieverURL reads the document over HTTP(S).
func fetchRetrieverURL(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
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
