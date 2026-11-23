// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Served is the content a verification vouches for: the union of what the
// PUSHABLE recipes reach on the medium — their recipe artifacts, the
// signature artifacts that travel with them and with their ingredients,
// the ingredients, and every manifest, index child and blob those name —
// each file of which this verification has just re-hashed against its
// inventory entry (B-063).
//
// It is what a guarded destination instance serves, and the ONLY thing
// it serves (FR-054: "content present on the media but not reachable from
// a verified recipe SHALL NOT be pushed"; serving is held to the same
// set). Anything else the medium carries — a tag added after the manifest
// was written, an inventoried file no recipe reaches, the content of a
// blocked recipe — is never handed out by /v2/ or /files/, whatever the
// findings say about it.
//
// The set is computed during the walk, never re-read from the medium
// afterwards, and its Digest is a hash over its canonical listing, so the
// log line that opens the gate names exactly what was opened.
type Served struct {
	repos map[string]*servedRepo
	// Digest is "sha256:<hex>" over the canonical listing (see digest).
	Digest string
}

type servedRepo struct {
	manifests map[string]bool
	blobs     map[string]bool
	// tags maps a tag to the digest it pointed at WHEN VERIFIED. A tag
	// moved on the medium afterwards keeps resolving to this digest.
	tags map[string]string
}

func newServed() *Served { return &Served{repos: map[string]*servedRepo{}} }

func (s *Served) repo(name string) *servedRepo {
	r := s.repos[name]
	if r == nil {
		r = &servedRepo{manifests: map[string]bool{}, blobs: map[string]bool{}, tags: map[string]string{}}
		s.repos[name] = r
	}
	return r
}

// merge adds a recipe's reach to the set.
func (s *Served) merge(o *Served) {
	for name, src := range o.repos {
		dst := s.repo(name)
		for d := range src.manifests {
			dst.manifests[d] = true
		}
		for d := range src.blobs {
			dst.blobs[d] = true
		}
		for t, d := range src.tags {
			dst.tags[t] = d
		}
	}
}

// seal computes the digest over the canonical listing: one line per
// (repository, kind, value), sorted.
func (s *Served) seal() {
	var lines []string
	for name, r := range s.repos {
		for d := range r.manifests {
			lines = append(lines, name+"\tmanifest\t"+d)
		}
		for d := range r.blobs {
			lines = append(lines, name+"\tblob\t"+d)
		}
		for t, d := range r.tags {
			lines = append(lines, name+"\ttag\t"+t+"="+d)
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	s.Digest = "sha256:" + hex.EncodeToString(sum[:])
}

// Empty reports whether the set vouches for nothing at all.
func (s *Served) Empty() bool { return s == nil || len(s.repos) == 0 }

// Repositories lists the repositories the set reaches, sorted.
func (s *Served) Repositories() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.repos))
	for name := range s.repos {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// HasRepository reports whether the set reaches anything in repo.
func (s *Served) HasRepository(repo string) bool {
	if s == nil {
		return false
	}
	_, ok := s.repos[repo]
	return ok
}

// Manifest reports whether repo@dgst is a verified manifest or index.
func (s *Served) Manifest(repo, dgst string) bool {
	if s == nil {
		return false
	}
	r := s.repos[repo]
	return r != nil && r.manifests[dgst]
}

// Blob reports whether repo@dgst is a verified blob — a layer or a config,
// or a manifest fetched through the blob endpoint.
func (s *Served) Blob(repo, dgst string) bool {
	if s == nil {
		return false
	}
	r := s.repos[repo]
	return r != nil && (r.blobs[dgst] || r.manifests[dgst])
}

// Tag resolves a tag to the digest it pointed at when verified.
func (s *Served) Tag(repo, tag string) (string, bool) {
	if s == nil {
		return "", false
	}
	r := s.repos[repo]
	if r == nil {
		return "", false
	}
	d, ok := r.tags[tag]
	return d, ok
}

// Tags lists the verified tags of repo, sorted.
func (s *Served) Tags(repo string) []string {
	if s == nil {
		return nil
	}
	r := s.repos[repo]
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.tags))
	for t := range r.tags {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Manifests lists the verified manifests of repo, sorted.
func (s *Served) Manifests(repo string) []string {
	if s == nil {
		return nil
	}
	r := s.repos[repo]
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.manifests))
	for d := range r.manifests {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// servedSummary is how the set serializes in the report: its digest and
// its volumetry. The listing itself stays in the process that verified —
// a medium of ten thousand blobs would otherwise put ten thousand digests
// in every poll of the Media screen.
type servedSummary struct {
	Digest       string `json:"digest"`
	Repositories int    `json:"repositories"`
	Manifests    int    `json:"manifests"`
	Blobs        int    `json:"blobs"`
	Tags         int    `json:"tags"`
}

// MarshalJSON writes the summary.
func (s *Served) MarshalJSON() ([]byte, error) {
	sum := servedSummary{Digest: s.Digest, Repositories: len(s.repos)}
	for _, r := range s.repos {
		sum.Manifests += len(r.manifests)
		sum.Blobs += len(r.blobs)
		sum.Tags += len(r.tags)
	}
	return json.Marshal(sum)
}

// UnmarshalJSON reads the summary back. The result vouches for nothing:
// only a verification in this process builds a set that serves (the
// gate never accepts a report that travelled through JSON).
func (s *Served) UnmarshalJSON(raw []byte) error {
	var sum servedSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		return err
	}
	*s = Served{Digest: sum.Digest}
	return nil
}
