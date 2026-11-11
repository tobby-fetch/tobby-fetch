#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# The known-vulnerability gate (ADR-0011): govulncheck in JSON, then
# tools/vulngate, which fails on every reachable finding (stdlib included)
# not covered by a live, reviewed entry of tools/govulncheck-allow.yaml.
# Used by CI and by `mise run vuln`. GOVULNCHECK overrides the binary; by
# default the pinned version is run through `go run` (sumdb-verified).
set -eu
cd "$(dirname "$0")/.."
# renovate: datasource=go depName=golang.org/x/vuln
VERSION="v1.9.0"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT
if [ -n "${GOVULNCHECK:-}" ]; then
    "$GOVULNCHECK" -format json ./... >"$out"
else
    go run "golang.org/x/vuln/cmd/govulncheck@$VERSION" -format json ./... >"$out"
fi
go run ./tools/vulngate tools/govulncheck-allow.yaml <"$out"
