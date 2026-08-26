#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Per-package coverage floor (NFR-016): every package under internal/ must
# hold at least FLOOR percent statement coverage. Packages without tests
# count as 0%. cmd/ (wiring only) is exempt.
set -eu

FLOOR="${FLOOR:-70}"
fail=0

go test -cover ./internal/... 2>&1 | while IFS= read -r line; do
    case "$line" in
    ok*coverage:*)
        pkg=$(printf '%s' "$line" | awk '{print $2}')
        pct=$(printf '%s' "$line" | grep -oE 'coverage: [0-9.]+' | awk '{print $2}')
        # awk rather than bc: the comparison is one floating-point test,
        # and bc is absent from Git Bash — the shell this script runs
        # under on the Windows runner (NFR-018). awk is in every POSIX
        # environment the project builds on.
        if [ "$(awk -v p="$pct" -v f="$FLOOR" 'BEGIN{print (p<f) ? 1 : 0}')" = 1 ]; then
            printf 'FAIL %s: %s%% < %s%% floor\n' "$pkg" "$pct" "$FLOOR"
            touch .coverage-floor-failed
        else
            printf 'ok   %s: %s%%\n' "$pkg" "$pct"
        fi
        ;;
    *"no test files"*)
        pkg=$(printf '%s' "$line" | awk '{print $2}')
        printf 'FAIL %s: no test files (0%% < %s%% floor)\n' "$pkg" "$FLOOR"
        touch .coverage-floor-failed
        ;;
    esac
done

if [ -e .coverage-floor-failed ]; then
    rm -f .coverage-floor-failed
    echo "coverage floor not met"
    exit 1
fi
echo "coverage floor met (>= ${FLOOR}% per internal package)"
