#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track C — provenance and trust, content by
# content, and the two hardening switches (FR-098, R-17):
#
#   1. One store receives a recipe-managed repository (signed recipe), one
#      admitted under a relaxed scope, one unit import, one packed FileSet and
#      one repository pushed with `docker push`.
#   2. Each shows its class in the content browser and the API, the relaxed
#      one with its reduced-assurance marker, the scanned tags with their scan
#      date; the unmanaged-content metric counts two unit-import and one seed;
#      log records carry the provenance field.
#   3. With hardening.disableUnitImport, the import screen, its API and
#      `tobby fileset pack` refuse with TBY-POL-005, the setting is named on
#      the screen and at startup, and a recipe synchronization still works.
#   4. With hardening.disableRegistryPush, `docker push`, `helm push` and
#      `oras push` as an ADMINISTRATOR are refused with DENIED carrying
#      TBY-POL-006, while pulls and recipe synchronizations still work; each
#      refusal is a hardening.refused audit event.
#
# Topology:
#   tbc-m7c-source (container, tbc-net)  source registry + cookbook
#   tbc-m7c-node   (container, tbc-net)  instance under test, real OCI clients
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/c-provenance.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-c-provenance.txt}"
. "$DIR/../../lib.sh"

report_start
fail "c-provenance: not written yet — track C (R-17, FR-098). This skeleton fails on purpose: m7 is not accepted while it is pending."
