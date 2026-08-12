#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-2 crucible scenario (ADR-0014, DEVELOPMENT-PLAN §2.2): the
# secure user-experience slice on real nodes, plus the one property only
# the crucible can exercise faithfully — task resumption across a hard
# instance kill (FR-029).
#
#   1. R-01: the instance refuses to start without an account (exit 3,
#      TBY-AUTH-001); the account is created through the CLI on the node.
#   2. Anonymous access refused on the API (RFC 9457) and the registry
#      (Basic challenge).
#   3. Unit import driven through /api/v1 from an in-zone source registry;
#      task polled to done; relocated repository browsable; the pinned
#      index digest is identical to the source (bit-exact, FR-022).
#   4. Standard third-party client pulls the import with Basic credentials.
#   5. FR-029: a second import is submitted and the instance is KILLED
#      (SIGKILL) mid-queue; on restart the task resumes or settles — never
#      an orphan — and ends done with zero re-transfer (FR-026).
#
# Topology: tbc-m2-source (container, tbc-net) runs the reference registry;
# tbc-m2-node (container, tbc-net) runs the secure Tobby under test.
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
. "$DIR/../../lib.sh"

IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.22}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
CRED_USER="admin"
CRED_PASS="m2-crucible-pass"

report_start

# -- Build the binary under test ---------------------------------------------
( cd "$DIR/../../.." && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -o /tmp/tobby-crucible ./cmd/tobby )
check "built tobby for linux/$ARCH"

for inst in tbc-m2-source tbc-m2-node; do
    inc delete --force "$inst" 2>/dev/null || true
done
inc launch "$IMAGE" tbc-m2-source --profile tbc-connected-node
inc launch "$IMAGE" tbc-m2-node --profile tbc-connected-node
check "instances launched (source registry + secure node)"

inc file push /tmp/tobby-crucible tbc-m2-node/usr/bin/tobby
inc exec tbc-m2-node -- chmod +x /usr/bin/tobby

# The in-zone source: the reference registry implementation.
inc exec tbc-m2-source -- sh -c '
    apk add --no-cache docker-registry >/dev/null 2>&1 ||
        apk add --no-cache registry >/dev/null 2>&1
    nohup registry serve /etc/docker-registry/config.yml >/var/log/registry.log 2>&1 &
' || fail "starting the source registry"
SOURCE_IP=$(inc list tbc-m2-source -c 4 -f csv | awk '{print $1}' | head -1)
NODE_IP=$(inc list tbc-m2-node -c 4 -f csv | awk '{print $1}' | head -1)
wait_ready tbc-m2-source "http://127.0.0.1:5000/v2/_catalog" || true
check "source registry serving ($SOURCE_IP:5000)"

# -- 1. R-01: refusal without an account, then account through the CLI -------
set +e
inc exec tbc-m2-node -- env TOBBY_MODE=mirror TOBBY_STORAGE_ROOT=/srv/store \
    TOBBY_STATE_ROOT=/srv/state tobby serve >/dev/null 2>/tmp/tbc-m2-r01.err
R01=$?
set -e
[ "$R01" -eq 3 ] || fail "no-account start exited $R01, want 3"
grep -q "TBY-AUTH-001" /tmp/tbc-m2-r01.err || fail "refusal misses TBY-AUTH-001"
check "R-01: no-account start refused (exit 3, TBY-AUTH-001)"

inc exec tbc-m2-node -- sh -c "printf '%s\n' '$CRED_PASS' |
    tobby user add $CRED_USER --state-root /srv/state --mode mirror --password-stdin" \
    >/dev/null || fail "creating the account on the node"

start_node() {
    inc exec tbc-m2-node -- sh -c '
        nohup env TOBBY_MODE=mirror TOBBY_STORAGE_ROOT=/srv/store \
            TOBBY_STATE_ROOT=/srv/state \
            TOBBY_REGISTRIES_INSECURE='"$SOURCE_IP:5000"' \
            TOBBY_SERVER_ADDR=:8080 tobby serve >>/var/log/tobby.log 2>&1 &
    '
    wait_ready tbc-m2-node http://127.0.0.1:8080/readyz
}
start_node
check "account created (first account admin), secure node ready"

# -- 2. Anonymous refused everywhere -----------------------------------------
API="http://$NODE_IP:8080/api/v1"
ANON=$(curl -s -o /tmp/tbc-m2-anon.json -w '%{http_code}' "$API/content")
[ "$ANON" = "401" ] || fail "anonymous API answered $ANON, want 401"
grep -q '"code":"TBY-AUTH-002"' /tmp/tbc-m2-anon.json || fail "401 is not the RFC 9457 document"
curl -s -o /dev/null -D - "http://$NODE_IP:8080/v2/" | grep -qi "WWW-Authenticate: Basic" ||
    fail "registry misses the Basic challenge"
check "anonymous refused on API (RFC 9457) and registry (Basic challenge)"

# -- 3. Import through the API, browse, bit-exact digest ----------------------
DIGEST=$( cd "$DIR/../../.." && go run ./test/topology/seed push "$SOURCE_IP:5000/library/sample:2.0.0" ) ||
    fail "seeding the source registry"
REF="$SOURCE_IP:5000/library/sample:2.0.0"
CRED="$CRED_USER:$CRED_PASS"

INSPECT=$(curl -fsS -u "$CRED" "$API/import/inspect?ref=$REF") || fail "inspection"
PLATFORMS=$(printf '%s' "$INSPECT" | jq '[.platforms[] | {name: .name, digest: .digest}]')
[ "$(printf '%s' "$PLATFORMS" | jq 'length')" = "3" ] || fail "inspection platform count"
TASK_ID=$(curl -fsS -u "$CRED" -H 'Content-Type: application/json' \
    -d "{\"reference\":\"$REF\",\"platforms\":$PLATFORMS}" "$API/import" | jq -r '.task.id')
i=0
while :; do
    STATUS=$(curl -fsS -u "$CRED" "$API/tasks/$TASK_ID" | jq -r '.task.status')
    [ "$STATUS" = "done" ] && break
    [ "$STATUS" = "failed" ] && fail "import task failed"
    i=$((i + 1)); [ "$i" -gt 200 ] && fail "import never settled"
    sleep 0.3
done
RELOCATED="${SOURCE_IP}_5000/library/sample"
STORED=$(curl -fsS -u "$CRED" "$API/content/$RELOCATED/-/tags/2.0.0" | jq -r '.digest')
[ "$STORED" = "$DIGEST" ] || fail "stored digest $STORED differs from source $DIGEST"
check "import through the API done (3/3), pinned digest preserved ($DIGEST)"

# -- 4. Standard client pull with credentials ---------------------------------
( cd "$DIR/../../.." && SEED_AUTH="$CRED" go run ./test/topology/seed pull \
    "$NODE_IP:8080/$RELOCATED:2.0.0" "$DIGEST" ) >/dev/null ||
    fail "standard client pull"
check "standard third-party client pulls the import with Basic credentials"

# -- 5. FR-029: hard kill mid-task, resume on restart -------------------------
TASK2_ID=$(curl -fsS -u "$CRED" -H 'Content-Type: application/json' \
    -d "{\"reference\":\"$REF\",\"platforms\":$PLATFORMS}" "$API/import" | jq -r '.task.id')
inc exec tbc-m2-node -- sh -c 'pkill -9 tobby' || true
sleep 1
start_node
i=0
while :; do
    STATUS=$(curl -fsS -u "$CRED" "$API/tasks/$TASK2_ID" | jq -r '.task.status')
    [ "$STATUS" = "done" ] && break
    [ "$STATUS" = "failed" ] && fail "resumed task failed"
    i=$((i + 1)); [ "$i" -gt 200 ] && fail "task orphaned across the kill (last: $STATUS)"
    sleep 0.3
done
MOVED=$(curl -fsS -u "$CRED" "$API/tasks/$TASK2_ID" | jq '[.task.items[].size_bytes // 0] | add // 0')
[ "$MOVED" = "0" ] || fail "resumed re-import moved $MOVED bytes, want 0 (FR-026)"
check "FR-029: task survived a SIGKILL restart, settled done, zero re-transfer"

# -- Cleanup ------------------------------------------------------------------
for inst in tbc-m2-source tbc-m2-node; do
    inc delete --force "$inst"
done
check "scenario m2 complete — report: $REPORT"
