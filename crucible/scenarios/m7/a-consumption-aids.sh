#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track A — the consumption aids (FR-065
# amendment 2026-11-16, public feature 7.1), checked against content that
# really crossed zones:
#
#   1. A connected instance promotes a recipe holding images from two
#      registries (one of them Docker Hub, under both of its aliases) and a
#      chart into a zone registry, under a destination.basePath.
#   2. The mapping table is exported as CSV, JSON and Markdown from the API
#      and the CLI: the same rows on every surface, and every relocated
#      reference resolves at the zone registry with the digest of its row.
#   3. The registries.yaml snippet has one mirrors entry per nominal host
#      (docker.io folded), rewrite rules matching the relocated paths, the
#      configs.tls.ca_file declared in consumption.caFile, and NO credential;
#      two generations are byte-identical.
#   4. A containerd node configured with the snippet pulls a pod image by its
#      NOMINAL reference from the zone registry (registry access log as the
#      witness), while a direct pull of the nominal host is impossible
#      (interface ACL). The full RKE2 bootstrap is the crown's job.
#   5. A destination-side instance with no destination and no
#      consumption.registry refuses with TBY-AID-001.
#   6. A FileSet ingredient reads as FileSet in the content browser and the
#      API (RECIPE-SPEC §7.4 marker), packed or published.
#
# Topology:
#   tbc-m7a-source (container, tbc-net)  source registry + cookbook
#   tbc-m7a-node   (container, tbc-net)  passthrough instance
#   tbc-m7a-zone   (container, tbc-net)  zone registry
#   tbc-m7a-k3s    (VM, tbc-net, egress ACL to the zone registry only)
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/a-consumption-aids.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-a-consumption-aids.txt}"
. "$DIR/../../lib.sh"

report_start
fail "a-consumption-aids: not written yet — track A (7.1, FR-065). This skeleton fails on purpose: m7 is not accepted while it is pending."
