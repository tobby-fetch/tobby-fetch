#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — the diagnostic bundle (FR-095,
# R-30): one gesture, one local archive, nothing in it that is secret.
#
#   1. An instance is configured with a planted value in EVERY secret-bearing
#      setting: proxy password, OIDC client secret file, SAML key, registry
#      credentials file, TLS key, a static token and a password set through
#      the CLI. The planted values are recorded by the scenario, not by Tobby.
#   2. A bundle is produced by `tobby diagnostics bundle` on the host and by
#      an administrator through the API; an operator and a viewer are refused.
#   3. Both archives are unpacked and grepped for every planted value, in
#      clear and base64: zero occurrence. Their MANIFEST digests match their
#      members; the members are the documented ones only.
#   4. The bundle holds the version, the redacted configuration, recent log
#      records, the last task reports, a metrics snapshot, the last integrity
#      report and the state inventory (names, sizes, modes — no contents).
#   5. The egress canary and the proxy witness log show no connection during
#      either generation (NFR-019); the audit log has one diagnostics.bundle
#      event per archive, with its requester.
#
# Topology:
#   tbc-m7b-proxy (container, tbc-net)  witness forward proxy (test/topology/proxy)
#   tbc-m7b-node  (container, tbc-net)  instance under test, egress via the proxy only
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-diagnostics.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-diagnostics.txt}"
. "$DIR/../../lib.sh"

report_start
fail "b-diagnostics: not written yet — track B (R-30, FR-095). This skeleton fails on purpose: m7 is not accepted while it is pending."
