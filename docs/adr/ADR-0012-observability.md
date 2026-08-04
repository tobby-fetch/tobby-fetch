# ADR-0012 — Observability & operations: structured logs, OpenMetrics, health probes

## Status

Accepted — 2026-07-11

## Context

Tobby runs in two very different operational postures. In **passthrough** mode it
is a long-lived containerized service in a connected or restricted zone, operated
like any other platform component: log collectors on stdout, Prometheus scraping,
orchestrator health probes. In **mirror** mode it runs on an isolated —
possibly transportable — workstation where there is no log collector, no
Prometheus, and often no network: the only durable record of *what crossed the
air gap, when, and with what verification results* is what Tobby writes onto the
transport medium itself. In regulated environments that record is audit
evidence, not debugging convenience.

The proof-of-concept supplied two direct lessons:

- Its JSON access/error log split (stdout/stderr) worked well and is kept.
- Its **hand-rolled metrics were wrong**: the value it exported as the process
  "working set" was actually the Go heap — a plausible-looking number that
  understated real memory use and would have misled capacity planning. Inventing
  metric plumbing is exactly the kind of code that fails silently; the
  battle-tested client library exists for a reason.

Finally, trust: Tobby is deployed inside networks that are cut off from the
internet *on purpose*. Any component that phones home — telemetry, update
checks, crash reporting — is disqualifying in this market, independent of what
the data contains.

## Decision

### Structured logs — `log/slog`, destination follows the mode

All logs are structured JSON, produced with the standard library's `log/slog`
(no third-party logging framework):

| Mode | Access/audit log destination | Error log destination |
|---|---|---|
| passthrough | **stdout** (collector-friendly, 12-factor) | **stderr** |
| mirror | **file on the transport medium**, path configurable | **stderr** (+ same file) |

```yaml
# tobby config excerpt — mirror mode writes its audit trail onto the medium
logging:
  level: info
  mirror:
    file: /media/tobby-store/logs/tobby.log   # configurable location on the medium
    maxSizeMB: 100                            # size-based rotation, rotated files kept alongside
```

In mirror mode the log file lives **inside the self-contained store**
(ADR-0006), so the record of a transfer travels with the artifacts it describes:
the destination-side operator receives both the payload and the evidence.
Log entries carry stable keys (`ts`, `level`, `msg`, plus contextual fields such
as `recipe`, `ingredient`, `digest`, `source`, `destination`, `user`, `outcome`)
so they are parseable without regexes:

```json
{"ts":"2026-07-11T14:03:22Z","level":"INFO","msg":"ingredient pushed",
 "recipe":"wordpress","ingredient":"nginx","digest":"sha256:9f2d…",
 "destination":"registry.zone.internal/cookbook/nginx","user":"operator-1",
 "outcome":"success"}
```

### Metrics — OpenMetrics via the official Prometheus client

Metrics are exposed on `/metrics` in OpenMetrics format using the **official
Prometheus Go client library** (`prometheus/client_golang`) — explicitly *not*
hand-rolled, encoding the POC lesson above. This provides correct process and Go
runtime collectors out of the box, plus Tobby's domain metrics: transfers and
bytes by outcome, per-Ingredient sync status, queue depth and task latencies,
signature verification and scan results by outcome, vulnerability-DB age
(ADR-0008), embedded registry storage size.

In mirror mode the endpoint still exists (useful interactively) but nothing is
assumed to scrape it; the log file remains the durable record.

### Health endpoints and graceful shutdown

- `GET /healthz` — liveness: the process is up and serving.
- `GET /readyz` — readiness: storage backend writable, configuration valid,
  embedded registry initialized; returns `503` during startup and drain.
- **Graceful shutdown** on SIGTERM/SIGINT with a **configurable delay**
  (`shutdown.gracePeriod`, default 30s): readiness flips to `503` first,
  in-flight HTTP requests and running transfer tasks get the grace period to
  complete or checkpoint, then the process exits. Interrupted transfers are safe
  by construction — imports are digest-addressed and idempotent, so a retry
  resumes without corruption.

### No outbound telemetry — ever, by default

Tobby makes **no network connection that was not explicitly configured** by the
operator (source registries, destination registries, IdP). No usage telemetry, no
crash reporting, no update checks, no DNS beacons. This is a product guarantee
stated in the documentation, not just an unset default: in air-gapped and
regulated environments, a tool that phones home is a tool that gets banned. Any
future opt-in diagnostics bundle would be a *local file export* the operator
chooses to share, never a transmission.

## Consequences

### Positive

- Passthrough deployments plug into standard platform tooling (log collectors,
  Prometheus, Kubernetes probes) with zero adapters.
- Mirror transfers are self-documenting: the medium carries an append-only,
  machine-parseable audit trail of everything that crossed, satisfying
  evidence expectations in regulated contexts.
- Using `log/slog` and `client_golang` — both boring, correct, maintained —
  removes two categories of self-inflicted bugs (the POC demonstrated one) and
  adds no exotic dependencies to the supply-chain surface (ADR-0011).
- The no-telemetry guarantee is a trust differentiator that costs nothing to
  honor because the architecture never needed outbound calls.

### Negative

- No distributed tracing in v1: debugging a multi-step transfer relies on
  correlated log fields (task/recipe/digest) rather than spans. Accepted —
  Tobby is a single process, and the correlation fields are designed so traces
  could be added later without reshaping the logs.
- File logging on removable media needs care around rotation and flushing
  (media can be yanked); size-based rotation and explicit fsync on task
  boundaries are implementation requirements tracked in the SRS.
- Domain metric names and labels become a de-facto API for users' dashboards;
  they are documented and versioned with the same compatibility discipline as
  the REST API.

### Neutral

- `/healthz` and `/readyz` follow Kubernetes conventions but are plain HTTP —
  equally usable by systemd, load balancers, or a human with `curl`.

## Alternatives considered

### Full OpenTelemetry (traces + metrics + logs, OTLP export)

The maximalist observability answer, and genuinely attractive for the tracing
model. Rejected **for v1** because: it is oversized for a single-binary tool
whose primary deployments include machines with no collector to export to; the
OTel SDK and its exporters are a large dependency subtree in a project that
fights for a minimal supply-chain surface (ADR-0011); and OTLP's push model sits
awkwardly next to the no-outbound-connections guarantee, inviting
misconfiguration. Revisit post-1.0 if users operating fleets of passthrough
instances ask for traces; the structured-log correlation fields keep that door
open.

### Hand-rolled metrics endpoint (the POC approach)

Rejected with prejudice: the POC's custom collector exported the Go heap as the
process working set — a correctness bug invisible until someone compares numbers
with the OS. Metric plumbing is undifferentiated code where a de-facto standard
library is strictly better on correctness, exposition-format compliance, and
reviewer familiarity.

### Logging framework (zap / zerolog) instead of `log/slog`

Marginal performance gains at Tobby's log volumes do not justify a third-party
dependency now that `slog` is standard library, structured, and levelled.
Performance-sensitive hot paths (per-blob transfer loops) simply avoid per-byte
logging regardless of the library chosen.
