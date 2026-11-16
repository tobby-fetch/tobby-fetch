#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Release-criterion coverage gate (DEVELOPMENT-PLAN §3): the packages named
# in tools/coverage-core.txt must each hold at least FLOOR percent statement
# coverage. One coverage profile of exactly those packages, judged by
# tools/covercheck. (tools/check-coverage.sh is the broader floor, every
# internal/ package; this one is the explicit core list.)
set -eu

FLOOR="${FLOOR:-70}"
LIST="$(dirname "$0")/coverage-core.txt"
PROFILE="${COVERPROFILE:-cover-core.out}"

pkgs=""
# shellcheck disable=SC2013 # one word per line: covercheck rejects any other shape
for p in $(sed -e 's/#.*//' "$LIST"); do
    pkgs="$pkgs ./$p"
done

# shellcheck disable=SC2086 # the package list is word-split on purpose
go test -count=1 -timeout 30m -coverprofile="$PROFILE" $pkgs
go run ./tools/covercheck -profile "$PROFILE" -packages "$LIST" -threshold "$FLOOR"
