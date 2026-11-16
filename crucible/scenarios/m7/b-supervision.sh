#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — the integration and supervision
# kit (R-11; FR-060 and FR-091 amendments), against the chart a site deploys:
#
#   1. The release kit: openapi.yaml and the metrics reference are staged the
#      way the release workflow stages them, signed and verified with cosign,
#      and openapi.yaml is byte-identical to the document the instance
#      serves. (Set CRUCIBLE_RELEASE_DIR to a downloaded release to verify
#      its keyless bundles and compare them with this tree.)
#   2. The reference chart is linted and rendered with the alerting and
#      dashboard values on; the PrometheusRule and the dashboard render, and
#      every expression names only families of the published reference.
#   3. A real promtool runs the unit tests of every alert: each fires when fed
#      its condition and stays silent otherwise.
#   4. A real Prometheus scrapes the instance: every family the reference
#      calls "always present" is there, nothing outside the reference is, the
#      sync families move on a synchronization, the last-success timestamp
#      advances, the certificate-expiry gauge equals the NotAfter of the
#      certificate presented, and it follows a certificate replacement.
#   5. Each alert is driven to fire by its condition — a stale last success,
#      repeated sync failures (source stopped), a store beyond its threshold,
#      a certificate expiring within the bound, an implausible clock (a binary
#      stamped with a build date in the future), an absent vulnerability
#      database — and, but for the database, goes back to inactive when the
#      condition clears. The saturation alert and the "silent" half of each
#      rule are covered by the promtool tests of step 3.
#
# Thresholds are shortened through the chart's own values, so that the
# conditions can be driven in seconds rather than hours.
#
# Topology:
#   tbc-m7b-source     (container, tbc-net)  source registry + cookbook
#   tbc-m7b-node       (container, tbc-net)  the instance
#   tbc-m7b-prometheus (container, tbc-net)  Prometheus with the shipped rules
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-supervision.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-supervision.txt}"
. "$DIR/../../lib.sh"
. "$DIR/b-lib.sh"
. "$DIR/../m6/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m7b-source
NODE=tbc-m7b-node
PROM=tbc-m7b-prometheus
STORE=/var/lib/tobby
cleanup() {
    inc delete --force "$SOURCE" "$NODE" "$PROM" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
b7_tooling
c_tooling
command -v helm >/dev/null 2>&1 || fail "helm not found"
B7_SCHEME=https
ADMIN="admin:supervision-pw-$(openssl rand -hex 6)"

# Two binaries: the normal one, and one stamped with a build date thirty days
# in the future — the clock alert needs a clock earlier than the build, and a
# container cannot set the host's clock.
mkdir -p "$WORK/normal" "$WORK/future"
b7_build "$WORK/normal" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
b7_build "$WORK/future" "$(date -u -d '+30 days' +%Y-%m-%dT%H:%M:%SZ)"
cp "$WORK/normal/tobby" "$WORK/tobby"

# -- 1. The release kit ---------------------------------------------------------------------
mkdir -p "$WORK/kit"
cp "$ROOT/internal/api/openapi.yaml" "$WORK/kit/openapi.yaml"
( cd "$ROOT" && go run ./tools/metricsref -o "$WORK/kit/metrics-reference.json" ) || fail "generating the metrics reference"
cmp "$WORK/kit/metrics-reference.json" "$ROOT/docs/metrics-reference.json" ||
    fail "docs/metrics-reference.json is not what the registry generates"
c_keypair kit
for f in openapi.yaml metrics-reference.json; do
    COSIGN_PASSWORD='' "$COSIGN" sign-blob --yes --key "$WORK/kit.key" --tlog-upload=false \
        --output-signature "$WORK/kit/$f.sig" "$WORK/kit/$f" >/dev/null 2>&1 || fail "signing $f"
    "$COSIGN" verify-blob --key "$WORK/kit.pub" --signature "$WORK/kit/$f.sig" --insecure-ignore-tlog=true \
        "$WORK/kit/$f" >/dev/null 2>&1 || fail "cosign does not verify $f"
    # The negative: a byte changed, the same signature no longer verifies.
    cp "$WORK/kit/$f" "$WORK/kit/$f.altered"
    printf ' ' >>"$WORK/kit/$f.altered"
    if "$COSIGN" verify-blob --key "$WORK/kit.pub" --signature "$WORK/kit/$f.sig" --insecure-ignore-tlog=true \
        "$WORK/kit/$f.altered" >/dev/null 2>&1; then fail "cosign verified an altered $f"; fi
done
if [ -n "${CRUCIBLE_RELEASE_DIR:-}" ]; then
    for f in openapi.yaml metrics-reference.json; do
        "$COSIGN" verify-blob --new-bundle-format --bundle "$CRUCIBLE_RELEASE_DIR/$f.bundle" \
            --certificate-oidc-issuer https://token.actions.githubusercontent.com \
            --certificate-identity-regexp '^https://github.com/tobby-fetch/tobby-fetch/' \
            "$CRUCIBLE_RELEASE_DIR/$f" >/dev/null 2>&1 || fail "cosign does not verify the released $f"
    done
    check "the released openapi.yaml and metrics-reference.json verify keyless against the release workflow's identity"
fi
check "the kit is staged as the release stages it; cosign verifies both files and refuses an altered one"

# -- 2. The chart ----------------------------------------------------------------------------
CHART="$ROOT/deploy/charts/tobby"
SHORT="--set alerting.prometheusRule.enabled=true --set alerting.rulesFile.enabled=true --set dashboard.enabled=true \
 --set alerting.thresholds.syncFailuresWindow=2m --set alerting.thresholds.syncMaxAgeSeconds=60 \
 --set alerting.thresholds.storeOccupancyFor=10s --set alerting.thresholds.certificateExpirySeconds=40000000 \
 --set alerting.thresholds.certificateExpiryFor=10s --set alerting.thresholds.clockFor=10s \
 --set alerting.thresholds.scannerFor=10s"
# shellcheck disable=SC2086 # SHORT is a list of helm words
helm lint "$CHART" $SHORT >/dev/null || fail "helm lint failed with the alerting and dashboard values on"
# shellcheck disable=SC2086
helm template t "$CHART" $SHORT >"$WORK/rendered.yaml" || fail "helm template failed"
grep -q '^kind: PrometheusRule' "$WORK/rendered.yaml" || fail "no PrometheusRule rendered"
grep -q 'grafana_dashboard' "$WORK/rendered.yaml" || fail "no dashboard rendered"
# shellcheck disable=SC2086
helm template t "$CHART" $SHORT -s templates/configmap-alert-rules.yaml |
    awk '/^  tobby-alerts.rules.yaml: \|$/ {on=1; next} on && /^    / {sub(/^    /, ""); print}' >"$WORK/rules-short.yaml"
[ -s "$WORK/rules-short.yaml" ] || fail "the plain rules file did not render"
# shellcheck disable=SC2086
helm template t "$CHART" -s templates/configmap-dashboard.yaml --set dashboard.enabled=true |
    awk '/^  tobby-dashboard.json: \|-$/ {on=1; next} on && /^    / {sub(/^    /, ""); print}' >"$WORK/dashboard.json"
jq -e '.panels | length >= 10' "$WORK/dashboard.json" >/dev/null || fail "the dashboard does not parse or has too few panels"
jq -r '.families[].name' "$WORK/kit/metrics-reference.json" | sort -u >"$WORK/families.txt"
for source in rules-short.yaml dashboard.json; do
    grep -o 'tobby_[a-z0-9_]*' "$WORK/$source" | sort -u | while read -r name; do
        base=$(printf '%s' "$name" | sed 's/_\(bucket\|sum\|count\)$//')
        grep -qx "$name\|$base" "$WORK/families.txt" || { echo "$source names $name" >&2; exit 1; }
    done || fail "an expression names a family outside the published reference"
done
check "the chart lints and renders a PrometheusRule, the plain rules file and the dashboard; every expression names only families of the reference"

# -- 3. promtool, for real ------------------------------------------------------------------------
c_launch "$PROM" tbc-connected-node
inc exec "$PROM" -- apk add --no-cache prometheus >/dev/null 2>&1 || fail "installing Prometheus in the connected zone"
inc exec "$PROM" -- sh -c 'command -v promtool >/dev/null' || fail "this Prometheus package carries no promtool"
inc exec "$PROM" -- mkdir -p /etc/prom/files /etc/prom/tests
inc file push "$CHART/files/tobby-alerts.rules.yaml" "$PROM/etc/prom/files/tobby-alerts.rules.yaml" >/dev/null
inc file push "$CHART/tests/tobby-alerts_test.yaml" "$PROM/etc/prom/tests/tobby-alerts_test.yaml" >/dev/null
inc exec "$PROM" -- promtool check rules /etc/prom/files/tobby-alerts.rules.yaml >"$WORK/promtool-check.txt" 2>&1 ||
    fail "promtool rejects the shipped rules: $(cat "$WORK/promtool-check.txt")"
inc exec "$PROM" -- sh -c 'cd /etc/prom/tests && promtool test rules tobby-alerts_test.yaml' >"$WORK/promtool-test.txt" 2>&1 ||
    fail "the alert unit tests failed: $(cat "$WORK/promtool-test.txt")"
check "promtool accepts the rules and every alert passes its unit tests: fires on its condition, silent otherwise ($(grep -c . "$WORK/promtool-test.txt") lines of output)"

# -- 4. The instance, and Prometheus scraping it ---------------------------------------------------
c_launch "$SOURCE" tbc-connected-node
b7_launch "$NODE" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
PROM_IP=$(instance_ip "$PROM")
c_serve_source "$SOURCE"
c_keypair a
IMG=$(c_seed_image "$SOURCE_IP:8080/library/kit:1.0.0")
c_publish "$SOURCE_IP:8080" kit-app kit "$SOURCE_IP:8080/library/kit" "$IMG" a >/dev/null
cat >"$WORK/retriever.yaml" <<YAML
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-kit
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: kit-app
      version: "1.0.0"
YAML
inc exec "$NODE" -- mkdir -p /etc/tobby "$STORE" /var/lib/tobby-state
inc file push "$WORK/retriever.yaml" "$NODE/etc/tobby/retriever.yaml" >/dev/null
inc file push "$WORK/a.pub" "$NODE/etc/tobby/a.pub" >/dev/null
write_config() {
    # $1: the store occupancy threshold.
    cat >"$WORK/config.yaml" <<YAML
mode: passthrough
zone: zone-kit
storage:
  root: $STORE
  occupancyThreshold: $1
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
  tls:
    enabled: true
sync:
  interval: 0
registries:
  insecure:
    - $SOURCE_IP:8080
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: a
      keyFile: /etc/tobby/a.pub
YAML
    inc file push "$WORK/config.yaml" "$NODE/etc/tobby/config.yaml" >/dev/null
}
write_config 1KiB
b7_user "$NODE" "${ADMIN%%:*}" admin "${ADMIN#*:}"
b7_start "$NODE"

cat >"$WORK/prometheus.yml" <<YAML
global:
  scrape_interval: 5s
  evaluation_interval: 5s
rule_files:
  - /etc/prom/rules.yaml
scrape_configs:
  - job_name: tobby
    scheme: https
    tls_config:
      insecure_skip_verify: true
    basic_auth:
      username: ${ADMIN%%:*}
      password: ${ADMIN#*:}
    static_configs:
      - targets: ["$NODE_IP:8080"]
YAML
inc file push "$WORK/prometheus.yml" "$PROM/etc/prom/prometheus.yml" >/dev/null
inc file push "$WORK/rules-short.yaml" "$PROM/etc/prom/rules.yaml" >/dev/null
inc exec "$PROM" -- sh -c 'mkdir -p /var/lib/prom && nohup prometheus --config.file=/etc/prom/prometheus.yml --storage.tsdb.path=/var/lib/prom --web.listen-address=:9090 >/var/log/prom.log 2>&1 &'
_n=0
until curl -s -f "http://$PROM_IP:9090/-/ready" >/dev/null; do
    _n=$((_n + 1)); [ "$_n" -gt 60 ] && fail "Prometheus never became ready"; sleep 1
done

# prom_q <expr>: the first value of an instant query, empty when there is none.
prom_q() { curl -s -G "http://$PROM_IP:9090/api/v1/query" --data-urlencode "query=$1" | jq -r '.data.result[0].value[1] // empty'; }
# alert_state <alertname>: "firing", "pending" or "inactive".
alert_state() {
    curl -s "http://$PROM_IP:9090/api/v1/alerts" |
        jq -r --arg n "$1" '[.data.alerts[] | select(.labels.alertname == $n) | .state] | (.[0] // "inactive")'
}
# wait_alert <alertname> <firing|inactive> [<seconds>]
wait_alert() {
    _n=0
    until [ "$(alert_state "$1")" = "$2" ]; do
        _n=$((_n + 1))
        [ "$_n" -gt "${3:-150}" ] && fail "alert $1 never became $2 (it is $(alert_state "$1"))"
        sleep 1
    done
}
scrape() { b7_api "$NODE_IP" "$ADMIN" GET /metrics; }

# The OpenAPI document, byte for byte.
b7_api "$NODE_IP" "$ADMIN" GET /api/v1/openapi.yaml >"$WORK/served-openapi.yaml"
cmp "$WORK/served-openapi.yaml" "$WORK/kit/openapi.yaml" || fail "the instance serves an openapi.yaml that differs from the one in the kit"
check "openapi.yaml of the kit is byte-identical to the document the instance serves"

# Every family documented as always present is on the scrape; nothing outside the reference is.
scrape >"$WORK/scrape.txt"
jq -r '.families[] | select(.presence == "always") | .name' "$WORK/kit/metrics-reference.json" | while read -r name; do
    grep -q "^$name" "$WORK/scrape.txt" || { echo "$name" >&2; exit 1; }
done || fail "a family the reference calls always present is missing from the scrape"
grep -o '^tobby_[a-z0-9_]*' "$WORK/scrape.txt" | sort -u | while read -r name; do
    base=$(printf '%s' "$name" | sed 's/_\(bucket\|sum\|count\)$//')
    grep -qx "$name\|$base" "$WORK/families.txt" || { echo "$name" >&2; exit 1; }
done || fail "the instance exposes a family the reference does not list"
for absent in tobby_sync_last_success_timestamp_seconds tobby_sync_ingredients; do
    if grep -q "^$absent" "$WORK/scrape.txt"; then fail "$absent is exposed before any synchronization: a 0 would state a fact nobody measured"; fi
done
check "the scrape holds every always-present family of the reference and nothing outside it; the timestamp and ingredient gauges are absent before a first run"

# A synchronization moves the sync families.
sync_and_wait() {
    _t=$(b7_api "$NODE_IP" "$ADMIN" POST /api/v1/sync '{}' | jq -r '.task.id')
    [ -n "$_t" ] && [ "$_t" != null ] || fail "the synchronization was not accepted"
    _n=0
    while :; do
        _s=$(b7_api "$NODE_IP" "$ADMIN" GET "/api/v1/tasks/$_t" | jq -r '.task.status')
        case "$_s" in done | failed) printf '%s' "$_s"; return 0 ;; esac
        _n=$((_n + 1)); [ "$_n" -gt 120 ] && fail "the synchronization never finished"
        sleep 1
    done
}
[ "$(sync_and_wait)" = "done" ] || fail "the first synchronization failed"
scrape >"$WORK/scrape1.txt"
grep -q '^tobby_sync_runs_total{outcome="success"} 1' "$WORK/scrape1.txt" || fail "tobby_sync_runs_total{success} did not reach 1"
LAST1=$(grep '^tobby_sync_last_success_timestamp_seconds' "$WORK/scrape1.txt" | awk '{printf "%.0f", $2}')
[ -n "$LAST1" ] || fail "no last-success timestamp after a successful run"
grep -q '^tobby_sync_ingredients{status="new"}' "$WORK/scrape1.txt" || fail "no ingredient gauge after a run"
grep -q '^tobby_sync_duration_seconds_count{outcome="success"} 1' "$WORK/scrape1.txt" || fail "the duration histogram did not count the run"
sleep 2
[ "$(sync_and_wait)" = "done" ] || fail "the second synchronization failed"
LAST2=$(scrape | grep '^tobby_sync_last_success_timestamp_seconds' | awk '{printf "%.0f", $2}')
[ "$LAST2" -gt "$LAST1" ] || fail "the last-success timestamp did not advance ($LAST1 -> $LAST2)"
scrape | grep -q '^tobby_sync_ingredients{status="up_to_date"} [1-9]' || fail "the second run did not count its ingredient as up to date"
check "a synchronization moves the sync families: runs, duration, ingredients; the last-success timestamp advances ($LAST1 -> $LAST2)"

# The certificate-expiry gauge equals the NotAfter of the certificate presented.
not_after() {
    openssl s_client -connect "$NODE_IP:8080" </dev/null 2>/dev/null | openssl x509 -noout -enddate | cut -d= -f2 |
        { read -r d; date -u -d "$d" +%s; }
}
gauge_cert() { scrape | grep '^tobby_server_certificate_expiry_timestamp_seconds' | awk '{printf "%.0f", $2}'; }
[ "$(gauge_cert)" = "$(not_after)" ] || fail "the certificate gauge ($(gauge_cert)) is not the NotAfter presented ($(not_after))"
check "tobby_server_certificate_expiry_timestamp_seconds equals the NotAfter of the certificate presented"

# -- 5. Each alert, driven ----------------------------------------------------------------------------
# 5a. Stale: no success for longer than 60 s.
sleep 70
wait_alert TobbySyncStale firing 60
[ "$(sync_and_wait)" = "done" ] || fail "the synchronization failed"
wait_alert TobbySyncStale inactive 60
check "TobbySyncStale: fires after 60 s without a success, inactive again on the next success"

# 5b. Repeated failures: stop the source, fail three runs, then restore it.
c_stop "$SOURCE"
for _i in 1 2 3; do
    [ "$(sync_and_wait)" = failed ] || fail "a synchronization against a stopped source did not fail"
done
scrape | grep -q '^tobby_sync_runs_total{outcome="failure"} [3-9]' || fail "the failure counter did not follow"
wait_alert TobbySyncFailing firing 60
c_serve_source "$SOURCE"
[ "$(sync_and_wait)" = "done" ] || fail "the synchronization did not recover when the source came back"
wait_alert TobbySyncFailing inactive 240
check "TobbySyncFailing: fires on three failed runs, inactive again once the 2-minute window has passed"

# 5c. The store beyond its (1 KiB) threshold, then a threshold it fits.
wait_alert TobbyStoreOccupancyHigh firing 90
b7_stop "$NODE"
write_config 100GiB
b7_start "$NODE"
wait_alert TobbyStoreOccupancyHigh inactive 90
check "TobbyStoreOccupancyHigh: fires with the store over its threshold, inactive once the threshold is raised"

# 5d. A certificate expiring within the bound (the generated one lasts a year,
# the bound is 463 days), then a five-year pair.
wait_alert TobbyServerCertificateExpiring firing 90
openssl req -x509 -newkey rsa:2048 -nodes -days 1825 -subj "/CN=tbc-m7b-node" \
    -addext "subjectAltName=IP:127.0.0.1,IP:$NODE_IP" -keyout "$WORK/new.key" -out "$WORK/new.crt" >/dev/null 2>&1 ||
    fail "generating the replacement pair"
jq -n --rawfile c "$WORK/new.crt" --rawfile k "$WORK/new.key" '{certificate: $c, key: $k}' >"$WORK/pair.json"
[ "$(curl -sk -o /dev/null -w '%{http_code}' -u "$ADMIN" -X PUT -H 'Content-Type: application/json' \
    --data-binary @"$WORK/pair.json" "https://$NODE_IP:8080/api/v1/network/certificate")" = 200 ] ||
    fail "the certificate replacement was refused"
NEW_NOT_AFTER=$(not_after)
[ "$(gauge_cert)" = "$NEW_NOT_AFTER" ] || fail "the gauge did not follow the replacement ($(gauge_cert) vs $NEW_NOT_AFTER)"
wait_alert TobbyServerCertificateExpiring inactive 90
check "TobbyServerCertificateExpiring: fires within the bound; the gauge follows a replacement and the alert goes inactive"

# 5e. An implausible clock: the binary stamped thirty days ahead.
b7_stop "$NODE"
inc file push "$WORK/future/tobby" "$NODE/usr/bin/tobby" >/dev/null
inc exec "$NODE" -- chmod +x /usr/bin/tobby
b7_start "$NODE"
scrape | grep -q '^tobby_clock_implausible{anchor="build"} 1' || fail "a binary built in the future did not raise the clock metric"
wait_alert TobbyClockImplausible firing 90
b7_stop "$NODE"
inc file push "$WORK/normal/tobby" "$NODE/usr/bin/tobby" >/dev/null
inc exec "$NODE" -- chmod +x /usr/bin/tobby
b7_start "$NODE"
wait_alert TobbyClockImplausible inactive 90
check "TobbyClockImplausible: fires for a build in the future, inactive with the normal binary"

# 5f. The vulnerability database: this instance was never delivered one.
if scrape | grep -q '^tobby_scan_db_present 0'; then
    wait_alert TobbyVulnerabilityDatabaseAbsent firing 90
    check "TobbyVulnerabilityDatabaseAbsent: fires with no database (clearing it needs m6's delivery fixtures and is covered by the promtool test)"
else
    fail "this instance reports a database it was never given: tobby_scan_db_present is not 0"
fi
