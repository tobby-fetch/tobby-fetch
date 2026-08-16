#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Reproducible Linux package build (extends the ADR-0011 chain). Wraps the
# release binaries produced by tools/release-build.sh into .deb, .rpm, and
# .apk packages via nfpm, bit-identically for a given SOURCE_DATE_EPOCH:
#
#   SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
#     sh tools/package-build.sh <version> <bindir> <outdir>
#
# Run from the repository root (the nfpm template path is relative). The
# same script is used by the release workflow, by the reproducibility gate
# that rebuilds and compares digests, and by independent verifiers
# (docs/release-verification.md) — one code path, no flag drift.
#
# Requires nfpm on PATH at the version pinned by NFPM_VERSION in
# .github/workflows/release.yml. nfpm derives all packaged-file mtimes and
# each format's build date from SOURCE_DATE_EPOCH, so the packages are
# independent of checkout and build times.
set -eu

VERSION="$1"   # release tag (e.g. v1.2.3)
BINDIR="$2"    # directory holding the tobby-linux-<arch> release binaries
OUTDIR="$3"

: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH must be set to the commit timestamp}"

# Package version follows the deb/rpm/apk convention: no leading "v".
TOBBY_VERSION="${VERSION#v}"
export TOBBY_VERSION

mkdir -p "$OUTDIR"
for arch in amd64 arm64; do
  TOBBY_ARCH="$arch"
  TOBBY_BINARY="${BINDIR}/tobby-linux-${arch}"
  export TOBBY_ARCH TOBBY_BINARY
  for format in deb rpm apk; do
    nfpm package \
      --config packaging/nfpm/nfpm.yaml \
      --packager "$format" \
      --target "${OUTDIR}/tobby_${TOBBY_VERSION}_linux_${arch}.${format}"
  done
done
