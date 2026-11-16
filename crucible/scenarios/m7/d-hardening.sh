#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track D — the 7.3 hardening seen from the
# wire, on an instance deployed from the reference chart behind TLS:
#
#   1. Security headers: every route class (UI page, API, /v2/, /files/,
#      /help, probes, /metrics) answers with the headers NFR-022 assigns it;
#      HSTS is present behind TLS; authenticated responses are no-store.
#   2. CSRF: a cookie-authenticated state-changing request without a token,
#      with a forged token, and from a cross-site Origin is refused with
#      TBY-AUTH-004; the same request with a bearer token succeeds.
#   3. Input bounds: an oversized body on each state-changing API route is
#      refused with its bounded code, the instance stays healthy.
#   4. The five OCI clients of FR-041 still pass their push/pull round trip
#      with the headers in place.
#
# The OCI conformance suite, coverage, fuzzing and the arm64 e2e run in CI
# (DEVELOPMENT-PLAN §3), not here.
#
# Topology:
#   tbc-m7d-node (container, tbc-net)  instance from the reference chart, TLS
#   tbc-m7d-cli  (container, tbc-net)  docker/podman/helm/oras/skopeo clients
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/d-hardening.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-d-hardening.txt}"
. "$DIR/../../lib.sh"

report_start
fail "d-hardening: not written yet — track D, for track C (7.3, NFR-012, NFR-022, NFR-023). This skeleton fails on purpose: m7 is not accepted while it is pending."
