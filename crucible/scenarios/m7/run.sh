#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenarios (ADR-0014, DEVELOPMENT-PLAN §2.7). The crown
# (crown.sh) is named explicitly: it is the scenario the milestone's exit
# criterion names, and it does not follow the m6 track prefix. Every other
# m7 scenario follows the m6 convention — one file per track, named by track
# prefix (a-*, b-*, …), *-lib.sh sourced and never run — and is discovered
# in name order after the crown, so a track adds a file without editing
# this one.
#
# Each scenario is independently invocable and writes its own raw report
# (crucible-report-m7-<name>.txt) for the milestone acceptance to publish
# unedited:
#
#   ./crucible/run.sh m7                    # every m7 scenario
#   sh crucible/scenarios/m7/crown.sh       # the crown, alone
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"

status=0
for scenario in "$DIR/crown.sh" "$DIR"/[a-z]-*.sh; do
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
