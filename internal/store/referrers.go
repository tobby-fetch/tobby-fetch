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
// The index is a HINT, never an answer (B-064). It lives under meta/, where
// an altered file on a transported medium is only a finding, so nothing it
// says is served: a listing re-reads every candidate manifest, keeps it only
// if its own payload names the subject asked about, and describes it from
// that payload — media type, size, artifact type and annotations. A forged
// entry pointing at a manifest of the repository is therefore dropped, and
// a forged description of a real referrer is replaced by the real one. On a
// guarded destination instance the candidates are not even read from the
// index: they are the verified manifests of the repository (WithReferrerScope).
//
// A READ never writes (B-064): an absent index is derived in memory and kept
// there until the next write to that repository, so a GET on a read-only
// medium answers, and a GET on a writable one leaves meta/ byte-identical.
//
// Bounds (R5 of the milestone-7 review). An index entry carries no
// annotations — they are served from the payload — so an entry is a few
// hundred bytes whatever a client pushed. A subject holds at most
// maxReferrersPerSubject referrers; the PUT of one more is refused before it
// is stored. Each indexed PUT rewrites the repository's index, which is
// linear in the entries of that one repository: bounded per subject, and
// left unsharded deliberately — the write is an operator push, and a
// per-subject layout would be a store format change for a cache.
//
// Garbage collection (FR-044, B-043). A referrer is reachable when its
// subject is: the mark phase follows the index from every reachable
// manifest, transitively. It marks a digest only if the manifest is held
// by THIS repository, like every other mark, so an index entry can never
// pin a blob the repository does not hold. A referrer whose subject is gone
// (or was never pushed) is reachable from nothing and follows the existing
// rule: swept once older than the grace period.

// referrerDescriptor is one entry of a referrers listing: the descriptor of
// the referring manifest plus its artifact type and annotations — every
// field read from the manifest's own payload.
type referrerDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       digest.Digest     `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// referrerEntry is one entry of the persisted index: which manifest names
// the subject. The other fields are informational (the listing re-reads
// the payload); annotations are deliberately not indexed (R5).
type referrerEntry struct {
	MediaType    string        `json:"mediaType"`
	Digest       digest.Digest `json:"digest"`
	Size         int64         `json:"size"`
	ArtifactType string        `json:"artifactType,omitempty"`
}

// referrerFile is the persisted index of one repository.
type referrerFile struct {
	Repository string                            `json:"repository"`
	Referrers  map[digest.Digest][]referrerEntry `json:"referrers"`
}

// maxReferrersPerSubject bounds the referrers one subject may carry (R5).
// Signatures, attestations, SBOMs and VEX documents of one image are a
// handful; thousands is a client gone wrong, or someone filling the index.
var maxReferrersPerSubject = 4096

// ReferrerScope names the candidate referrers of a repository a caller
// vouches for. A guarded destination instance passes the verified manifests
// of the repository (internal/mediagate), so neither a forged index entry
// nor a forged omission can change what it lists.
type ReferrerScope func(repo string) []digest.Digest

type referrerScopeKey struct{}

// WithReferrerScope restricts the referrers listings served under ctx to
// the candidates scope names.
func WithReferrerScope(ctx context.Context, scope ReferrerScope) context.Context {
	return context.WithValue(ctx, referrerScopeKey{}, scope)
}

func referrerScopeOf(ctx context.Context) ReferrerScope {
	s, _ := ctx.Value(referrerScopeKey{}).(ReferrerScope)
	return s
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
		f.Referrers = map[digest.Digest][]referrerEntry{}
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
// revisions and persists it. Caller holds refMu. Only write paths call it
// (a manifest write, a garbage collection, the integrity tooling).
func (s *Store) rebuildReferrerIndex(ctx context.Context, repo string) (*referrerFile, error) {
	out, err := s.deriveReferrerIndex(ctx, repo)
	if err != nil {
		return nil, err
	}
	delete(s.refMemo, repo)
	return out, s.saveReferrerIndex(out)
}

// deriveReferrerIndex computes a repository's index from its manifest
// revisions, in memory. Caller holds refMu.
func (s *Store) deriveReferrerIndex(ctx context.Context, repo string) (*referrerFile, error) {
	out := &referrerFile{Repository: repo, Referrers: map[digest.Digest][]referrerEntry{}}
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
			addReferrer(out, subject, referrerEntry{
				MediaType: mediaType, Digest: d, Size: int64(len(payload)),
				ArtifactType: facts.artifactTypeOrConfig(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, mapBrowseErr("enumerating manifests of "+repo, err)
	}
	return out, nil
}

// referrerIndex returns the index of a repository, rebuilding (and
// persisting) it when it is absent. Caller holds refMu. Write paths only.
func (s *Store) referrerIndex(ctx context.Context, repo string) (*referrerFile, error) {
	if f, ok := s.loadReferrerIndex(repo); ok {
		return f, nil
	}
	return s.rebuildReferrerIndex(ctx, repo)
}

// referrerIndexForRead returns the index of a repository for a listing:
// the persisted file, or one derived in memory and kept there until the
// next write to the repository. It never writes (B-064). Caller holds
// refMu.
func (s *Store) referrerIndexForRead(ctx context.Context, repo string) (*referrerFile, error) {
	if f, ok := s.loadReferrerIndex(repo); ok {
		return f, nil
	}
	if f, ok := s.refMemo[repo]; ok {
		return f, nil
	}
	f, err := s.deriveReferrerIndex(ctx, repo)
	if err != nil {
		return nil, err
	}
	if s.refMemo == nil {
		s.refMemo = map[string]*referrerFile{}
	}
	s.refMemo[repo] = f
	return f, nil
}

// referrerCount is how many referrers the index knows for subject, and
// whether d is one of them.
func (s *Store) referrerCount(ctx context.Context, repo string, subject, d digest.Digest) (n int, known bool, err error) {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	f, err := s.referrerIndexForRead(ctx, repo)
	if err != nil {
		return 0, false, err
	}
	for _, e := range f.Referrers[subject] {
		if e.Digest == d {
			known = true
		}
	}
	return len(f.Referrers[subject]), known, nil
}

func addReferrer(f *referrerFile, subject digest.Digest, d referrerEntry) {
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
	delete(s.refMemo, repo)
	f, err := s.referrerIndex(ctx, repo)
	if err != nil {
		return err
	}
	addReferrer(f, subject, referrerEntry{
		MediaType: mediaType, Digest: dgst, Size: int64(len(payload)),
		ArtifactType: facts.artifactTypeOrConfig(),
	})
	return s.saveReferrerIndex(f)
}

// forgetReferrer removes a deleted manifest from the index, both as a
// referrer and as a subject's key (its referrers stay: they are manifests
// in their own right, and the garbage collector decides their fate).
func (s *Store) forgetReferrer(repo string, dgst digest.Digest) error {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	delete(s.refMemo, repo)
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
	s.refMu.Lock()
	delete(s.refMemo, repo)
	s.refMu.Unlock()
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
// optionally restricted to an artifact type.
//
// Every candidate is re-read and kept only if its own payload hashes to its
// digest and names the subject, and every field of its descriptor comes
// from that payload (B-064): the index only says where to look. Under
// WithReferrerScope the candidates are the scope's, not the index's. A
// listing never writes.
func (s *Store) Referrers(ctx context.Context, repo string, subject digest.Digest, artifactType string) ([]referrerDescriptor, error) {
	var candidates []digest.Digest
	if scope := referrerScopeOf(ctx); scope != nil {
		candidates = scope(repo)
	} else {
		s.refMu.Lock()
		f, err := s.referrerIndexForRead(ctx, repo)
		if err == nil {
			for _, e := range f.Referrers[subject] {
				candidates = append(candidates, e.Digest)
			}
		}
		s.refMu.Unlock()
		if err != nil {
			return nil, err
		}
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
	seen := map[digest.Digest]bool{}
	for _, d := range candidates {
		if seen[d] {
			continue
		}
		seen[d] = true
		desc, ok := readReferrer(ctx, ms, d, subject)
		if !ok || (artifactType != "" && desc.ArtifactType != artifactType) {
			continue
		}
		out = append(out, desc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out, nil
}

// readReferrer reads one candidate manifest and describes it from its own
// payload, if that payload hashes to d and names subject.
func readReferrer(ctx context.Context, ms distribution.ManifestService, d, subject digest.Digest) (referrerDescriptor, bool) {
	m, err := ms.Get(ctx, d)
	if err != nil {
		return referrerDescriptor{}, false
	}
	mediaType, payload, err := m.Payload()
	if err != nil || d.Algorithm() != digest.SHA256 || digest.FromBytes(payload) != d {
		return referrerDescriptor{}, false
	}
	got, facts, ok := parseSubject(payload)
	if !ok || got != subject {
		return referrerDescriptor{}, false
	}
	if facts.MediaType != "" {
		mediaType = facts.MediaType
	}
	return referrerDescriptor{
		MediaType: mediaType, Digest: d, Size: int64(len(payload)),
		ArtifactType: facts.artifactTypeOrConfig(), Annotations: facts.Annotations,
	}, true
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
		// R5: a subject's referrers are bounded, and the refusal comes
		// BEFORE the manifest is stored, not after.
		n, known, err := s.referrerCount(r.Context(), repo, subject, digest.FromBytes(body))
		if err == nil && !known && n >= maxReferrersPerSubject {
			writeRegistryError(w, http.StatusBadRequest, "DENIED",
				fmt.Sprintf("subject %s already has %d referrers, the most this registry indexes", subject, maxReferrersPerSubject))
			return
		}
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
