// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	storagedriver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/distribution/v3/registry/storage/driver/factory"
)

// The storage driver Tobby actually runs on: the library's filesystem
// driver with two of its operations replaced (B-023).
//
// distribution's filesystem driver is written for a POSIX filesystem, and
// two of its assumptions are false on Windows — the platform NFR-018 puts
// in the operating scope of the whole mirror journey:
//
//   - PutContent writes a temporary file, commits it, renames it over the
//     target, and only THEN closes it (`defer writer.Close()` sits above
//     the rename). Unix renames an open file happily. Windows opens files
//     without FILE_SHARE_DELETE, so MoveFileEx answers a sharing
//     violation and the write fails. PutContent is how every manifest
//     revision link, every layer link and every tag is stored — so on
//     Windows the embedded registry could not record a single manifest or
//     tag. It also fsyncs the containing directory afterwards, which
//     Windows answers with ERROR_ACCESS_DENIED on a read-only directory
//     handle.
//   - List joins the parent key and each entry name with filepath.Join,
//     which yields backslashes on Windows. Every consumer inside the
//     library parses those keys with the slash-only `path` package:
//     the catalog walk never recognizes a "_manifests" component and so
//     enumerates nothing, and the tag store returns whole paths where tag
//     names belong. Repository listing, tag listing, garbage collection
//     and reset all read through it.
//
// Both replacements are single code paths, not Windows branches: closing
// before renaming and joining keys with `path` are correct everywhere and
// byte-for-byte identical to the library's behaviour on Unix. That is
// deliberate — a Windows-only branch would be exercised by one runner in
// the matrix, and this one is exercised by all of them.
//
// The rest of the driver is the library's, reached through the embedded
// interface: this is a narrow correction, not a fork.

// driverName is the factory name the corrected driver registers under.
// The library selects a driver by the single key of cfg.Storage, so the
// name has to be one it does not already know.
const driverName = "tobby-filesystem"

func init() { factory.Register(driverName, storeDriverFactory{}) }

type storeDriverFactory struct{}

func (storeDriverFactory) Create(ctx context.Context, parameters map[string]any) (storagedriver.StorageDriver, error) {
	base, err := factory.Create(ctx, "filesystem", parameters)
	if err != nil {
		return nil, err
	}
	root, _ := parameters["rootdirectory"].(string)
	if root == "" {
		return nil, fmt.Errorf("store: %s needs a rootdirectory parameter", driverName)
	}
	return &storeDriver{StorageDriver: base, root: root}, nil
}

// storeDriver is the library's filesystem driver with PutContent, List
// and Walk corrected. Everything else is inherited unchanged.
type storeDriver struct {
	storagedriver.StorageDriver
	// root is the storage root, needed to fsync the directory a rename
	// landed in — the library keeps its own copy private.
	root string
}

func (d *storeDriver) Name() string { return driverName }

// PutContent stores small objects — manifest revision links, layer links,
// tag targets — through a temporary file that is CLOSED before it is
// renamed over the target.
//
// The ordering is the whole point. The library commits (flush + fsync)
// and renames with the handle still open, which Unix allows and Windows
// refuses. Closing first costs nothing on Unix and is what makes the
// embedded registry writable on Windows at all.
func (d *storeDriver) PutContent(ctx context.Context, subPath string, contents []byte) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("store: naming a temporary object: %w", err)
	}
	tempPath := subPath + "." + hex.EncodeToString(suffix[:]) + ".tmp"

	//nolint:staticcheck // QF1008: the embedded field is named on purpose — this
	// method overrides PutContent, and `d.Writer` two lines from `d.Move`
	// would read as recursion to anyone skimming it.
	w, err := d.StorageDriver.Writer(ctx, tempPath, false)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, bytes.NewReader(contents)); err != nil {
		_ = w.Cancel(ctx)
		return err
	}
	if err := w.Commit(ctx); err != nil {
		_ = w.Close()
		_ = d.StorageDriver.Delete(ctx, tempPath) //nolint:staticcheck // QF1008: see the Writer call above
		return err
	}
	// Before the rename, never after.
	if err := w.Close(); err != nil {
		_ = d.StorageDriver.Delete(ctx, tempPath) //nolint:staticcheck // QF1008: see the Writer call above
		return err
	}
	//nolint:staticcheck // QF1008: see the Writer call above
	if err := d.StorageDriver.Move(ctx, tempPath, subPath); err != nil {
		_ = d.StorageDriver.Delete(ctx, tempPath) //nolint:staticcheck // QF1008: same
		return err
	}
	return syncDir(filepath.Dir(filepath.Join(d.root, filepath.FromSlash(subPath))))
}

// List returns the direct descendants of a key as KEYS — slash-separated,
// in the namespace the library's own parsers use.
//
// The library builds them with filepath.Join, which is the same thing on
// Unix and a backslash path on Windows. A backslash key survives every
// `path.Split` the catalog and tag stores perform, so they read garbage
// rather than failing: the repository enumeration comes back empty and
// the garbage collector marks nothing reachable.
func (d *storeDriver) List(_ context.Context, subPath string) ([]string, error) {
	full := filepath.Join(d.root, filepath.FromSlash(subPath))
	entries, err := os.ReadDir(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, storagedriver.PathNotFoundError{Path: subPath, DriverName: driverName}
		}
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, path.Join(subPath, e.Name()))
	}
	return keys, nil
}

// Walk re-enters the fallback walker through THIS driver.
//
// The library's Walk hands the walker its own receiver, so a corrected
// List would be bypassed by every caller that walks instead of listing —
// which is most of the garbage collector.
func (d *storeDriver) Walk(ctx context.Context, from string, f storagedriver.WalkFn, options ...func(*storagedriver.WalkOptions)) error {
	return storagedriver.WalkFallback(ctx, d, from, f, options...)
}
