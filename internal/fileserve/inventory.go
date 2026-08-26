// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"context"
	"sort"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
)

// The FileSet inventory: what this instance holds and what it serves, in
// one list, computed once for both the screen and the API endpoint
// (FR-061 — parity is cheapest when there is only one answer to give).
//
// Two populations meet here. FileSets delivered by a Recipe arrived
// verified from a cookbook, under their source registry's host. FileSets
// packed on this host (FR-048) arrived from a local directory, under the
// reserved localhost namespace, unsigned and recorded as manual imports.
// FR-048 requires the second kind to be distinguishable from the first
// in listings, and Provenance is what distinguishes them.

// Provenance values of an inventory entry.
const (
	// FromRecipe is content a Recipe manages: verified on arrival, and
	// removed by removing the Recipe (FR-044).
	FromRecipe = "recipe"
	// FromManualImport is a FileSet packed on this host (FR-048):
	// unsigned, of local origin, never pruned (FR-045) because nothing
	// would bring it back.
	FromManualImport = "manual-import"
	// FromUnitImport is a FileSet imported one-off from a registry
	// (FR-023).
	FromUnitImport = "unit-import"
	// FromSeed is content pushed through /v2/ by a standard client, which
	// leaves no record (UC3 seeding).
	FromSeed = "seed"
)

// Catalog is the store read surface the inventory needs. The provenance
// is flattened to plain strings so this package keeps knowing nothing
// about the store's types.
type Catalog interface {
	// Repositories returns every repository of the store, sorted.
	Repositories(ctx context.Context) ([]string, error)
	// Tags returns the tags of one repository, sorted ascending.
	Tags(ctx context.Context, repo string) ([]string, error)
	// Provenance returns one of the From* values for repo.
	Provenance(repo string) string
}

// Declared is one files.filesets entry (FR-047), flattened.
type Declared struct {
	Name      string
	Ref       string
	Version   string
	Anonymous bool
}

// Entry is one FileSet of the inventory.
type Entry struct {
	// Name is the /files/<name>/ segment; empty for a FileSet held but
	// not declared for serving.
	Name string
	// Reference is the nominal reference — what goes in files.filesets[].ref.
	Reference string
	// Repository is where the content sits in the store.
	Repository string
	// Versions are the tags held locally, ascending.
	Versions []string
	// Version pins the declared tag, when the declaration pins one.
	Version string
	// Digest is the served manifest; empty when the FileSet is not served.
	Digest string
	// Provenance is one of the From* values.
	Provenance string
	// Declared says the FileSet appears in files.filesets.
	Declared bool
	// Served says the instance is currently serving it under /files/.
	Served bool
	// Anonymous says reads of it need no credentials — an opt-in
	// reported like the FR-075 override, never silent.
	Anonymous bool
	// Signed says the entry can carry a signature at all. Packing
	// produces unsigned content and Tobby holds no key (ADR-0007), so a
	// manual import is never signed — and every surface says so rather
	// than leaving the question open.
	Signed bool
	// URL is the base path the FileSet is served under, empty when it is
	// not served.
	URL string
}

// Surface is the dependency set of the two FileSet surfaces — the screen
// and the REST endpoints. One type and one Inventory for both: FR-061
// parity is cheapest when there is a single answer to give, and a second
// implementation of "which FileSets exist" is a second answer.
type Surface struct {
	// Catalog is the store read surface.
	Catalog Catalog
	// Packer performs FR-048 packing. On the remote surfaces it is always
	// built with WithPackRoots.
	Packer *Packer
	// Served reports what the instance is serving right now.
	Served func() []FileSet
	// Declared is the files.filesets configuration (FR-047).
	Declared []Declared
	// BasePrefix is the instance's relocation base prefix (FR-035).
	BasePrefix string
	// PackEnabled says files.packRoots names at least one directory. With
	// none, packing from this surface is refused and the surface says so
	// rather than only failing (FR-075).
	PackEnabled bool
}

// Inventory lists what this instance holds and what it serves.
func (s *Surface) Inventory(ctx context.Context) ([]Entry, error) {
	var served []FileSet
	if s.Served != nil {
		served = s.Served()
	}
	return inventory(ctx, s.Catalog, s.Declared, served, s.BasePrefix)
}

// Pack packages a local directory through the surface's confined packer.
func (s *Surface) Pack(ctx context.Context, req PackRequest) (*PackResult, error) {
	return s.Packer.Pack(ctx, req)
}

// inventory merges the declared FileSets (FR-047 configuration), what the
// server is actually serving, and the FileSets packed on this host
// (FR-048) into one list ordered by reference.
//
// A declared FileSet with no content in the store yet is listed, not
// hidden: "declared but not served" is the state an operator has to be
// able to see — it is what a FileSet looks like before its Recipe has
// been synchronized, and hiding it turns a waiting state into a mystery.
func inventory(ctx context.Context, cat Catalog, declared []Declared, served []FileSet, basePrefix string) ([]Entry, error) {
	byRepo := map[string]*Entry{}
	var order []string

	add := func(repo string, e Entry) {
		if _, ok := byRepo[repo]; ok {
			return
		}
		e.Repository = repo
		byRepo[repo] = &e
		order = append(order, repo)
	}

	for _, d := range declared {
		repo, err := relocate.PathWithBase(basePrefix, d.Ref)
		if err != nil {
			// A malformed reference is a configuration error the startup
			// validation already reports; the inventory skips it rather
			// than failing the whole listing.
			continue
		}
		add(repo, Entry{
			Name:      d.Name,
			Reference: d.Ref,
			Version:   d.Version,
			Anonymous: d.Anonymous,
			Declared:  true,
		})
	}

	// Everything under the reserved namespace is a packed FileSet, and
	// the store is the authority on which ones exist: one packed and
	// never declared still has to appear, or the screen would hide the
	// content it just produced.
	repos, err := cat.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	prefix := packedPrefix(basePrefix)
	for _, repo := range repos {
		if !strings.HasPrefix(repo, prefix) {
			continue
		}
		add(repo, Entry{
			Name:      strings.TrimPrefix(repo, prefix),
			Reference: PackReference(strings.TrimPrefix(repo, prefix)),
		})
	}

	servedByRepo := map[string]FileSet{}
	for _, s := range served {
		servedByRepo[s.Repo] = s
	}

	out := make([]Entry, 0, len(order))
	for _, repo := range order {
		e := byRepo[repo]
		if s, ok := servedByRepo[repo]; ok {
			e.Served = true
			e.Digest = s.ManifestDigest
			e.Anonymous = s.Anonymous
		}
		e.Provenance = cat.Provenance(repo)
		e.Signed = e.Provenance != FromManualImport
		if e.Served {
			e.URL = RoutePrefix + e.Name + "/"
		}
		if tags, err := cat.Tags(ctx, repo); err == nil {
			e.Versions = filterVersionTags(tags)
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Reference < out[j].Reference })
	return out, nil
}

// packedPrefix is the store path every packed FileSet sits under.
func packedPrefix(basePrefix string) string {
	base := strings.Trim(basePrefix, "/")
	prefix := PackedHost + "/" + PackedNamespace + "/"
	if base == "" {
		return prefix
	}
	return base + "/" + prefix
}

// filterVersionTags drops the cosign tag convention (`sha256-<hex>`),
// which addresses attached signatures rather than a version of the
// content.
func filterVersionTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if !strings.HasPrefix(t, "sha256-") {
			out = append(out, t)
		}
	}
	return out
}
