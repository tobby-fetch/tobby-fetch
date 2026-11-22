---
title: Supervise an instance
description: The metrics reference, the alert rules and the dashboard shipped with the chart, and the signed OpenAPI document — what to alert on, and what the release publishes for it.
sidebar:
  order: 12
---

An instance that reconciles for months needs to be watched by something other
than the person who set it up. This page is the **supervision kit**: the
metrics reference, alert rules and an example dashboard that ship with the
reference chart, and the two documents a release publishes, signed, for
whoever integrates against Tobby.

## What the release publishes

Every release attaches two files, each with a cosign bundle beside it, signed
keyless the way the SBOMs are:

| File | What it is |
|---|---|
| `openapi.yaml` | The OpenAPI 3.1 document of the REST API — byte-identical to what a running instance serves at `/api/v1/openapi.yaml`. |
| `metrics-reference.json` | Every `tobby_*` metric family: type, help, labels with their closed value sets, unit, the requirement it serves, and when it is present. |

```sh
cosign verify-blob --new-bundle-format \
  --bundle metrics-reference.json.bundle \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/tobby-fetch/tobby-fetch/.github/workflows/release.yml@refs/tags/v1.0.0 \
  metrics-reference.json
```

Replace `v1.0.0` with the version you verify: the identity is pinned to the release tag, because a rehearsal of the release workflow signs identical files from a branch of the same repository.

The reference is **generated from the running registry** (`go run
./tools/metricsref`), and a test fails if a family exists that the reference
does not list, or the reverse — it cannot drift from the endpoint. Renaming or
removing a family is a breaking change under the 1.x promise; a new family is
not. The standard `go_*` and `process_*` families of the Prometheus client
library are outside it: their set depends on the Go version and the platform.

## Families added for supervision

| Family | Meaning |
|---|---|
| `tobby_sync_runs_total{outcome}` | Synchronization runs, `success` or `failure` (a run that failed, or left an item failed). |
| `tobby_sync_duration_seconds{outcome}` | Their duration, as a histogram. |
| `tobby_sync_last_success_timestamp_seconds` | End of the last successful run. Absent until one has succeeded since the process started. |
| `tobby_sync_ingredients{status}` | Ingredients of the last run: `new`, `outdated`, `up_to_date`. |
| `tobby_sync_parallelism` | The configured bound on concurrent transfers, to compare with `tobby_sync_transfers_inflight`. |
| `tobby_server_certificate_expiry_timestamp_seconds` | `NotAfter` of the certificate the listener presents, self-signed fallback included; read at each scrape, so a replacement is followed. Absent on plain HTTP. |
| `tobby_clock_implausible{anchor}` | 1 while the [clock](../../air-gap/clock-check/) is found earlier than the build or a medium. |
| `tobby_diagnostics_bundles_total{outcome}` | [Diagnostic bundles](../diagnostics/) by outcome. |

A family is exposed from the first scrape **with a true value, or not at
all**: a timestamp gauge that has measured nothing is absent, never `0`,
because `0` would say "the last success was in 1970". An alert on staleness
therefore tells *never* from *long ago* by presence.

## The alert rules

The reference chart ships these rules, **disabled by default** and enabled by
one value each, in two forms: a `PrometheusRule` resource, and the same rules
as a plain Prometheus rules file.

```sh
helm upgrade tobby oci://ghcr.io/tobby-fetch/charts/tobby --version <version> \
  --namespace tobby --reuse-values \
  --set alerting.prometheusRule.enabled=true \
  --set alerting.prometheusRule.labels.release=<your Prometheus release> \
  --set dashboard.enabled=true
```

`alerting.rulesFile.enabled=true` renders the plain file into a ConfigMap
(`tobby-alerts.rules.yaml`) for a Prometheus that is not run by the Operator;
the same file, at default thresholds, is kept in the repository at
`deploy/charts/tobby/files/tobby-alerts.rules.yaml` and is generated from the
chart, never edited.

| Alert | Fires when | Default bound (`alerting.thresholds.*`) |
|---|---|---|
| `TobbySyncFailing` | At least `syncFailures` failed runs within `syncFailuresWindow`. | 3 in `1h` |
| `TobbySyncStale` | No successful synchronization for longer than `syncMaxAgeSeconds`. | `21600` (6 h); `0` leaves the rule out |
| `TobbySyncNeverSucceeded` | Runs have failed and none has ever succeeded since the process started. | for `syncFailuresWindow` |
| `TobbySyncSaturated` | Transfers pinned at `sync.parallelism`. | for `saturationFor`, `15m` |
| `TobbyStoreOccupancyHigh` | The store is beyond `storage.occupancyThreshold`. | for `storeOccupancyFor`, `15m` |
| `TobbyVulnerabilityDatabaseStale` | The database is older than `scan.maxDbAge`. | for `scannerFor`, `1h` |
| `TobbyVulnerabilityDatabaseAbsent` | No vulnerability database. | for `scannerFor`, `1h` |
| `TobbyScannerDegraded` | No usable verified scanner. | for `scannerFor`, `1h` |
| `TobbyServerCertificateExpiring` | The presented certificate expires within `certificateExpirySeconds`, or already has. | `1209600` (14 d), for `certificateExpiryFor`, `10m` |
| `TobbyClockImplausible` | The clock is earlier than the build or a medium. | for `clockFor`, `5m` |

Some honest notes. **Mirror instances** synchronize when an operator says so:
a bound on the age of the last success would page nobody usefully, so set
`alerting.thresholds.syncMaxAgeSeconds` to `0` there. The rules are
**starting points, not tuned for your site**: read what your instance does for
a week, then move the bounds. `/metrics` is **unauthenticated**, like the
probes, so the scrape needs no credentials;
`metrics.serviceMonitor.basicAuth` only serves a setup where you put an
authenticating proxy in front of the instance.

Every expression names only families of the published reference, and the
labels it selects on are inside the closed sets the reference documents; a
test holds the shipped file to that. Each rule also has a `promtool` unit test
that feeds it the condition it is for and the condition it is not
(`deploy/charts/tobby/tests/tobby-alerts_test.yaml`):

```sh
promtool test rules deploy/charts/tobby/tests/tobby-alerts_test.yaml
```

## The dashboard

`dashboard.enabled=true` renders a ConfigMap with the label the Grafana
sidecar watches (`dashboard.labels`). It is an **example**: runs by outcome,
age of the last success, duration, ingredients of the last run, transfers
against the bound, bytes, store occupancy, certificate time left, the
vulnerability database, the clock, refusals by code, sign-ins and bundles.
The JSON is `deploy/charts/tobby/files/tobby-dashboard.json`; import it by
hand if you do not run the sidecar.

What to do when an alert fires is on
[Troubleshooting](../../reference/troubleshooting/), organized by symptom.
