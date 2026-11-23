#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenarios (ADR-0014, DEVELOPMENT-PLAN §2.7). One file
# per external feature, named by track prefix — a-* (consumption aids),
# b-* (operations and supervision), c-* (content controls and hardening),
# d-* (quality), e-* (documentation) — and z-crown-rke2.sh, the crown, last
# by name because it exercises every milestone at once. Files named
# *-lib.sh are sourced helpers, never run.
#
# Each scenario is independently invocable and writes its own raw report
# (crucible-report-m7-<name>.txt) for the milestone acceptance to publish
# unedited:
#
#   ./crucible/run.sh m7                          # every m7 scenario
#   sh crucible/scenarios/m7/z-crown-rke2.sh      # one of them, alone
#
# Until its track writes it, a scenario is a named skeleton that FAILS,
# stating what it will prove: m7 is not accepted while one is pending, and
# a skeleton that passed would be a check nobody made. Replay the completed
# milestones meanwhile with ./crucible/run.sh m1 m2 m3 m4 m5 m6.
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"

status=0
for scenario in "$DIR"/[a-z]-*.sh; do
    [ -f "$scenario" ] || continue
    case "$(basename "$scenario")" in
        *-lib.sh) continue ;;
    esac
    echo "--- m7 scenario $(basename "$scenario" .sh) ---"
    if ! SCENARIO=m7 sh "$scenario"; then
        echo "--- $(basename "$scenario" .sh) FAILED ---"
        status=1
    fi
done
exit $status
