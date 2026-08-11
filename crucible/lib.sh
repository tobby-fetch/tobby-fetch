#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Shared helpers for crucible scenarios: check bookkeeping and the raw
# report each milestone acceptance publishes unedited.

PROJECT="tobby-crucible"
REPORT="${CRUCIBLE_REPORT:-crucible-report-${SCENARIO:-unnamed}.txt}"

report_start() {
    : >"$REPORT"
    check "scenario ${SCENARIO:-unnamed} started $(date -u +%Y-%m-%dT%H:%M:%SZ)"
}

check() { printf 'ok   %s\n' "$*" | tee -a "$REPORT"; }

fail() {
    printf 'FAIL %s\n' "$*" | tee -a "$REPORT"
    exit 1
}

# inc: incus scoped to the crucible project.
inc() { incus --project "$PROJECT" "$@"; }

# wait_ready <instance> <url>: poll an HTTP endpoint from inside the guest.
# busybox wget, not curl: air-gapped guests cannot install anything — no
# route to a package mirror, by construction.
wait_ready() {
    _i=0
    until inc exec "$1" -- wget -q -T 2 -O /dev/null "$2" >/dev/null 2>&1; do
        _i=$((_i + 1))
        [ "$_i" -gt 60 ] && fail "$1: $2 never became ready"
        sleep 1
    done
}
