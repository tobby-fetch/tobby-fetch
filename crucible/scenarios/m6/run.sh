#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenarios (ADR-0014, DEVELOPMENT-PLAN §2.6). One file
# per track scenario, named by track prefix — a-* (vulnerability scanning),
# b-* (authentication), c-* (on-demand integrity, reduced assurance, trust
# compartments) — so three tracks write their scenarios without ever
# editing the same file. Files named *-lib.sh are sourced helpers, never run.
#
# Each scenario is independently invocable and writes its own raw report
# (crucible-report-m6-<name>.txt) for the milestone acceptance to publish
# unedited:
#
#   ./crucible/run.sh m6                          # every m6 scenario
#   sh crucible/scenarios/m6/c-integrity.sh       # one of them, alone
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"

status=0
for scenario in "$DIR"/[a-z]-*.sh; do
    [ -f "$scenario" ] || continue
    case "$(basename "$scenario")" in
        *-lib.sh) continue ;;
    esac
    echo "--- m6 scenario $(basename "$scenario" .sh) ---"
    if ! SCENARIO=m6 sh "$scenario"; then
        echo "--- $(basename "$scenario" .sh) FAILED ---"
        status=1
    fi
done
exit $status
