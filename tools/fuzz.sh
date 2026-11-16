#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Runs every native Go fuzz target of the repository for a bounded time
# (hardening, DEVELOPMENT-PLAN §2.7): the parsers that read foreign bytes —
# OCI manifests and indexes, recipes and Retrievers, the media manifest,
# the configuration file, SAML responses, scanner output, and whatever a
# later change adds. Targets are DISCOVERED (every `func FuzzXxx` in a
# _test.go file), so a new target is run without editing this script.
#
#   sh tools/fuzz.sh [seconds-per-target]     default 30
#
# `go test -fuzz` takes one target at a time, hence the loop. A failing
# input is written by the toolchain to testdata/fuzz/<Target>/ next to the
# test: commit it, it becomes a permanent regression seed, run by a plain
# `go test`.
set -eu

SECONDS_PER_TARGET="${1:-30}"
fail=0
n=0

targets="$(grep -rEn --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*\(' . |
    sed -E 's|^\./([^:]*)/[^/:]*:[0-9]+:func (Fuzz[A-Za-z0-9_]*)\(.*|\1 \2|' | sort -u)"
[ -n "$targets" ] || { echo "no fuzz target found" >&2; exit 1; }

# Word splitting on newlines only: one "dir target" pair per line.
IFS='
'
for line in $targets; do
    dir="${line% *}"
    target="${line#* }"
    n=$((n + 1))
    printf '== fuzz %s (%s) %ss\n' "$target" "$dir" "$SECONDS_PER_TARGET"
    if ! go test -run '^$' -fuzz "^${target}\$" -fuzztime "${SECONDS_PER_TARGET}s" "./$dir"; then
        echo "FAIL fuzz $target in $dir" >&2
        fail=1
    fi
done
echo "fuzzed $n target(s)"
exit "$fail"
