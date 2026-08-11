#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Crucible scenario runner. Scenarios are tagged by milestone and
# independently invocable (ADR-0014): the suite of all completed milestones
# stays replayable at any time, in whole or as any subset.
#
#   ./crucible/run.sh m1          # one scenario
#   ./crucible/run.sh m1 m3       # a subset
#   ./crucible/run.sh all         # every completed milestone
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"

[ $# -ge 1 ] || { echo "usage: $0 <scenario …|all>"; exit 2; }

command -v incus >/dev/null 2>&1 || {
    echo "incus not found: the crucible runs on a Linux host with KVM (see crucible/README.md)"
    exit 2
}

if [ "$1" = "all" ]; then
    set -- $(ls "$DIR/scenarios")
fi

status=0
for scenario in "$@"; do
    if [ ! -x "$DIR/scenarios/$scenario/run.sh" ]; then
        echo "unknown scenario: $scenario (available: $(ls "$DIR/scenarios" | tr '\n' ' '))"
        exit 2
    fi
    echo "=== crucible scenario $scenario ==="
    if SCENARIO="$scenario" sh "$DIR/scenarios/$scenario/run.sh"; then
        echo "=== $scenario PASSED ==="
    else
        echo "=== $scenario FAILED ==="
        status=1
    fi
done
exit $status
