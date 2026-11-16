#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — clock plausibility in an isolated
# zone (FR-096, R-32), on real VMs whose clocks the scenario sets:
#
#   1. An air-gapped VM (own kernel, own clock) is set BEFORE the build date
#      of the binary: the instance starts, and the startup log record, the
#      persistent banner, the metric and a clock.implausible audit event name
#      the gap (TBY-CLK-001). Nothing is corrected, no verdict changes.
#   2. A medium produced in the connected zone is opened on a destination VM
#      whose clock is set behind the medium's resolution time beyond the
#      tolerance: TBY-CLK-002 on the CLI output of `tobby media verify`, on
#      the Media screen and in the audit log, naming the medium.
#   3. Within the tolerance (clock 20 minutes behind), nothing is raised.
#   4. The scenario proves its own manipulation first: `date` on the guest
#      reads the value it set before Tobby is asked anything.
#
# Topology:
#   tbc-m7b-source  (container, tbc-net)     produces the medium
#   tbc-m7b-airgap  (VM, tbc-airgap)         destination, clock under test
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-clock.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-clock.txt}"
. "$DIR/../../lib.sh"

report_start
fail "b-clock: not written yet — track B (R-32, FR-096). This skeleton fails on purpose: m7 is not accepted while it is pending."
