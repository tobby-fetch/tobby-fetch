#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-4 hermetic topology scenario (ADR-0014 tier 1,
# DEVELOPMENT-PLAN §2.4): continuous promotion between two connected
# zones, end to end, through the real surfaces — the four exit criteria
# of the milestone, in one replayable run.
#
#   1. The instance under test is configured by the REFERENCE CHART:
#      `helm template deploy/charts/tobby` renders the Secret, and its
#      config.yaml is what the container reads. The rendered pod's
#      hardening (non-root, read-only root filesystem, no capability, no
#      privilege escalation) is asserted and reproduced by the topology.
#   2. Secure by default: anonymous is refused on the API (RFC 9457) and
#      on the embedded registry (Basic challenge). The documented FR-075
#      override opens a separate instance and puts the permanent danger
#      banner on every page.
#   3. Passthrough: one cycle resolves the recipe, fetches the missing
#      ingredients and pushes them to the zone registry with the recipe
#      itself (FR-034); the SECOND cycle pushes nothing at all (FR-026,
#      FR-028). The reconciliation interval is then changed on the running
#      instance and survives a restart (FR-013).
#   4. A destination that is NOT on the allow-list is refused before any
#      byte moves, with the dedicated taxonomy code on the audit log and
#      on the metrics (FR-030) — and nothing lands at that destination.
#   5. The same passthrough with DIRECT EGRESS BLOCKED: the instance sits
#      on an internal segment whose only neighbour is an authenticated
#      forward proxy (FR-080), and the destination registry presents a
#      certificate issued by a private authority configured in Tobby's
#      trust store (FR-081) — trusted, never bypassed.
#
# What this tier proves and what it does not. The chart is rendered by
# helm and the rendered configuration is what runs, so the chart's
# templating and the settings it produces are exercised for real; a
# kubelet is not involved, and running the chart on a cluster stays the
# job of the RKE2 crucible node of a later milestone (ADR-0014). The
# recipes here are UNSIGNED, admitted by an explicitly declared trust
# scope: the compatibility proof of the real cosign path belongs to the
# crucible tier, which has the tooling (crucible/scenarios/m3, m4).
#
# Requires: docker (compose v2), go, jq, helm, openssl. Run from the
# repository root:
#   sh test/topology/scenario-m4.sh
set -eu

TOPO_DIR="$(dirname "$0")"
COMPOSE="docker compose -f $TOPO_DIR/compose-m4.yaml"
GEN="$TOPO_DIR/.m4"
REPORT="${TOPOLOGY_REPORT:-/tmp/tobby-topology-m4-report.txt}"

SOURCE_PORT=5410
DEST_PORT=5411
DEST_TLS_PORT=5412
DIRECT_PORT=5413
POLICY_PORT=5414
OPEN_PORT=5415

CRED="admin:m4-scenario-pass"
API="http://127.0.0.1:$DIRECT_PORT/api/v1"
POLICY_API="http://127.0.0.1:$POLICY_PORT/api/v1"
# The relocated repository path is computed by the convention (FR-035),
# never written by hand in a configuration: the scenario writes it down
# once, here, precisely so that a change to the convention breaks a check
# instead of quietly passing.
RELOCATED="source-registry_5000/library/sample"
ACCEPT_MANIFEST="application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v2+json"

: >"$REPORT"
step() { printf '%s\n' "$*" | tee -a "$REPORT"; }
cleanup() {
    $COMPOSE down -v >/dev/null 2>&1 || true
    rm -rf "$GEN" "$TOPO_DIR/tobby" "$TOPO_DIR/tobby-proxy"
}
fail() {
    step "FAIL $*"
    cleanup
    exit 1
}

for tool in docker go jq helm openssl; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required by this scenario"
done

step "== milestone-4 topology scenario $(date -u +%Y-%m-%dT%H:%M:%SZ) =="

# -- Build the binaries under test and the topology image --------------------
GOOS=linux CGO_ENABLED=0 go build -trimpath -o "$TOPO_DIR/tobby" ./cmd/tobby ||
    fail "building tobby"
GOOS=linux CGO_ENABLED=0 go build -trimpath -o "$TOPO_DIR/tobby-proxy" ./test/topology/proxy ||
    fail "building the topology forward proxy"
docker build -q -t tobby-topology-m4:local -f "$TOPO_DIR/Dockerfile.m4" "$TOPO_DIR" >/dev/null ||
    fail "building the topology image"
rm -f "$TOPO_DIR/tobby" "$TOPO_DIR/tobby-proxy"
step "ok   build: binary, forward proxy, topology image"

$COMPOSE down -v >/dev/null 2>&1 || true
rm -rf "$GEN"
mkdir -p "$GEN"

# -- 1. Private PKI: an authority of our own, not a disabled check -----------
# FR-081 asks for private authorities to be TRUSTED. The scenario therefore
# issues a real chain: a throwaway root, and a leaf whose subject names are
# the ones the topology actually uses — the service name Tobby dials
# through the proxy, and the loopback address the driver checks from.
cat >"$GEN/leaf.ext" <<'EOF'
basicConstraints = CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:dest-registry-tls, DNS:localhost, IP:127.0.0.1
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
    -keyout "$GEN/ca.key" -out "$GEN/ca.crt" \
    -subj "/CN=Tobby topology private CA" >/dev/null 2>&1 ||
    fail "issuing the private certificate authority"
openssl req -newkey rsa:2048 -nodes \
    -keyout "$GEN/tls.key" -out "$GEN/tls.csr" \
    -subj "/CN=dest-registry-tls" >/dev/null 2>&1 ||
    fail "issuing the destination registry key"
openssl x509 -req -in "$GEN/tls.csr" -days 2 \
    -CA "$GEN/ca.crt" -CAkey "$GEN/ca.key" -CAcreateserial \
    -extfile "$GEN/leaf.ext" -out "$GEN/tls.crt" >/dev/null 2>&1 ||
    fail "signing the destination registry certificate"
chmod 0644 "$GEN/tls.key"
openssl verify -CAfile "$GEN/ca.crt" "$GEN/tls.crt" >/dev/null 2>&1 ||
    fail "the issued certificate does not chain to the private authority"
step "ok   private PKI issued: throwaway root, leaf for dest-registry-tls"

# -- 2. The reference chart renders the instance under test ------------------
helm lint deploy/charts/tobby -f "$TOPO_DIR/values-m4.yaml" >/dev/null ||
    fail "helm lint on the reference chart"
RENDERED="$GEN/rendered.yaml"
helm template tobby-m4 deploy/charts/tobby -f "$TOPO_DIR/values-m4.yaml" >"$RENDERED" ||
    fail "rendering the reference chart"

# The pod hardening the milestone promises (public feature 4.5) is read
# back off the rendered manifest rather than trusted: a chart that stopped
# dropping capabilities would still deploy, and would still pass every
# other check in this file.
for want in \
    'readOnlyRootFilesystem: true' \
    'runAsNonRoot: true' \
    'allowPrivilegeEscalation: false' \
    'path: /healthz' \
    'path: /readyz'; do
    grep -q "$want" "$RENDERED" || fail "the rendered chart lost: $want"
done
grep -A2 'capabilities:' "$RENDERED" | grep -q -- '- ALL' ||
    fail "the rendered chart no longer drops every capability"
# The chart owns storage.root, state.root and server.addr, and the
# topology's volumes are mounted at those very paths: a drift here would
# start an instance whose store is not where its volume is.
grep -q 'root: /var/lib/tobby/store' "$RENDERED" || fail "storage.root is not chart-managed"
grep -q 'root: /var/lib/tobby/state' "$RENDERED" || fail "state.root is not chart-managed"

helm template tobby-m4 deploy/charts/tobby -f "$TOPO_DIR/values-m4.yaml" \
    --show-only templates/secret-config.yaml |
    awk '/^  config\.yaml: \|$/ {found = 1; next} found {sub(/^    /, ""); print}' \
        >"$GEN/chart-config.yaml"
[ -s "$GEN/chart-config.yaml" ] || fail "the chart rendered an empty configuration"
grep -q '^mode: passthrough' "$GEN/chart-config.yaml" ||
    fail "the extracted configuration is not the chart's"
step "ok   reference chart: linted, rendered, hardening asserted, config.yaml extracted"

# -- 3. The desired state and the two hand-written instance configurations ---
cat >"$GEN/retriever.yaml" <<'EOF'
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-b
spec:
  cookbook: source-registry:5000/cookbook
  recipes:
    - name: sample-app
      version: "1.0.0"
EOF

# The allow-list instance: same topology, one difference — the list names
# the source and NOT the destination (FR-030). Its destination is the TLS
# registry, untouched at this point in the run, so "nothing landed" is
# observable rather than inferred.
cat >"$GEN/policy-config.yaml" <<'EOF'
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
  registry: dest-registry-tls:5000
  cookbook: cookbook
sync:
  parallelism: 2
  retries: 1
  interval: 0s
registries:
  insecure:
    - source-registry:5000
  allowlist:
    - source-registry:5000
trust:
  scopes:
    - name: topology-lab
      repositories:
        # Both spellings: see the note in values-m4.yaml — the import side
        # and the pre-push re-verification do not currently agree on the
        # pattern space for a host that carries a port.
        - source-registry_5000/cookbook/*
        - source-registry:5000/cookbook/*
      allowUnsigned: true
EOF

# The blocked-egress instance: every outbound byte through the
# authenticated proxy, and the destination's private authority in the
# trust store. Note what is absent: any way to skip verification. The
# destination is deliberately NOT in registries.insecure — it speaks TLS,
# and it is trusted because its authority is configured.
cat >"$GEN/egress-config.yaml" <<'EOF'
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
  registry: dest-registry-tls:5000
  cookbook: cookbook
sync:
  parallelism: 2
  retries: 2
  interval: 0s
network:
  proxy:
    url: http://proxy:3128
    username: tobby
    noProxy:
      - 127.0.0.1
      - localhost
  tls:
    caFiles:
      - /etc/tobby-ca/ca.crt
registries:
  insecure:
    - source-registry:5000
  allowlist:
    - source-registry:5000
    - dest-registry-tls:5000
trust:
  scopes:
    - name: topology-lab
      repositories:
        # Both spellings: see the note in values-m4.yaml — the import side
        # and the pre-push re-verification do not currently agree on the
        # pattern space for a host that carries a port.
        - source-registry_5000/cookbook/*
        - source-registry:5000/cookbook/*
      allowUnsigned: true
EOF
step "ok   desired state and instance configurations generated"

# -- 4. Registries up, source seeded -----------------------------------------
$COMPOSE up -d source-registry dest-registry dest-registry-tls >/dev/null 2>&1 ||
    $COMPOSE up -d source-registry dest-registry dest-registry-tls ||
    fail "starting the registries"

i=0
until curl -fsS "http://127.0.0.1:$SOURCE_PORT/v2/" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the source registry never came up"
    sleep 0.2
done
i=0
until curl -fsS --cacert "$GEN/ca.crt" "https://127.0.0.1:$DEST_TLS_PORT/v2/" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the private-PKI registry never came up"
    sleep 0.2
done
step "ok   registries up; the private-PKI one verifies against its own authority"

IMG_DIGEST=$(go run ./test/topology/seed push \
    "127.0.0.1:$SOURCE_PORT/library/sample:1.0.0") || fail "seeding the sample image"
cat >"$GEN/sample-app.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: sample-app
  version: 1.0.0
spec:
  ingredients:
    - name: sample
      kind: ContainerImage
      ref: source-registry:5000/library/sample
      version: 1.0.0
      digest: $IMG_DIGEST
EOF
RECIPE_DIGEST=$(go run ./test/topology/seed push-recipe \
    "127.0.0.1:$SOURCE_PORT/cookbook/sample-app:1.0.0" "$GEN/sample-app.yaml") ||
    fail "publishing the recipe into the source cookbook"
step "ok   source zone seeded: image $IMG_DIGEST, recipe $RECIPE_DIGEST"

# -- 5. R-01: no account, no service; then the account, then the service -----
set +e
$COMPOSE run --rm -T tobby-direct serve >/dev/null 2>"$GEN/r01.err"
R01=$?
set -e
[ "$R01" -eq 3 ] || fail "the chart-configured instance started without an account (exit $R01, want 3)"
grep -q "TBY-AUTH-001" "$GEN/r01.err" || fail "the refusal does not carry TBY-AUTH-001"
printf '%s\n' "${CRED#*:}" | $COMPOSE run --rm -T tobby-direct \
    user add "${CRED%%:*}" --state-root /var/lib/tobby/state --password-stdin \
    >/dev/null 2>&1 || fail "creating the account through the CLI"
$COMPOSE up -d tobby-direct >/dev/null 2>&1 || $COMPOSE up -d tobby-direct
i=0
until curl -fsS "http://127.0.0.1:$DIRECT_PORT/readyz" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the chart-configured instance never became ready"
    sleep 0.2
done
step "ok   R-01: no-account start refused (exit 3, TBY-AUTH-001); chart-configured instance ready"

# -- 6. Secure by default: anonymous refused on every surface ----------------
ANON=$(curl -s -o "$GEN/anon.json" -w '%{http_code}' "$API/content")
[ "$ANON" = "401" ] || fail "anonymous API answered $ANON, want 401"
grep -q '"code":"TBY-AUTH-002"' "$GEN/anon.json" ||
    fail "the 401 body is not the RFC 9457 taxonomy document"
curl -s -o /dev/null -D - "http://127.0.0.1:$DIRECT_PORT/v2/" | grep -qi "WWW-Authenticate: Basic" ||
    fail "the embedded registry misses the Basic challenge"
step "ok   unauthenticated requests rejected by default (RFC 9457 + Basic challenge)"

# -- 7. First promotion cycle ------------------------------------------------
sync_and_wait() {
    _api="$1"
    _tid=$(curl -fsS -u "$CRED" -X POST "$_api/sync" | jq -r '.task.id') || return 1
    _i=0
    while :; do
        _status=$(curl -fsS -u "$CRED" "$_api/tasks/$_tid" | jq -r '.task.status')
        case "$_status" in
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
printf '%s' "$CYCLE1" | jq -e '[.task.resolutions[] | select(.destination != null)] | length > 0' >/dev/null ||
    fail "the first cycle promoted nothing (no destination on any resolution)"
printf '%s' "$CYCLE1" | jq -e '[.task.resolutions[].pushed_bytes // 0] | add > 0' >/dev/null ||
    fail "the first cycle pushed zero bytes"
# Fetching and promoting one ingredient are two rows with two outcomes:
# the promotion row is the one carrying a destination.
printf '%s' "$CYCLE1" | jq -e \
    '[.task.resolutions[] | select(.ingredient == "sample" and .destination != null) | .destination]
     | first == "dest-registry:5000/source-registry_5000/library/sample"' >/dev/null ||
    fail "the ingredient did not land under its relocated destination path (FR-035)"
printf '%s' "$CYCLE1" | jq -e \
    '[.task.resolutions[] | select(.destination != null) | .destination]
     | any(. == "dest-registry:5000/cookbook/sample-app:1.0.0")' >/dev/null ||
    fail "the recipe was not promoted into the zone cookbook (FR-034)"

# The check that matters is not what the task says it did: it is what the
# destination registry now holds, read back by a standard OCI client.
go run ./test/topology/seed pull \
    "127.0.0.1:$DEST_PORT/$RELOCATED:1.0.0" "$IMG_DIGEST" >/dev/null ||
    fail "the destination registry does not serve the promoted image at the pinned digest"
curl -fsS -H "Accept: $ACCEPT_MANIFEST" \
    "http://127.0.0.1:$DEST_PORT/v2/cookbook/sample-app/manifests/1.0.0" >/dev/null ||
    fail "the recipe was not re-published in the zone cookbook (FR-034)"
step "ok   cycle 1: recipe resolved, missing artifacts pushed, zone cookbook published"

# -- 8. Second cycle moves nothing (FR-026, FR-028) --------------------------
TASK2=$(sync_and_wait "$API") || fail "the second promotion cycle never settled"
CYCLE2=$(curl -fsS -u "$CRED" "$API/tasks/$TASK2")
printf '%s' "$CYCLE2" | jq -e '.task.status == "done"' >/dev/null ||
    fail "the second cycle did not end done"
MOVED=$(printf '%s' "$CYCLE2" | jq '[.task.resolutions[].pushed_bytes // 0] | add // 0')
[ "$MOVED" = "0" ] || fail "the second cycle pushed $MOVED bytes, want 0 (FR-028)"
printf '%s' "$CYCLE2" | jq -e \
    '[.task.resolutions[] | select(.destination != null) | .destination_status]
     | length > 0 and all(. == "up-to-date")' >/dev/null ||
    fail "the second cycle did not see every destination item up to date"
step "ok   cycle 2: nothing pushed, every destination item already up to date"

# -- 9. The cadence changes on the running instance and survives (FR-013) ----
curl -fsS -u "$CRED" -X PUT -H 'Content-Type: application/json' \
    -d '{"interval":"1m"}' "$API/retriever/interval" >/dev/null ||
    fail "changing the reconciliation interval"
curl -fsS -u "$CRED" "$API/retriever" |
    jq -e '.interval.effective == "1m0s" and .interval.overridden == true' >/dev/null ||
    fail "the new interval is not the effective one"
$COMPOSE restart tobby-direct >/dev/null 2>&1 || fail "restarting the instance"
i=0
until curl -fsS "http://127.0.0.1:$DIRECT_PORT/readyz" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the instance never came back after the restart"
    sleep 0.2
done
curl -fsS -u "$CRED" "$API/retriever" |
    jq -e '.interval.effective == "1m0s" and .interval.overridden == true' >/dev/null ||
    fail "the interval override did not survive the restart (it must outlive the pod)"
curl -fsS -u "$CRED" -X DELETE "$API/retriever/interval" >/dev/null ||
    fail "clearing the interval override"
step "ok   FR-013: interval changed at runtime, persisted across a restart, cleared"

# -- 10. A destination outside the allow-list is refused (FR-030) ------------
printf '%s\n' "${CRED#*:}" | $COMPOSE run --rm -T tobby-policy \
    user add "${CRED%%:*}" --state-root /var/lib/tobby/state --password-stdin \
    >/dev/null 2>&1 || fail "creating the account on the allow-list instance"
$COMPOSE up -d tobby-policy >/dev/null 2>&1 || $COMPOSE up -d tobby-policy
i=0
until curl -fsS "http://127.0.0.1:$POLICY_PORT/readyz" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the allow-list instance never became ready"
    sleep 0.2
done
POLICY_TASK=$(sync_and_wait "$POLICY_API") || fail "the refused cycle never settled"
POLICY_JSON=$(curl -fsS -u "$CRED" "$POLICY_API/tasks/$POLICY_TASK")
printf '%s' "$POLICY_JSON" |
    jq -e '[.task.items[] | select(.error.code == "TBY-POL-001")] | length > 0' >/dev/null ||
    fail "the off-list destination was not refused with TBY-POL-001"
printf '%s' "$POLICY_JSON" | jq -e '[.task.resolutions[].pushed_bytes // 0] | add == 0' >/dev/null ||
    fail "bytes crossed to a destination that is not on the allow-list"
CATALOG=$(curl -fsS --cacert "$GEN/ca.crt" "https://127.0.0.1:$DEST_TLS_PORT/v2/_catalog")
printf '%s' "$CATALOG" | jq -e '.repositories | length == 0' >/dev/null ||
    fail "the off-list destination holds content: $CATALOG"

# The auditable entry FR-030 requires: the dedicated code, the refusal, and
# the destination it was about — on the instance's own structured log.
$COMPOSE logs tobby-policy 2>/dev/null >"$GEN/policy.log" || true
grep -q 'TBY-POL-001' "$GEN/policy.log" || fail "no TBY-POL-001 entry on the audit log"
grep -q 'promotion refused' "$GEN/policy.log" || fail "the refusal is not logged as such"
grep -q 'dest-registry-tls:5000' "$GEN/policy.log" ||
    fail "the log entry does not name the destination it refused"
METRICS=$(curl -fsS -u "$CRED" "http://127.0.0.1:$POLICY_PORT/metrics")
printf '%s' "$METRICS" | awk '
    /^tobby_promotion_refusals_total\{code="TBY-POL-001"\}/ && $2 > 0 { found = 1 }
    END { exit found ? 0 : 1 }' ||
    fail "the refusal was not counted in tobby_promotion_refusals_total"
printf '%s' "$METRICS" | awk '
    /^tobby_policy_rejections_total\{code="TBY-POL-001"\}/ && $2 > 0 { found = 1 }
    END { exit found ? 0 : 1 }' ||
    fail "the refusal was not counted in tobby_policy_rejections_total"
step "ok   FR-030: off-list destination refused before transfer, audited, counted, nothing landed"

# -- 11. The documented FR-075 override, and its permanent banner ------------
$COMPOSE up -d tobby-open >/dev/null 2>&1 || $COMPOSE up -d tobby-open
i=0
until curl -fsS "http://127.0.0.1:$OPEN_PORT/readyz" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the override instance never became ready"
    sleep 0.2
done
OPEN_CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$OPEN_PORT/api/v1/content")
[ "$OPEN_CODE" = "200" ] || fail "the override instance answered $OPEN_CODE anonymously, want 200"
curl -fsS -H 'Accept-Language: en' "http://127.0.0.1:$OPEN_PORT/" >"$GEN/open.html" ||
    fail "the override instance does not serve the UI"
grep -q 'Authentication is disabled on this instance' "$GEN/open.html" ||
    fail "the permanent warning banner is missing (FR-075)"
grep -q 't-banner--danger' "$GEN/open.html" ||
    fail "the override banner is not rendered as a danger banner"
step "ok   FR-075: the documented override opens access and shows the permanent banner"

# -- 12. The same passthrough with direct egress blocked ---------------------
drive() { $COMPOSE run --rm -T driver "$1" 2>/dev/null; }

$COMPOSE up -d proxy >/dev/null 2>&1 || $COMPOSE up -d proxy
# The gap is structural, and proven before anything relies on it: from
# inside the sealed segment, neither registry exists.
if drive "curl -m 5 -fsS http://source-registry:5000/v2/" >/dev/null 2>&1; then
    fail "the sealed segment reached the source registry directly"
fi
if drive "curl -m 5 -fsSk https://dest-registry-tls:5000/v2/" >/dev/null 2>&1; then
    fail "the sealed segment reached the destination registry directly"
fi
PROXY_ANON=$(drive "curl -s -o /dev/null -w '%{http_code}' -m 10 -x http://proxy:3128 http://source-registry:5000/v2/")
[ "$PROXY_ANON" = "407" ] ||
    fail "the forward proxy answered $PROXY_ANON to an anonymous request, want 407"
drive "curl -m 10 -fsS -x http://tobby:proxy-secret@proxy:3128 http://source-registry:5000/v2/" >/dev/null ||
    fail "the authenticated proxy is not a working way out of the sealed segment"
step "ok   sealed segment: no direct route out, the proxy refuses anonymous egress (407)"

printf '%s\n' "${CRED#*:}" | $COMPOSE run --rm -T tobby-egress \
    user add "${CRED%%:*}" --state-root /var/lib/tobby/state --password-stdin \
    >/dev/null 2>&1 || fail "creating the account on the sealed instance"
$COMPOSE up -d tobby-egress >/dev/null 2>&1 || $COMPOSE up -d tobby-egress
i=0
until drive "curl -m 5 -fsS http://tobby-egress:8080/readyz" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the sealed instance never became ready"
    sleep 0.3
done
$COMPOSE logs tobby-egress 2>/dev/null >"$GEN/egress.log" || true
grep -q 'authenticated as tobby' "$GEN/egress.log" ||
    fail "the instance does not report an authenticated proxy at startup"
grep -q 'trusting 1 configured authorities' "$GEN/egress.log" ||
    fail "the instance does not report the private authority it was given"
if grep -q 'proxy-secret' "$GEN/egress.log"; then
    fail "the proxy password appears in the logs (NFR-015)"
fi
step "ok   sealed instance up: proxy authenticated, private authority trusted, credential never printed"

SEALED_TASK=$(drive "curl -m 30 -fsS -u $CRED -X POST http://tobby-egress:8080/api/v1/sync" | jq -r '.task.id')
[ -n "$SEALED_TASK" ] && [ "$SEALED_TASK" != "null" ] || fail "the sealed cycle was not created"
i=0
while :; do
    SEALED_JSON=$(drive "curl -m 30 -fsS -u $CRED http://tobby-egress:8080/api/v1/tasks/$SEALED_TASK")
    SEALED_STATUS=$(printf '%s' "$SEALED_JSON" | jq -r '.task.status')
    case "$SEALED_STATUS" in
    done | failed) break ;;
    esac
    i=$((i + 1)); [ "$i" -gt 200 ] && fail "the sealed cycle never settled (last: $SEALED_STATUS)"
    sleep 0.5
done
[ "$SEALED_STATUS" = "done" ] || fail "the sealed cycle ended $SEALED_STATUS, want done"
printf '%s' "$SEALED_JSON" | jq -e '[.task.resolutions[].pushed_bytes // 0] | add > 0' >/dev/null ||
    fail "the sealed cycle pushed nothing through the proxy"

TLS_DIGEST=$(curl -fsS -o /dev/null -D - --cacert "$GEN/ca.crt" -H "Accept: $ACCEPT_MANIFEST" \
    "https://127.0.0.1:$DEST_TLS_PORT/v2/$RELOCATED/manifests/1.0.0" |
    tr -d '\r' | awk 'tolower($1) == "docker-content-digest:" {print $2}')
[ "$TLS_DIGEST" = "$IMG_DIGEST" ] ||
    fail "the private-PKI destination holds $TLS_DIGEST, want the pinned $IMG_DIGEST"
curl -fsS --cacert "$GEN/ca.crt" -H "Accept: $ACCEPT_MANIFEST" \
    "https://127.0.0.1:$DEST_TLS_PORT/v2/cookbook/sample-app/manifests/1.0.0" >/dev/null ||
    fail "the recipe was not re-published in the private-PKI zone cookbook"

# The proxy is the witness: it logs every destination it was asked to
# reach, so "the traffic went through the proxy" is observed rather than
# assumed.
$COMPOSE logs proxy 2>/dev/null >"$GEN/proxy.log" || true
grep -q 'CONNECT dest-registry-tls:5000' "$GEN/proxy.log" ||
    fail "the destination was not reached through the proxy's CONNECT tunnel"
grep -q 'source-registry:5000' "$GEN/proxy.log" ||
    fail "the source was not fetched through the proxy"
step "ok   blocked egress: fetched and promoted entirely through the authenticated proxy, over private-PKI TLS"

SEALED_TASK2=$(drive "curl -m 30 -fsS -u $CRED -X POST http://tobby-egress:8080/api/v1/sync" | jq -r '.task.id')
i=0
while :; do
    SEALED_JSON2=$(drive "curl -m 30 -fsS -u $CRED http://tobby-egress:8080/api/v1/tasks/$SEALED_TASK2")
    SEALED_STATUS2=$(printf '%s' "$SEALED_JSON2" | jq -r '.task.status')
    case "$SEALED_STATUS2" in
    done | failed) break ;;
    esac
    i=$((i + 1)); [ "$i" -gt 200 ] && fail "the second sealed cycle never settled"
    sleep 0.5
done
[ "$SEALED_STATUS2" = "done" ] || fail "the second sealed cycle ended $SEALED_STATUS2"
SEALED_MOVED=$(printf '%s' "$SEALED_JSON2" | jq '[.task.resolutions[].pushed_bytes // 0] | add // 0')
[ "$SEALED_MOVED" = "0" ] ||
    fail "the second sealed cycle pushed $SEALED_MOVED bytes, want 0 (FR-028)"
step "ok   blocked egress, cycle 2: nothing pushed — idempotent behind the proxy too"

cleanup
step "== scenario m4 PASSED — raw report: $REPORT =="
