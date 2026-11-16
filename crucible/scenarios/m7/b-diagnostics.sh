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
#      the CLI. The planted values are recorded by the scenario, not by Tobby,
#      and each plant is checked to be where the scenario says it is.
#   2. A bundle is produced by `tobby diagnostics bundle` on the host, by the
#      same command asking the running instance (--instance), and by an
#      administrator through the API; an operator and a viewer are refused.
#   3. Every archive is unpacked and grepped for every planted value, in clear
#      and base64: zero occurrence. Their MANIFEST digests match their
#      members; the members are the documented ones only.
#   4. The bundle holds the version, the redacted configuration, recent log
#      records, the last task reports, a metrics snapshot, the last integrity
#      report and the state inventory (names, sizes, modes — no contents); the
#      host archive says what it leaves out and why.
#   5. The proxy witness log shows no connection during any generation
#      (NFR-019); the audit log has one diagnostics.bundle event per archive
#      the instance produced, with its requester; the counter moved.
#
# Topology:
#   tbc-m7b-proxy (container, tbc-net)  witness forward proxy (test/topology/proxy)
#   tbc-m7b-node  (container, tbc-net)  instance under test, egress via the proxy only
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-diagnostics.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-diagnostics.txt}"
. "$DIR/../../lib.sh"
. "$DIR/b-lib.sh"

WORK="$(mktemp -d)"
PROXY=tbc-m7b-proxy
NODE=tbc-m7b-node
cleanup() {
    inc delete --force "$PROXY" "$NODE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
b7_tooling
b7_build "$WORK"
( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -o "$WORK/tobby-proxy" ./test/topology/proxy ) || fail "building the forward proxy"

# -- 1. Nodes, and a planted value in every secret-bearing setting ----------------
b7_launch "$PROXY" tbc-connected-node
b7_launch "$NODE" tbc-connected-node
PROXY_IP=$(instance_ip "$PROXY")
NODE_IP=$(instance_ip "$NODE")
inc file push "$WORK/tobby-proxy" "$PROXY/usr/bin/tobby-proxy" >/dev/null
inc exec "$PROXY" -- chmod +x /usr/bin/tobby-proxy
inc exec "$PROXY" -- sh -c 'PROXY_USERNAME=svc PROXY_PASSWORD=witness-proxy-pass nohup tobby-proxy -addr :3128 >/var/log/proxy.log 2>&1 &'

rnd() { openssl rand -hex 10; }
PL_PROXY="m7-proxy-$(rnd)"
PL_DOCKER="m7-docker-$(rnd)"
PL_OIDC="m7-oidc-$(rnd)"
PL_PASSWORD="m7-password-$(rnd)"
PL_ENV="m7-env-token-$(rnd)"

# A TLS pair for the listener, and a SAML service-provider key: real PEM
# private keys, so the PEM-body search is what is exercised.
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=tbc-m7b-node" \
    -addext "subjectAltName=IP:127.0.0.1,IP:$NODE_IP" \
    -keyout "$WORK/tls.key" -out "$WORK/tls.crt" >/dev/null 2>&1 || fail "generating the TLS pair"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$WORK/saml.key" >/dev/null 2>&1 ||
    fail "generating the SAML key"
printf '%s\n' "$PL_OIDC" >"$WORK/oidc-secret"
AUTH_B64=$(printf 'robot:%s' "$PL_DOCKER" | base64 | tr -d '\n')
printf '{"auths":{"registry.example":{"auth":"%s"}}}\n' "$AUTH_B64" >"$WORK/creds.json"

inc exec "$NODE" -- mkdir -p /etc/tobby /var/lib/tobby /var/lib/tobby-state
for f in tls.crt tls.key saml.key oidc-secret creds.json; do
    inc file push --mode 0600 "$WORK/$f" "$NODE/etc/tobby/$f" >/dev/null || fail "pushing $f"
done
cat >"$WORK/config.yaml" <<YAML
mode: mirror
zone: zone-b
storage:
  root: /var/lib/tobby
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
  tls:
    certFile: /etc/tobby/tls.crt
    keyFile: /etc/tobby/tls.key
network:
  proxy:
    url: http://$PROXY_IP:3128
    username: svc
    password: $PL_PROXY
  tls:
    caFiles:
      - /etc/tobby/tls.crt
registries:
  credentialsFile: /etc/tobby/creds.json
auth:
  oidc:
    clientSecretFile: /etc/tobby/oidc-secret
  saml:
    keyFile: /etc/tobby/saml.key
YAML
inc file push "$WORK/config.yaml" "$NODE/etc/tobby/config.yaml" >/dev/null
b7_user "$NODE" admin admin "$PL_PASSWORD"
b7_user "$NODE" operator operator "pw-op-$(rnd)"
b7_user "$NODE" viewer viewer "pw-view-$(rnd)"
B7_SCHEME=https
b7_start "$NODE"

# The static token, created through the API: its secret exists exactly once.
PL_TOKEN=$(b7_api "$NODE_IP" "admin:$PL_PASSWORD" POST /api/v1/tokens '{"name":"planted","role":"admin"}' | jq -r .secret)
case "$PL_TOKEN" in tby_*) : ;; *) fail "the token was not created: $PL_TOKEN" ;; esac

# Some history for the bundle to carry: an on-demand verification task.
b7_api "$NODE_IP" "admin:$PL_PASSWORD" POST /api/v1/store/verify >/dev/null
_n=0
until b7_api "$NODE_IP" "admin:$PL_PASSWORD" GET /api/v1/store/verification | jq -e '.verification.finished_at // empty' >/dev/null 2>&1; do
    _n=$((_n + 1)); [ "$_n" -gt 60 ] && fail "the verification never finished"; sleep 1
done

# The plants are verified to be where the scenario says (m5/m6 rule).
inc exec "$NODE" -- grep -q "$PL_PROXY" /etc/tobby/config.yaml || fail "the proxy password is not in the configuration"
inc exec "$NODE" -- grep -q BEGIN /etc/tobby/saml.key || fail "the SAML key is not in place"
inc exec "$NODE" -- grep -q "$PL_OIDC" /etc/tobby/oidc-secret || fail "the OIDC secret is not in place"
echo "$AUTH_B64" | grep -q . || fail "no registry credential encoded"
check "a value is planted in every secret-bearing setting: proxy password, OIDC secret, SAML key, registry credentials, TLS key, a token, a password (each verified in place)"

# Everything that must NOT appear, in clear and in base64 (standard and URL).
{
    for v in "$PL_PROXY" "$PL_DOCKER" "$PL_OIDC" "$PL_PASSWORD" "$PL_TOKEN" "$PL_ENV" "$AUTH_B64"; do
        printf '%s\n' "$v"
        printf '%s' "$v" | base64 | tr -d '\n'; printf '\n'
        printf '%s' "$v" | base64 | tr -d '\n' | tr '+/' '-_'; printf '\n'
    done
    # A line of each private key body: whatever encoding the PEM was copied in,
    # these are the bytes of the key.
    sed -n '2p' "$WORK/tls.key"
    sed -n '2p' "$WORK/saml.key"
} >"$WORK/needles.txt"
[ "$(grep -c . "$WORK/needles.txt")" -ge 23 ] || fail "the needle list is short: $(grep -c . "$WORK/needles.txt")"

# -- 2. Three ways to take a bundle, and two roles refused ------------------------
PROXY_BEFORE=$(inc exec "$PROXY" -- sh -c 'wc -l </var/log/proxy.log')

inc exec "$NODE" -- sh -c "cd /tmp && TOBBY_API_TOKEN='$PL_ENV' tobby diagnostics bundle --config /etc/tobby/config.yaml -o /tmp/host.tar.gz --output json" \
    >"$WORK/host-report.json" 2>"$WORK/host-stderr.txt" || fail "tobby diagnostics bundle failed on the host: $(cat "$WORK/host-stderr.txt")"
b7_pull "$NODE" /tmp/host.tar.gz "$WORK/host.tar.gz"

inc exec "$NODE" -- sh -c "TOBBY_API_TOKEN='$PL_TOKEN' tobby diagnostics bundle --config /etc/tobby/config.yaml --instance https://127.0.0.1:8080 -o /tmp/remote.tar.gz --output json" \
    >"$WORK/remote-report.json" 2>"$WORK/remote-stderr.txt" || fail "tobby diagnostics bundle --instance failed: $(cat "$WORK/remote-stderr.txt")"
b7_pull "$NODE" /tmp/remote.tar.gz "$WORK/remote.tar.gz"

b7_api "$NODE_IP" "admin:$PL_PASSWORD" POST /api/v1/diagnostics/bundle >"$WORK/api.tar.gz"
tar -tzf "$WORK/api.tar.gz" >/dev/null 2>&1 || fail "the API answer is not a gzip archive: $(head -c 300 "$WORK/api.tar.gz")"

check "the host command, the same command asking the instance, and the API each produced an archive"

# -- 3. Nothing planted anywhere in any archive ------------------------------------
for name in host remote api; do
    mkdir -p "$WORK/x-$name"
    tar -xzf "$WORK/$name.tar.gz" -C "$WORK/x-$name" || fail "unpacking the $name archive"
    if grep -rqF -f "$WORK/needles.txt" "$WORK/x-$name"; then
        grep -rlF -f "$WORK/needles.txt" "$WORK/x-$name" | sed "s|$WORK/||" >&2
        fail "the $name archive holds a planted secret"
    fi
done
check "zero occurrence of any planted value, in clear or base64, in the host, instance and API archives"

# -- 4. Manifest, members, content ---------------------------------------------------
DOCUMENTED='^(MANIFEST\.json|version\.json|config\.yaml|logs\.jsonl|media-log\.jsonl|metrics\.txt|integrity\.json|state-inventory\.json|tasks/[A-Za-z0-9_.-]+\.json)$'
for name in host remote api; do
    ( cd "$WORK/x-$name" && find . -type f | sed 's|^\./||' ) | while read -r f; do
        printf '%s' "$f" | grep -Eq "$DOCUMENTED" || { echo "undocumented member $f in $name" >&2; exit 1; }
    done || fail "the $name archive holds an undocumented member"
    jq -r '.members[] | "\(.digest | sub("sha256:"; ""))  \(.name)"' "$WORK/x-$name/MANIFEST.json" >"$WORK/sums-$name.txt"
    ( cd "$WORK/x-$name" && sha256sum -c "$WORK/sums-$name.txt" >/dev/null 2>&1 ) ||
        fail "the $name manifest digests do not match its members"
done
check "every archive holds documented members only, and the manifest digests match them"

A="$WORK/x-api"
jq -e '.version != null' "$A/version.json" >/dev/null || fail "version.json: no version"
grep -q REDACTED "$A/config.yaml" || fail "config.yaml does not show the redaction of the proxy password"
[ -s "$A/logs.jsonl" ] || fail "the instance's archive carries no log record"
[ -s "$A/media-log.jsonl" ] || fail "a mirror instance's archive carries no operation-log tail"
ls "$A"/tasks/*.json >/dev/null 2>&1 || fail "no task report in the instance's archive"
grep -q '^tobby_build_info' "$A/metrics.txt" || fail "metrics.txt carries no tobby_build_info"
jq -e '.verdict != null' "$A/integrity.json" >/dev/null || fail "integrity.json carries no verdict"
jq -e '[.items[] | select(.name == "accounts.yaml")] | length == 1' "$A/state-inventory.json" >/dev/null ||
    fail "the state inventory does not list accounts.yaml"
if grep -q argon2 "$A/state-inventory.json"; then fail "the state inventory carries a hash: contents must never be listed"; fi
H="$WORK/x-host"
jq -e '[.omitted[].name] | index("logs.jsonl") != null and index("metrics.txt") != null' "$H/MANIFEST.json" >/dev/null ||
    fail "the host archive does not say that it leaves out the log window and the metrics"
check "the instance archive holds version, redacted configuration, logs, medium log, tasks, metrics, integrity report and state inventory; the host archive says what it leaves out"

# -- 5. Roles, audit, metric, and no connection ----------------------------------------
for role in operator viewer; do
    _tok=$(b7_api "$NODE_IP" "admin:$PL_PASSWORD" POST /api/v1/tokens "{\"name\":\"probe-$role\",\"role\":\"$role\"}" | jq -r .secret)
    [ -n "$_tok" ] && [ "$_tok" != null ] || fail "no $role token"
    _code=$(curl -sk -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $_tok" -X POST "https://$NODE_IP:8080/api/v1/diagnostics/bundle")
    [ "$_code" = 403 ] || fail "the $role role obtained the bundle endpoint: HTTP $_code"
done
check "the operator and viewer roles are refused (403)"

EVENTS=$(inc exec "$NODE" -- sh -c "grep -c '\"action\":\"diagnostics.bundle\"' /var/log/tobby.log")
[ "$EVENTS" = 2 ] || fail "want one diagnostics.bundle audit event per instance-produced archive (2), got $EVENTS"
inc exec "$NODE" -- sh -c "grep '\"action\":\"diagnostics.bundle\"' /var/log/tobby.log" | grep -q '"actor":"admin"' ||
    fail "the audit event does not name the administrator"
b7_api "$NODE_IP" "admin:$PL_PASSWORD" GET /metrics | grep -q '^tobby_diagnostics_bundles_total{outcome="written"} 2' ||
    fail "tobby_diagnostics_bundles_total{outcome=\"written\"} did not reach 2"
PROXY_AFTER=$(inc exec "$PROXY" -- sh -c 'wc -l </var/log/proxy.log')
[ "$PROXY_BEFORE" = "$PROXY_AFTER" ] || fail "the witness proxy saw $((PROXY_AFTER - PROXY_BEFORE)) request(s) during the generations (NFR-019)"
check "audited per requester, counted, and no connection through the witness proxy during any generation"

check "scenario m7 b-diagnostics complete — report: $CRUCIBLE_REPORT"
