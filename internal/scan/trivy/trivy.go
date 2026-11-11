// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package trivy is the reference implementation of the scanner-neutral
// seam (ADR-0008 decisions 4 and 5): a pinned external `trivy` binary, run
// out of process against a copy of the content, offline, with a minimal
// environment and bounded outputs, its digest verified before every
// execution; the vulnerability database read from the instance's own
// store, where the tobby-system recipe delivered it (FR-032).
//
// Nothing outside this package sees a Trivy type: Scan returns the
// normalized model of package scan.
package trivy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// DefaultTimeout bounds one scan when the configuration says nothing.
const DefaultTimeout = 10 * time.Minute

// versionTimeout bounds the `trivy version` probe.
const versionTimeout = 30 * time.Second

// cacheSize bounds the in-memory result cache: a scan of an unchanged
// digest with an unchanged database and binary is redundant (ADR-0008
// consequences), and a passthrough service re-evaluates the same content
// every cycle.
const cacheSize = 1024

// Origins of the executed binary, reported at startup and in the status.
const (
	OriginConfig  = "configuration"
	OriginImage   = "image"
	OriginChannel = "channel"
	OriginPath    = "path"
)

// Options configures the adapter.
type Options struct {
	// Store holds the tobby-system deliveries and the content to scan.
	Store Store
	// WorkDir is a private directory of the instance (under state.root,
	// never on the transportable store): the unpacked database, the
	// channel-delivered binary, and one scratch directory per scan.
	WorkDir string
	// Path, Digest and Ref are scan.trivy.*.
	Path, Digest, Ref string
	// DBRepository is scan.dbRepository; empty finds the relocation of
	// DefaultDBRef.
	DBRepository string
	// BasePrefix is the instance's relocation base, to name the expected
	// database repository in a degraded-mode message.
	BasePrefix string
	// Timeout bounds one scan; zero is DefaultTimeout.
	Timeout time.Duration
	Logger  *slog.Logger

	// Test seams; zero values select the real platform.
	GOOS, GOARCH string
	ImagePath    string
	LookPath     func(string) (string, error)
	Now          func() time.Time
}

// binary is a located, verified scanner executable.
type binary struct {
	path    string
	digest  string
	version string
	origin  string
	pinned  bool
	// source is the channel artifact digest it was unpacked from.
	source string
}

// database is the unpacked vulnerability database.
type database struct {
	dir      string
	artifact string
	meta     DBMetadata
}

type cacheKey struct{ target, platform, db, bin string }

// Scanner implements scan.Scanner with Trivy.
type Scanner struct {
	opts Options

	// scanMu serializes executions and state refreshes: one Trivy at a
	// time (it is heavy), and the database never swapped under a scan.
	scanMu sync.Mutex

	stateMu sync.RWMutex
	bin     *binary
	binErr  *taxonomy.Error
	db      *database
	dbErr   *taxonomy.Error
	cache   map[cacheKey]*scan.Result
	order   []cacheKey
}

var _ scan.Scanner = (*Scanner)(nil)

// New builds the adapter. It performs no I/O: call Refresh to locate the
// binary and the database.
func New(opts *Options) *Scanner {
	o := *opts
	opts = &o
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	if opts.ImagePath == "" {
		opts.ImagePath = ImagePath
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Scanner{
		opts:   *opts,
		binErr: taxonomy.New(taxonomy.CodeScannerUnavailable, taxonomy.Params{"detail": "the scanner has not been located yet"}),
		dbErr:  taxonomy.New(taxonomy.CodeScanDBMissing, taxonomy.Params{"repository": "(not looked up yet)"}),
		cache:  map[cacheKey]*scan.Result{},
	}
}

// Refresh locates and verifies the scanner binary and the database, and
// unpacks a database (or a channel-delivered binary) that changed since
// the previous call. Called at startup and after every synchronization —
// the moments a tobby-system delivery can land. It returns the new status.
func (s *Scanner) Refresh(ctx context.Context) scan.Status {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	bin, binErr := s.locateBinary(ctx)
	db, dbErr := s.prepareDB(ctx)

	s.stateMu.Lock()
	s.bin, s.binErr = bin, binErr
	s.db, s.dbErr = db, dbErr
	s.stateMu.Unlock()
	return s.Status(ctx)
}

// Status reports the last refreshed state, without I/O: it backs the UI
// banner and the metrics, read on every page.
func (s *Scanner) Status(_ context.Context) scan.Status {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	st := scan.Status{Available: s.bin != nil}
	if s.bin != nil {
		st.Scanner = s.bin.info()
	}
	if s.db != nil {
		d := s.db.describe()
		st.Database = &d
	}
	switch {
	case s.bin == nil && s.binErr != nil:
		st.Reason = s.binErr.Code()
	case s.db == nil && s.dbErr != nil:
		st.Reason = s.dbErr.Code()
	}
	return st
}

// Cause is the taxonomy error behind the current degraded mode, nil when
// the scanner and its database are both usable.
func (s *Scanner) Cause() *taxonomy.Error {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.bin == nil {
		return s.binErr
	}
	if s.db == nil {
		return s.dbErr
	}
	return nil
}

// Origin says where the executed binary came from, and whether it is the
// pinned release; empty when there is none.
func (s *Scanner) Origin() (origin string, pinned bool) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.bin == nil {
		return "", false
	}
	return s.bin.origin, s.bin.pinned
}

func (b *binary) info() scan.Info {
	return scan.Info{Name: "trivy", Version: b.version, Digest: b.digest}
}

func (d *database) describe() scan.Database {
	return scan.Database{
		Version: strconv.Itoa(d.meta.Version),
		Digest:  d.artifact,
		BuiltAt: d.meta.UpdatedAt.UTC(),
	}
}

// Scan runs the scanner on one stored item (scan.Scanner).
func (s *Scanner) Scan(ctx context.Context, t scan.Target) (*scan.Result, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	s.stateMu.RLock()
	bin, binErr, db, dbErr := s.bin, s.binErr, s.db, s.dbErr
	s.stateMu.RUnlock()
	if bin == nil {
		return nil, binErr
	}
	if db == nil {
		return nil, dbErr
	}
	// FR-031: the digest is verified before EVERY execution. A binary that
	// changed under the instance is never run, and the scanner is
	// unavailable until a refresh finds a good one.
	if actual, err := fileDigest(bin.path); err != nil || actual != bin.digest {
		if err != nil {
			actual = "unreadable: " + err.Error()
		}
		e := taxonomy.New(taxonomy.CodeScannerUnverified, taxonomy.Params{"path": bin.path, "expected": bin.digest, "actual": actual})
		s.stateMu.Lock()
		s.bin, s.binErr = nil, e
		s.stateMu.Unlock()
		s.opts.Logger.LogAttrs(ctx, slog.LevelError, "scanner binary changed since it was verified: it is not executed",
			slog.String("path", bin.path), slog.String("expected", bin.digest), slog.String("actual", actual),
			slog.String("code", string(e.Code())), slog.String("requirement", "FR-031"))
		return nil, e
	}

	key := cacheKey{target: t.Digest, platform: t.Platform, db: db.artifact, bin: bin.digest}
	s.stateMu.RLock()
	cached := s.cache[key]
	s.stateMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	ref := t.Repository + "@" + t.Digest
	fail := func(err error) error {
		return taxonomy.New(taxonomy.CodeScanFailed, taxonomy.Params{"reference": ref, "detail": err.Error()}).WithCause(err)
	}
	targets, err := targetsOf(ctx, s.opts.Store, t.Repository, t.Digest, t.Platform)
	if err != nil {
		return nil, fail(err)
	}
	var (
		findings []scan.Finding
		seen     = map[string]bool{}
		version  = bin.version
	)
	for _, img := range targets {
		v, fs, err := s.scanImage(ctx, bin, db, t.Repository, img)
		if err != nil {
			return nil, fail(err)
		}
		if v != "" {
			version = v
		}
		for _, f := range fs {
			k := f.ID + "\x00" + f.Package + "\x00" + f.InstalledVersion
			if !seen[k] {
				seen[k] = true
				findings = append(findings, f)
			}
		}
	}
	info := bin.info()
	info.Version = version
	res := &scan.Result{
		Scanner:   info,
		ScannedAt: s.opts.Now().UTC(),
		Database:  db.describe(),
		Findings:  findings,
	}
	s.remember(key, res)
	return res, nil
}

// scanImage scans one single-platform image from a private copy.
func (s *Scanner) scanImage(ctx context.Context, bin *binary, db *database, repo string, img imageTarget) (string, []scan.Finding, error) {
	if err := os.MkdirAll(s.opts.WorkDir, 0o700); err != nil {
		return "", nil, err
	}
	scratch, err := os.MkdirTemp(s.opts.WorkDir, "scan-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(scratch) //nolint:errcheck // best-effort cleanup of a private directory
	layout := filepath.Join(scratch, "layout")
	if err := writeLayout(ctx, s.opts.Store, repo, img, layout); err != nil {
		return "", nil, err
	}
	args := []string{
		"image",
		"--input", layout,
		"--format", "json",
		"--quiet",
		"--scanners", "vuln",
		"--list-all-pkgs=false",
		// Offline, always (NFR-019, FR-032): the database is the one the
		// channel delivered, and nothing is looked up anywhere.
		"--offline-scan",
		"--skip-db-update",
		"--skip-java-db-update",
		"--skip-check-update",
		"--skip-version-check",
		"--skip-vex-repo-update",
		"--disable-telemetry",
		"--cache-dir", db.dir,
		"--cache-backend", "memory",
		"--timeout", s.opts.Timeout.String(),
	}
	out, err := run(ctx, bin.path, args, filepath.Join(scratch, "home"), s.opts.Timeout+time.Minute)
	if err != nil {
		return "", nil, err
	}
	return parseReport(out)
}

func (s *Scanner) remember(k cacheKey, r *scan.Result) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if _, ok := s.cache[k]; ok {
		return
	}
	if len(s.order) >= cacheSize {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
	s.cache[k] = r
	s.order = append(s.order, k)
}

// locateBinary finds and verifies the scanner executable, in the order the
// FR-031 amendment fixes per platform.
func (s *Scanner) locateBinary(ctx context.Context) (*binary, *taxonomy.Error) {
	o := &s.opts
	pin, hasPin := PinnedDigest(o.GOOS, o.GOARCH)
	unavailable := func(detail string) (*binary, *taxonomy.Error) {
		return nil, taxonomy.New(taxonomy.CodeScannerUnavailable, taxonomy.Params{"detail": detail})
	}
	unverified := func(p, expected, actual string) (*binary, *taxonomy.Error) {
		return nil, taxonomy.New(taxonomy.CodeScannerUnverified, taxonomy.Params{"path": p, "expected": expected, "actual": actual})
	}
	var b binary
	switch {
	case o.Path != "":
		actual, err := fileDigest(o.Path)
		if err != nil {
			return unavailable(fmt.Sprintf("scan.trivy.path %s cannot be read: %v", o.Path, err))
		}
		expected := o.Digest
		switch {
		case expected != "":
		case hasPin:
			expected = pin
		case o.GOOS == "darwin":
			expected = actual
		default:
			return unverified(o.Path, "a scan.trivy.digest pin (no built-in pin exists for "+o.GOOS+"/"+o.GOARCH+")", actual)
		}
		if actual != expected {
			return unverified(o.Path, expected, actual)
		}
		b = binary{path: o.Path, digest: actual, origin: OriginConfig, pinned: o.Digest != "" || actual == pin}
	case o.GOOS == "darwin":
		p, err := o.LookPath("trivy")
		if err != nil {
			return unavailable("no trivy executable on the PATH — on macOS the scanner is taken from the PATH only, without channel distribution")
		}
		actual, err := fileDigest(p)
		if err != nil {
			return unavailable(fmt.Sprintf("%s cannot be read: %v", p, err))
		}
		b = binary{path: p, digest: actual, origin: OriginPath, pinned: hasPin && actual == pin}
	case o.GOOS == "linux" && exists(o.ImagePath):
		actual, err := fileDigest(o.ImagePath)
		if err != nil {
			return unavailable(fmt.Sprintf("%s cannot be read: %v", o.ImagePath, err))
		}
		if !hasPin {
			return unverified(o.ImagePath, "a built-in pin for "+o.GOOS+"/"+o.GOARCH, actual)
		}
		if actual != pin {
			return unverified(o.ImagePath, pin, actual)
		}
		b = binary{path: o.ImagePath, digest: actual, origin: OriginImage, pinned: true}
	default:
		ref := o.Ref
		if ref == "" {
			ref = DefaultBinaryRef
		}
		loc, err := locate(o.Store, "", ref)
		if err != nil {
			return unavailable("reading the recipe graph: " + err.Error())
		}
		if loc == nil {
			return unavailable(fmt.Sprintf("no tobby-system delivery of %s is in the store yet — synchronize the tobby-system recipe that carries the scanner first, signed and verified (a recipe admitted under a relaxed trust scope never delivers it)", ref))
		}
		// The previous unpack is reused when the delivery did not change;
		// its digest was computed from the verified layer at that time.
		s.stateMu.RLock()
		prev := s.bin
		s.stateMu.RUnlock()
		if prev != nil && prev.origin == OriginChannel && prev.source == loc.Digest && exists(prev.path) {
			b = *prev
			break
		}
		name := "trivy"
		if o.GOOS == "windows" {
			name = "trivy.exe"
		}
		dst := filepath.Join(o.WorkDir, "bin", strings.TrimPrefix(loc.Digest, "sha256:"), name)
		sum, err := unpackBinary(ctx, o.Store, loc.Repo, loc.Digest, o.GOOS, o.GOARCH, dst)
		if err != nil {
			return unavailable(fmt.Sprintf("the tobby-system delivery %s@%s could not be unpacked: %v", loc.Repo, loc.Digest, err))
		}
		s.prune(filepath.Join(o.WorkDir, "bin"), strings.TrimPrefix(loc.Digest, "sha256:"))
		b = binary{path: dst, digest: sum, origin: OriginChannel, pinned: true, source: loc.Digest}
	}
	v, err := probeVersion(ctx, b.path, filepath.Join(o.WorkDir, "probe"))
	if err != nil {
		return unavailable(fmt.Sprintf("%s does not run: %v", b.path, err))
	}
	b.version = v
	if b.origin == OriginImage && v != PinnedVersion {
		return unverified(b.path, "version "+PinnedVersion, "version "+v)
	}
	s.opts.Logger.LogAttrs(ctx, slog.LevelInfo, "vulnerability scanner located",
		slog.String("path", b.path), slog.String("origin", b.origin), slog.String("version", b.version),
		slog.String("digest", b.digest), slog.Bool("pinned", b.pinned), slog.String("requirement", "FR-031"))
	if !b.pinned {
		s.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "vulnerability scanner is not the pinned release",
			slog.String("path", b.path), slog.String("pinned_version", PinnedVersion),
			slog.String("note", "macOS takes the scanner from the PATH as it is; the digest is still verified before every execution against the one read now"),
			slog.String("requirement", "FR-031"))
	}
	return &b, nil
}

// prepareDB finds the database the tobby-system recipe delivered and
// unpacks it when it changed.
func (s *Scanner) prepareDB(ctx context.Context) (*database, *taxonomy.Error) {
	o := &s.opts
	expected := o.DBRepository
	if expected == "" {
		if p, err := relocate.PathWithBase(o.BasePrefix, DefaultDBRef); err == nil {
			expected = p
		}
	}
	missing := func() (*database, *taxonomy.Error) {
		return nil, taxonomy.New(taxonomy.CodeScanDBMissing, taxonomy.Params{"repository": expected})
	}
	loc, err := locate(o.Store, o.DBRepository, DefaultDBRef)
	if err != nil {
		return missing()
	}
	if loc == nil && o.DBRepository != "" {
		// Delivered outside a recipe (a unit import, FR-023): the schema
		// tag is the only name it has.
		if _, _, dgst, err := o.Store.RawManifest(ctx, o.DBRepository, DBTag); err == nil {
			loc = &located{Repo: o.DBRepository, Tag: DBTag, Digest: dgst}
		}
	}
	if loc == nil {
		return missing()
	}
	s.stateMu.RLock()
	prev := s.db
	s.stateMu.RUnlock()
	if prev != nil && prev.artifact == loc.Digest {
		return prev, nil
	}
	root := filepath.Join(o.WorkDir, "db")
	hexDigest := strings.TrimPrefix(loc.Digest, "sha256:")
	final := filepath.Join(root, hexDigest)
	if meta, ok := readUnpacked(final); ok {
		s.prune(root, hexDigest)
		return &database{dir: final, artifact: loc.Digest, meta: meta}, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return s.dbFailed(ctx, loc, expected, err)
	}
	tmp, err := os.MkdirTemp(root, ".unpack-")
	if err != nil {
		return s.dbFailed(ctx, loc, expected, err)
	}
	_, meta, err := unpackDB(ctx, o.Store, loc.Repo, loc.Digest, tmp)
	if err != nil {
		os.RemoveAll(tmp) //nolint:errcheck,gosec // best-effort cleanup
		return s.dbFailed(ctx, loc, expected, err)
	}
	os.RemoveAll(final) //nolint:errcheck,gosec // a partial leftover of an interrupted unpack
	if err := os.Rename(tmp, final); err != nil {
		os.RemoveAll(tmp) //nolint:errcheck,gosec // best-effort cleanup
		return s.dbFailed(ctx, loc, expected, err)
	}
	s.prune(root, hexDigest)
	o.Logger.LogAttrs(ctx, slog.LevelInfo, "vulnerability database unpacked",
		slog.String("repository", loc.Repo), slog.String("digest", loc.Digest),
		slog.Int("schema", meta.Version), slog.Time("built_at", meta.UpdatedAt),
		slog.String("requirement", "FR-032"))
	return &database{dir: final, artifact: loc.Digest, meta: meta}, nil
}

func (s *Scanner) dbFailed(ctx context.Context, loc *located, expected string, err error) (*database, *taxonomy.Error) {
	s.opts.Logger.LogAttrs(ctx, slog.LevelError, "vulnerability database could not be unpacked",
		slog.String("repository", loc.Repo), slog.String("digest", loc.Digest),
		slog.String("error", err.Error()), slog.String("requirement", "FR-032"))
	return nil, taxonomy.New(taxonomy.CodeScanDBMissing, taxonomy.Params{"repository": expected}).WithCause(err)
}

// readUnpacked reads a database a previous run already unpacked.
func readUnpacked(dir string) (DBMetadata, bool) {
	var meta DBMetadata
	raw, err := os.ReadFile(filepath.Join(dir, "db", "metadata.json")) //nolint:gosec // G304: this adapter's own directory
	if err != nil || json.Unmarshal(raw, &meta) != nil || meta.UpdatedAt.IsZero() {
		return meta, false
	}
	if !exists(filepath.Join(dir, "db", "trivy.db")) {
		return meta, false
	}
	return meta, true
}

// prune removes every entry of dir but keep: only the current database
// and binary are kept unpacked.
func (s *Scanner) prune(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep && !strings.HasPrefix(e.Name(), ".unpack-") {
			os.RemoveAll(filepath.Join(dir, e.Name())) //nolint:errcheck,gosec // best-effort cleanup of a private directory
		}
	}
}

// probeVersion asks the binary its version.
func probeVersion(ctx context.Context, bin, home string) (string, error) {
	out, err := run(ctx, bin, []string{"version", "--format", "json"}, home, versionTimeout)
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.Version == "" {
		return "", errors.New("it does not state a version")
	}
	return v.Version, nil
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() || err == nil && st.IsDir()
}
