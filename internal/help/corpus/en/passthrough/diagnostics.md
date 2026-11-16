---
title: Collect a diagnostic bundle
description: One local, redacted archive to hand to support — what it holds, how to produce it from the command line, the interface or the API, and how redaction fails closed.
sidebar:
  order: 10
---

When something goes wrong, the person helping you needs the same dozen
things: which build, how it is configured, what it logged, what its last
tasks did, what the metrics say, whether the store verifies, what the state
directory looks like. The diagnostic bundle gathers them in **one gesture**
into **one archive** — and it is built so that you can hand it over without
reading it line by line for secrets.

Tobby sends the archive nowhere. There is no upload, no call home, no
address to configure (NFR-019): it is a file you write or download, and you
choose who receives it.

## What it holds

A gzip-compressed tar, with files at fixed names so a support engineer finds
the same thing in every archive:

| File | What it is |
|---|---|
| `MANIFEST.json` | Every file of the archive with its size and SHA-256 digest, the format version, and every documented file **left out** with the reason. |
| `version.json` | Version, commit, build date, Go version, platform, mode, zone. |
| `config.yaml` | The effective configuration, redacted exactly as [`tobby config dump`](../../reference/cli/) redacts it. |
| `logs.jsonl` | The most recent log records the instance holds in memory — a window of `diagnostics.logRecords` records (default 2000, at most 100000). |
| `media-log.jsonl` | Mirror mode: the tail of the operation log on the medium. |
| `tasks/<id>.json` | The reports of the most recent tasks — `diagnostics.tasks` of them (default 20, at most 500) — with their items, errors and resolutions. |
| `metrics.txt` | A snapshot of the metrics, as a scrape would return them. |
| `integrity.json` | The last [on-demand verification](../verify-store/) report, if one was run. |
| `state-inventory.json` | The [state directory](../backup-rebuild/): names, classes, sizes and permissions — never contents. |

The log window is a **window, never an archive**: it lives in the memory of
the running process and is gone when the process ends. A passthrough instance
logs to its standard output and keeps nothing else, so what matters for a
later incident has to be collected by your own log pipeline too.

## Produce it

**On the host**, with the configuration the instance uses:

```sh
tobby diagnostics bundle --config /etc/tobby/config.yaml
# diagnostic bundle written to tobby-diagnostics-20261116T101500Z.tar.gz (...)
tobby diagnostics bundle -o /var/tmp/support.tar.gz --output json
```

Without a running instance the archive is assembled from the configuration,
the store and the state directory. The log window and the metrics belong to a
running process: they are listed as **omitted**, with the reason, in the
manifest. An existing file is never overwritten, and the archive is written
readable by its owner only.

**From the running instance**, by the same command asking the API:

```sh
export TOBBY_API_TOKEN=...        # an administrator's token (never a flag)
tobby diagnostics bundle --instance https://tobby.example:8443
```

or from the administration screen **Diagnostics** (`/admin/diagnostics`), or
with `POST /api/v1/diagnostics/bundle`. The instance's archive adds the log
window and the metrics. It needs the **administrator** role: the operator and
viewer roles are refused.

Generating a bundle is an event of the [audit log](../../security/audit-log/)
(`diagnostics.bundle`) naming who asked and whether it was written, and the
counter `tobby_diagnostics_bundles_total{outcome}` counts the outcomes:
`written`, `refused`, `failed`.

## Redaction fails closed

The archive is checked **before it is released**, in memory, and not written
at all if the check finds a secret. Two steps:

1. **Scrub.** In the files that carry text other components wrote — the log
   window, the medium's log, the task reports, the metrics — the secrets the
   instance knows are replaced with `[REDACTED]`, together with credentials of
   a recognizable shape: `Authorization` and `Cookie` headers, bearer and
   basic credentials, a password inside a URL, a private key block, and the
   value of a key named `password`, `secret`, `token` and the like.
2. **Check.** *Every* file is then searched for the secrets the instance
   knows. The structured files Tobby writes itself — version, configuration,
   integrity report, state inventory — are not scrubbed: a secret in one of
   them is a defect, and the bundle is **aborted** with
   [`TBY-DIA-002`](../../reference/errors/#tby-dia-002), naming the file, never
   the secret.

"The secrets the instance knows" are: every secret value of the configuration
(the proxy password, wherever it was set), the **content** of every secret file
the configuration references (registry credentials, the TLS private key, the
OIDC client secret, the SAML key) and of the key files in the state directory,
and the value of the environment variables that look like credentials
(`*PASSWORD*`, `*SECRET*`, `*TOKEN*`, `*KEY*`…). Each is searched in clear and
in the encodings it is likely to appear in: base64 (standard, raw, URL), hex,
URL-escaped, JSON-escaped; for a private key, its PEM body; for a registry
credentials file, each credential and the decoded `user:password`. If a
configured secret file exists but cannot be read, the bundle fails with
[`TBY-DIA-001`](../../reference/errors/#tby-dia-001) rather than guess what it
holds.

What this does **not** promise, stated plainly:

- A secret **shorter than 4 bytes** is not searched; one shorter than 8 is
  only matched as a whole word, so that a short password does not redact every
  word that contains it. The value of an environment variable is only taken
  as a secret from 8 bytes on, because a variable that merely has "KEY" in its
  name and the value `true` would otherwise make every document that contains
  the word abort the bundle.
- A secret the instance does not know — typed by a person into a free-text
  field of a task, say — is caught only if it has a recognizable shape.
- The check protects against **secrets**. Hostnames, registry names, image
  references and account names are in the archive, and may be sensitive to you.
  Read what you share.

## Configuration

| Key | Default | Bound |
|---|---|---|
| `diagnostics.logRecords` | `2000` | 1 to 100000 — the size of the in-memory window. |
| `diagnostics.tasks` | `20` | 1 to 500 — how many recent task reports. |
