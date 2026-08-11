#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-1 reduced scenario (ADR-0014): registry push/pull across the
# topology, egress canary, media round-trip.
#
#   1. Seed the upstream production registry with a multi-arch index.
#   2. Push a multi-arch index into the connected Tobby's embedded registry
#      under its relocated name, pull it back, digests must match.
#   3. Canary: the air-gapped segment must have NO route out (the probe
#      must FAIL) while in-zone routes work.
#   4. Media round-trip: the store filled on the connected side is served
#      by the air-gapped instance — the manifest is retrievable from inside
#      the zone by its digest.
#
# Requires: docker (compose v2), go. Run from the repository root:
#   sh test/topology/scenario-m1.sh
set -eu

TOPO_DIR="$(dirname "$0")"
COMPOSE="docker compose -f $TOPO_DIR/compose.yaml"
REPORT="${TOPOLOGY_REPORT:-/tmp/tobby-topology-m1-report.txt}"

: >"$REPORT"
step() { printf '%s\n' "$*" | tee -a "$REPORT"; }
fail() { step "FAIL $*"; $COMPOSE down -v >/dev/null 2>&1 || true; exit 1; }

step "== milestone-1 topology scenario $(date -u +%Y-%m-%dT%H:%M:%SZ) =="

# -- Build the binary under test and its topology image ----------------------
GOOS=linux CGO_ENABLED=0 go build -trimpath -o "$TOPO_DIR/tobby" ./cmd/tobby
docker build -q -t tobby-topology:local -f "$TOPO_DIR/Dockerfile.test" "$TOPO_DIR" >/dev/null
rm -f "$TOPO_DIR/tobby"
step "ok   build: binary and topology image"

$COMPOSE down -v >/dev/null 2>&1 || true
$COMPOSE up -d upstream-registry tobby-connected >/dev/null 2>&1 || $COMPOSE up -d upstream-registry tobby-connected

# -- Wait for readiness ------------------------------------------------------
i=0
until curl -fsS http://127.0.0.1:5211/readyz >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -gt 50 ] && fail "connected instance never became ready"
    sleep 0.2
done
step "ok   connected instance ready"

# -- 1. Seed the upstream production registry --------------------------------
UPSTREAM_DIGEST=$(go run ./test/topology/seed push 127.0.0.1:5210/library/sample:1.0.0) ||
    fail "seeding upstream registry"
go run ./test/topology/seed pull 127.0.0.1:5210/library/sample:1.0.0 "$UPSTREAM_DIGEST" >/dev/null ||
    fail "upstream registry pull-back"
step "ok   upstream registry seeded ($UPSTREAM_DIGEST)"

# -- 2. Push/pull through the embedded registry, relocated name --------------
STORE_DIGEST=$(go run ./test/topology/seed push 127.0.0.1:5211/docker.io/library/sample:1.0.0) ||
    fail "pushing into the embedded registry"
go run ./test/topology/seed pull 127.0.0.1:5211/docker.io/library/sample:1.0.0 "$STORE_DIGEST" >/dev/null ||
    fail "embedded registry pull-back"
step "ok   embedded registry round-trip under relocated name ($STORE_DIGEST)"

# -- 3. Bring up the air-gapped side on the same store -----------------------
# The store was written by the connected instance into the shared volume;
# stopping the writer first models the detach half of the transport.
$COMPOSE stop tobby-connected >/dev/null 2>&1
$COMPOSE up -d tobby-airgap >/dev/null 2>&1 || $COMPOSE up -d tobby-airgap
sleep 1

# -- 4. Egress canary: the gap must be structural (NFR-019) ------------------
if $COMPOSE run --rm canary "curl -m 5 -fsS http://upstream-registry:5000/v2/" >/dev/null 2>&1; then
    fail "canary reached the connected zone from inside the air gap"
fi
if $COMPOSE run --rm canary "curl -m 5 -fsS https://example.com/" >/dev/null 2>&1; then
    fail "canary reached the internet from inside the air gap"
fi
step "ok   egress canary failed as required (air gap proven by construction)"

# -- 5. Media round-trip: the transported store serves in the zone -----------
$COMPOSE run --rm canary \
    "curl -m 5 -fsS http://tobby-airgap:8080/readyz" >/dev/null 2>&1 ||
    fail "air-gapped instance not ready on the transported store"
$COMPOSE run --rm canary \
    "curl -m 5 -fsS -H 'Accept: application/vnd.oci.image.index.v1+json' \
     http://tobby-airgap:8080/v2/docker.io/library/sample/manifests/$STORE_DIGEST" >/dev/null 2>&1 ||
    fail "transported content not servable inside the air-gapped zone"
step "ok   media round-trip: store filled in the connected zone serves in the air-gapped zone"

$COMPOSE down -v >/dev/null 2>&1
step "== scenario m1 PASSED — raw report: $REPORT =="
