// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package store is Tobby's embedded OCI store: the CNCF
// distribution/distribution v3 codebase used as a library (ADR-0004),
// wrapped behind a deliberately narrow interface so the engine never
// couples to the library's internals and the implementation stays
// swappable.
//
// The store serves the standard OCI Distribution API on /v2/ (FR-040) from
// a filesystem backend rooted in the self-contained store directory
// (FR-050). Repository names follow the ingredient-relocation convention —
// nominal, canonicalized source host as the repository prefix (ADR-0013,
// package relocate) — one layout shared by the embedded store and every
// destination. Nested repository paths such as "docker.io/bitnami/wordpress"
// are first-class.
//
// The import pipeline (ADR-0005) will write through the storage backend
// directly, never through the HTTP loopback; the same rule holds for the
// browsing accessors of browse.go (FR-062), which read the backend through
// a second, library-level registry sharing the same root directory.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"

	distribution "github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/handlers"
	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/distribution/v3/registry/storage/driver/factory"

	// Filesystem storage driver, selected through the driver factory by the
	// configuration below.
	_ "github.com/distribution/distribution/v3/registry/storage/driver/filesystem"

	"github.com/sirupsen/logrus"
)

// Store is the embedded OCI registry over the local storage directory.
type Store struct {
	app  *handlers.App
	root string
	// browse is the second, read-side access to the same backend: the
	// browsing accessors (browse.go) go through the storage library, never
	// through the HTTP loopback (package doc).
	browse distribution.Namespace
}

// Open initializes the embedded registry on the given storage root.
//
// The registry is read/write: standard clients push into it and pull from
// it (FR-041). Deletion is enabled so recipe-scoped removal and garbage
// collection (FR-044) can build on it.
func Open(ctx context.Context, root string, logger *slog.Logger) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("store: root directory is required")
	}

	cfg := &configuration.Configuration{}
	cfg.Storage = configuration.Storage{
		"filesystem": configuration.Parameters{"rootdirectory": root},
		"delete":     configuration.Parameters{"enabled": true},
	}
	// The HTTP secret signs upload-session state. Ephemeral is fine for a
	// single instance; generating it ourselves keeps the library from
	// logging a startup warning.
	cfg.HTTP.Secret = randomSecret()
	cfg.Catalog.MaxEntries = 1000

	// The distribution library logs through logrus. Tobby's own logs are
	// slog JSON (FR-090); the library's internal noise is kept to warnings
	// and errors, JSON-formatted so the streams stay machine-parseable.
	cfg.Log.Level = "warn"
	cfg.Log.Formatter = "json"
	logrus.SetLevel(logrus.WarnLevel)
	logrus.SetFormatter(&logrus.JSONFormatter{TimestampFormat: "2006-01-02T15:04:05.999999999Z07:00", FieldMap: logrus.FieldMap{
		logrus.FieldKeyTime:  "ts",
		logrus.FieldKeyLevel: "level",
		logrus.FieldKeyMsg:   "msg",
	}})

	app := handlers.NewApp(ctx, cfg)

	// Second access to the same backend for the browsing accessors
	// (browse.go): the storage library over the same root directory as the
	// app handlers — never the HTTP loopback (FR-062, package doc).
	driver, err := factory.Create(ctx, "filesystem", configuration.Parameters{"rootdirectory": root})
	if err != nil {
		return nil, fmt.Errorf("store: creating browse driver: %w", err)
	}
	browse, err := storage.NewRegistry(ctx, driver)
	if err != nil {
		return nil, fmt.Errorf("store: creating browse registry: %w", err)
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "embedded registry initialized",
		slog.String("root", root))
	return &Store{app: app, root: root, browse: browse}, nil
}

// APIHandler returns the OCI Distribution API (/v2/) handler. Mount it on
// the instance's HTTP listener.
func (s *Store) APIHandler() http.Handler {
	return s.app
}

// Root returns the storage root directory (the self-contained store,
// FR-050).
func (s *Store) Root() string {
	return s.root
}

// Close releases the store's resources.
func (s *Store) Close() error {
	// The filesystem-backed app holds no long-lived resources needing
	// teardown today; the method exists so callers already own a lifecycle
	// when backends that do (cache connections…) arrive.
	return nil
}

func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("store: reading random bytes: %v", err))
	}
	return hex.EncodeToString(b[:])
}
