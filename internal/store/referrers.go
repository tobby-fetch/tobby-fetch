// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	distribution "github.com/distribution/distribution/v3"
	"github.com/opencontainers/go-digest"
)

// The OCI 1.1 Referrers API (distribution-spec "Listing Referrers").
//
// The registry library the store embeds (distribution v3.1) serves neither
// GET /v2/<name>/referrers/<digest> nor the OCI-Subject header, so both are
// provided here, in front of the library's handler. Because the handler is
// wrapped at its source (Store.APIHandler), the surfaces that mount it keep
// every guard they already put in front of /v2/: authentication, the role
// ladder, the serving gate of a transported medium.
//
// The index. For each repository, meta/referrers/<sha256 of the name>.json
// maps a subject digest to the descriptors of the manifests that name it.
// It is maintained as manifests are written (PutManifest, the HTTP PUT) and
// removed (the HTTP DELETE), and it is a CACHE of facts the repository
// already holds: a missing file is rebuilt from the repository's manifest
// revisions on first use, and every garbage collection rebuilds it before
// marking. Losing meta/ loses nothing but that bookkeeping (meta.go).
//
// Garbage collection (FR-044, B-043). A referrer is reachable when its
// subject is: the mark phase follows the index from every reachable
// manifest, transitively. It marks a digest only if the manifest is held
// by THIS repository, like every other mark, so an index entry can never
// pin a blob the repository does not hold. A referrer whose subject is gone
// (or was never pushed) is reachable from nothing and follows the existing
// rule: swept once older than the grace period.

// referrerDescriptor is one entry of a referrers listing: the descriptor of
// the referring manifest plus its artifact type and annotations.
type referrerDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       digest.Digest     `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// referrerFile is the persisted index of one repository.
type referrerFile struct {
	Repository string                                 `json:"repository"`
	Referrers  map[digest.Digest][]referrerDescriptor `json:"referrers"`
}

// maxManifestBody is the library's own bound on a manifest body; a larger
// PUT is passed through untouched (and refused by the library).
const maxManifestBody = 4 << 20

const (
	indexMediaType = "application/vnd.oci.image.index.v1+json"
	apiVersionHdr  = "Docker-Distribution-Api-Version"
)

var (
	referrersPath = regexp.MustCompile(`^/v2/(.+)/referrers/([^/]+)$`)
	manifestPath  = regexp.MustCompile(`^/v2/(.+)/manifests/([^/]+)$`)
)

// manifestFacts is the part of a manifest or index the referrers API reads.
type manifestFacts struct {
	MediaType    string `json:"mediaType"`
	ArtifactType string `json:"artifactType"`
	Config       *struct {
		MediaType string `json:"mediaType"`
	} `json:"config"`
	Subject *struct {
		Digest string `json:"digest"`
	} `json:"subject"`
	Annotations map[string]string `json:"annotations"`
}

// parseSubject extracts the subject digest of a manifest payload. ok is
// false when the payload names no valid subject.
func parseSubject(payload []byte) (subject digest.Digest, f manifestFacts, ok bool) {
	if err := json.Unmarshal(payload, &f); err != nil || f.Subject == nil {
		return "", f, false
	}
	d, err := digest.Parse(f.Subject.Digest)
	if err != nil {
		return "", f, false
	}
	return d, f, true
}

// artifactType is the manifest's artifactType, or, absent that, the media
// type of its config (the spec's rule for image manifests).
func (f manifestFacts) artifactTypeOrConfig() string {
	if f.ArtifactType != "" {
		return f.ArtifactType
	}
	if f.Config != nil {
		return f.Config.MediaType
	}
	return ""
}

func referrerIndexPath(root, repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(root, "meta", "referrers", hex.EncodeToString(sum[:])+".json")
}

// loadReferrerIndex reads the persisted index of a repository; ok is false
// when there is none (or it is unreadable, which is the same instruction:
// rebuild).
func (s *Store) loadReferrerIndex(repo string) (*referrerFile, bool) {
	raw, err := os.ReadFile(referrerIndexPath(s.root, repo))
	if err != nil {
		return nil, false
	}
	var f referrerFile
	if json.Unmarshal(raw, &f) != nil || f.Repository != repo {
		return nil, false
	}
	if f.Referrers == nil {
		f.Referrers = map[digest.Digest][]referrerDescriptor{}
	}
	return &f, true
}

func (s *Store) saveReferrerIndex(f *referrerFile) error {
	if s.inspect {
		return nil // a store opened for inspection stays byte-identical
	}
	return writeJSON(referrerIndexPath(s.root, f.Repository), f)
}

// rebuildReferrerIndex derives a repository's index from its manifest
// revisions. Caller holds refMu.
func (s *Store) rebuildReferrerIndex(ctx context.Context, repo string) (*referrerFile, error) {
	out := &referrerFile{Repository: repo, Referrers: map[digest.Digest][]referrerDescriptor{}}
	r, err := s.repository(ctx, repo)
	if err != nil {
		return nil, err
	}
	ms, err := r.Manifests(ctx)
	if err != nil {
		return nil, mapBrowseErr("opening manifests of "+repo, err)
	}
	en, ok := ms.(distribution.ManifestEnumerator)
	if !ok {
		return nil, fmt.Errorf("store: the manifest service of %s cannot enumerate revisions", repo)
	}
	err = en.Enumerate(ctx, func(d digest.Digest) error {
		m, err := ms.Get(ctx, d)
		if err != nil {
			return nil //nolint:nilerr // an unreadable revision is not a referrer; the integrity check reports it
		}
		mediaType, payload, err := m.Payload()
		if err != nil {
			return nil //nolint:nilerr // see above
		}
		if subject, facts, ok := parseSubject(payload); ok {
			addReferrer(out, subject, referrerDescriptor{
				MediaType: mediaType, Digest: d, Size: int64(len(payload)),
				ArtifactType: facts.artifactTypeOrConfig(), Annotations: facts.Annotations,
			})
		}
		return nil
	})
	if err != nil {
		return nil, mapBrowseErr("enumerating manifests of "+repo, err)
	}
	return out, s.saveReferrerIndex(out)
}

// referrerIndex returns the index of a repository, rebuilding it when it is
// absent. Caller holds refMu.
func (s *Store) referrerIndex(ctx context.Context, repo string) (*referrerFile, error) {
	if f, ok := s.loadReferrerIndex(repo); ok {
		return f, nil
	}
	return s.rebuildReferrerIndex(ctx, repo)
}

func addReferrer(f *referrerFile, subject digest.Digest, d referrerDescriptor) {
	list := f.Referrers[subject]
	for i := range list {
		if list[i].Digest == d.Digest {
			list[i] = d
			f.Referrers[subject] = list
			return
		}
	}
	f.Referrers[subject] = append(list, d)
}

// noteReferrer records a manifest that has just been stored. A payload with
// no subject is a no-op.
func (s *Store) noteReferrer(ctx context.Context, repo, mediaType string, dgst digest.Digest, payload []byte) error {
	subject, facts, ok := parseSubject(payload)
	if !ok {
		return nil
	}
	s.refMu.Lock()
	defer s.refMu.Unlock()
	f, err := s.referrerIndex(ctx, repo)
	if err != nil {
		return err
	}
	addReferrer(f, subject, referrerDescriptor{
		MediaType: mediaType, Digest: dgst, Size: int64(len(payload)),
		ArtifactType: facts.artifactTypeOrConfig(), Annotations: facts.Annotations,
	})
	return s.saveReferrerIndex(f)
}

// forgetReferrer removes a deleted manifest from the index, both as a
// referrer and as a subject's key (its referrers stay: they are manifests
// in their own right, and the garbage collector decides their fate).
func (s *Store) forgetReferrer(repo string, dgst digest.Digest) error {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	f, ok := s.loadReferrerIndex(repo)
	if !ok {
		return nil // nothing persisted: the next use rebuilds from the repository
	}
	changed := false
	for subject, list := range f.Referrers {
		kept := list[:0]
		for _, d := range list {
			if d.Digest == dgst {
				changed = true
				continue
			}
			kept = append(kept, d)
		}
		if len(kept) == 0 {
			delete(f.Referrers, subject)
		} else {
			f.Referrers[subject] = kept
		}
	}
	if !changed {
		return nil
	}
	return s.saveReferrerIndex(f)
}

// dropReferrerIndex forgets a repository's index (the repository is gone).
func (s *Store) dropReferrerIndex(repo string) {
	if s.inspect {
		return
	}
	_ = os.Remove(referrerIndexPath(s.root, repo))
}

// RebuildReferrers regenerates the referrers index of every repository from
// the manifests they hold, replacing whatever is persisted. It is what a
// garbage collection does implicitly, exposed for the integrity tooling.
func (s *Store) RebuildReferrers(ctx context.Context) error {
	repos, err := s.Repositories(ctx)
	if err != nil {
		return err
	}
	s.refMu.Lock()
	defer s.refMu.Unlock()
	for _, repo := range repos {
		if _, err := s.rebuildReferrerIndex(ctx, repo); err != nil {
			return err
		}
	}
	return nil
}

// Referrers lists the manifests of a repository whose subject is subject,
// optionally restricted to an artifact type. Entries whose manifest has
// since left the repository are left out.
func (s *Store) Referrers(ctx context.Context, repo string, subject digest.Digest, artifactType string) ([]referrerDescriptor, error) {
	s.refMu.Lock()
	f, err := s.referrerIndex(ctx, repo)
	var list []referrerDescriptor
	if err == nil {
		list = append(list, f.Referrers[subject]...)
	}
	s.refMu.Unlock()
	if err != nil {
		return nil, err
	}
	r, err := s.repository(ctx, repo)
	if err != nil {
		return nil, err
	}
	ms, err := r.Manifests(ctx)
	if err != nil {
		return nil, mapBrowseErr("opening manifests of "+repo, err)
	}
	out := []referrerDescriptor{}
	for _, d := range list {
		if artifactType != "" && d.ArtifactType != artifactType {
			continue
		}
		if ok, err := ms.Exists(ctx, d.Digest); err != nil || !ok {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out, nil
}

// markReferrers extends a repository's reachable set with the referrers of
// everything already reachable, transitively. The index was refreshed by
// the caller (sweep), so it reflects what the repository holds.
func (s *Store) markReferrers(ctx context.Context, name string, ms distribution.ManifestService, reach map[digest.Digest]bool) error {
	s.refMu.Lock()
	f, ok := s.loadReferrerIndex(name)
	s.refMu.Unlock()
	if !ok || len(f.Referrers) == 0 {
		return nil
	}
	for {
		grew := false
		for subject, list := range f.Referrers {
			if !reach[subject] {
				continue
			}
			for _, d := range list {
				if reach[d.Digest] {
					continue
				}
				// markManifest marks the digest only if THIS repository
				// holds the manifest (B-043), and what it references.
				if err := s.markManifest(ctx, ms, d.Digest, reach); err != nil {
					return err
				}
				if reach[d.Digest] {
					grew = true
				}
			}
		}
		if !grew {
			return nil
		}
	}
}

// ── HTTP ────────────────────────────────────────────────────────────────

// referrersHandler puts the Referrers API in front of the library's
// handler.
func (s *Store) referrersHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			if m := referrersPath.FindStringSubmatch(r.URL.Path); m != nil {
				s.serveReferrers(w, r, m[1], m[2])
				return
			}
		case http.MethodPut:
			if m := manifestPath.FindStringSubmatch(r.URL.Path); m != nil {
				s.putManifestHTTP(next, w, r, m[1])
				return
			}
		case http.MethodDelete:
			if m := manifestPath.FindStringSubmatch(r.URL.Path); m != nil {
				s.deleteManifestHTTP(next, w, r, m[1], m[2])
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeRegistryError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(apiVersionHdr, "registry/2.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": message}}})
}

func (s *Store) serveReferrers(w http.ResponseWriter, r *http.Request, repo, ref string) {
	subject, err := digest.Parse(ref)
	if err != nil {
		writeRegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "the subject is not a valid digest")
		return
	}
	if _, err := s.repository(r.Context(), repo); err != nil || !s.repositoryExists(repo) {
		writeRegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	filter := ""
	if vals, ok := r.URL.Query()["artifactType"]; ok && len(vals) > 0 {
		filter = vals[0]
	}
	list, err := s.Referrers(r.Context(), repo, subject, filter)
	if err != nil {
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "listing referrers failed")
		return
	}
	body, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     indexMediaType,
		"manifests":     list,
	})
	if err != nil {
		writeRegistryError(w, http.StatusInternalServerError, "UNKNOWN", "encoding referrers failed")
		return
	}
	w.Header().Set("Content-Type", indexMediaType)
	w.Header().Set(apiVersionHdr, "registry/2.0")
	if filter != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// repositoryExists reports whether the repository has any content on disk.
// The name has been validated as a repository reference by the caller (no
// "..", no absolute path): see serveReferrers.
func (s *Store) repositoryExists(repo string) bool {
	_, err := os.Stat(filepath.Join(s.root, "docker", "registry", "v2", "repositories", filepath.FromSlash(repo))) //nolint:gosec // G703: repo was parsed by reference.WithName, which rejects path elements like ".."
	return err == nil
}

// recorder captures what the library answered and injects OCI-Subject on a
// successful manifest PUT.
type recorder struct {
	http.ResponseWriter
	subject string
	status  int
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
		if code == http.StatusCreated && r.subject != "" {
			r.Header().Set("OCI-Subject", r.subject)
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Store) putManifestHTTP(next http.Handler, w http.ResponseWriter, r *http.Request, repo string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxManifestBody+1))
	if err != nil || len(body) > maxManifestBody {
		// Unreadable or oversized: the library's own limits answer.
		r.Body = readCloser{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		next.ServeHTTP(w, r)
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	rec := &recorder{ResponseWriter: w}
	subject, _, hasSubject := parseSubject(body)
	if hasSubject {
		rec.subject = subject.String()
	}
	next.ServeHTTP(rec, r)
	if rec.status != http.StatusCreated || !hasSubject {
		return
	}
	dgst, err := digest.Parse(w.Header().Get("Docker-Content-Digest"))
	if err != nil {
		return
	}
	if err := s.noteReferrer(r.Context(), repo, r.Header.Get("Content-Type"), dgst, body); err != nil {
		// The manifest is stored; only the cache failed. Forget the file so
		// the next use rebuilds it from the repository.
		s.dropReferrerIndex(repo)
	}
}

type readCloser struct {
	io.Reader
	io.Closer
}

func (s *Store) deleteManifestHTTP(next http.Handler, w http.ResponseWriter, r *http.Request, repo, ref string) {
	rec := &recorder{ResponseWriter: w}
	next.ServeHTTP(rec, r)
	if rec.status != http.StatusAccepted {
		return
	}
	// A tag reference removes the tag only; the manifest stays.
	if d, err := digest.Parse(unescape(ref)); err == nil {
		if err := s.forgetReferrer(repo, d); err != nil {
			s.dropReferrerIndex(repo)
		}
	}
}

func unescape(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}
