#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Reproducible release build (ADR-0011). Produces the six release binaries
# bit-identically for a given (version, commit, date, SOURCE_DATE_EPOCH):
#
#   SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
#     sh tools/release-build.sh <version> <commit> <date-rfc3339> <outdir>
#
# The same script is used by the release workflow, by the reproducibility
# gate that rebuilds and compares digests, by the melange package build, and
# by independent verifiers (docs/release-verification.md) — one code path,
# no flag drift.
set -eu

VERSION="$1"
COMMIT="$2"
DATE="$3"
OUTDIR="$4"

: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH must be set to the commit timestamp}"

# Force the exact Go toolchain declared in go.mod, regardless of what is
# installed locally — a different patch release would change the binaries.
GOTOOLCHAIN="go$(sed -n 's/^go //p' go.mod)"
export GOTOOLCHAIN
export CGO_ENABLED=0

PKG="github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
LDFLAGS="-X ${PKG}.version=${VERSION} -X ${PKG}.commit=${COMMIT} -X ${PKG}.date=${DATE}"

mkdir -p "$OUTDIR"
# darwin targets are the NFR-001 convenience tier (Homebrew): same
# reproducible chain — Go's ad-hoc arm64 code signature is derived from
# the binary content, so the double-build gate holds for them too.
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  case "$os" in
    windows) ext=".exe" ;;
    *) ext="" ;;
  esac
  GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "$LDFLAGS" \
    -o "${OUTDIR}/tobby-${os}-${arch}${ext}" ./cmd/tobby
done
