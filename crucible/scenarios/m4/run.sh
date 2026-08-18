#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-4 crucible scenario (ADR-0014, DEVELOPMENT-PLAN §2.4): the
# first complete operating mode — a long-lived service continuously
# promoting artifacts from a production registry into a zone registry —
# on real nodes, with the properties only this tier can exercise
# faithfully: REAL cosign signatures re-verified before every push, a real
# TLS server certificate issued by a private authority, a real
# authenticated forward proxy, and a real network policy that makes direct
# egress impossible rather than merely unused.
#
#   1. Fixtures: a production registry (zone A) holds two multi-arch
#      images and a cookbook of recipes signed by a key this scenario
#      generates; a zone registry (zone B) serves TLS with a certificate
#      issued by this scenario's own authority and demands a write-scoped
#      account (FR-004, FR-082).
#   2. The instance under test is deployed from the REFERENCE CHART:
#      `helm template deploy/charts/tobby` renders its configuration and
#      that rendering — not a hand-written file — is what the node reads
#      (public feature 4.5).
#   3. Secure by default (FR-075, ADR-0009): anonymous is refused on the
#      API and on the embedded registry; the documented override, active
#      on the fixture registry, opens access and puts the permanent danger
#      banner on every page.
#   4. Promotion: one cycle resolves the recipe, fetches what is missing,
#      re-verifies the signature over the exact bytes about to leave, and
#      pushes them to the zone registry with the recipe itself (FR-013,
#      FR-026, FR-028, FR-033, FR-034). The SECOND cycle pushes nothing.
#   5. The cadence changes on the running instance, a scheduled cycle
#      actually fires under the new interval, and the override outlives a
#      restart (FR-013, FR-094).
#   6. A destination outside the allow-list is refused before any byte
#      moves, with the dedicated code on the audit log and on the metrics —
#      and the refused recipe never appears at the destination (FR-030).
#   7. The same promotion with DIRECT EGRESS BLOCKED: a second node whose
#      network ACL permits nothing but the proxy, reaching the source and
#      the private-PKI destination entirely through an authenticated
#      forward proxy (FR-080, FR-081).
#
# Topology (containers on tbc-net, profile tbc-connected-node):
#   tbc-m4-source   production registry of zone A + cookbook
#   tbc-m4-dest     zone-B registry: TLS from the private CA, account-gated
#   tbc-m4-proxy    the authenticated forward proxy
#   tbc-m4-node     the instance under test, deployed from the chart
#   tbc-m4-egress   the instance under test with direct egress blocked
#
# Requires, on top of the crucible baseline: helm (the milestone deploys
# from the reference chart), cosign (installed on demand like m3),
# openssl, jq and curl on the crucible host.
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
. "$DIR/../../lib.sh"

IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.22}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
CRED_USER="admin"
CRED_PASS="m4-crucible-pass"
PUSH_USER="promoter"
PUSH_PASS="m4-crucible-push"
PROXY_USER="tobby"
PROXY_PASS="m4-crucible-proxy"
ACL="tbc-m4-proxyonly"
SEALED_NET="tbc-m4-sealed"
SEALED_CIDR="10.180.30.0/24"
INSTANCES="tbc-m4-source tbc-m4-dest tbc-m4-proxy tbc-m4-node tbc-m4-egress"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

report_start

# -- Tooling -----------------------------------------------------------------
command -v openssl >/dev/null 2>&1 || fail "openssl is required to issue the scenario's private PKI"
command -v jq >/dev/null 2>&1 || fail "jq is required"
# The milestone's first exit criterion is "with the instance deployed from
# the reference chart": without helm there is no chart rendering, and a
# hand-written configuration would be a different claim. Fail rather than
# quietly substitute one.
command -v helm >/dev/null 2>&1 ||
    fail "helm is required: the milestone-4 instance is deployed from deploy/charts/tobby"

COSIGN="${COSIGN:-cosign}"
if ! command -v "$COSIGN" >/dev/null 2>&1; then
    GOBIN="$WORK/bin" go install github.com/sigstore/cosign/v3/cmd/cosign@v3.0.4 ||
        fail "cosign unavailable and go install failed"
    COSIGN="$WORK/bin/cosign"
fi
check "tooling available (helm $(helm version --short 2>/dev/null || echo unknown), cosign present, openssl, jq)"

# -- Build the programs under test -------------------------------------------
( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -o /tmp/tobby-crucible ./cmd/tobby ) ||
    fail "building tobby for linux/$ARCH"
# The forward proxy is built from this repository (test/topology/proxy)
# rather than pulled: the crucible installs nothing at scenario time, and a
# proxy of our own can act as a witness — it logs every destination it was
# asked to reach.
( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -o /tmp/tobby-crucible-proxy ./test/topology/proxy ) ||
    fail "building the forward proxy for linux/$ARCH"
check "built tobby and the forward proxy for linux/$ARCH"

# -- Fresh instances ---------------------------------------------------------
for inst in $INSTANCES; do
    inc delete --force "$inst" 2>/dev/null || true
done
incus network unset "$SEALED_NET" security.acls >/dev/null 2>&1 || true
incus network acl delete "$ACL" >/dev/null 2>&1 || true
# The sealed node lives on its own bridge (see step 9): the segment, not
# the instance, carries the policy.
incus network show "$SEALED_NET" >/dev/null 2>&1 ||
    incus network create "$SEALED_NET" \
        ipv4.address="${SEALED_CIDR%.0/24}.1/24" ipv4.nat=true ipv6.address=none >/dev/null
incus network unset "$SEALED_NET" security.acls >/dev/null 2>&1 || true
for inst in $INSTANCES; do
    case "$inst" in
    tbc-m4-egress) inc launch "$IMAGE" "$inst" --profile tbc-connected-node --network "$SEALED_NET" ;;
    *) inc launch "$IMAGE" "$inst" --profile tbc-connected-node ;;
    esac
    inc file push /tmp/tobby-crucible "$inst/usr/bin/tobby"
    inc exec "$inst" -- chmod +x /usr/bin/tobby
done
inc file push /tmp/tobby-crucible-proxy tbc-m4-proxy/usr/bin/tobby-proxy
inc exec tbc-m4-proxy -- chmod +x /usr/bin/tobby-proxy
check "instances launched (source, destination, proxy, node, sealed node)"

# A freshly launched instance has no address until DHCP completes: wait for
# each rather than capturing an empty string that only fails minutes later
# (the m3 acceptance run learned this the hard way).
instance_ip() {
    _i=0
    while :; do
        _ip=$(inc list "$1" -c 4 -f csv | awk '{print $1}' | head -1)
        if [ -n "$_ip" ]; then
            printf '%s' "$_ip"
            return 0
        fi
        _i=$((_i + 1))
        [ "$_i" -gt 60 ] && fail "instance $1 never received an address"
        sleep 1
    done
}
SOURCE_IP=$(instance_ip tbc-m4-source)
DEST_IP=$(instance_ip tbc-m4-dest)
PROXY_IP=$(instance_ip tbc-m4-proxy)
NODE_IP=$(instance_ip tbc-m4-node)
EGRESS_IP=$(instance_ip tbc-m4-egress)
check "addresses assigned (source $SOURCE_IP, dest $DEST_IP, proxy $PROXY_IP, node $NODE_IP, sealed $EGRESS_IP)"

# -- 1. A private authority of our own, and a leaf for the zone registry -----
# FR-081 asks for private authorities to be TRUSTED, not for verification
# to be dropped: the scenario therefore issues a real chain, and the only
# thing Tobby is ever given is the authority.
cat >"$WORK/leaf.ext" <<EOF
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = IP:$DEST_IP, DNS:tbc-m4-dest
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
    -keyout "$WORK/ca.key" -out "$WORK/ca.crt" \
    -subj "/CN=Tobby crucible private CA" >/dev/null 2>&1 ||
    fail "issuing the private certificate authority"
openssl req -newkey rsa:2048 -nodes -keyout "$WORK/tls.key" -out "$WORK/tls.csr" \
    -subj "/CN=tbc-m4-dest" >/dev/null 2>&1 || fail "issuing the zone registry key"
openssl x509 -req -in "$WORK/tls.csr" -days 2 \
    -CA "$WORK/ca.crt" -CAkey "$WORK/ca.key" -CAcreateserial \
    -extfile "$WORK/leaf.ext" -out "$WORK/tls.crt" >/dev/null 2>&1 ||
    fail "signing the zone registry certificate"
openssl verify -CAfile "$WORK/ca.crt" "$WORK/tls.crt" >/dev/null 2>&1 ||
    fail "the issued certificate does not chain to the private authority"
check "private PKI issued (root + leaf for $DEST_IP)"

# -- 2. Fixtures: the production registry, the zone registry, the proxy ------
# The production registry is a Tobby with the explicit FR-075 opt-out — a
# standards-compliant OCI registry (m1 proves its wire behavior), and the
# instance this scenario later uses to observe the override's banner.
inc exec tbc-m4-source -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/srv/source \
        TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m4-source http://127.0.0.1:8080/readyz

# The zone registry: TLS with the private leaf (FR-082) and a write-scoped
# account (FR-004). Promotion authenticates like any other client — the
# destination is not a special case with a special door.
cat >"$WORK/dest.yaml" <<EOF
mode: passthrough
storage:
  root: /srv/dest
state:
  root: /srv/dest-state
server:
  addr: :8080
  tls:
    certFile: /etc/tobby/tls.crt
    keyFile: /etc/tobby/tls.key
logging:
  level: info
EOF
inc exec tbc-m4-dest -- mkdir -p /etc/tobby
inc file push "$WORK/dest.yaml" tbc-m4-dest/etc/tobby/config.yaml
inc file push "$WORK/tls.crt" tbc-m4-dest/etc/tobby/tls.crt
inc file push "$WORK/tls.key" tbc-m4-dest/etc/tobby/tls.key
inc exec tbc-m4-dest -- sh -c "printf '%s\n' '$PUSH_PASS' |
    tobby user add $PUSH_USER --state-root /srv/dest-state --password-stdin" >/dev/null ||
    fail "creating the write-scoped account on the zone registry"
inc exec tbc-m4-dest -- sh -c '
    nohup tobby serve --config /etc/tobby/config.yaml >/var/log/tobby.log 2>&1 &
'
_i=0
until curl -fsS --cacert "$WORK/ca.crt" "https://$DEST_IP:8080/readyz" >/dev/null 2>&1; do
    _i=$((_i + 1))
    [ "$_i" -gt 60 ] && fail "the zone registry never served TLS from the private authority"
    sleep 1
done

inc exec tbc-m4-proxy -- sh -c "
    nohup env PROXY_USERNAME='$PROXY_USER' PROXY_PASSWORD='$PROXY_PASS' \
        tobby-proxy -addr :3128 >/var/log/proxy.log 2>&1 &
"
_i=0
until curl -s -o /dev/null -m 5 -x "http://$PROXY_IP:3128" "http://$SOURCE_IP:8080/v2/" 2>/dev/null; do
    _i=$((_i + 1))
    [ "$_i" -gt 60 ] && fail "the forward proxy never started"
    sleep 1
done
check "fixtures serving: production registry, TLS zone registry (private CA), forward proxy"

# -- 3. Signed recipes in the cookbook ---------------------------------------
IMG_A=$( cd "$ROOT" && go run ./test/topology/seed push "$SOURCE_IP:8080/library/sample:1.0.0" ) ||
    fail "seeding the first image"
IMG_B=$( cd "$ROOT" && go run ./test/topology/seed push "$SOURCE_IP:8080/library/other:1.0.0" ) ||
    fail "seeding the second image"
( cd "$WORK" && COSIGN_PASSWORD= "$COSIGN" generate-key-pair >/dev/null 2>&1 ) ||
    fail "generating the signing key pair"

write_recipe() {
    cat >"$WORK/recipe-$1.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: zone-app
  version: $1
spec:
  ingredients:
    - name: $2
      kind: ContainerImage
      ref: $SOURCE_IP:8080/library/$2
      version: 1.0.0
      digest: $3
EOF
}
# RECIPE-SPEC §12.2 and ADR-0007 pin the classic attached-signature
# convention ("sha256-<hex>.sig"); cosign 3.x defaults to the bundle
# layout, so the format is stated explicitly here (m3 proves both layouts
# verify).
sign_recipe() {
    COSIGN_PASSWORD= "$COSIGN" sign --key "$WORK/cosign.key" --yes --allow-insecure-registry \
        --use-signing-config=false --new-bundle-format=false --tlog-upload=false "$1" \
        >/dev/null 2>&1
}
write_recipe 1.0.0 sample "$IMG_A"
RECIPE_A=$( cd "$ROOT" && go run ./test/topology/seed push-recipe \
    "$SOURCE_IP:8080/cookbook/zone-app:1.0.0" "$WORK/recipe-1.0.0.yaml" ) ||
    fail "publishing recipe 1.0.0"
sign_recipe "$SOURCE_IP:8080/cookbook/zone-app@$RECIPE_A" || fail "signing recipe 1.0.0"
write_recipe 1.0.1 other "$IMG_B"
RECIPE_B=$( cd "$ROOT" && go run ./test/topology/seed push-recipe \
    "$SOURCE_IP:8080/cookbook/zone-app:1.0.1" "$WORK/recipe-1.0.1.yaml" ) ||
    fail "publishing recipe 1.0.1"
sign_recipe "$SOURCE_IP:8080/cookbook/zone-app@$RECIPE_B" || fail "signing recipe 1.0.1"
check "cookbook published and signed by the real cosign (1.0.0 → $IMG_A, 1.0.1 → $IMG_B)"

# -- 4. The node is deployed from the reference chart ------------------------
# The trust root is a YAML block scalar: its content must be indented more
# than the "key:" that introduces it (8 spaces here, hence 10).
PUB_KEY_INDENTED=$(sed 's/^/          /' "$WORK/cosign.pub")

render_values() {
    # $1 destination host, $2 output file, $3… extra allow-list entries
    _dest="$1"
    _out="$2"
    shift 2
    cat >"$WORK/values.yaml" <<EOF
image:
  repository: tobby-crucible
  tag: local
config:
  mode: passthrough
  logging:
    level: debug
  retriever:
    source: /etc/tobby/retriever.yaml
  destination:
    registry: $_dest
    cookbook: cookbook
  sync:
    parallelism: 2
    retries: 2
    # The periodic loop is parked so that the scenario decides when a cycle
    # happens; the runtime override is exercised explicitly later, and only
    # then does the scheduler fire on its own.
    interval: 0s
  registries:
    insecure:
      - $SOURCE_IP:8080
    credentialsFile: /etc/tobby/creds.json
    allowlist:
EOF
    for _entry in "$@"; do
        printf '      - %s\n' "$_entry" >>"$WORK/values.yaml"
    done
    cat >>"$WORK/values.yaml" <<EOF
  network:
    tls:
      caFiles:
        - /etc/tobby/ca.crt
  trust:
    roots:
      - name: crucible
        key: |
$PUB_KEY_INDENTED
EOF
    helm template tobby-m4 "$ROOT/deploy/charts/tobby" -f "$WORK/values.yaml" \
        --show-only templates/secret-config.yaml |
        awk '/^  config\.yaml: \|$/ {found = 1; next} found {sub(/^    /, ""); print}' >"$_out"
    [ -s "$_out" ] || fail "the chart rendered an empty configuration"
}

helm lint "$ROOT/deploy/charts/tobby" >/dev/null || fail "helm lint on the reference chart"
render_values "$DEST_IP:8080" "$WORK/node-config.yaml" "$SOURCE_IP:8080" "$DEST_IP:8080"
# The chart owns storage.root, state.root and server.addr — the settings
# the pod spec depends on too. Read them back rather than assume them: the
# node's directories below are the chart's, not this scenario's.
grep -q 'root: /var/lib/tobby/store' "$WORK/node-config.yaml" ||
    fail "the chart no longer manages storage.root"
grep -q 'root: /var/lib/tobby/state' "$WORK/node-config.yaml" ||
    fail "the chart no longer manages state.root"
# The hardening the milestone promises (public feature 4.5) is asserted on
# the rendered pod, once, here: the crucible runs the binary directly, so a
# chart that stopped dropping capabilities would otherwise pass unnoticed.
helm template tobby-m4 "$ROOT/deploy/charts/tobby" -f "$WORK/values.yaml" >"$WORK/rendered.yaml"
for want in 'readOnlyRootFilesystem: true' 'runAsNonRoot: true' \
    'allowPrivilegeEscalation: false' 'path: /healthz' 'path: /readyz'; do
    grep -q "$want" "$WORK/rendered.yaml" || fail "the rendered chart lost: $want"
done
grep -A2 'capabilities:' "$WORK/rendered.yaml" | grep -q -- '- ALL' ||
    fail "the rendered chart no longer drops every capability"

cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-b
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: zone-app
      version: "1.0.0"
EOF
PUSH_AUTH=$(printf '%s' "$PUSH_USER:$PUSH_PASS" | base64 | tr -d '\n')
printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$DEST_IP:8080" "$PUSH_AUTH" >"$WORK/creds.json"

push_node_files() {
    inc exec "$1" -- mkdir -p /etc/tobby
    inc file push "$WORK/retriever.yaml" "$1/etc/tobby/retriever.yaml"
    inc file push "$WORK/creds.json" "$1/etc/tobby/creds.json"
    inc file push "$WORK/ca.crt" "$1/etc/tobby/ca.crt"
}
push_node_files tbc-m4-node
inc file push "$WORK/node-config.yaml" tbc-m4-node/etc/tobby/config.yaml
inc exec tbc-m4-node -- sh -c "printf '%s\n' '$CRED_PASS' |
    tobby user add $CRED_USER --state-root /var/lib/tobby/state --password-stdin" >/dev/null ||
    fail "creating the account on the node"
start_node() {
    inc exec "$1" -- sh -c '
        nohup tobby serve --config /etc/tobby/config.yaml >>/var/log/tobby.log 2>&1 &
    '
    wait_ready "$1" http://127.0.0.1:8080/readyz
}
start_node tbc-m4-node
check "node deployed from the reference chart (rendered config, chart-managed roots, hardening asserted)"

API="http://$NODE_IP:8080/api/v1"
CRED="$CRED_USER:$CRED_PASS"

# -- 5. Secure by default, and the documented override -----------------------
ANON=$(curl -s -o "$WORK/anon.json" -w '%{http_code}' "$API/content")
[ "$ANON" = "401" ] || fail "anonymous API answered $ANON, want 401"
grep -q '"code":"TBY-AUTH-002"' "$WORK/anon.json" ||
    fail "the 401 body is not the RFC 9457 taxonomy document"
curl -s -o /dev/null -D - "http://$NODE_IP:8080/v2/" | grep -qi "WWW-Authenticate: Basic" ||
    fail "the embedded registry misses the Basic challenge"
OPEN=$(curl -s -o /dev/null -w '%{http_code}' "http://$SOURCE_IP:8080/api/v1/content")
[ "$OPEN" = "200" ] || fail "the override instance answered $OPEN anonymously, want 200"
curl -fsS -H 'Accept-Language: en' "http://$SOURCE_IP:8080/" >"$WORK/open.html" ||
    fail "the override instance does not serve the UI"
grep -q 'Authentication is disabled on this instance' "$WORK/open.html" ||
    fail "the permanent warning banner is missing (FR-075)"
grep -q 't-banner--danger' "$WORK/open.html" ||
    fail "the override banner is not rendered as a danger banner"
check "secure by default (401 RFC 9457 + Basic challenge); the override opens access with its permanent banner"

# -- 6. Promotion, and the second cycle that moves nothing -------------------
sync_and_wait() {
    _api="$1"
    _tid=$(curl -fsS -u "$CRED" -X POST "$_api/sync" | jq -r '.task.id') || return 1
    _i=0
    while :; do
        _st=$(curl -fsS -u "$CRED" "$_api/tasks/$_tid" | jq -r '.task.status')
        case "$_st" in
        done | failed) break ;;
        esac
        _i=$((_i + 1))
        [ "$_i" -gt 300 ] && return 1
        sleep 0.4
    done
    printf '%s' "$_tid"
}

TASK1=$(sync_and_wait "$API") || fail "the first promotion cycle never settled"
CYCLE1=$(curl -fsS -u "$CRED" "$API/tasks/$TASK1")
printf '%s' "$CYCLE1" | jq -e '.task.status == "done"' >/dev/null ||
    fail "the first cycle ended $(printf '%s' "$CYCLE1" | jq -r '.task.status'), want done"
printf '%s' "$CYCLE1" | jq -e '[.task.resolutions[].pushed_bytes // 0] | add > 0' >/dev/null ||
    fail "the first cycle pushed nothing"
RELOCATED="${SOURCE_IP}_8080/library/sample"
printf '%s' "$CYCLE1" | jq -e --arg want "$DEST_IP:8080/$RELOCATED" \
    '[.task.resolutions[] | select(.destination != null) | .destination] | any(. == $want)' >/dev/null ||
    fail "the ingredient did not land under its relocated destination path (FR-035)"

# What the destination actually holds, read back over its own TLS with the
# private authority and the write-scoped account — not what the task says
# it did.
dest_digest() {
    curl -fsS -o /dev/null -D - --cacert "$WORK/ca.crt" -u "$PUSH_USER:$PUSH_PASS" \
        -H 'Accept: application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.list.v2+json' \
        "https://$DEST_IP:8080/v2/$1/manifests/$2" |
        tr -d '\r' | awk 'tolower($1) == "docker-content-digest:" {print $2}'
}
LANDED=$(dest_digest "$RELOCATED" 1.0.0)
[ "$LANDED" = "$IMG_A" ] || fail "the destination holds $LANDED, want the pinned $IMG_A"
[ -n "$(dest_digest cookbook/zone-app 1.0.0)" ] ||
    fail "the recipe was not re-published in the zone cookbook (FR-034)"
check "cycle 1: recipe verified and promoted, zone cookbook published, pinned digest intact at the destination"

TASK2=$(sync_and_wait "$API") || fail "the second promotion cycle never settled"
CYCLE2=$(curl -fsS -u "$CRED" "$API/tasks/$TASK2")
MOVED=$(printf '%s' "$CYCLE2" | jq '[.task.resolutions[].pushed_bytes // 0] | add // 0')
[ "$MOVED" = "0" ] || fail "the second cycle pushed $MOVED bytes, want 0 (FR-028)"
printf '%s' "$CYCLE2" | jq -e \
    '[.task.resolutions[] | select(.destination != null) | .destination_status]
     | length > 0 and all(. == "up-to-date")' >/dev/null ||
    fail "the second cycle did not see every destination item up to date"
check "cycle 2: nothing pushed, every destination item already up to date (FR-026, FR-028)"

# -- 7. The cadence changes on a running instance and outlives a restart -----
BEFORE=$(curl -fsS -u "$CRED" "$API/tasks" | jq '[.tasks[] | select(.type == "sync")] | length')
curl -fsS -u "$CRED" -X PUT -H 'Content-Type: application/json' \
    -d '{"interval":"1m"}' "$API/retriever/interval" >/dev/null ||
    fail "changing the reconciliation interval"
curl -fsS -u "$CRED" "$API/retriever" |
    jq -e '.interval.effective == "1m0s" and .interval.overridden == true' >/dev/null ||
    fail "the new interval is not the effective one"
# A cadence that changes only in the report would be a setting, not a
# schedule: wait for the loop to actually fire one cycle under the new
# interval. The first tick happens one interval after the change, never at
# once (a restart storm would otherwise hammer the peer zone).
_i=0
while :; do
    NOW=$(curl -fsS -u "$CRED" "$API/tasks" | jq '[.tasks[] | select(.type == "sync")] | length')
    [ "$NOW" -gt "$BEFORE" ] && break
    _i=$((_i + 1))
    [ "$_i" -gt 100 ] && fail "no cycle fired within 100s of setting a 1m interval"
    sleep 1
done
curl -fsS -u "$CRED" "$API/tasks" |
    jq -e '[.tasks[] | select(.type == "sync" and .actor == "local")] | length > 0' >/dev/null ||
    fail "the cycle that fired was not the scheduler's"
inc exec tbc-m4-node -- sh -c 'pkill -TERM tobby' || true
sleep 2
start_node tbc-m4-node
curl -fsS -u "$CRED" "$API/retriever" |
    jq -e '.interval.effective == "1m0s" and .interval.overridden == true' >/dev/null ||
    fail "the interval override did not survive the restart (it must outlive the pod)"
inc exec tbc-m4-node -- grep -q '"action":"config.promotion_interval"' /var/log/tobby.log ||
    fail "the interval change was not audited (FR-094)"
curl -fsS -u "$CRED" -X DELETE "$API/retriever/interval" >/dev/null ||
    fail "clearing the interval override"
check "FR-013: interval changed at runtime, a scheduled cycle fired, the override survived a restart, the change was audited"

# -- 8. A destination outside the allow-list is refused (FR-030) -------------
# The refused recipe is a NEW one, so "nothing landed" is observable at the
# destination rather than inferred from content an earlier cycle put there.
sed 's/version: "1.0.0"/version: "1.0.1"/' "$WORK/retriever.yaml" >"$WORK/retriever-101.yaml"
inc file push "$WORK/retriever-101.yaml" tbc-m4-node/etc/tobby/retriever.yaml
render_values "$DEST_IP:8080" "$WORK/policy-config.yaml" "$SOURCE_IP:8080"
inc file push "$WORK/policy-config.yaml" tbc-m4-node/etc/tobby/config.yaml
inc exec tbc-m4-node -- sh -c 'pkill -TERM tobby' || true
sleep 2
start_node tbc-m4-node
inc exec tbc-m4-node -- grep -q '"msg":"registry allowlist active"' /var/log/tobby.log ||
    fail "the instance does not report its allow-list at startup"

POLICY_TASK=$(sync_and_wait "$API") || fail "the refused cycle never settled"
POLICY_JSON=$(curl -fsS -u "$CRED" "$API/tasks/$POLICY_TASK")
printf '%s' "$POLICY_JSON" |
    jq -e '[.task.items[] | select(.error.code == "TBY-POL-001")] | length > 0' >/dev/null ||
    fail "the off-list destination was not refused with TBY-POL-001"
printf '%s' "$POLICY_JSON" | jq -e '[.task.resolutions[].pushed_bytes // 0] | add == 0' >/dev/null ||
    fail "bytes crossed to a destination that is not on the allow-list"
if [ -n "$(dest_digest cookbook/zone-app 1.0.1)" ]; then
    fail "the refused recipe landed at the destination anyway"
fi
inc exec tbc-m4-node -- grep -q 'TBY-POL-001' /var/log/tobby.log ||
    fail "no TBY-POL-001 entry on the audit log"
inc exec tbc-m4-node -- grep -q '"msg":"promotion refused"' /var/log/tobby.log ||
    fail "the refusal is not logged as such"
inc exec tbc-m4-node -- grep -q "$DEST_IP:8080" /var/log/tobby.log ||
    fail "the log entry does not name the destination it refused"
METRICS=$(curl -fsS -u "$CRED" "http://$NODE_IP:8080/metrics")
printf '%s' "$METRICS" | awk '
    /^tobby_promotion_refusals_total\{code="TBY-POL-001"\}/ && $2 > 0 { found = 1 }
    END { exit found ? 0 : 1 }' ||
    fail "the refusal was not counted in tobby_promotion_refusals_total"
printf '%s' "$METRICS" | awk '
    /^tobby_policy_rejections_total\{code="TBY-POL-001"\}/ && $2 > 0 { found = 1 }
    END { exit found ? 0 : 1 }' ||
    fail "the refusal was not counted in tobby_policy_rejections_total"
check "FR-030: off-list destination refused before transfer, audited, counted; the refused recipe never landed"

# -- 9. The same promotion with direct egress blocked ------------------------
# The gap is structural, not conventional: the sealed node sits on its own
# bridge whose ACL permits the forward proxy and nothing else. A
# configuration that merely NAMED a proxy would keep working if the code
# forgot to use it; this one does not — the route is absent, so a request
# that skips the proxy does not go somewhere else, it goes nowhere.
#
# The ACL is applied to the NETWORK, not to the instance's NIC:
# "security.acls" is not a valid device option on a bridged nic, and
# crucible/setup.sh already seals the air-gapped zone the same way. One
# mechanism, proven once.
incus network acl delete "$ACL" >/dev/null 2>&1 || true
incus network acl create "$ACL" >/dev/null
incus network acl rule add "$ACL" egress destination="$PROXY_IP/32" action=allow >/dev/null
incus network acl rule add "$ACL" egress destination="$SEALED_CIDR" action=allow >/dev/null
incus network acl rule add "$ACL" ingress source="$SEALED_CIDR" action=allow >/dev/null
incus network acl rule add "$ACL" ingress source="$PROXY_IP/32" action=allow >/dev/null
incus network set "$SEALED_NET" security.acls="$ACL" >/dev/null ||
    fail "applying the egress ACL to the sealed segment"
sleep 2
if inc exec tbc-m4-egress -- wget -q -T 3 -O /dev/null "http://$SOURCE_IP:8080/v2/" >/dev/null 2>&1; then
    fail "the sealed node reached the production registry directly"
fi
if inc exec tbc-m4-egress -- wget -q -T 3 -O /dev/null "https://$DEST_IP:8080/v2/" >/dev/null 2>&1; then
    fail "the sealed node reached the zone registry directly"
fi
PROXY_ANON=$(curl -s -o /dev/null -w '%{http_code}' -m 10 \
    -x "http://$PROXY_IP:3128" "http://$SOURCE_IP:8080/v2/")
[ "$PROXY_ANON" = "407" ] ||
    fail "the forward proxy answered $PROXY_ANON to an anonymous request, want 407"
check "sealed node: no direct route to either registry, and the proxy refuses anonymous egress (407)"

cat >"$WORK/egress-config.yaml" <<EOF
mode: passthrough
storage:
  root: /var/lib/tobby/store
state:
  root: /var/lib/tobby/state
server:
  addr: :8080
logging:
  level: debug
retriever:
  source: /etc/tobby/retriever.yaml
destination:
  registry: $DEST_IP:8080
  cookbook: cookbook
sync:
  parallelism: 2
  retries: 2
  interval: 0s
network:
  proxy:
    url: http://$PROXY_IP:3128
    username: $PROXY_USER
    noProxy:
      - 127.0.0.1
      - localhost
  tls:
    caFiles:
      - /etc/tobby/ca.crt
registries:
  insecure:
    - $SOURCE_IP:8080
  credentialsFile: /etc/tobby/creds.json
  allowlist:
    - $SOURCE_IP:8080
    - $DEST_IP:8080
trust:
  roots:
    - name: crucible
      key: |
$(sed 's/^/        /' "$WORK/cosign.pub")
EOF
push_node_files tbc-m4-egress
inc file push "$WORK/retriever-101.yaml" tbc-m4-egress/etc/tobby/retriever.yaml
inc file push "$WORK/egress-config.yaml" tbc-m4-egress/etc/tobby/config.yaml
inc exec tbc-m4-egress -- sh -c "printf '%s\n' '$CRED_PASS' |
    tobby user add $CRED_USER --state-root /var/lib/tobby/state --password-stdin" >/dev/null ||
    fail "creating the account on the sealed node"
# The proxy credential arrives through the environment, never the
# configuration file and never a flag: a flag value is readable in the
# process table (FR-080, NFR-015).
inc exec tbc-m4-egress -- sh -c "
    nohup env TOBBY_NETWORK_PROXY_PASSWORD='$PROXY_PASS' \
        tobby serve --config /etc/tobby/config.yaml >>/var/log/tobby.log 2>&1 &
"
wait_ready tbc-m4-egress http://127.0.0.1:8080/readyz
inc exec tbc-m4-egress -- grep -q 'authenticated as tobby' /var/log/tobby.log ||
    fail "the sealed node does not report an authenticated proxy at startup"
inc exec tbc-m4-egress -- grep -q 'trusting 1 configured authorities' /var/log/tobby.log ||
    fail "the sealed node does not report the private authority it was given"
if inc exec tbc-m4-egress -- grep -q "$PROXY_PASS" /var/log/tobby.log; then
    fail "the proxy password appears in the logs (NFR-015)"
fi

EGRESS_API="http://$EGRESS_IP:8080/api/v1"
SEALED_TASK=$(sync_and_wait "$EGRESS_API") || fail "the sealed cycle never settled"
SEALED_JSON=$(curl -fsS -u "$CRED" "$EGRESS_API/tasks/$SEALED_TASK")
printf '%s' "$SEALED_JSON" | jq -e '.task.status == "done"' >/dev/null ||
    fail "the sealed cycle ended $(printf '%s' "$SEALED_JSON" | jq -r '.task.status'), want done"
printf '%s' "$SEALED_JSON" | jq -e '[.task.resolutions[].pushed_bytes // 0] | add > 0' >/dev/null ||
    fail "the sealed cycle pushed nothing through the proxy"
RELOCATED_B="${SOURCE_IP}_8080/library/other"
LANDED_B=$(dest_digest "$RELOCATED_B" 1.0.0)
[ "$LANDED_B" = "$IMG_B" ] ||
    fail "the private-PKI destination holds $LANDED_B, want the pinned $IMG_B"
[ -n "$(dest_digest cookbook/zone-app 1.0.1)" ] ||
    fail "the recipe refused a moment ago did not land once policy allowed it"
inc exec tbc-m4-proxy -- grep -q "CONNECT $DEST_IP:8080" /var/log/proxy.log ||
    fail "the destination was not reached through the proxy's CONNECT tunnel"
inc exec tbc-m4-proxy -- grep -q "$SOURCE_IP:8080" /var/log/proxy.log ||
    fail "the source was not fetched through the proxy"
check "blocked egress: fetched and promoted entirely through the authenticated proxy, over private-PKI TLS"

SEALED_TASK2=$(sync_and_wait "$EGRESS_API") || fail "the second sealed cycle never settled"
SEALED_MOVED=$(curl -fsS -u "$CRED" "$EGRESS_API/tasks/$SEALED_TASK2" |
    jq '[.task.resolutions[].pushed_bytes // 0] | add // 0')
[ "$SEALED_MOVED" = "0" ] ||
    fail "the second sealed cycle pushed $SEALED_MOVED bytes, want 0 (FR-028)"
check "blocked egress, cycle 2: nothing pushed — idempotent behind the proxy too"

# -- Cleanup ------------------------------------------------------------------
for inst in $INSTANCES; do
    inc delete --force "$inst"
done
# The ACL must go before the network that references it.
incus network unset "$SEALED_NET" security.acls >/dev/null 2>&1 || true
incus network acl delete "$ACL" >/dev/null 2>&1 || true
incus network delete "$SEALED_NET" >/dev/null 2>&1 || true
check "scenario m4 complete — report: $REPORT"
