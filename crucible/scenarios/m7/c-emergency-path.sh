#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, tracks C and E — the emergency path (R-21,
# NFR-021), followed LITERALLY from the published procedure: the scenario
# extracts the commands from the documentation page, so a procedure that
# drifts from the product fails here.
#
#   1. A local recipe is written with the published SDK and schemas
#      (`recipe lint` validates it), its digests copied from the content
#      browser of the zone instance (FR-062).
#   2. It is signed with an "organization emergency key" by the exact cosign
#      command of the page; Tobby signs nothing.
#   3. The zone instance admits it only under the dedicated, declared
#      emergency trust scope: the scope is visible in the configuration dump,
#      the task report names it, and the same recipe outside the scope is
#      refused with TBY-SIG-001 naming the scope that refused it.
#   4. No other trust root accepts the emergency key; removing the scope
#      closes the path again.
#
# Topology:
#   tbc-m7c-airgap (container, tbc-airgap)  zone instance, no egress
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/c-emergency-path.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-c-emergency-path.txt}"
. "$DIR/../../lib.sh"

report_start
fail "c-emergency-path: not written yet — track C, with track E (R-21, NFR-021). This skeleton fails on purpose: m7 is not accepted while it is pending."
