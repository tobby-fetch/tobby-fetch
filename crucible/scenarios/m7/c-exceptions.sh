#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track C — scoped and bounded vulnerability
# exceptions (FR-031 amendment 2026-11-16, R-15), under a blocking policy,
# with the pinned scanner and a database carried by the tobby-system channel:
#
#   1. An exception without expires is a configuration error: the instance
#      refuses to start, naming the entry.
#   2. Two images carry the same known CVE. An exception scoped to one digest
#      admits that image; the other is refused, and its report names the
#      exception as out of scope (TBY-SCN-026).
#   3. Scopes by source repository and by recipe behave the same way.
#   4. The exception's expiry passes (the scenario moves time with a short
#      expiry, never the clock): the next evaluation refuses the image again,
#      the report names the expired exception, a scan.exception_expired audit
#      event and the reblock metric record it.
#   5. A local OpenVEX document with a not_affected statement for the image
#      admits it, the report names the document; a statement with an impact
#      statement only admits nothing; a malformed document refuses the start
#      (TBY-SCN-027).
#
# Topology:
#   tbc-m7c-source (container, tbc-net)  source registry, cookbook, channel
#   tbc-m7c-node   (container, tbc-net)  instance under test
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/c-exceptions.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-c-exceptions.txt}"
. "$DIR/../../lib.sh"

report_start
fail "c-exceptions: not written yet — track C (R-15, FR-031). This skeleton fails on purpose: m7 is not accepted while it is pending."
