#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — backup and rebuild of an instance
# (FR-097, R-27): the state directory is the backup target, and a rebuild
# from it is the original instance.
#
#   1. An instance is exercised until every documented state entry exists:
#      accounts and a token, an adopted certificate, a runtime interval
#      override, a media import record, the trust cache, partial downloads.
#   2. `tobby state inspect` lists exactly the documented entries, with their
#      class and mode; secret entries are 0600.
#   3. The state directory, the configuration and the store are copied to a
#      FRESH node (no shared volume), discardable entries left behind; the
#      instance starts there.
#   4. The original account and token sign in, the certificate fingerprint is
#      the original one, the import record refuses the old medium again, the
#      interval override holds.
#   5. A restore that loosened the accounts file to 0644 refuses the start
#      with TBY-STA-002 naming it; a missing import record starts with
#      TBY-STA-003.
#
# Topology:
#   tbc-m7b-old (container, tbc-net)  the original instance
#   tbc-m7b-new (container, tbc-net)  the rebuilt instance
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-rebuild.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-rebuild.txt}"
. "$DIR/../../lib.sh"

report_start
fail "b-rebuild: not written yet — track B (R-27, FR-097). This skeleton fails on purpose: m7 is not accepted while it is pending."
