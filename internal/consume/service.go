// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package consume

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Source is what the aids read: the recorded recipe graph of the store.
// *store.Store satisfies it.
type Source interface {
	RecipeRecords() ([]store.RecipeRecord, error)
}

// Settings is everything a destination-bound aid needs from the
// configuration, resolved once (FromConfig) so the three surfaces — UI, API
// and command line — cannot read the settings differently.
type Settings struct {
	// Registry is the bare host the zone's clients pull from. Empty makes
	// every generation refuse with TBY-AID-001.
	Registry string
	// BasePath is the repository prefix under which the relocated paths sit
	// at Registry.
	BasePath string
	// StoragePrefix is storage.basePrefix: the prefix the recorded paths of
	// THIS store carry, stripped to recover the nominal reference.
	StoragePrefix string
	// CAFile is consumption.caFile, as-is.
	CAFile string
	// Insecure reports that Registry is reached over plain HTTP
	// (registries.insecure), which selects the endpoint scheme.
	Insecure bool
	// Version is the Tobby version stated in the snippet's header.
	Version string
}

// FromConfig resolves the settings of an instance (FR-065 amendment).
//
// The registry is consumption.registry when set, destination.registry
// otherwise. The base path is destination.basePath when the instance
// pushes to a destination — the content then sits where it was pushed —
// and storage.basePrefix when the registry is this instance itself (UC3),
// whose /v2/ serves the store's own paths.
func FromConfig(cfg *config.Config, version string) Settings {
	s := Settings{
		Registry:      cfg.Consumption.Registry,
		StoragePrefix: strings.Trim(cfg.Storage.BasePrefix, "/"),
		CAFile:        cfg.Consumption.CAFile,
		Version:       version,
	}
	if s.Registry == "" {
		s.Registry = cfg.Destination.Registry
	}
	if cfg.Destination.Configured() {
		s.BasePath = strings.Trim(cfg.Destination.BasePath, "/")
	} else {
		s.BasePath = s.StoragePrefix
	}
	s.Registry = canonicalHost(s.Registry)
	for _, h := range cfg.Registries.Insecure {
		if canonicalHost(h) == s.Registry && s.Registry != "" {
			s.Insecure = true
		}
	}
	return s
}

// WithRegistry returns the settings pointed at another registry host (the
// command line's --destination), re-deriving the scheme from the
// insecure list it was resolved against.
func (s Settings) WithRegistry(host string, insecure []string) Settings {
	s.Registry = canonicalHost(host)
	s.Insecure = false
	for _, h := range insecure {
		if canonicalHost(h) == s.Registry && s.Registry != "" {
			s.Insecure = true
		}
	}
	return s
}

// canonicalHost lowercases a registry host and folds the Docker Hub
// aliases, the comparison form of the allowlist (ADR-0013). An unparsable
// value comes back lowercased: the validation of the configuration owns
// the refusal, not this helper.
func canonicalHost(h string) string {
	if h == "" {
		return ""
	}
	if c, err := relocate.Host(h); err == nil {
		return c
	}
	return strings.ToLower(h)
}

// Service computes the aids for one instance.
type Service struct {
	src Source
	set Settings
}

// New builds the service over a recipe graph and its settings.
func New(src Source, set Settings) *Service { return &Service{src: src, set: set} }

// Registry reports the registry host the aids point at, empty when none is
// configured.
func (s *Service) Registry() string { return s.set.Registry }

// Selector picks recipes: a name, optionally pinned to a version.
type Selector struct {
	Name string
	// Version is empty for every recorded version of the recipe.
	Version string
}

// ParseSelector reads "name" or "name@version".
func ParseSelector(v string) (Selector, error) {
	name, version, hasVersion := strings.Cut(v, "@")
	if name == "" || strings.Count(v, "@") > 1 || (hasVersion && version == "") || strings.Contains(v, "/") {
		return Selector{}, fmt.Errorf("recipe selector %q must be a recipe name, optionally name@version", v)
	}
	return Selector{Name: name, Version: version}, nil
}

func (s Selector) String() string {
	if s.Version == "" {
		return s.Name
	}
	return s.Name + "@" + s.Version
}

// Table is the mapping table of one destination: the JSON document of the
// API and of the command line.
type Table struct {
	// Destination is the registry the destination references name.
	Destination string `json:"destination"`
	Entries     []Row  `json:"entries"`
}

// selection resolves the selectors against the recorded graph, ordered by
// recipe name then version so the same store yields the same bytes. No
// selector selects the whole store; a selector matching nothing is a 404,
// never an empty answer that reads as "this recipe has no images".
func (s *Service) selection(sel []Selector) ([]store.RecipeRecord, error) {
	records, err := s.src.RecipeRecords()
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
	var out []store.RecipeRecord
	if len(sel) == 0 {
		out = records
	} else {
		for _, want := range sel {
			found := false
			for i := range records {
				if records[i].Name == want.Name && (want.Version == "" || records[i].Version == want.Version) {
					out = append(out, records[i])
					found = true
				}
			}
			if !found {
				return nil, taxonomy.New(taxonomy.CodeNotFound, nil)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	// A recipe named twice (a name and its name@version) counts once.
	dedup := out[:0]
	for i := range out {
		if len(dedup) > 0 && dedup[len(dedup)-1].Name == out[i].Name && dedup[len(dedup)-1].Version == out[i].Version {
			continue
		}
		dedup = append(dedup, out[i])
	}
	return dedup, nil
}

func (s *Service) requireRegistry() error {
	if s.set.Registry == "" {
		return taxonomy.New(taxonomy.CodeAidsNoRegistry, nil)
	}
	return nil
}

// Table builds the mapping table of the selected recipes.
func (s *Service) Table(sel []Selector) (*Table, error) {
	if err := s.requireRegistry(); err != nil {
		return nil, err
	}
	records, err := s.selection(sel)
	if err != nil {
		return nil, err
	}
	rows, err := s.rows(records)
	if err != nil {
		return nil, err
	}
	return &Table{Destination: s.set.Registry, Entries: rows}, nil
}

// rows maps every ingredient of the records.
func (s *Service) rows(records []store.RecipeRecord) ([]Row, error) {
	rows := make([]Row, 0)
	for i := range records {
		rec := &records[i]
		for j := range rec.Ingredients {
			ing := &rec.Ingredients[j]
			nominal, rest, err := s.nominal(ing.Repo)
			if err != nil {
				return nil, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
					"detail": fmt.Sprintf("recipe %s@%s, ingredient %s: %v", rec.Name, rec.Version, ing.Name, err),
				}).WithCause(err)
			}
			relocated := rest
			if s.set.BasePath != "" {
				relocated = s.set.BasePath + "/" + rest
			}
			rows = append(rows, Row{
				Recipe: rec.Name, RecipeVersion: rec.Version,
				Ingredient: ing.Name, Kind: ing.Kind,
				Nominal: nominal, Relocated: relocated,
				Tag: ing.Tag, Digest: ing.Digest,
				Source:      reference(nominal, ing.Tag, ing.Digest),
				Destination: reference(s.set.Registry+"/"+relocated, ing.Tag, ing.Digest),
			})
		}
	}
	return rows, nil
}

// nominal recovers, from the path this store holds an ingredient under,
// the nominal reference (host[:port]/repository) and the relocated path
// without any prefix: the inverse of relocate.PathWithBase (ADR-0013). The
// recorded path is the only thing the store keeps, so a record whose path
// does not carry the configured storage.basePrefix is refused rather than
// guessed at.
func (s *Service) nominal(repo string) (nominal, unprefixed string, err error) {
	rest := repo
	if p := s.set.StoragePrefix; p != "" {
		if !strings.HasPrefix(repo, p+"/") {
			return "", "", fmt.Errorf("recorded path %q does not start with storage.basePrefix %q: the store was written under another prefix", repo, p)
		}
		rest = strings.TrimPrefix(repo, p+"/")
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok || host == "" || path == "" || !strings.ContainsAny(host, "._") && host != "localhost" {
		return "", "", fmt.Errorf("recorded path %q is not a relocated path (<host>/<repository>): set storage.basePrefix to the prefix this store was written with", repo)
	}
	return strings.ReplaceAll(host, "_", ":") + "/" + path, rest, nil
}

// reference joins a repository to its tag, or to its digest when the
// ingredient carries no tag.
func reference(repo, tag, digest string) string {
	switch {
	case tag != "":
		return repo + ":" + tag
	case digest != "":
		return repo + "@" + digest
	}
	return repo
}
