# Spike — resuming inside a blob: grafting onto go-containerregistry vs. a direct Range GET

> Decision record for the measurement spike required by R-29 ("fine-grained resume
> of large downloads"), which completes FR-029. FR-029 already retries with bounded
> backoff and resumes an interrupted *synchronization* from persisted state; what is
> missing is resumption *inside a single blob*. A multi-gigabyte layer cut at 90 %
> today restarts at 0 %, and on a saturated enterprise link that is the difference
> between a run that finishes and one that never does.
>
> The question this spike answers, by measurement and not by reading the source:
> can the resume be grafted on top of `remote.Layer(...).Compressed()` — reusing
> go-containerregistry's authenticated transport as-is — or does it need a direct
> GET path that reuses the same transport and the same authentication?

**Date:** 2026-08-18 · **Toolchain:** go1.25.6 · **go-containerregistry:** v0.21.9
(the version pinned in `go.mod`) · **Host:** darwin/arm64

## Methodology

No mock of the OCI protocol. The origin is a **real registry**: the project's own
embedded store (`internal/store`, CNCF distribution/distribution v3, filesystem
driver) served over its real `/v2/` API through `httptest`, exactly as
`internal/engine/helpers_test.go` builds its sources.

- **Workload:** a single 64 MiB layer (`random.Image(64<<20, 1)`, 67 114 626 –
  67 114 628 bytes depending on the draw), pushed with `remote.Write` as any
  standard client would.
- **Link emulation:** the origin's `http.ResponseWriter` is wrapped so that every
  `Write` (a) counts the bytes the *server* actually put on the wire and (b) sleeps
  for the transmission time of those bytes at **32 MiB/s**. Counting server-side is
  the only honest measure: the thing under test is precisely whether the client
  re-requests bytes it already has, and a client-side counter would agree with the
  bug.
- **Interruption:** the transfer is cut after **90 %** of the blob (60 403 163
  bytes), then resumed.
- **Transport:** every measurement goes through `netx.Egress.RoundTripper()` — the
  instance's single shared outbound transport (FR-080, FR-081). No measurement
  builds an `http.Transport` of its own.

Harness: a throwaway `internal/spiketmp` test package, run with
`go test -run TestSpike -v`, removed after the measurements were taken. The raw
`MEASURE` lines below are its output, verbatim.

## Measurements

### 1. Does the origin honor `Range` at all?

```
MEASURE origin-range: status=206 Content-Range="bytes 60403164-67114626/67114627"
  ETag="\"sha256:048d2d27809bd9b21555756af859776de439897e7c28e74595aab5ab03602b3e\""
  Accept-Ranges="bytes" bytes_read=6711463 want=6711463
```

Yes: `206 Partial Content`, a correct `Content-Range`, `Accept-Ranges: bytes`, and
a **strong validator whose value is the blob digest itself**. That last point is
free integrity: an `ETag` change between two attempts means the bytes behind the
digest changed, which must restart the transfer rather than concatenate two
halves of different content.

This is distribution v3 serving through `http.ServeContent`. It is not a
guarantee about every registry in the field — hence the requirement, tested
separately, to handle a server that ignores `Range` and answers `200` with the
whole body.

### 2. Path A — graft onto `remote.Layer(...).Compressed()`

**A1/A2 — reopen and discard the prefix client-side.** The only resume the public
API allows: call `Compressed()` again and throw away what you already have.

```
MEASURE pathA-reopen: blob=67114626 interrupted_after=60403163(90%)
  first_attempt_server_bytes=60424605 wall=2.13s
MEASURE pathA-reopen: resume_server_bytes=67114628 resume_wall=2.493s
  useful_bytes=6711463 overhead_ratio=10.00
```

The server put **67 114 628 bytes** on the wire to deliver **6 711 463** useful
ones: a **×10.00** overhead, and 2.493 s of wall clock against the 0.234 s the
same 10 % costs when actually resumed (measurement 3). This is not a resume. It is
a full re-download with the prefix discarded in the client, and it scales with
blob size exactly like the failure it is supposed to fix.

**A3 — inject a `Range` header into the transport under `remote.Layer`.** The
hypothesis worth testing: keep go-containerregistry's authenticated fetch, and add
the header from a `RoundTripper` wrapper.

```
MEASURE pathA-inject: Compressed() error = body exceeds 65536 byte limit
  (server sent 98306 bytes)
```

It fails, and it fails badly. `remote.(*fetcher).fetchBlobURL` asserts
`transport.CheckError(resp, http.StatusOK)`: a `206` is an *unexpected status*, so
the library then tries to parse the 6.7 MB partial body as a registry error
document under a 64 KiB cap. The caller gets an error about a byte limit that
mentions neither ranges nor partial content — a diagnosis that would cost an
operator an afternoon.

The API surface confirms there is no supported seam: `remote.fetcher`,
`fetchBlob` and `fetchBlobURL` are unexported, `remoteLayer.Compressed()` is the
only public read, and no `remote.Option` carries a byte offset. Grafting resume
onto `remote.Layer` is not "unsupported today" — it is refused by the library at
the status check.

### 3. Path B — direct GET reusing the same transport and the same authentication

```
MEASURE pathB: status=206 Content-Range="bytes 60403165-67114627/67114628"
  etag="\"sha256:96808755…\"" resumed_bytes=6711463 resume_wall=234ms
  first_wall=2.238186125s
MEASURE pathB: server_bytes_on_resume=6711463 useful=6711463 digest_match=true
  (want sha256:96808755… got sha256:96808755…) blob=67114628
```

Exactly the missing 6 711 463 bytes cross the wire — **overhead ×1.00** — in
**234 ms** against 2.493 s for path A, a **10.6× reduction in wall clock** on this
workload at this link rate. The SHA-256 taken over `prefix ++ suffix` equals the
pinned digest, so the concatenation is bit-exact.

### 4. Does path B inherit the registry authentication, or would it need its own?

The decisive question, because a second credential path would be a worse defect
than the one being fixed. Measured against the same real registry placed behind a
**bearer-token challenge** — the flow every production registry uses — with the
client built from a keychain and `transport.NewWithContext` layered on top of
`netx.Egress.RoundTripper()`:

```
MEASURE pathB-auth: status=206 Content-Range="bytes 2097622-4195244/4195245"
  bytes=2097623 want=2097623 token_endpoint_hits=2 challenges=2
```

The challenge was answered, the token fetched from the realm endpoint, and the
partial content served. `transport.NewWithContext(ctx, registry, authenticator,
base, scopes)` is **public** and takes an arbitrary base `RoundTripper`. So path B
reuses go-containerregistry's whole authentication stack (ping, `WWW-Authenticate`
parsing, token acquisition, scope handling) *on top of* the shared egress
transport, and reuses the instance's existing keychain. Nothing is
reimplemented, and the proxy and the private-PKI trust store still apply, because
the base of the stack is the one shared `http.Transport`.

## Analysis

- **Path A cannot resume.** Not "is awkward to resume": at ×10.00 overhead on a
  90 % interruption it is arithmetically the behavior R-29 exists to remove, and
  the header-injection variant is rejected by the library with a misleading error.
  The two sub-measurements close both readings of "graft a resume layer on top".
- **Path B is not a bypass of the shared transport.** This is the constraint that
  decides the shape of the implementation, not just its existence. The direct GET
  is issued by an `http.Client` whose transport is
  `transport.NewWithContext(…, egress.RoundTripper(), …)` — gcr's auth layer over
  the *instance's single* `http.Transport`. The proxy selection, the proxy
  credentials, and the private authorities of FR-080/FR-081 are on the path
  unchanged. A new outbound path nonetheless deserves to be *proved* on the
  shared transport rather than asserted, which is why it is added as a case of
  `TestEveryOutboundPathUsesTheSharedTransport`.
- **What path B costs.** Tobby takes over three things gcr was doing for it, and
  each must be re-established explicitly: the digest verification of the streamed
  bytes (gcr wraps its body in `verify.ReadCloser`), the `Content-Length`
  cross-check, and the SSRF guard on redirects (`checkRedirectSSRF` — a registry
  answering `302` towards `169.254.169.254` is a real attack, not a hypothetical).
  These are ~40 lines, they are testable, and they are the price of the ×10.
- **What path B does not cost.** It does not fork the fetch of *every* blob. Below
  the configured threshold, blobs keep streaming through `remote.Layer(...)`
  exactly as today: one code path, no spool file, no behavior change. The direct
  path exists only where the arithmetic above matters.
- **Where the partial bytes live.** In the **state directory**, never in the
  store (R-16): the store is transportable and a half-received blob is not
  content, it is local progress. It must not board the media, and it must not
  survive a store copy.

## Decision: **direct Range GET over the shared transport** (path B)

Blobs whose declared size reaches the configured threshold
(`transfer.resumeThreshold`, default 64 MiB) are fetched by an explicit
`GET /v2/<repo>/blobs/<digest>` carrying `Range: bytes=<offset>-`, issued by an
`http.Client` built from `transport.NewWithContext` over
`netx.Egress.RoundTripper()` and the instance's existing keychain. Bytes land in
a spool file under `<state.root>/partials/`, with the offset and the origin's
validator persisted beside them, so an interruption — a cut connection, a failed
attempt, a killed process — resumes at the offset instead of at zero. Blobs below
the threshold are untouched and keep the current `remote.Layer` streaming path.

Consequences for the implementation:

- **Integrity stays blocking.** The spooled bytes are hashed on their way into the
  store and the store's own commit verifies the digest a second time; a mismatch
  discards the spool whole and reports `TBY-SIG-002`. A resumed blob is never
  admitted on the strength of "the pieces looked right".
- **A server that ignores `Range` must be detected, not concatenated.** Answering
  `200` with the full body to a ranged request is common in the field (caching
  proxies, older registries, some object stores). The response status, not the
  request, decides where the bytes are written: a `200` truncates the spool and
  restarts it, a `206` whose `Content-Range` does not start exactly at the
  requested offset is refused outright.
- **A changed validator restarts from zero.** The `ETag` (or `Last-Modified`)
  observed on the first attempt is persisted and re-sent as `If-Range`; a
  different validator on the resumed response discards the spool. Registries are
  content-addressed, so this should never fire — which is exactly why it must be
  handled rather than assumed.
- **The threshold is configurable and can be turned off.**
  `transfer.resumeThreshold: 0` disables the direct path entirely and restores
  today's behavior byte for byte, which is the escape hatch for an environment
  where the extra disk traffic in the state directory is the worse trade.
- **The spool costs disk.** A resumable blob transits the state directory before
  reaching the store, so a resumable transfer needs its size in free space there
  temporarily. That is the reason for a threshold rather than resuming everything.
- **Chunked resumable *uploads* are explicitly out of scope** (post-v1): this
  spike and this decision cover the download side only.
