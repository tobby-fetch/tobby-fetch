#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-2 scenario (ADR-0014): the secure user-experience slice, end to
# end through the real surfaces.
#
#   1. R-01: a fresh instance REFUSES to start without a local account —
#      policy exit class (3), taxonomy code on stderr.
#   2. Account created through the CLI (hash computed by the tool), the
#      instance starts; anonymous access is refused on the API (RFC 9457)
#      and on the embedded registry (Basic challenge).
#   3. Unit import driven through /api/v1 (the FR-061 parity surface):
#      inspect → select all platforms → task created → polled to done.
#   4. Browsing sees the relocated repository; the pinned index digest is
#      IDENTICAL to the upstream one (bit-exact, FR-022).
#   5. A standard third-party client (go-containerregistry) pulls the
#      imported index from the embedded registry with Basic credentials —
#      the milestone exit criterion.
#   6. Idempotence (FR-026): a second import settles with zero new bytes.
#
# Requires: docker (compose v2), go, jq. Run from the repository root:
#   sh test/topology/scenario-m2.sh
set -eu

TOPO_DIR="$(dirname "$0")"
COMPOSE="docker compose -f $TOPO_DIR/compose.yaml"
REPORT="${TOPOLOGY_REPORT:-/tmp/tobby-topology-m2-report.txt}"
API="http://127.0.0.1:5212/api/v1"
CRED="admin:m2-scenario-pass"

: >"$REPORT"
step() { printf '%s\n' "$*" | tee -a "$REPORT"; }
fail() { step "FAIL $*"; $COMPOSE down -v >/dev/null 2>&1 || true; exit 1; }

step "== milestone-2 topology scenario $(date -u +%Y-%m-%dT%H:%M:%SZ) =="

# -- Build the binary under test and its topology image ----------------------
GOOS=linux CGO_ENABLED=0 go build -trimpath -o "$TOPO_DIR/tobby" ./cmd/tobby
docker build -q -t tobby-topology:local -f "$TOPO_DIR/Dockerfile.test" "$TOPO_DIR" >/dev/null
rm -f "$TOPO_DIR/tobby"
step "ok   build: binary and topology image"

$COMPOSE down -v >/dev/null 2>&1 || true
$COMPOSE up -d upstream-registry >/dev/null 2>&1 || $COMPOSE up -d upstream-registry

# -- 1. R-01: refusal to start without an account ----------------------------
set +e
$COMPOSE run --rm tobby-secure serve --mode=mirror \
    --storage-root=/m2/store --state-root=/m2/state >/dev/null 2>"$REPORT.r01"
R01_EXIT=$?
set -e
[ "$R01_EXIT" -eq 3 ] || fail "no-account start exited $R01_EXIT, want 3 (policy class)"
grep -q "TBY-AUTH-001" "$REPORT.r01" || fail "refusal does not carry TBY-AUTH-001"
step "ok   R-01: no-account start refused (exit 3, TBY-AUTH-001)"

# -- 2. Account through the CLI, instance up, anonymous refused --------------
printf '%s\n' "${CRED#*:}" | $COMPOSE run --rm -T tobby-secure \
    user add "${CRED%%:*}" --state-root /m2/state --mode mirror --password-stdin \
    >/dev/null 2>&1 || fail "creating the account through the CLI"
$COMPOSE up -d tobby-secure >/dev/null 2>&1 || $COMPOSE up -d tobby-secure
i=0
until curl -fsS http://127.0.0.1:5212/readyz >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 50 ] && fail "secure instance never became ready"
    sleep 0.2
done
step "ok   account created (first account admin), instance ready"

ANON_CODE=$(curl -s -o /tmp/m2-anon.json -w '%{http_code}' "$API/content")
[ "$ANON_CODE" = "401" ] || fail "anonymous API answered $ANON_CODE, want 401"
grep -q '"code":"TBY-AUTH-002"' /tmp/m2-anon.json || fail "401 body is not the RFC 9457 taxonomy document"
curl -s -o /dev/null -D - http://127.0.0.1:5212/v2/ | grep -qi "WWW-Authenticate: Basic" ||
    fail "registry misses the Basic challenge"
curl -fsS -u "${CRED%%:*}:wrong-password" "$API/content" >/dev/null 2>&1 &&
    fail "wrong password accepted on the API"
step "ok   anonymous refused everywhere (RFC 9457 + Basic challenge), wrong password refused"

# -- 3. Import through the API: inspect, submit, poll ------------------------
UPSTREAM_DIGEST=$(go run ./test/topology/seed push 127.0.0.1:5210/library/sample:2.0.0) ||
    fail "seeding upstream registry"
REF="upstream-registry:5000/library/sample:2.0.0"

INSPECT=$(curl -fsS -u "$CRED" "$API/import/inspect?ref=$REF") || fail "inspection through the API"
PLATFORMS=$(printf '%s' "$INSPECT" | jq '[.platforms[] | {name: .name, digest: .digest}]')
COUNT=$(printf '%s' "$PLATFORMS" | jq 'length')
[ "$COUNT" = "3" ] || fail "inspection saw $COUNT platforms, want 3"

TASK=$(curl -fsS -u "$CRED" -H 'Content-Type: application/json' \
    -d "{\"reference\":\"$REF\",\"platforms\":$PLATFORMS}" "$API/import") ||
    fail "import submission"
TASK_ID=$(printf '%s' "$TASK" | jq -r '.task.id')
step "ok   import submitted ($TASK_ID, 3 platforms)"

i=0
while :; do
    STATUS=$(curl -fsS -u "$CRED" "$API/tasks/$TASK_ID" | jq -r '.task.status')
    [ "$STATUS" = "done" ] && break
    [ "$STATUS" = "failed" ] && fail "import task failed"
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "import task never settled (last: $STATUS)"
    sleep 0.3
done
DONE=$(curl -fsS -u "$CRED" "$API/tasks/$TASK_ID" | jq -r '.task.items_done')
[ "$DONE" = "3" ] || fail "items_done = $DONE, want 3"
step "ok   task settled: done, 3/3 items"

# -- 4. Browsing sees the relocated repo, digest is bit-exact ----------------
RELOCATED="upstream-registry_5000/library/sample"
curl -fsS -u "$CRED" "$API/content?q=sample" | jq -e \
    ".repositories[] | select(.name == \"$RELOCATED\")" >/dev/null ||
    fail "relocated repository absent from browsing"
STORED_DIGEST=$(curl -fsS -u "$CRED" "$API/content/$RELOCATED/-/tags/2.0.0" | jq -r '.digest')
[ "$STORED_DIGEST" = "$UPSTREAM_DIGEST" ] ||
    fail "stored digest $STORED_DIGEST differs from upstream $UPSTREAM_DIGEST (bit-exact broken)"
step "ok   browsing sees $RELOCATED, pinned digest preserved ($STORED_DIGEST)"

# -- 5. Standard-client pull with Basic credentials (exit criterion) ---------
SEED_AUTH="$CRED" go run ./test/topology/seed pull \
    "127.0.0.1:5212/$RELOCATED:2.0.0" "$UPSTREAM_DIGEST" >/dev/null ||
    fail "standard client pull of the imported index"
go run ./test/topology/seed pull "127.0.0.1:5212/$RELOCATED:2.0.0" "$UPSTREAM_DIGEST" \
    >/dev/null 2>&1 && fail "anonymous standard-client pull succeeded on a secured registry"
step "ok   standard third-party client pulls with credentials, anonymous pull refused"

# -- 6. Idempotence: a second import transfers nothing -----------------------
TASK2=$(curl -fsS -u "$CRED" -H 'Content-Type: application/json' \
    -d "{\"reference\":\"$REF\",\"platforms\":$PLATFORMS}" "$API/import")
TASK2_ID=$(printf '%s' "$TASK2" | jq -r '.task.id')
i=0
while :; do
    STATUS=$(curl -fsS -u "$CRED" "$API/tasks/$TASK2_ID" | jq -r '.task.status')
    [ "$STATUS" = "done" ] && break
    [ "$STATUS" = "failed" ] && fail "second import failed"
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "second import never settled"
    sleep 0.3
done
MOVED=$(curl -fsS -u "$CRED" "$API/tasks/$TASK2_ID" | jq '[.task.items[].size_bytes // 0] | add // 0')
[ "$MOVED" = "0" ] || fail "second import moved $MOVED bytes, want 0 (FR-026)"
step "ok   idempotence: second import moved 0 bytes"

$COMPOSE down -v >/dev/null 2>&1
step "== scenario m2 PASSED — raw report: $REPORT =="
