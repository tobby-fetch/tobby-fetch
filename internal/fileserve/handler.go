// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package fileserve

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

// RoutePrefix is the URL prefix the handler expects; mount the handler
// on it without stripping (e.g. mux.Handle(RoutePrefix, s.Handler())).
const RoutePrefix = "/files/"

// Handler serves GET/HEAD /files/<name>/<path…> read-only: Range
// honored (206) and If-Modified-Since supported via http.ServeContent;
// unknown fileset or path → 404; directory paths → 404; no write
// surface at all (any other method → 405). The <name> must match an
// enabled FileSet. Responses are bare HTTP statuses — see the package
// documentation for why the taxonomy does not apply here.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	// r.URL.Path is percent-decoded by net/http: encoded traversal
	// (%2e%2e%2f) and encoded NUL bytes surface here as literals.
	urlPath := r.URL.Path
	if strings.ContainsRune(urlPath, 0) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	rest, ok := strings.CutPrefix(urlPath, RoutePrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, filePath, ok := strings.Cut(rest, "/")
	if !ok || filePath == "" {
		// "/files/<name>" or "/files/<name>/": the fileset root is a
		// directory, and directories are 404 (FR-047).
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(filePath, "/") {
		// Trailing slash addresses a directory.
		http.NotFound(w, r)
		return
	}
	// NFR-011 path hygiene: no ".", "..", or empty ("//") segments. The
	// serve-time os.Root below stays the authoritative barrier.
	for _, seg := range strings.Split(filePath, "/") {
		if seg == "" || seg == "." || seg == ".." {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
	}

	// The file is opened under the read lock, so a concurrent Sync
	// closing this root cannot race the open; the returned *os.File
	// stays valid for the whole transfer regardless of later purges.
	s.mu.RLock()
	served := s.served[name]
	if served == nil {
		s.mu.RUnlock()
		http.NotFound(w, r)
		return
	}
	f, err := served.root.Open(filePath)
	s.mu.RUnlock()
	if err != nil {
		// Not found, or a symlink escaping the rootfs that os.Root
		// refused to resolve — indistinguishable on purpose.
		http.NotFound(w, r)
		return
	}
	defer f.Close() //nolint:errcheck // read-only file

	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}

	ctype := mime.TypeByExtension(path.Ext(filePath))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	// ServeContent handles HEAD, Range (206) and If-Modified-Since; the
	// modification time is the extraction time, stable per digest.
	http.ServeContent(w, r, path.Base(filePath), fi.ModTime(), f)
}
