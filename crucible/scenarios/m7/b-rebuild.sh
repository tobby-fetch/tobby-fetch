#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — backup and rebuild of an instance
# (FR-097, R-27): the state directory is the backup target, and a rebuild
# from it is the original instance.
#
#   1. An instance is exercised until every documented state entry exists:
#      accounts and a token, its generated TLS pair, a runtime interval
#      override, a media import record, the trust cache, partial downloads.
#   2. `tobby state inspect` lists exactly the documented entries, with their
#      class and mode; secret entries are 0600.
#   3. The state directory (identity entries only), the configuration and the
#      store are carried to a FRESH node (no shared volume, through the
#      crucible host), discardable entries left behind; the instance starts
#      there.
#   4. The original account and token sign in, the certificate fingerprint is
#      the original one, the import record is still the instance's, the
#      interval override holds.
#   5. A restore that loosened the accounts file to 0644 refuses the start
#      with TBY-STA-002 naming it (exit 3); group access (0640) is tolerated;
#      a missing import record starts with TBY-STA-003.
#
# The import record is planted in its documented format rather than produced
# by a medium import: producing one is m5's scenario, and what this one proves
# is that the record survives a rebuild and is read by the rebuilt instance.
#
# Topology:
#   tbc-m7b-old (container, tbc-net)  the original instance
#   tbc-m7b-new (container, tbc-net)  the rebuilt instance
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-rebuild.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-rebuild.txt}"
. "$DIR/../../lib.sh"
. "$DIR/b-lib.sh"

WORK="$(mktemp -d)"
OLD=tbc-m7b-old
NEW=tbc-m7b-new
STATE=/var/lib/tobby-state
cleanup() {
    inc delete --force "$OLD" "$NEW" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
b7_tooling
b7_build "$WORK"
B7_SCHEME=https

# The configuration is the same on both nodes: a rebuild restores it as it is.
cat >"$WORK/config.yaml" <<YAML
mode: passthrough
zone: zone-b
storage:
  root: /var/lib/tobby
state:
  root: $STATE
server:
  addr: :8080
  tls:
    enabled: true
retriever:
  source: /etc/tobby/retriever.yaml
YAML
cat >"$WORK/retriever.yaml" <<'YAML'
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-b
spec:
  cookbook: 127.0.0.1:9/cookbook
  recipes: []
YAML

# -- 1. The original instance, exercised ---------------------------------------------
b7_launch "$OLD" tbc-connected-node
b7_launch "$NEW" tbc-connected-node
OLD_IP=$(instance_ip "$OLD")
NEW_IP=$(instance_ip "$NEW")
inc exec "$OLD" -- mkdir -p /etc/tobby /var/lib/tobby "$STATE"
inc file push "$WORK/config.yaml" "$OLD/etc/tobby/config.yaml" >/dev/null
inc file push "$WORK/retriever.yaml" "$OLD/etc/tobby/retriever.yaml" >/dev/null
ADMIN_PW="rebuild-pw-$(openssl rand -hex 8)"
b7_user "$OLD" admin admin "$ADMIN_PW"

# The import record of zone-b, in its documented format, before the start.
RECORD_MEDIA="20261001T090000Z-rebuild0000000001"
cat >"$WORK/media-imports.json" <<JSON
{"zones":{"zone-b":{"mediaId":"$RECORD_MEDIA","resolvedAt":"2026-10-01T09:00:00Z","importedAt":"2026-10-01T10:00:00Z","runId":"run_rebuild"}}}
JSON
inc file push --mode 0600 "$WORK/media-imports.json" "$OLD$STATE/media-imports.json" >/dev/null
b7_start "$OLD"

TOKEN=$(b7_api "$OLD_IP" "admin:$ADMIN_PW" POST /api/v1/tokens '{"name":"ci","role":"operator"}' | jq -r .secret)
case "$TOKEN" in tby_*) : ;; *) fail "no token was created: $TOKEN" ;; esac
b7_api "$OLD_IP" "admin:$ADMIN_PW" PUT /api/v1/retriever/interval '{"interval":"42m"}' | jq -e '.interval.overridden == true' >/dev/null ||
    fail "the interval override was not accepted"
FP_OLD=$(b7_fingerprint "$OLD_IP")
[ "${#FP_OLD}" = 64 ] || fail "no certificate fingerprint read from the original instance: '$FP_OLD'"
# Discardable entries the instance would have created while working.
inc exec "$OLD" -- sh -c "mkdir -p $STATE/partials $STATE/trust-cache && echo x >$STATE/partials/left-behind && echo x >$STATE/trust-cache/left-behind"
check "the original instance is exercised: account, token, generated certificate ($FP_OLD), interval override 42m, import record, partials and trust cache"

# -- 2. The inventory is the documented layout ---------------------------------------------
inc exec "$OLD" -- tobby state inspect --config /etc/tobby/config.yaml --output json >"$WORK/inventory.json" ||
    fail "tobby state inspect failed"
DOCUMENTED='["layout.json","accounts.yaml","tls","schedule.json","media-imports.json","trust-cache","partials","scanner","serving.json"]'
jq -e --argjson want "$DOCUMENTED" '[.items[] | select(.documented) | .name] | sort == ($want | sort)' "$WORK/inventory.json" >/dev/null ||
    fail "the documented entries are not the layout: $(jq -c '[.items[] | select(.documented) | .name]' "$WORK/inventory.json")"
jq -e '[.items[] | select(.present and (.documented | not))] | length == 0' "$WORK/inventory.json" >/dev/null ||
    fail "the instance wrote an entry the layout does not document: $(jq -c '[.items[] | select(.present and (.documented | not)) | .name]' "$WORK/inventory.json")"
jq -e '[.items[] | select(.class == "identity" and (.present | not))] | length == 0' "$WORK/inventory.json" >/dev/null ||
    fail "an identity entry is missing from the exercised instance: $(jq -c '[.items[] | select(.class == "identity" and (.present | not)) | .name]' "$WORK/inventory.json")"
jq -e '[.items[] | select(.secret and .present and .access == "broad")] | length == 0' "$WORK/inventory.json" >/dev/null ||
    fail "a secret entry is readable by every user"
jq -e '[.items[] | select(.name == "accounts.yaml")][0].mode == "0600"' "$WORK/inventory.json" >/dev/null ||
    fail "accounts.yaml is not 0600"
check "tobby state inspect lists exactly the documented entries; identity entries present, secret entries 0600"

# -- 3. Carried to a fresh node ---------------------------------------------------------------
b7_stop "$OLD"
# The identity entries only: no partials, no trust cache, no serving marker.
inc exec "$OLD" -- sh -c "cd / && tar -cpf /tmp/backup.tar \
    etc/tobby var/lib/tobby \
    $(echo "$STATE" | sed 's|^/||')/layout.json $(echo "$STATE" | sed 's|^/||')/accounts.yaml \
    $(echo "$STATE" | sed 's|^/||')/tls $(echo "$STATE" | sed 's|^/||')/schedule.json \
    $(echo "$STATE" | sed 's|^/||')/media-imports.json" || fail "taking the backup"
b7_pull "$OLD" /tmp/backup.tar "$WORK/backup.tar"
inc file push "$WORK/backup.tar" "$NEW/tmp/backup.tar" >/dev/null
inc exec "$NEW" -- sh -c "cd / && tar -xpf /tmp/backup.tar" || fail "restoring the backup"
inc exec "$NEW" -- sh -c "test ! -e $STATE/partials/left-behind" || fail "a discardable entry travelled"
check "the state directory (identity entries), the configuration and the store were carried to a fresh node; the discardable entries stayed behind"

# -- 4. The rebuilt instance is the original ---------------------------------------------------
b7_start "$NEW"
[ "$(b7_status "$NEW_IP" "admin:$ADMIN_PW" GET /api/v1/accounts)" = 200 ] || fail "the original account cannot sign in on the rebuilt instance"
[ "$(b7_status "$NEW_IP" "admin:wrong-password" GET /api/v1/accounts)" = 401 ] || fail "a wrong password was accepted"
[ "$(curl -sk -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "https://$NEW_IP:8080/api/v1/tasks")" = 200 ] ||
    fail "the original token is refused by the rebuilt instance"
FP_NEW=$(b7_fingerprint "$NEW_IP")
[ "$FP_NEW" = "$FP_OLD" ] || fail "the certificate fingerprint changed: $FP_OLD -> $FP_NEW"
b7_api "$NEW_IP" "admin:$ADMIN_PW" GET /api/v1/retriever | jq -e '.interval.overridden == true and .interval.effective == "42m0s"' >/dev/null ||
    fail "the interval override did not survive the rebuild"
inc exec "$NEW" -- sh -c "cat $STATE/media-imports.json" | jq -e --arg id "$RECORD_MEDIA" '.zones["zone-b"].mediaId == $id' >/dev/null ||
    fail "the import record is not the original one"
inc exec "$NEW" -- tobby state inspect --config /etc/tobby/config.yaml --output json |
    jq -e '[.items[] | select(.class == "identity" and (.present | not))] | length == 0' >/dev/null ||
    fail "an identity entry is missing from the rebuilt instance"
check "the rebuilt instance: same account and password, same token, same certificate fingerprint, same import record, same interval override"

# -- 5. A restore that lost its permissions, and an incomplete one -------------------------------------
b7_stop "$NEW"
inc exec "$NEW" -- chmod 0644 "$STATE/accounts.yaml"
inc exec "$NEW" -- sh -c "stat -c %a $STATE/accounts.yaml" | grep -qx 644 || fail "the accounts file was not loosened: the manipulation did not happen"
CODE=$(inc exec "$NEW" -- sh -c "tobby serve --config /etc/tobby/config.yaml >/tmp/refuse.log 2>&1; echo \$?")
[ "$CODE" = 3 ] || fail "a world-readable accounts file: exit $CODE, want 3 (policy refusal)"
inc exec "$NEW" -- grep -q "TBY-STA-002" /tmp/refuse.log || fail "the refusal does not carry TBY-STA-002"
inc exec "$NEW" -- grep -q "$STATE/accounts.yaml" /tmp/refuse.log || fail "the refusal does not name the file"
check "a restore that loosened accounts.yaml to 0644 refuses the start: exit 3, TBY-STA-002, the file named"

# Group access is what a volume's fsGroup grants: tolerated.
inc exec "$NEW" -- chmod 0640 "$STATE/accounts.yaml"
b7_start "$NEW"
b7_stop "$NEW"
check "group access (0640), as a Kubernetes fsGroup grants it, is tolerated"

# An identity entry the restore forgot: the instance starts and says so.
inc exec "$NEW" -- rm -f "$STATE/media-imports.json"
b7_start "$NEW"
inc exec "$NEW" -- grep -q "TBY-STA-003" /var/log/tobby.log || fail "no TBY-STA-003 for a missing import record"
inc exec "$NEW" -- grep '"msg":"state directory entry missing"' /var/log/tobby.log | grep -q "media-imports.json" ||
    fail "the TBY-STA-003 record does not name media-imports.json"
check "a missing import record: the instance starts and reports TBY-STA-003 naming media-imports.json"

check "scenario m7 b-rebuild complete — report: $CRUCIBLE_REPORT"
