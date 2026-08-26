---
title: Supported platforms
description: The feature matrix per operating system — what is validated on Linux, on Windows and on macOS, how each is verified, and the platform-specific behaviour an operator has to know about.
sidebar:
  order: 7
  badge:
    text: J5
    variant: note
---

:::note[Upcoming — milestone 5]
Windows enters the validated operating scope with the v0.5.x release train.
Everything on this page is implemented and exercised in continuous
integration today; it becomes a supported claim when that train ships.
Track it on the [project status](../../discover/status/) page.
:::

Tobby ships as a single statically linked binary for Linux and Windows on
amd64 and arm64, plus macOS binaries as a convenience tier (SRS NFR-001).
Running everywhere and being **validated** everywhere are different
claims, and this page keeps them apart.

Three words are used below and they mean exactly this:

- **Validated** — an end-to-end operating scenario runs on this platform
  in continuous integration, and the behaviour is supported in
  production.
- **Tested** — the unit and integration suite runs on this platform in
  continuous integration, under the race detector, twice per run.
- **Builds** — the target compiles and the binary starts. Nothing more is
  claimed.

## Matrix

| Capability | Linux | Windows | macOS |
|---|---|---|---|
| Mirror mode — manual synchronization (FR-014) | Validated | **Validated** | Tested |
| Self-contained transportable store (FR-050) | Validated | **Validated** | Tested |
| Operation log on the transport store (FR-053) | Validated | **Validated** | Tested |
| Media manifest and its verification (FR-054) | Validated | **Validated** | Tested |
| Destination-side operation, `tobby media` (FR-052) | Validated | **Validated** | Tested |
| OCI image layout export / import (FR-051) | Validated | **Validated** | Tested |
| Embedded OCI registry on `/v2/` (FR-040) | Validated | **Validated** | Tested |
| Web UI and REST API (FR-062, FR-061) | Validated | **Validated** | Tested |
| FileSet serving under `/files/` (FR-047) | Validated | Validated, with one caveat below | Tested |
| FileSet packing (FR-048) | Validated | Tested | Tested |
| Passthrough mode — continuous promotion (FR-013) | Validated | Builds | Tested |
| Signed recipes, cosign verification (FR-033) | Validated | Tested | Tested |
| Removable-medium transport on a real block device | Validated | Simulated in CI | Not covered |
| Container image (`ghcr.io/tobby-fetch/tobby-fetch`) | Validated | — | — |
| Linux packages (`.deb`, `.rpm`, `.apk`) | Validated | — | — |

Passthrough mode is delivered and validated as a containerized Linux
service. The single Windows binary can run it, but passthrough on Windows
is outside the v1.0.0 validated scope (NFR-018): a zone that is
permanently connected is a server, and Tobby's server story is Linux.

macOS is a convenience tier. The full test suite runs on macOS runners and
the binaries go through the same reproducible, SBOM-and-provenance-carrying
release chain, but no end-to-end operating scenario is validated on it and
no production support is implied.

## How each platform is verified

Continuous integration runs the whole suite — `go test -race -count=2` —
on `ubuntu-latest`, `ubuntu-24.04-arm`, `macos-latest` and
`windows-latest`, and every job gates merges. Beyond that:

- **Linux** carries the hermetic topology scenarios and the browser
  non-regression suite, and is the platform the acceptance crucible runs
  on — including the one thing no CI runner can do, which is writing to a
  real removable block device and carrying it between two isolated
  networks.
- **Windows** runs the UC2 journey end to end: a mirror synchronization
  produces a store, the store is copied to a path it has never occupied
  (the transport, simulated — a hosted runner has no removable device),
  and a destination-side instance verifies it and pushes its content, with
  digests identical from one end to the other. The runner also attaches a
  genuine FAT32 volume so the file-size pre-flight of FR-055 is exercised
  against the filesystem it exists for, rather than against a fixture.
- **macOS** runs the same unit and integration suite, and creates a real
  FAT32 disk image for the same pre-flight check.

## Windows specifics

### Installing

The Windows binaries are portable: a single `.exe` with no runtime
dependency and no installer. Two channels are prepared —
[winget](https://learn.microsoft.com/windows/package-manager/) and
[Scoop](https://scoop.sh/) — and both install the same release artifact
pinned by SHA-256. Until they are accepted into their respective indexes,
download `tobby-windows-amd64.exe` or `tobby-windows-arm64.exe` from the
[releases page](https://github.com/tobby-fetch/tobby-fetch/releases) and
verify it as described in
[Verify a release](../../project/verify-a-release/).

### File permissions are an access list, not mode bits

Files holding secret material — registry credentials, TLS private keys,
the local user database, static tokens — are created owner-only (NFR-020).
On Unix that is mode `0600`. On Windows mode bits carry nothing: Windows
maps the write bit onto the read-only attribute and discards the rest, so
a file "created 0600" there would be readable by every account the parent
directory admits, which in a domain-joined deployment is a great many of
them.

What enforces the rule on Windows is the discretionary access control
list, replaced outright with a single entry naming the file's own owner
and marked *protected*, so the parent directory's inheritable entries are
not merged back in. The owner is read from the object rather than assumed
to be the current process, so a file restored from a backup, or created by
an installer running as another account, still ends up readable by exactly
one account — the one that owns it.

A consequence worth knowing: copying such a file to a Unix host, or onto a
FAT32 medium, does not carry the access list. Secrets are not supposed to
travel at all (NFR-020) — Tobby refuses to start when a configured secret
path resolves under the store root — but a backup taken with a tool that
ignores access lists is a different matter.

### FAT32 and the 4 GiB ceiling

A USB stick formatted on a Windows workstation is very often FAT32, and
FAT32 stores a file length in 32 bits: no single file may reach 4 GiB.
Tobby identifies the filesystem of the store and of an export target
before an operation starts and refuses one that cannot hold the largest
file it would write, naming the limit (FR-055). Format the medium exFAT or
NTFS when a single blob or a single export archive can exceed 4 GiB.

### Symbolic links in FileSets

Extracting a FileSet that contains a symbolic link requires
`SeCreateSymbolicLinkPrivilege`, which Windows grants to administrators
and to nobody else by default. An account without it gets a refusal naming
the privilege; grant it through *Create symbolic links* in the local
security policy, or enable Developer Mode. FileSets containing no symbolic
link are unaffected.

### Long paths

The store nests content under digest-derived directories, which stays well
inside the 260-character limit for any reasonable store root. A store root
that is itself deeply nested can still reach it; enable long-path support
(`HKLM\SYSTEM\CurrentControlSet\Control\FileSystem\LongPathsEnabled`) or
keep the store nearer the root of its volume.

### Graceful shutdown

`SIGTERM` is never raised by the Windows kernel. Tobby drains gracefully on
Ctrl+C and Ctrl+Break in a console; a stop that bypasses the console —
`taskkill /F`, a service stop — terminates the process without a drain.
An interrupted synchronization is never lost: it stays running on disk and
resumes on the next start (FR-029).

## What is deliberately not covered

Honesty about the gaps is part of the matrix:

- **A real removable block device on Windows.** The transport leg is a
  directory copy in CI. The real device — mounted, filled, unmounted,
  carried — is exercised on Linux by the acceptance crucible.
- **The third-party OCI client checks (FR-076) on Windows and macOS.**
  They need a Linux Docker daemon and a way to install a private trust
  anchor for it; they run on the Linux runners and are skipped, by name,
  elsewhere.
- **Passthrough mode on Windows**, as stated above.
- **Exact permission bits of an extracted FileSet on Windows.** The
  extraction preserves what the platform can express; Windows expresses
  neither the setuid bit nor a three-digit mode, so those assertions run
  on Unix only.
