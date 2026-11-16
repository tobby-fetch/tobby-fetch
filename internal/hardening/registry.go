// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package hardening

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Ledger is what the registry guard needs of the store: recording a
// pushed repository as seeded content, and classifying it for the log.
type Ledger interface {
	RecordSeed(repo string) (bool, error)
	Classify(repo string) store.Classification
}

// Registry wraps the embedded registry's /v2/ handler. Mounted INSIDE the
// authentication middleware, so every refusal names the identity that
// asked.
//
// With DisableRegistryPush every write — blob upload start, chunk and
// commit, cross-repository mount, manifest put, deletion: anything that
// is not GET or HEAD — answers the OCI DENIED error carrying TBY-POL-006,
// for every client and every role, administrators included, and is
// audited. Pulls pass untouched. Recipe synchronizations never come
// through here: they write the store directly (ADR-0005).
//
// Without it, a manifest a client pushed records its repository as seeded
// content (FR-098: class seed) unless the ledger already knows better,
// and the push and the removal of a manifest are logged with the
// repository's provenance (FR-090).
func Registry(sw Switches, ledger Ledger, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if sw.DisableRegistryPush {
			id, _ := auth.IdentityFrom(r.Context())
			repo, _ := manifestTarget(r.URL.Path)
			if repo == "" {
				repo = repositoryOf(r.URL.Path)
			}
			AuditRefused(r.Context(), logger, id.Name, auth.ClientOrigin(r), repo, SettingDisableRegistryPush)
			mediagate.RegistryRefusal(w, r, RegistryPushRefusal())
			return
		}
		repo, ref := manifestTarget(r.URL.Path)
		if repo == "" || (r.Method != http.MethodPut && r.Method != http.MethodDelete) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		observeManifest(r, ledger, logger, repo, ref, rec.status)
	})
}

// observeManifest records and logs one manifest write the registry
// accepted.
func observeManifest(r *http.Request, ledger Ledger, logger *slog.Logger, repo, ref string, status int) {
	id, _ := auth.IdentityFrom(r.Context())
	switch {
	case r.Method == http.MethodPut && status == http.StatusCreated:
		if _, err := ledger.RecordSeed(repo); err != nil {
			logger.LogAttrs(r.Context(), slog.LevelWarn, "the provenance of a pushed repository could not be recorded",
				slog.String("repository", repo), slog.String("error", err.Error()),
				slog.String("requirement", "FR-098"))
		}
		logger.LogAttrs(r.Context(), slog.LevelInfo, "manifest pushed",
			slog.String("repository", repo), slog.String("reference", ref),
			slog.String(logging.KeyProvenance, string(ledger.Classify(repo).Class)),
			slog.String("actor", id.Name), slog.String("requirement", "FR-098"))
	case r.Method == http.MethodDelete && status == http.StatusAccepted:
		logger.LogAttrs(r.Context(), slog.LevelInfo, "manifest deleted",
			slog.String("repository", repo), slog.String("reference", ref),
			slog.String(logging.KeyProvenance, string(ledger.Classify(repo).Class)),
			slog.String("actor", id.Name), slog.String("requirement", "FR-098"))
	}
}

// manifestTarget splits "/v2/<name>/manifests/<reference>". The reference
// (a tag or a digest) never holds a slash, so the LAST "/manifests/" is
// the separator — a repository path may itself have a "manifests"
// segment.
func manifestTarget(path string) (repo, ref string) {
	rest, ok := strings.CutPrefix(path, "/v2/")
	if !ok {
		return "", ""
	}
	i := strings.LastIndex(rest, "/manifests/")
	if i <= 0 {
		return "", ""
	}
	ref = rest[i+len("/manifests/"):]
	if ref == "" || strings.Contains(ref, "/") {
		return "", ""
	}
	return rest[:i], ref
}

// repositoryOf names the repository of a blob or upload route, for the
// audit target of a refused push; empty when the path names none.
func repositoryOf(path string) string {
	rest, ok := strings.CutPrefix(path, "/v2/")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, "/blobs/"); i > 0 {
		return rest[:i]
	}
	return ""
}

// statusRecorder remembers the status the registry answered.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
