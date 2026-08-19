// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package blobfetch_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/blobfetch"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Every test here runs against a REAL HTTP server. The registry that
// honors ranges is Tobby's own embedded store serving its real /v2/ API —
// the same source the engine tests use — and the misbehaving ones are
// real HTTP servers answering a real blob GET the way registries, caches
// and object stores actually misbehave in the field. A blob download is a
// plain GET; there is nothing about it worth mocking, and mocking it is
// how a resume implementation ends up passing tests it should fail.

// blobSize is large enough that an interruption is meaningful and small
// enough to keep the suite fast.
const blobSize = 1 << 20

// TestResumeAgainstARealRegistryReDownloadsNothing is the R-29 acceptance:
// a blob cut mid-stream finishes without re-sending a single byte the
// client already had.
func TestResumeAgainstARealRegistryReDownloadsNothing(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	origin.cutFirstResponseAfter(blobSize / 2)

	dgst, size, content := origin.seed(t)
	rc, err := newResumer(t, t.TempDir()).Open(t.Context(), origin.repository(t), dgst, size, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("delivered %d bytes, want the %d-byte blob", len(got), len(content))
	}
	// The whole point, stated as arithmetic: the origin put the blob on
	// the wire exactly once across the two attempts. Anything more is a
	// restart wearing a resume's clothes.
	if served := origin.blobBytes(); served != size {
		t.Errorf("origin served %d bytes for a %d-byte blob: %d were re-downloaded", served, size, served-size)
	}
	if n := origin.rangedRequests(); n != 1 {
		t.Errorf("ranged requests = %d, want exactly 1 (the resume)", n)
	}
}

// TestAnInterruptedInstanceResumesFromTheStateDirectory is the crash half
// of FR-029: the bytes outlive the process that received them. Each
// Resumer here stands for one instance lifetime — a fresh object over the
// same state directory, exactly what a restart produces.
func TestAnInterruptedInstanceResumesFromTheStateDirectory(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	// Never more than an eighth of the blob per response: no single
	// instance lifetime can finish it.
	origin.capResponse(blobSize / 8)
	dgst, size, content := origin.seed(t)
	state := t.TempDir()

	var got []byte
	for lifetime := 1; lifetime <= 6; lifetime++ {
		rc, err := newResumer(t, state).Open(t.Context(), origin.repository(t), dgst, size, nil)
		if err == nil {
			got, err = io.ReadAll(rc)
			if cerr := rc.Close(); cerr != nil {
				t.Errorf("Close: %v", cerr)
			}
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if lifetime == 6 {
			t.Fatalf("six instance lifetimes did not finish the blob: %v", err)
		}
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("delivered %d bytes, want %d", len(got), len(content))
	}
	if served := origin.blobBytes(); served != size {
		t.Errorf("origin served %d bytes for a %d-byte blob across restarts: %d were re-downloaded", served, size, served-size)
	}
	// Nothing is left behind once the blob has landed: a completed
	// transfer must not keep a gigabyte of state on disk forever.
	if entries := spoolFiles(t, state); len(entries) != 0 {
		t.Errorf("the state directory still holds %v after a completed transfer", entries)
	}
}

// TestPartialsLiveInTheStateDirectoryNeverInTheStore is R-16 restated
// where it can actually be violated: the store is transportable, and a
// half-received blob must never board the media.
func TestPartialsLiveInTheStateDirectoryNeverInTheStore(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	origin.capResponse(blobSize / 4)
	dgst, size, _ := origin.seed(t)
	state, storeRoot := t.TempDir(), t.TempDir()

	// One doomed lifetime, so a partial is on disk when we look.
	_, err := newResumer(t, state).Open(t.Context(), origin.repository(t), dgst, size, nil)
	if err == nil {
		t.Fatal("the capped origin should not have completed the blob")
	}
	files := spoolFiles(t, state)
	if len(files) == 0 {
		t.Fatal("no partial was persisted in the state directory: nothing would resume")
	}
	for _, f := range files {
		if !strings.HasPrefix(f, filepath.Join("partials", dgst.Algorithm().String())) {
			t.Errorf("unexpected state entry %q", f)
		}
	}
	if got := walk(t, storeRoot); len(got) != 0 {
		t.Errorf("the store root was written to: %v", got)
	}
}

// TestAnOriginIgnoringRangeIsDetectedNotConcatenated is the field case the
// requirement calls out by name: a server that answers 200 with the whole
// body to a ranged request. Appending that body to the prefix already on
// disk would produce a corrupt blob out of two perfectly valid halves.
func TestAnOriginIgnoringRangeIsDetectedNotConcatenated(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	origin.ignoreRange()
	origin.cutFirstResponseAfter(blobSize / 2)
	dgst, size, content := origin.seed(t)

	rc, err := newResumer(t, t.TempDir()).Open(t.Context(), origin.repository(t), dgst, size, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("delivered %d bytes; a concatenation would have produced %d", len(got), size+size/2)
	}
	if origin.rangedRequests() == 0 {
		t.Error("no ranged request was made: the test did not exercise the 200 fallback")
	}
}

// TestAnInconsistentContentRangeIsRefused covers a 206 that does not
// answer the question that was asked. Writing its body at the requested
// offset would silently corrupt the blob at a position no one can point
// at afterwards.
func TestAnInconsistentContentRangeIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*registry){
		"the 206 starts at the wrong byte": func(r *registry) { r.shiftContentRange(-1024) },
		"the 206 declares another length": func(r *registry) {
			r.rewriteContentRange(func(start, end, total int64) string {
				return "bytes " + itoa(start) + "-" + itoa(end) + "/" + itoa(total+4096)
			})
		},
		"the Content-Range is unparseable": func(r *registry) {
			r.rewriteContentRange(func(int64, int64, int64) string { return "octets 12-34/56" })
		},
	}
	for label, misbehave := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			origin := newRegistry(t)
			origin.cutFirstResponseAfter(blobSize / 2)
			misbehave(origin)
			dgst, size, _ := origin.seed(t)

			_, err := newResumer(t, t.TempDir()).Open(t.Context(), origin.repository(t), dgst, size, nil)
			if err == nil {
				t.Fatal("the inconsistent partial response was accepted")
			}
			assertCode(t, err, taxonomy.CodeRangeUnusable)
		})
	}
}

// TestChangedContentRestartsRatherThanSplices is the nastiest case, and
// the reason the validator is persisted at all: the bytes behind the
// digest changed between two attempts. Splicing half of one object onto
// half of another is how a store acquires content that never existed
// anywhere.
//
// The origin here serves a DIFFERENT blob after the interruption, under a
// new ETag. Only the second blob hashes to the requested digest, so a
// naive concatenation cannot pass: the test asserts both that the
// transfer succeeded and that what it delivered is the second blob whole.
func TestChangedContentRestartsRatherThanSplices(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	dgst, size, content := origin.seed(t)

	// The origin serves a DIFFERENT object first, under ETag "v1", and
	// only reveals the requested blob under "v2" after the interruption.
	// A splice of the two cannot hash to the requested digest, so success
	// here means the prefix was thrown away rather than appended to.
	decoy := make([]byte, size)
	for i := range decoy {
		decoy[i] = content[i] ^ 0xff
	}
	origin.serveThenSwap(decoy, content, size/2)

	rc, err := newResumer(t, t.TempDir()).Open(t.Context(), origin.repository(t), dgst, size, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("the delivered bytes are not the requested blob: two different objects were spliced")
	}
}

// TestSpooledBytesAreVerifiedBeforeTheyAreOffered keeps the integrity
// gate where FR-029 must not move it. A spool tampered with between two
// instance lifetimes — a bad disk, a careless operator, an attacker with
// the state directory — is caught before a single byte is offered to the
// store, and the poisoned spool is destroyed rather than resumed forever.
func TestSpooledBytesAreVerifiedBeforeTheyAreOffered(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	origin.capResponse(blobSize / 4)
	dgst, size, _ := origin.seed(t)
	state := t.TempDir()

	if _, err := newResumer(t, state).Open(t.Context(), origin.repository(t), dgst, size, nil); err == nil {
		t.Fatal("the capped origin should not have completed the blob")
	}
	part := filepath.Join(state, "partials", dgst.Algorithm().String(), dgst.Encoded()+".part")
	raw, err := os.ReadFile(part) //nolint:gosec // G304: a path this test built
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(part, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	origin.capResponse(0) // let it finish this time
	_, err = newResumer(t, state).Open(t.Context(), origin.repository(t), dgst, size, nil)
	if err == nil {
		t.Fatal("a tampered spool was accepted into the store")
	}
	assertCode(t, err, taxonomy.CodeDigestMismatch)
	if _, serr := os.Stat(part); !os.IsNotExist(serr) {
		t.Error("the tampered spool survived: the next attempt would resume the same mismatch forever")
	}
}

// TestOnlyBlobsAboveTheThresholdTakeTheResumablePath: below it, nothing
// changes — no spool, no extra disk traffic, the caller's own streaming
// opener is used.
func TestOnlyBlobsAboveTheThresholdTakeTheResumablePath(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	r := blobfetch.New(netx.Direct(), nil, state, 1<<20)

	cases := []struct {
		size int64
		want bool
	}{{0, false}, {-1, false}, {1<<20 - 1, false}, {1 << 20, true}, {1 << 30, true}}
	for _, c := range cases {
		if got := r.Handles(c.size); got != c.want {
			t.Errorf("Handles(%d) = %v, want %v", c.size, got, c.want)
		}
	}

	used := false
	rc, err := r.OpenOr(t.Context(), name.Repository{}, digest.FromString("x"), 4096, nil,
		func() (io.ReadCloser, error) {
			used = true
			return io.NopCloser(strings.NewReader("small")), nil
		})
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if !used {
		t.Error("a sub-threshold blob did not use the plain streaming opener")
	}
	if files := spoolFiles(t, state); len(files) != 0 {
		t.Errorf("a sub-threshold blob left %v in the state directory", files)
	}
}

// TestResumeDisabledKeepsTodaysBehavior: the two ways to switch the
// mechanism off must both land on the untouched streaming path, because
// that is the escape hatch the configuration promises.
func TestResumeDisabledKeepsTodaysBehavior(t *testing.T) {
	t.Parallel()
	for label, r := range map[string]*blobfetch.Resumer{
		"threshold zero":     blobfetch.New(netx.Direct(), nil, t.TempDir(), 0),
		"no state directory": blobfetch.New(netx.Direct(), nil, "", 1<<20),
		"no resumer at all":  nil,
	} {
		t.Run(label, func(t *testing.T) {
			if r.Handles(1 << 30) {
				t.Error("Handles reported a resumable blob on a disabled resumer")
			}
			if r.Threshold() != 0 {
				t.Errorf("Threshold() = %d, want 0", r.Threshold())
			}
			used := false
			rc, err := r.OpenOr(t.Context(), name.Repository{}, digest.FromString("x"), 1<<30, nil,
				func() (io.ReadCloser, error) {
					used = true
					return io.NopCloser(strings.NewReader("streamed")), nil
				})
			if err != nil {
				t.Fatal(err)
			}
			_ = rc.Close()
			if !used {
				t.Error("the plain streaming opener was not used")
			}
		})
	}
}

// TestProgressIsReportedPerBlob: the mechanism has to be visible, or a
// four-hour transfer is indistinguishable from a hung one.
func TestProgressIsReportedPerBlob(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	origin.cutFirstResponseAfter(blobSize / 2)
	dgst, size, _ := origin.seed(t)

	var mu sync.Mutex
	var last int64
	sawResumed := false
	rc, err := newResumer(t, t.TempDir()).Open(t.Context(), origin.repository(t), dgst, size,
		func(received, total int64, resumed bool) {
			mu.Lock()
			defer mu.Unlock()
			if total != size {
				t.Errorf("progress reported total %d, want %d", total, size)
			}
			last = received
			sawResumed = sawResumed || resumed
		})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
	mu.Lock()
	defer mu.Unlock()
	if last != size {
		t.Errorf("final progress = %d, want %d", last, size)
	}
	if !sawResumed {
		t.Error("the resume was never reported: the screen would show a restart")
	}
}

// TestMissingBlobIsNotFound and its neighbours keep the taxonomy honest:
// an operator reading "TBY-REG-005" goes somewhere different from one
// reading "TBY-REG-002".
func TestSourceFailuresAreTaxonomized(t *testing.T) {
	t.Parallel()
	t.Run("unknown blob", func(t *testing.T) {
		t.Parallel()
		origin := newRegistry(t)
		_, size, _ := origin.seed(t)
		absent := digest.FromString("a blob this registry never had")
		_, err := newResumer(t, t.TempDir()).Open(t.Context(), origin.repository(t), absent, size, nil)
		assertCode(t, err, taxonomy.CodeRefNotFound)
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		origin := newRegistry(t)
		dgst, size, _ := origin.seed(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := newResumer(t, t.TempDir()).Open(ctx, origin.repository(t), dgst, size, nil)
		if err == nil {
			t.Fatal("a cancelled fetch succeeded")
		}
	})
}

// ------------------------------------------------------------------ helpers

func newResumer(t *testing.T, state string) *blobfetch.Resumer {
	t.Helper()
	eg, err := netx.New(&config.Network{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eg.CloseIdleConnections)
	// Threshold 1: every blob in this package is "large", because the
	// threshold is tested on its own and inflating fixtures to 64 MiB to
	// re-test it here would only make the suite slow.
	return blobfetch.New(eg, nil, state, 1)
}

// registry is a real embedded-store registry served over its real /v2/
// API, behind a front that can be told to misbehave the way registries,
// caches and object stores do in the field.
type registry struct {
	st   *store.Store
	addr string
	repo string

	mu sync.Mutex
	// cutAfter truncates the NEXT response body to this many bytes.
	cutAfter int64
	// cap truncates EVERY response body to this many bytes.
	capAt int64
	// dropRange strips the Range header before the real registry sees
	// it, so the origin answers 200 with the whole body.
	dropRange bool
	// rewrite rebuilds the Content-Range header of a 206.
	rewrite func(start, end, total int64) string
	// swapTo replaces the served bytes (with a new ETag) once served
	// exceeds swapAfter.
	swapTo    []byte
	swapAfter int64

	served  int64
	ranged  int
	content []byte
	// want is the digest this front answers for. Anything else is the
	// real registry's 404 to serve, not this front's.
	want string
}

func newRegistry(t *testing.T) *registry {
	t.Helper()
	st, err := store.Open(t.Context(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing store: %v", cerr)
		}
	})
	r := &registry{st: st, repo: "library/big"}
	srv := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(srv.Close)
	r.addr = srv.Listener.Addr().String()
	return r
}

// seed pushes a single-layer image through go-containerregistry, as a
// standard client would, and returns the layer's digest, size and bytes.
func (r *registry) seed(t *testing.T) (dgst digest.Digest, size int64, body []byte) {
	t.Helper()
	img, err := random.Image(blobSize, 1, random.WithSource(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(r.addr+"/"+r.repo+":1", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	h, err := layers[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	rc, err := layers[0].Compressed()
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	dgst = digest.NewDigestFromEncoded(digest.Algorithm(h.Algorithm), h.Hex)
	r.mu.Lock()
	r.content, r.want = content, dgst.String()
	r.served, r.ranged = 0, 0
	r.mu.Unlock()
	return dgst, int64(len(content)), content
}

func (r *registry) repository(t *testing.T) name.Repository {
	t.Helper()
	repo, err := name.NewRepository(r.addr+"/"+r.repo, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func (r *registry) cutFirstResponseAfter(n int64) { //nolint:unparam // one caller, one cut size today; the parameter is what makes the fixture readable at the call site
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cutAfter = n
}

func (r *registry) capResponse(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capAt = n
}

func (r *registry) ignoreRange() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropRange = true
}

func (r *registry) shiftContentRange(delta int64) {
	r.rewriteContentRange(func(start, end, total int64) string {
		return "bytes " + itoa(start+delta) + "-" + itoa(end) + "/" + itoa(total)
	})
}

func (r *registry) rewriteContentRange(fn func(start, end, total int64) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rewrite = fn
}

// serveThenSwap serves `first` under ETag "v1" until `after` bytes have
// crossed, cuts the connection, and from then on serves `then` under
// "v2" — a registry whose content moved under a stable digest.
func (r *registry) serveThenSwap(first, then []byte, after int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content = first
	r.swapTo, r.swapAfter, r.cutAfter = then, after, after
}

func (r *registry) blobBytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.served
}

func (r *registry) rangedRequests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ranged
}

// serve answers the blob GET itself so the misbehaviors are reachable,
// and delegates everything else — ping, manifests, uploads — to the real
// embedded registry.
func (r *registry) serve(w http.ResponseWriter, req *http.Request) {
	blobPrefix := "/v2/" + r.repo + "/blobs/sha256:"
	if req.Method != http.MethodGet || !strings.HasPrefix(req.URL.Path, blobPrefix) {
		r.st.APIHandler().ServeHTTP(w, req)
		return
	}
	r.mu.Lock()
	body, dropRange, rewrite := r.content, r.dropRange, r.rewrite
	etag := "\"v1\""
	if r.swapTo != nil && r.served >= r.swapAfter {
		body, etag = r.swapTo, "\"v2\""
	}
	if body == nil || r.want == "" || !strings.HasSuffix(req.URL.Path, r.want) {
		r.mu.Unlock()
		r.st.APIHandler().ServeHTTP(w, req)
		return
	}
	rangeHdr := req.Header.Get("Range")
	if rangeHdr != "" {
		r.ranged++
	}
	if dropRange {
		rangeHdr = ""
	}
	limit := int64(len(body))
	if r.cutAfter > 0 {
		limit, r.cutAfter = r.cutAfter, 0
	} else if r.capAt > 0 {
		limit = r.capAt
	}
	r.mu.Unlock()

	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	start := int64(0)
	if off, ok := strings.CutPrefix(rangeHdr, "bytes="); ok {
		s, _, _ := strings.Cut(off, "-")
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil || parsed > int64(len(body)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start = parsed
	}
	chunk := body[start:]
	if limit < int64(len(chunk)) {
		chunk = chunk[:limit]
	}
	if start > 0 {
		cr := "bytes " + itoa(start) + "-" + itoa(int64(len(body))-1) + "/" + itoa(int64(len(body)))
		if rewrite != nil {
			cr = rewrite(start, int64(len(body))-1, int64(len(body)))
		}
		w.Header().Set("Content-Range", cr)
		w.WriteHeader(http.StatusPartialContent)
	}
	n, _ := w.Write(chunk)
	r.mu.Lock()
	r.served += int64(n)
	r.mu.Unlock()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// spoolFiles lists what the state directory holds, relative to its root.
func spoolFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, p := range walk(t, root) {
		if strings.HasSuffix(p, ".part") || strings.HasSuffix(p, ".json") {
			out = append(out, p)
		}
	}
	return out
}

func walk(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertCode(t *testing.T, err error, want taxonomy.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s", want)
	}
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		t.Fatalf("error %v is not taxonomized, want %s", err, want)
	}
	if te.Code() != want {
		t.Fatalf("code = %s, want %s (%v)", te.Code(), want, err)
	}
}

var _ = v1.Hash{}

// TestConcurrentFetchesOfOneDigestDoNotShareASpool is the parallelism
// case that actually happens: ingredients transfer concurrently
// (NFR-008) and container images share base layers, so two in-flight
// transfers routinely want the same blob. Interleaved writes into one
// content-addressed spool file would corrupt exactly the largest blobs,
// forever, on every retry.
func TestConcurrentFetchesOfOneDigestDoNotShareASpool(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	dgst, size, content := origin.seed(t)
	r := newResumer(t, t.TempDir())

	const callers = 4
	var wg sync.WaitGroup
	results := make([][]byte, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rc, err := r.Open(t.Context(), origin.repository(t), dgst, size, nil)
			if err != nil {
				errs[i] = err
				return
			}
			results[i], errs[i] = io.ReadAll(rc)
			_ = rc.Close()
		}(i)
	}
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if !bytes.Equal(results[i], content) {
			t.Fatalf("caller %d received %d bytes, want the %d-byte blob",
				i, len(results[i]), len(content))
		}
	}
}

// TestAWaitingFetchIsCancellable: a transfer queued behind another one
// must still honor its deadline, or a shutdown would hang for as long as
// the largest blob in flight.
func TestAWaitingFetchIsCancellable(t *testing.T) {
	t.Parallel()
	origin := newRegistry(t)
	origin.capResponse(4096) // the holder will never finish
	dgst, size, _ := origin.seed(t)
	r := newResumer(t, t.TempDir())

	holderCtx, stopHolder := context.WithCancel(t.Context())
	holding := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		close(holding)
		_, _ = r.Open(holderCtx, origin.repository(t), dgst, size, nil)
	}()
	<-holding
	// The holder has to be gone before the test returns: it is still
	// writing into the spool, and t.TempDir's cleanup races it otherwise.
	// That is how this test failed in CI — on "directory not empty"
	// during cleanup rather than on anything it asserts.
	defer func() {
		stopHolder()
		<-holderDone
	}()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := r.Open(ctx, origin.repository(t), dgst, size, nil)
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Error("a cancelled fetch waiting on the digest lock succeeded")
	}
}
