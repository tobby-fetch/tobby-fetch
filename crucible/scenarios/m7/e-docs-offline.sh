#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track E — the bilingual documentation, as
# the operator who needs it most meets it: inside the air-gapped zone.
#
#   1. An instance on tbc-airgap (no egress, canary first) serves /help in
#      English and in French: every page of the published corpus is there in
#      both languages, and a crawl of the embedded corpus finds no dangling
#      link, anchor or image.
#   2. Every taxonomy code rendered by the instance links to its entry, and
#      the entry exists in both languages.
#   3. The pages NFR-021 requires are present: installation, configuration,
#      both modes, security model with the entry-point table, monitoring,
#      backup and rebuild, troubleshooting, the emergency path.
#
# Topology:
#   tbc-m7e-airgap (container, tbc-airgap)  instance under test, no egress
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/e-docs-offline.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-e-docs-offline.txt}"
. "$DIR/../../lib.sh"

report_start
fail "e-docs-offline: not written yet — track E (7.2, NFR-003, NFR-021). This skeleton fails on purpose: m7 is not accepted while it is pending."
