#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — the integration and supervision
# kit (R-11; FR-060 and FR-091 amendments), against the chart a site deploys:
#
#   1. The release artifacts carry openapi.yaml and the metrics reference,
#      signed: cosign verifies both, and openapi.yaml is byte-identical to the
#      document the instance serves.
#   2. The instance is rendered from the reference chart with the alerting and
#      dashboard values on; the PrometheusRule and the dashboard render, and
#      every expression names only families of the published reference.
#   3. A Prometheus fixture scrapes the instance: every family of the
#      reference is present, the sync families move on a synchronization, the
#      last-success timestamp advances, the certificate-expiry gauge equals
#      the NotAfter of the certificate presented, and it follows a
#      certificate replacement.
#   4. Each alert is driven to fire by its condition — repeated sync failures
#      (source stopped), no success for longer than the bound, a certificate
#      expiring within the bound, a stale or absent database, an implausible
#      clock — and goes back to inactive when the condition clears.
#
# Topology:
#   tbc-m7b-source     (container, tbc-net)  source registry + cookbook
#   tbc-m7b-node       (container, tbc-net)  instance from the reference chart
#   tbc-m7b-prometheus (container, tbc-net)  Prometheus with the shipped rules
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-supervision.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-supervision.txt}"
. "$DIR/../../lib.sh"

report_start
fail "b-supervision: not written yet — track B (R-11, FR-060, FR-091). This skeleton fails on purpose: m7 is not accepted while it is pending."
