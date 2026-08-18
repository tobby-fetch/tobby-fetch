// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package blobfetch resumes a large blob download INSIDE the blob
// (FR-029, R-29).
//
// FR-029 was half implemented before this package existed: transfers were
// retried with bounded backoff and an interrupted synchronization resumed
// from persisted state — but a single blob cut at 90 % restarted at 0 %.
// On a multi-gigabyte image behind an enterprise link that is not a slow
// run, it is a run that never terminates: the window between two cuts is
// shorter than the blob.
//
// The spike docs/spikes/blob-resume-range-vs-gcr.md measured the two ways
// to fix it. go-containerregistry cannot do it: `remote.Layer(…)` exposes
// only `Compressed()`, resuming means reopening and discarding the prefix
// — measured at ×10 the useful bytes on a 90 % interruption — and feeding
// it a 206 makes it fail on an unrelated body-size limit, because
// `fetchBlobURL` asserts the status is exactly 200. So this package issues
// the blob GET itself, with a Range header.
//
// Issuing it itself is NOT building a transport of its own. The request
// goes out through gcr's own authentication transport layered on the
// instance's single shared egress (FR-080, FR-081): same proxy, same proxy
// credentials, same private authorities, same keychain. That layering is
// what makes the direct path admissible; internal/netx's wiring test is
// what proves it stayed that way.
//
// Three invariants shape the code:
//
//   - The partial bytes live in the STATE directory, never in the store
//     (R-16). The store is transportable; a half-received blob is local
//     progress, not content, and must not board the media.
//   - Integrity stays blocking. The expected digest is computed over the
//     WHOLE spool — the resumed prefix included, re-hashed from disk — and
//     checked before a single byte is offered to the store.
//   - The response status decides where bytes are written, never the
//     request. A server that ignores Range and answers 200 with the full
//     body is common in the field (caching proxies, object stores, older
//     registries); appending that body to what we already had would
//     produce a corrupt blob out of two perfectly valid halves.
package blobfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Tunables that are deliberately not configuration. An operator who needs
// a different chunk size or a fourth attempt has a link problem these
// knobs do not solve, and every extra setting is another thing two zones
// can disagree about. The one knob that IS configurable is the threshold,
// because it trades network against disk and only the operator knows
// which of the two is scarce (config.Transfer.ResumeThreshold).
const (
	// attempts bounds the in-blob retries of a single Open call. The
	// caller's own bounded backoff (engine withRetries, FR-029) and the
	// task requeue at startup both resume from the spool afterwards, so
	// this is the fast inner loop, not the last line of defense.
	attempts = 3
	// backoffBase is doubled per attempt, mirroring the engine's shape.
	backoffBase = 500 * time.Millisecond
	// checkpoint is how often the received bytes are flushed and the
	// offset recorded. Small enough that a crash loses seconds of link
	// time, large enough not to fsync per read.
	checkpoint = 8 << 20
	// progressEvery paces the progress callback: the task detail is
	// polled every 2s, so reporting faster only costs writes.
	progressEvery = time.Second
	// errorBodyLimit caps how much of an error response is read before
	// go-containerregistry's error decoder sees it.
	errorBodyLimit = 64 << 10
)

// Progress reports one blob's advance to the caller, which persists it on
// the task item so the operator sees WHICH blob is moving and how far —
// "78 % of layer 3 of 9" instead of a task that has said "running" for
// eleven minutes. resumed is true once the transfer picked up a spool
// left by an earlier attempt.
type Progress func(received, total int64, resumed bool)

// Resumer opens resumable readers over registry blobs. Built once per
// instance and shared.
//
// Safe for concurrent use, and it has to be: ingredients are transferred
// in parallel (NFR-008) and container images share base layers, so two
// in-flight transfers routinely want the SAME blob digest. Without
// serialization both would open the same spool file and interleave their
// writes into it — a corruption that the digest check would catch, over
// and over, on every retry, on exactly the largest blobs. Per-digest
// locking is therefore not a nicety here; it is what makes the spool
// safe to share.
type Resumer struct {
	egress   *netx.Egress
	keychain authn.Keychain
	// locks serializes concurrent fetches of one digest. Keyed by digest
	// rather than by call, because the spool is content-addressed and
	// two callers wanting the same blob are two callers wanting the same
	// file.
	locksMu sync.Mutex
	locks   map[string]chan struct{}
	// root is the spool directory under the instance state directory.
	// Empty disables resuming: a state directory is not guaranteed to
	// exist (authentication can be off, FR-075), and there is no second
	// place partial content may be written.
	root string
	// threshold is the declared blob size from which the resumable path
	// applies. Zero disables it.
	threshold int64
}

// New builds the instance's resumer.
//
// eg is the shared outbound transport; kc the keychain the reading side
// already uses (nil means anonymous, which is what a remote option without
// WithAuth resolves to). stateRoot is the instance state directory —
// empty, or a threshold of zero, leaves every blob on the plain streaming
// path.
func New(eg *netx.Egress, kc authn.Keychain, stateRoot string, threshold config.Size) *Resumer {
	r := &Resumer{
		egress:    netx.Or(eg),
		keychain:  kc,
		threshold: threshold.Bytes(),
		locks:     map[string]chan struct{}{},
	}
	if stateRoot != "" {
		r.root = filepath.Join(stateRoot, "partials")
	}
	return r
}

// Handles reports whether a blob of that declared size takes the
// resumable path. A size the manifest did not declare (zero or negative)
// never does: without a length there is nothing to resume towards, and
// guessing would turn an unknown into a truncation.
func (r *Resumer) Handles(size int64) bool {
	return r != nil && r.root != "" && r.threshold > 0 && size >= r.threshold
}

// Threshold reports the configured threshold in bytes, for the startup
// line that says whether this instance resumes at all.
func (r *Resumer) Threshold() int64 {
	if r == nil {
		return 0
	}
	if r.root == "" {
		return 0
	}
	return r.threshold
}

// Open downloads the blob into the spool — resuming a previous attempt
// when one is on disk — verifies it whole against dgst, and returns a
// reader over the verified bytes. Closing the reader removes the spool.
//
// The digest check happens here, before the caller sees a single byte:
// FR-029 buys nothing if the price is a store that can be poisoned by
// concatenating two halves of different content.
func (r *Resumer) Open(ctx context.Context, repo name.Repository, dgst digest.Digest, size int64, report Progress) (io.ReadCloser, error) {
	// The lock is held until the RETURNED READER is closed, not until
	// this function returns: the spool file is the reader, and a second
	// caller that opened it while the first was still streaming it into
	// the store would read a file being deleted underneath it.
	release, err := r.lock(ctx, dgst)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()

	sp, err := openSpool(r.root, dgst)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !ok {
			_ = sp.close()
		}
	}()

	client, blobURL, err := r.client(ctx, repo, dgst)
	if err != nil {
		return nil, err
	}

	// A spool whose recorded length disagrees with the blob we are asked
	// for is not this blob's spool: a truncated manifest, a reused
	// digest file, an interrupted write. Starting over costs one
	// download; trusting it costs a corrupt store.
	if sp.offset() > size {
		if err := sp.reset(); err != nil {
			return nil, err
		}
	}
	if err := sp.rehash(); err != nil {
		return nil, err
	}

	// A spool that is already complete asks for nothing. This is not an
	// optimization: it is the state left by a process killed between the
	// last byte and the store write, and requesting "bytes <size>-" from
	// there is a range no origin can answer sensibly.
	var lastErr error
	for attempt := range attempts {
		if size > 0 && sp.offset() == size {
			lastErr = nil
			break
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<(attempt-1)) * backoffBase):
			}
		}
		lastErr = r.pull(ctx, client, blobURL, sp, size, report)
		if lastErr == nil {
			break
		}
		if !retryable(lastErr) {
			return nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}

	if !sp.verified() {
		// The bytes on disk are not the blob that was asked for. Discard
		// them whole rather than keep a prefix that would be resumed into
		// the same mismatch forever.
		_ = sp.discard()
		return nil, taxonomy.New(taxonomy.CodeDigestMismatch, taxonomy.Params{
			"reference": repo.Name() + "@" + dgst.String(),
			"expected":  dgst.String(),
			"actual":    sp.actual(),
		})
	}
	rc, err := sp.reader(release)
	if err != nil {
		return nil, err
	}
	ok = true
	return rc, nil
}

// lock takes the per-digest lock, honoring the caller's deadline: a
// transfer waiting behind another one must still be cancellable, or a
// shutdown would hang for as long as the largest blob in flight.
func (r *Resumer) lock(ctx context.Context, dgst digest.Digest) (release func(), err error) {
	key := dgst.String()
	for {
		r.locksMu.Lock()
		held, busy := r.locks[key]
		if !busy {
			r.locks[key] = make(chan struct{})
			r.locksMu.Unlock()
			return func() {
				r.locksMu.Lock()
				ch := r.locks[key]
				delete(r.locks, key)
				r.locksMu.Unlock()
				close(ch)
			}, nil
		}
		r.locksMu.Unlock()
		select {
		case <-held:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// pull performs one attempt: one request, from the spool's current offset
// to the end of the blob.
func (r *Resumer) pull(ctx context.Context, client *http.Client, blobURL string, sp *spool, size int64, report Progress) error {
	off := sp.offset()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL, http.NoBody)
	if err != nil {
		return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	if off > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
		// If-Range makes a compliant origin answer 200 with the whole
		// body when the content changed — which the 200 branch below
		// handles by restarting. Registries are content-addressed, so
		// this should never fire; it is here because "should never" is
		// not a integrity argument.
		if v := sp.validator(); v != "" {
			req.Header.Set("If-Range", v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return mapTransportErr(blobURL, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side; the digest decides

	switch resp.StatusCode {
	case http.StatusOK:
		// The origin ignored Range — a caching proxy, an object store, an
		// older registry. It is sending the WHOLE blob, so everything
		// already spooled is about to be duplicated. Truncating is the
		// only correct move: appending would concatenate the prefix with
		// a full copy of itself and produce a blob that verifies against
		// nothing.
		if err := sp.reset(); err != nil {
			return err
		}
		off = 0
	case http.StatusPartialContent:
		start, total, perr := parseContentRange(resp.Header.Get("Content-Range"))
		if perr != nil {
			return rangeErr(blobURL, perr.Error())
		}
		if start != off {
			return rangeErr(blobURL, fmt.Sprintf("206 starts at byte %d, %d was requested", start, off))
		}
		if total >= 0 && size > 0 && total != size {
			return rangeErr(blobURL, fmt.Sprintf("206 declares a %d-byte blob, the manifest declares %d", total, size))
		}
		// A validator that moved between two attempts means the bytes
		// behind this digest changed. Resuming would splice two
		// different objects; the spool goes, and the next attempt starts
		// from zero.
		if before, now := sp.validator(), responseValidator(resp); before != "" && now != "" && before != now {
			if err := sp.reset(); err != nil {
				return err
			}
			return rangeErr(blobURL, "the source content changed between two attempts (validator "+before+" became "+now+")")
		}
	default:
		return mapStatus(blobURL, resp)
	}

	if off == 0 {
		sp.setValidator(responseValidator(resp))
	}
	if err := sp.append(ctx, resp.Body, size, report); err != nil {
		return err
	}
	if size > 0 && sp.offset() != size {
		return rangeErr(blobURL, fmt.Sprintf("the source closed after %d of %d bytes", sp.offset(), size))
	}
	return nil
}

// client builds the authenticated HTTP client and the blob URL.
//
// transport.NewWithContext is go-containerregistry's own auth stack —
// ping, WWW-Authenticate parsing, token acquisition, scopes — and it takes
// an arbitrary base RoundTripper. Handing it the shared egress is what
// makes this direct path go through the instance's proxy and private
// authorities like every other outbound path, instead of beside them.
func (r *Resumer) client(ctx context.Context, repo name.Repository, dgst digest.Digest) (*http.Client, string, error) {
	auth := authn.Anonymous
	if r.keychain != nil {
		resolved, err := authn.Resolve(ctx, r.keychain, repo)
		if err != nil {
			return nil, "", taxonomy.New(taxonomy.CodeRegistryAuth, taxonomy.Params{
				"host": repo.RegistryStr(),
			}).WithCause(err)
		}
		auth = resolved
	}
	tr, err := transport.NewWithContext(ctx, repo.Registry, auth, r.egress.RoundTripper(),
		[]string{repo.Scope(transport.PullScope)})
	if err != nil {
		return nil, "", mapTransportErr(repo.RegistryStr(), err)
	}
	client := &http.Client{Transport: tr, CheckRedirect: refuseSSRFRedirect}
	u := url.URL{
		Scheme: repo.Scheme(),
		Host:   repo.RegistryStr(),
		Path:   "/v2/" + repo.RepositoryStr() + "/blobs/" + dgst.String(),
	}
	return client, u.String(), nil
}

// refuseSSRFRedirect is the guard go-containerregistry applies to its own
// blob fetches, and it has to be restated here because taking over the
// request means taking over its hazards: a hostile registry answering 302
// towards a link-local metadata service is a real attack, not a
// hypothetical one. Same-host redirects and DNS names are allowed, as
// upstream does; only a cross-host redirect to a private IP literal is
// refused.
func refuseSSRFRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if req.URL.Hostname() == via[0].URL.Hostname() {
		return nil
	}
	if ip := net.ParseIP(req.URL.Hostname()); ip != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
			return fmt.Errorf("refusing a redirect from %q to the private address %q", via[0].URL.Hostname(), req.URL.Hostname())
		}
	}
	return nil
}

// responseValidator returns the strong cache validator of a response, in
// order of usefulness. distribution serves the blob digest as its ETag,
// which makes the check below free integrity rather than an extra round
// trip.
func responseValidator(resp *http.Response) string {
	if v := resp.Header.Get("ETag"); v != "" {
		return v
	}
	return resp.Header.Get("Last-Modified")
}

// parseContentRange reads "bytes <start>-<end>/<total>". total is -1 when
// the origin sent "*", which is legal and simply means it declines to say.
func parseContentRange(v string) (start, total int64, err error) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !ok {
		return 0, 0, fmt.Errorf("unusable Content-Range %q", v)
	}
	rng, totalStr, ok := strings.Cut(spec, "/")
	if !ok {
		return 0, 0, fmt.Errorf("unusable Content-Range %q", v)
	}
	startStr, endStr, ok := strings.Cut(rng, "-")
	if !ok {
		return 0, 0, fmt.Errorf("unusable Content-Range %q", v)
	}
	start, err = strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("unusable Content-Range %q", v)
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return 0, 0, fmt.Errorf("unusable Content-Range %q", v)
	}
	total = -1
	if totalStr != "*" {
		total, err = strconv.ParseInt(totalStr, 10, 64)
		if err != nil || total <= end {
			return 0, 0, fmt.Errorf("unusable Content-Range %q", v)
		}
	}
	return start, total, nil
}

// rangeErr is the taxonomy entry for an origin whose partial responses do
// not add up. It is operational, not a verification failure: nothing has
// been proven wrong about the content, the conversation about it is what
// broke.
func rangeErr(blobURL, detail string) error {
	return taxonomy.New(taxonomy.CodeRangeUnusable, taxonomy.Params{
		"reference": redactURL(blobURL),
		"detail":    detail,
	})
}

// mapStatus turns an unexpected status into the taxonomy, reusing
// go-containerregistry's error decoder so a registry that explains itself
// in an OCI error document still gets to.
func mapStatus(blobURL string, resp *http.Response) error {
	resp.Body = io.NopCloser(io.LimitReader(resp.Body, errorBodyLimit))
	err := transport.CheckError(resp, http.StatusOK, http.StatusPartialContent)
	host := hostOf(blobURL)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return taxonomy.New(taxonomy.CodeRegistryAuth, taxonomy.Params{"host": host}).WithCause(err)
	case http.StatusNotFound:
		return taxonomy.New(taxonomy.CodeRefNotFound, taxonomy.Params{"reference": redactURL(blobURL)}).WithCause(err)
	case http.StatusRequestedRangeNotSatisfiable:
		return rangeErr(blobURL, "the source refused the requested byte range")
	default:
		return taxonomy.New(taxonomy.CodeRegistryUnreachable, taxonomy.Params{"host": host}).WithCause(err)
	}
}

// mapTransportErr turns a transport failure into the taxonomy. A deadline
// keeps its own code, as everywhere else: "took too long" and "is not
// there" send an operator to two different places.
func mapTransportErr(target string, err error) error {
	host := hostOf(target)
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return taxonomy.New(taxonomy.CodeInspectTimeout, taxonomy.Params{
			"host": host, "timeout": "the transfer deadline",
		}).WithCause(err)
	}
	return taxonomy.New(taxonomy.CodeRegistryUnreachable, taxonomy.Params{"host": host}).WithCause(err)
}

// retryable reports whether another attempt on the same spool can plausibly
// do better. Verification verdicts and policy refusals cannot: retrying
// them only delays the same answer.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te.Entry().Class == taxonomy.ClassOperational
	}
	return true
}

// hostOf extracts the host of a URL for error parameters, falling back to
// the string itself when it is already a host.
func hostOf(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return target
	}
	return u.Host
}

// redactURL renders a blob URL for a user-visible message: scheme, host
// and path only. Query strings are dropped because a redirected blob URL
// routinely carries a signed credential in them (NFR-015).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

// OpenOr is the one-line form every transfer path uses: the resumable
// path above the threshold, the caller's plain streaming opener below it.
//
// It exists so that no call site has to spell the threshold test itself.
// A transfer path that forgot it would not fail visibly — it would simply
// stop resuming, which is the defect this package was written to remove
// and the kind that is only noticed on the day a link degrades.
func (r *Resumer) OpenOr(ctx context.Context, repo name.Repository, dgst digest.Digest, size int64, report Progress, plain func() (io.ReadCloser, error)) (io.ReadCloser, error) {
	if !r.Handles(size) {
		return plain()
	}
	return r.Open(ctx, repo, dgst, size, report)
}
