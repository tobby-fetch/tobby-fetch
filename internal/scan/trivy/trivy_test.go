// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package trivy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

var fakeTrivy string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "faketrivy-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := "trivy"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	fakeTrivy = filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", fakeTrivy, "./testdata/faketrivy") //nolint:gosec // test helper build
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the fake trivy:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir) //nolint:errcheck,gosec // test cleanup
	os.Exit(code)
}

// memStore is an in-memory Store: manifests by repository and reference,
// blobs by digest, and a recipe graph.
type memStore struct {
	mu        sync.Mutex
	manifests map[string][]byte // repo + "@" + digest or repo + ":" + tag
	types     map[string]string
	blobs     map[string][]byte
	records   []store.RecipeRecord
}

func newMemStore() *memStore {
	return &memStore{manifests: map[string][]byte{}, types: map[string]string{}, blobs: map[string][]byte{}}
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func (m *memStore) RawManifest(_ context.Context, repo, ref string) (payload []byte, mediaType, dgst string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sep := ":"
	if strings.HasPrefix(ref, "sha256:") {
		sep = "@"
	}
	raw, ok := m.manifests[repo+sep+ref]
	if !ok {
		return nil, "", "", store.ErrNotFound
	}
	return raw, m.types[repo+sep+ref], digestOf(raw), nil
}

func (m *memStore) BlobReader(_ context.Context, _, dgst string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[dgst]
	if !ok {
		return nil, store.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStore) RecipeRecords() ([]store.RecipeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.RecipeRecord(nil), m.records...), nil
}

func (m *memStore) blob(b []byte) string {
	d := digestOf(b)
	m.mu.Lock()
	m.blobs[d] = b
	m.mu.Unlock()
	return d
}

func (m *memStore) manifest(repo, tag, mediaType string, v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	d := digestOf(raw)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manifests[repo+"@"+d] = raw
	m.types[repo+"@"+d] = mediaType
	if tag != "" {
		m.manifests[repo+":"+tag] = raw
		m.types[repo+":"+tag] = mediaType
	}
	return d
}

func (m *memStore) record(ings ...store.IngredientRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, systemRecord(ings...))
}

// systemRecord is a verified tobby-system record, written by the instance
// whose base prefix its ingredients carry (everything before the
// relocated ghcr.io path), as the engine writes one.
func systemRecord(ings ...store.IngredientRecord) store.RecipeRecord {
	const cookbook = "registry.example.com/cookbook/tobby-system"
	artifact := cookbook
	if len(ings) > 0 {
		if base, _, ok := strings.Cut(ings[0].Repo, "/ghcr.io/"); ok && base != "" && !strings.HasPrefix(ings[0].Repo, "ghcr.io/") {
			artifact = base + "/" + cookbook
		}
	}
	return store.RecipeRecord{
		Name: SystemRecipe, Version: "1.0.0", CookbookRepo: cookbook, ArtifactRepo: artifact,
		ResolvedAt: time.Now(), Verified: true, Ingredients: ings,
	}
}

func tarOf(t *testing.T, gz bool, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = zw
	}
	tw := tar.NewWriter(w)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

const (
	ociManifestType = "application/vnd.oci.image.manifest.v1+json"
	ociIndexType    = "application/vnd.oci.image.index.v1+json"
)

func imageManifest(st *memStore, layers ...[]byte) map[string]any {
	cfg := st.blob([]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`))
	var ls []map[string]any
	for _, l := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": st.blob(l), "size": len(l)})
	}
	return map[string]any{
		"schemaVersion": 2, "mediaType": ociManifestType,
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": cfg, "size": 10},
		"layers": ls,
	}
}

// putImage stores a single-platform image and returns its digest.
func putImage(t *testing.T, st *memStore, repo string) string {
	t.Helper()
	return st.manifest(repo, "", ociManifestType, imageManifest(st, tarOf(t, true, map[string][]byte{"etc/os-release": []byte("ID=alpine")})))
}

// putDB stores a database artifact built at builtAt, delivered by a
// tobby-system recipe under repo.
func putDB(t *testing.T, st *memStore, repo string, builtAt time.Time) string {
	t.Helper()
	meta := fmt.Sprintf(`{"Version":2,"NextUpdate":%q,"UpdatedAt":%q}`, builtAt.Add(6*time.Hour).Format(time.RFC3339Nano), builtAt.Format(time.RFC3339Nano))
	layer := tarOf(t, true, map[string][]byte{"trivy.db": []byte("bolt"), "metadata.json": []byte(meta), "../escape": []byte("no")})
	d := st.manifest(repo, DBTag, ociManifestType, map[string]any{
		"schemaVersion": 2, "mediaType": ociManifestType,
		"config": map[string]any{"mediaType": "application/vnd.aquasec.trivy.config.v1+json", "digest": st.blob([]byte("{}")), "size": 2},
		"layers": []map[string]any{{"mediaType": mediaTypeDBLayer, "digest": st.blob(layer), "size": len(layer)}},
	})
	st.record(store.IngredientRecord{Name: "trivy-db", Kind: "OCIArtifact", Repo: repo, Tag: DBTag, Digest: d})
	return d
}

type fakeConf struct {
	Version string `json:"version"`
	Report  string `json:"report"`
	Mode    string `json:"mode"`
	Log     string `json:"log"`
}

// installFake copies the fake binary into dir with its configuration and
// returns its path and digest.
func installFake(t *testing.T, dir string, c fakeConf) (bin, pin string) {
	t.Helper()
	raw, err := os.ReadFile(fakeTrivy) //nolint:gosec // G304: the fake this test binary built
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, filepath.Base(fakeTrivy))
	if err := os.WriteFile(p, raw, 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	configure(t, p, c)
	return p, digestOf(raw)
}

func configure(t *testing.T, bin string, c fakeConf) {
	t.Helper()
	if c.Version == "" {
		c.Version = PinnedVersion
	}
	if c.Report == "" {
		abs, err := filepath.Abs("testdata/alpine-3.10.0.json")
		if err != nil {
			t.Fatal(err)
		}
		c.Report = abs
	}
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(bin+".conf.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

type calls struct{ path string }

func (c calls) list(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test read
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func (c calls) images(t *testing.T) int {
	n := 0
	for _, m := range c.list(t) {
		if args, _ := m["args"].([]any); len(args) > 0 && args[0] == "image" {
			n++
		}
	}
	return n
}

type fixture struct {
	st    *memStore
	opts  Options
	calls calls
	bin   string
	pin   string
	image string
	db    string
	now   time.Time
}

const appRepo = "tobby/registry.example/app"

func newFixture(t *testing.T, c fakeConf) *fixture {
	t.Helper()
	dir := t.TempDir()
	c.Log = filepath.Join(dir, "calls.log")
	bin, pin := installFake(t, dir, c)
	st := newMemStore()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	f := &fixture{st: st, calls: calls{c.Log}, bin: bin, pin: pin, now: now}
	f.image = putImage(t, st, appRepo)
	f.db = putDB(t, st, "tobby/ghcr.io/aquasecurity/trivy-db", now.Add(-30*time.Hour))
	f.opts = Options{
		Store: st, WorkDir: filepath.Join(dir, "work"), Path: bin, Digest: pin,
		BasePrefix: "tobby", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		ImagePath: filepath.Join(dir, "no-image"), Now: func() time.Time { return now },
		Timeout: 20 * time.Second,
	}
	return f
}

func codeOf(err error) taxonomy.Code {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te.Code()
	}
	return ""
}

// TestScanNormalizesARealReport runs the adapter end to end against the
// fake binary and the real report it replays: offline flags, minimal
// environment, OCI layout input, database from the store; findings
// normalized, deduplicated, sorted; database facts from its metadata.
func TestScanNormalizesARealReport(t *testing.T) {
	f := newFixture(t, fakeConf{})
	s := New(&f.opts)
	st := s.Refresh(context.Background())
	if !st.Available || st.Reason != "" || st.Database == nil {
		t.Fatalf("status = %+v", st)
	}
	if st.Scanner.Version != PinnedVersion || st.Scanner.Digest != f.pin {
		t.Errorf("scanner = %+v", st.Scanner)
	}
	if origin, pinned := s.Origin(); origin != OriginConfig || !pinned {
		t.Errorf("origin = %s pinned=%v", origin, pinned)
	}
	res, err := s.Scan(context.Background(), scan.Target{Digest: f.image, Repository: appRepo, Kind: "ContainerImage"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Database.Version != "2" || res.Database.Digest != f.db || !res.Database.BuiltAt.Equal(f.now.Add(-30*time.Hour)) {
		t.Errorf("database = %+v", res.Database)
	}
	if !res.ScannedAt.Equal(f.now) {
		t.Errorf("scanned at %v", res.ScannedAt)
	}
	byKey := map[string]scan.Finding{}
	var ids []string
	for _, fd := range res.Findings {
		byKey[fd.ID+"/"+fd.Package] = fd
		ids = append(ids, fd.ID+"/"+fd.Package)
	}
	want := []string{
		"CVE-2019-14697/musl", "CVE-2019-14697/musl-utils", "CVE-2019-1563/libcrypto1.1",
		"CVE-2020-28928/musl", "CVE-2021-28831/busybox", "CVE-2021-30139/apk-tools",
		"CVE-2021-36159/apk-tools", "CVE-2099-0001/libcrypto1.1", "CVE-2099-0002/libssl1.1",
	}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("findings = %v\nwant       %v", ids, want)
	}
	check := func(key string, sev scan.Severity, cvss *float64, fixed string) {
		t.Helper()
		fd := byKey[key]
		if fd.Severity != sev {
			t.Errorf("%s severity = %s; want %s", key, fd.Severity, sev)
		}
		switch {
		case (fd.CVSS == nil) != (cvss == nil):
			t.Errorf("%s cvss = %v; want %v", key, fd.CVSS, cvss)
		case cvss != nil && *fd.CVSS != *cvss:
			t.Errorf("%s cvss = %v; want %v", key, *fd.CVSS, *cvss)
		}
		if fixed != "" && fd.FixedVersion != fixed {
			t.Errorf("%s fixed = %q; want %q", key, fd.FixedVersion, fixed)
		}
	}
	score := func(v float64) *float64 { return &v }
	check("CVE-2021-36159/apk-tools", scan.SeverityCritical, score(9.1), "2.10.7-r0")
	check("CVE-2019-14697/musl", scan.SeverityCritical, score(9.8), "")
	// nvd scores it 4.3 in v2 and 3.7 in v3: the source's v3 score counts.
	check("CVE-2019-1563/libcrypto1.1", scan.SeverityLow, score(3.7), "")
	check("CVE-2099-0001/libcrypto1.1", scan.SeverityUnknown, nil, "")
	check("CVE-2099-0002/libssl1.1", scan.SeverityHigh, nil, "")
	if byKey["CVE-2021-36159/apk-tools"].Source != "alpine" {
		t.Errorf("source = %q", byKey["CVE-2021-36159/apk-tools"].Source)
	}

	// The invocation itself: the environment is the minimal one.
	list := f.calls.list(t)
	if len(list) != 2 { // version probe + one image scan
		t.Fatalf("calls = %d", len(list))
	}
	for _, kv := range list[1]["env"].([]any) {
		if s := kv.(string); strings.HasPrefix(s, "TOBBY_") || strings.Contains(s, "TRIVY_") {
			t.Errorf("the scanner saw %s", s)
		}
	}

	// A second scan of the same digest with the same database and binary
	// is answered from the cache.
	if _, err := s.Scan(context.Background(), scan.Target{Digest: f.image, Repository: appRepo}); err != nil {
		t.Fatal(err)
	}
	if n := f.calls.images(t); n != 1 {
		t.Errorf("image scans = %d; want 1 (cached)", n)
	}
	// The scratch directory is gone.
	entries, _ := os.ReadDir(f.opts.WorkDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "scan-") {
			t.Errorf("scratch directory %s left behind", e.Name())
		}
	}
	// The unpacked database holds exactly the two expected files: the
	// archive's "../escape" entry was not written anywhere.
	if _, err := os.Stat(filepath.Join(f.opts.WorkDir, "escape")); err == nil {
		t.Error("an archive entry escaped the database directory")
	}
}

// TestBinaryIsVerifiedBeforeEveryExecution: a binary that changes after it
// was located is never executed, and the scanner reports itself
// unavailable with the unverified code.
func TestBinaryIsVerifiedBeforeEveryExecution(t *testing.T) {
	f := newFixture(t, fakeConf{})
	s := New(&f.opts)
	if st := s.Refresh(context.Background()); !st.Available {
		t.Fatalf("status = %+v", st)
	}
	raw, err := os.ReadFile(f.bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.bin, append(raw, 0), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	_, err = s.Scan(context.Background(), scan.Target{Digest: f.image, Repository: appRepo})
	if codeOf(err) != taxonomy.CodeScannerUnverified {
		t.Fatalf("scan error = %v; want %s", err, taxonomy.CodeScannerUnverified)
	}
	if f.calls.images(t) != 0 {
		t.Error("the changed binary was executed")
	}
	if st := s.Status(context.Background()); st.Available || st.Reason != taxonomy.CodeScannerUnverified {
		t.Errorf("status after tampering = %+v", st)
	}
}

func TestLocateBinary(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *fixture, o *Options)
		code   taxonomy.Code
		origin string
		pinned bool
	}{
		{"configured path and digest", func(*fixture, *Options) {}, "", OriginConfig, true},
		{"configured digest mismatch", func(_ *fixture, o *Options) { o.Digest = "sha256:" + strings.Repeat("0", 64) }, taxonomy.CodeScannerUnverified, "", false},
		{"configured path, no digest, not the pinned release (linux)", func(_ *fixture, o *Options) { o.Digest, o.GOOS, o.GOARCH = "", "linux", "amd64" }, taxonomy.CodeScannerUnverified, "", false},
		{"configured path, no digest, platform without a pin", func(_ *fixture, o *Options) { o.Digest, o.GOOS, o.GOARCH = "", "linux", "riscv64" }, taxonomy.CodeScannerUnverified, "", false},
		{"configured path missing", func(_ *fixture, o *Options) { o.Path = filepath.Join(o.WorkDir, "absent") }, taxonomy.CodeScannerUnavailable, "", false},
		{"macOS takes the PATH, unpinned", func(f *fixture, o *Options) {
			o.Path, o.Digest, o.GOOS = "", "", "darwin"
			o.LookPath = func(string) (string, error) { return f.bin, nil }
		}, "", OriginPath, false},
		{"macOS without trivy on the PATH", func(_ *fixture, o *Options) {
			o.Path, o.Digest, o.GOOS = "", "", "darwin"
			o.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		}, taxonomy.CodeScannerUnavailable, "", false},
		{"image binary that is not the pin", func(f *fixture, o *Options) {
			o.Path, o.Digest, o.GOOS, o.GOARCH, o.ImagePath = "", "", "linux", "amd64", f.bin
		}, taxonomy.CodeScannerUnverified, "", false},
		{"linux without image binary nor channel delivery", func(_ *fixture, o *Options) {
			o.Path, o.Digest, o.GOOS, o.GOARCH = "", "", "linux", "amd64"
		}, taxonomy.CodeScannerUnavailable, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, fakeConf{})
			o := f.opts
			c.mutate(f, &o)
			s := New(&o)
			st := s.Refresh(context.Background())
			if c.code == "" {
				if !st.Available {
					t.Fatalf("status = %+v (%v)", st, s.Cause())
				}
				if origin, pinned := s.Origin(); origin != c.origin || pinned != c.pinned {
					t.Errorf("origin = %s pinned=%v; want %s %v", origin, pinned, c.origin, c.pinned)
				}
				return
			}
			if st.Available || st.Reason != c.code || codeOf(s.Cause()) != c.code {
				t.Fatalf("status = %+v cause=%v; want reason %s", st, s.Cause(), c.code)
			}
			// Not scanned, with the reason — never silently.
			if _, err := s.Scan(context.Background(), scan.Target{Digest: f.image, Repository: appRepo}); codeOf(err) != c.code {
				t.Errorf("scan error = %v; want %s", err, c.code)
			}
		})
	}
}

// TestChannelDeliveredBinary: on Linux and Windows hosts the binary comes
// from the tobby-system recipe — a FileSet whose platform child holds the
// executable — unpacked into the work directory and pinned to the bytes
// of the verified layer.
func TestChannelDeliveredBinary(t *testing.T) {
	for _, plat := range []struct{ goos, goarch, entry string }{
		{"linux", "amd64", "usr/local/bin/trivy"},
		{"windows", "amd64", "trivy.exe"},
	} {
		t.Run(plat.goos, func(t *testing.T) {
			f := newFixture(t, fakeConf{})
			raw, err := os.ReadFile(f.bin)
			if err != nil {
				t.Fatal(err)
			}
			repo := "other-base/ghcr.io/aquasecurity/trivy" // written by another instance
			older := tarOf(t, true, map[string][]byte{plat.entry: []byte("older layer, overridden")})
			layer := tarOf(t, true, map[string][]byte{plat.entry: raw})
			child := f.st.manifest(repo, "", ociManifestType, imageManifest(f.st, older, layer))
			arm := f.st.manifest(repo, "", ociManifestType, imageManifest(f.st, tarOf(t, true, map[string][]byte{plat.entry: []byte("arm")})))
			idx := f.st.manifest(repo, PinnedVersion, ociIndexType, map[string]any{
				"schemaVersion": 2, "mediaType": ociIndexType,
				"manifests": []map[string]any{
					{"mediaType": ociManifestType, "digest": arm, "size": 1, "platform": map[string]string{"os": plat.goos, "architecture": "arm64"}},
					{"mediaType": ociManifestType, "digest": child, "size": 1, "platform": map[string]string{"os": plat.goos, "architecture": plat.goarch}},
				},
			})
			f.st.record(store.IngredientRecord{Name: "trivy", Kind: "FileSet", Repo: repo, Tag: PinnedVersion, Digest: idx})

			o := f.opts
			o.Path, o.Digest, o.GOOS, o.GOARCH = "", "", plat.goos, plat.goarch
			// The fake reads its configuration next to the executable: give
			// the copy about to be unpacked one.
			name := "trivy"
			if plat.goos == "windows" {
				name = "trivy.exe"
			}
			unpacked := filepath.Join(o.WorkDir, "bin", strings.TrimPrefix(idx, "sha256:"))
			if err := os.MkdirAll(unpacked, 0o700); err != nil {
				t.Fatal(err)
			}
			configure(t, filepath.Join(unpacked, name), fakeConf{Log: f.calls.path})
			s := New(&o)
			st := s.Refresh(context.Background())
			if !st.Available {
				t.Fatalf("status = %+v (%v)", st, s.Cause())
			}
			if origin, _ := s.Origin(); origin != OriginChannel {
				t.Errorf("origin = %s", origin)
			}
			if st.Scanner.Digest != digestOf(raw) {
				t.Errorf("pinned digest = %s; want the unpacked bytes' %s", st.Scanner.Digest, digestOf(raw))
			}
			if _, err := os.Stat(filepath.Join(unpacked, name)); err != nil {
				t.Errorf("unpacked binary not where expected: %v", err)
			}
			if _, err := s.Scan(context.Background(), scan.Target{Digest: f.image, Repository: appRepo}); err != nil {
				t.Fatal(err)
			}
			// A refresh with the same delivery does not unpack again.
			if st := s.Refresh(context.Background()); !st.Available {
				t.Fatalf("second refresh: %+v", st)
			}
		})
	}
}

func TestChannelBinaryWhiteoutAndMissingPlatform(t *testing.T) {
	f := newFixture(t, fakeConf{})
	repo := "tobby/ghcr.io/aquasecurity/trivy"
	img := f.st.manifest(repo, "", ociManifestType, imageManifest(f.st,
		tarOf(t, true, map[string][]byte{"usr/local/bin/trivy": []byte("x")}),
		tarOf(t, false, map[string][]byte{"usr/local/bin/.wh.trivy": nil}),
	))
	f.st.record(store.IngredientRecord{Name: "trivy", Kind: "FileSet", Repo: repo, Digest: img})
	o := f.opts
	o.Path, o.Digest, o.GOOS, o.GOARCH = "", "", "linux", "amd64"
	s := New(&o)
	if st := s.Refresh(context.Background()); st.Available || !strings.Contains(fmt.Sprint(s.Cause().ParamsMap()["detail"]), "holds no scanner executable") {
		t.Errorf("a whited-out executable was found: %+v %v", st, s.Cause())
	}
}

func TestDatabaseLookup(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		f := newFixture(t, fakeConf{})
		f.st.records = nil
		s := New(&f.opts)
		st := s.Refresh(context.Background())
		if !st.Available || st.Database != nil || st.Reason != taxonomy.CodeScanDBMissing {
			t.Fatalf("status = %+v", st)
		}
		if got := s.Cause().ParamsMap()["repository"]; got != "tobby/ghcr.io/aquasecurity/trivy-db" {
			t.Errorf("the degraded mode names %v", got)
		}
		if _, err := s.Scan(context.Background(), scan.Target{Digest: f.image, Repository: appRepo}); codeOf(err) != taxonomy.CodeScanDBMissing {
			t.Errorf("scan error = %v", err)
		}
	})
	t.Run("explicit repository, unit-imported", func(t *testing.T) {
		f := newFixture(t, fakeConf{})
		f.st.records = nil
		o := f.opts
		o.DBRepository = "tobby/ghcr.io/aquasecurity/trivy-db"
		s := New(&o)
		if st := s.Refresh(context.Background()); st.Database == nil || st.Database.Digest != f.db {
			t.Fatalf("status = %+v (%v)", st, s.Cause())
		}
	})
	t.Run("newest delivery wins, older unpack removed", func(t *testing.T) {
		f := newFixture(t, fakeConf{})
		s := New(&f.opts)
		s.Refresh(context.Background())
		newer := putDB(t, f.st, "zone-b/ghcr.io/aquasecurity/trivy-db", f.now.Add(-time.Hour))
		st := s.Refresh(context.Background())
		if st.Database == nil || st.Database.Digest != newer || !st.Database.BuiltAt.Equal(f.now.Add(-time.Hour)) {
			t.Fatalf("status = %+v", st)
		}
		entries, _ := os.ReadDir(filepath.Join(f.opts.WorkDir, "db"))
		if len(entries) != 1 || entries[0].Name() != strings.TrimPrefix(newer, "sha256:") {
			t.Errorf("unpacked databases = %v", entries)
		}
		// A new scanner instance reuses the unpacked copy.
		if st := New(&f.opts).Refresh(context.Background()); st.Database == nil || st.Database.Digest != newer {
			t.Errorf("reuse: %+v", st)
		}
	})
	t.Run("corrupt layer", func(t *testing.T) {
		f := newFixture(t, fakeConf{})
		f.st.mu.Lock()
		for d, b := range f.st.blobs {
			if bytes.HasPrefix(b, []byte{0x1f, 0x8b}) && len(b) > 40 {
				f.st.blobs[d] = append(append([]byte(nil), b[:len(b)-8]...), make([]byte, 8)...)
			}
		}
		f.st.mu.Unlock()
		s := New(&f.opts)
		if st := s.Refresh(context.Background()); st.Database != nil || st.Reason != taxonomy.CodeScanDBMissing {
			t.Fatalf("a corrupt database was accepted: %+v", st)
		}
	})
	t.Run("not a database", func(t *testing.T) {
		f := newFixture(t, fakeConf{})
		img := putImage(t, f.st, "tobby/ghcr.io/aquasecurity/trivy-db")
		f.st.records = nil
		f.st.record(store.IngredientRecord{Name: "trivy-db", Repo: "tobby/ghcr.io/aquasecurity/trivy-db", Digest: img})
		if st := New(&f.opts).Refresh(context.Background()); st.Database != nil {
			t.Fatalf("an image was taken for a database: %+v", st)
		}
	})
}

func TestScanFailuresAreReported(t *testing.T) {
	for _, c := range []struct {
		mode, detail string
		timeout      time.Duration
	}{
		{"fail", "unable to initialize a scan service", 0},
		{"sleep", "did not finish within", 2 * time.Second},
		{"huge", "exceeded the bound", 0},
		{"schema1", "unsupported scanner report schema 1", 0},
	} {
		t.Run(c.mode, func(t *testing.T) {
			f := newFixture(t, fakeConf{Mode: c.mode})
			o := f.opts
			if c.timeout > 0 {
				o.Timeout = c.timeout
			}
			s := New(&o)
			s.Refresh(context.Background())
			// The deadline handed to run is the configured timeout plus a
			// minute; shorten the outer context for the sleeping case.
			ctx := context.Background()
			if c.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
			}
			_, err := s.Scan(ctx, scan.Target{Digest: f.image, Repository: appRepo})
			if codeOf(err) != taxonomy.CodeScanFailed {
				t.Fatalf("error = %v; want %s", err, taxonomy.CodeScanFailed)
			}
			var te *taxonomy.Error
			errors.As(err, &te)
			if d := fmt.Sprint(te.ParamsMap()["detail"]); c.mode != "sleep" && !strings.Contains(d, c.detail) {
				t.Errorf("detail = %q; want it to contain %q", d, c.detail)
			}
		})
	}
}

// TestIndexTargets: a multi-platform index is scanned child by child, for
// the children the store holds (a sparse index carries only the selected
// platforms) and never for an attestation manifest.
func TestIndexTargets(t *testing.T) {
	f := newFixture(t, fakeConf{})
	amd := putImage(t, f.st, appRepo)
	att := f.st.manifest(appRepo, "", ociManifestType, imageManifest(f.st, []byte("attestation")))
	idx := f.st.manifest(appRepo, "1.0", ociIndexType, map[string]any{
		"schemaVersion": 2, "mediaType": ociIndexType,
		"manifests": []map[string]any{
			{"mediaType": ociManifestType, "digest": amd, "size": 1, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
			{"mediaType": ociManifestType, "digest": "sha256:" + strings.Repeat("a", 64), "size": 1, "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
			{"mediaType": ociManifestType, "digest": att, "size": 1, "platform": map[string]string{"os": "unknown", "architecture": "unknown"}},
		},
	})
	s := New(&f.opts)
	s.Refresh(context.Background())
	res, err := s.Scan(context.Background(), scan.Target{Digest: idx, Repository: appRepo})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.calls.images(t); n != 1 {
		t.Errorf("image scans = %d; want 1 (amd64 only)", n)
	}
	if len(res.Findings) != 9 {
		t.Errorf("findings = %d", len(res.Findings))
	}
	if _, err := s.Scan(context.Background(), scan.Target{Digest: idx, Repository: appRepo, Platform: "linux/arm64"}); codeOf(err) != taxonomy.CodeScanFailed {
		t.Errorf("a platform the store does not hold scanned: %v", err)
	}
}

func TestBoundedBufferAndLastLine(t *testing.T) {
	b := &boundedBuffer{limit: 4}
	if n, _ := b.Write([]byte("abcdef")); n != 6 || !b.overflow || b.buf.String() != "abcd" {
		t.Errorf("bounded write: n=%d overflow=%v buf=%q", n, b.overflow, b.buf.String())
	}
	if got := lastLine("first\nsecond\n"); got != "second" {
		t.Errorf("lastLine = %q", got)
	}
	if got := lastLine(""); got != "no diagnostic on stderr" {
		t.Errorf("lastLine(empty) = %q", got)
	}
}

// TestOnlyTheVerifiedSystemRecipeDelivers locks the tobby-system channel
// (FR-054, R-25): the scanner binary and its database come from the
// tobby-system recipe admitted with its signature verified, at exactly the
// relocated repository — never from a recipe admitted under a relaxed
// trust scope, never from another recipe, never from a repository that
// merely ends like the right one. A later, untrusted delivery never
// displaces the trusted one.
func TestOnlyTheVerifiedSystemRecipeDelivers(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	const (
		binRepo = "site/ghcr.io/aquasecurity/trivy"
		dbRepo  = "site/ghcr.io/aquasecurity/trivy-db"
	)
	// The record is written by the "site" instance; only the ingredient's
	// repository varies.
	rec := func(repo, dgst string, at time.Time, mut func(*store.RecipeRecord)) store.RecipeRecord {
		r := systemRecord(store.IngredientRecord{Repo: binRepo, Digest: dgst})
		r.Ingredients[0].Repo = repo
		r.ResolvedAt = at
		if mut != nil {
			mut(&r)
		}
		return r
	}
	relaxed := func(r *store.RecipeRecord) { r.Verified, r.TrustScope = false, "relaxed-vendor" }
	other := func(r *store.RecipeRecord) { r.Name = "third-party-app" }
	for _, c := range []struct {
		name string
		repo string // where the untrusted delivery sits; "" = the right repository
		mut  func(*store.RecipeRecord)
	}{
		{"another recipe, relaxed scope, look-alike repository", "site/evil.example/ghcr.io/aquasecurity/trivy",
			func(r *store.RecipeRecord) { other(r); relaxed(r) }},
		{"another recipe, verified", "", other},
		{"tobby-system under a relaxed scope", "", relaxed},
		{"tobby-system, look-alike repository", "site/evil.example/ghcr.io/aquasecurity/trivy", nil},
		{"tobby-system, prefix its record does not state", "elsewhere/ghcr.io/aquasecurity/trivy", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := c.repo
			if repo == "" {
				repo = binRepo
			}
			st := newMemStore()
			st.records = []store.RecipeRecord{
				rec(binRepo, "sha256:good", t0, nil),
				rec(repo, "sha256:evil", t0.Add(time.Hour), c.mut),
			}
			loc, err := locate(st, "", DefaultBinaryRef)
			if err != nil {
				t.Fatal(err)
			}
			if loc == nil || loc.Digest != "sha256:good" {
				t.Fatalf("binary located at %+v; want the verified tobby-system delivery sha256:good", loc)
			}
			if c.repo != "" {
				return
			}
			// The explicit repository (scan.dbRepository) obeys the same
			// admission rules.
			st.records = []store.RecipeRecord{
				rec(dbRepo, "sha256:good", t0, nil),
				rec(dbRepo, "sha256:evil", t0.Add(time.Hour), c.mut),
			}
			if loc, err = locate(st, dbRepo, DefaultDBRef); err != nil {
				t.Fatal(err)
			}
			if loc == nil || loc.Digest != "sha256:good" {
				t.Fatalf("database located at %+v; want sha256:good", loc)
			}
		})
	}
}
