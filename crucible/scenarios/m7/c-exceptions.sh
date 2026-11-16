#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track C — scoped and bounded vulnerability
# exceptions (FR-031 amendment 2026-11-16, R-15), under a blocking policy,
# with the pinned scanner and a database carried by the tobby-system channel,
# on an instance with authentication ON:
#
#   1. An exception without expires, or without a justification, is a
#      configuration error: the instance refuses to start, naming the entry.
#      So does a repository scope not written in canonical form, and a
#      malformed OpenVEX document (TBY-SCN-027, naming the file).
#   2. Two images carry the same known CVE (CVE-2019-14697): A, and B — the
#      same filesystem under another manifest, hence another digest. An
#      exception scoped to A's digest admits A; B is refused, and its report
#      names the exception as out of scope (TBY-SCN-026), as does its log.
#   3. Scopes by source repository (canonical form, the port written "_",
#      a glob) and by recipe (name, name@version) behave the same way.
#   4. The exception's expiry passes (the scenario moves time with a short
#      expiry, never the clock): the exception gauge turns to expired on the
#      running process before any evaluation, then the next synchronization
#      refuses A again — the report names the expired exception
#      (TBY-SCN-021), a scan.exception_expired audit event and the reblock
#      counter record it.
#   5. A local OpenVEX document with a justified not_affected statement for
#      A's digest admits A, the report names the document and its @id; a
#      statement with an impact statement only admits nothing.
#   6. A Retriever exception without expiry applies and is reported
#      unbounded (TBY-SCN-028) in the report, the log and the policy view.
#   7. The effective policy: GET /api/v1/policy with an admin token (a
#      viewer token is refused, TBY-AUTH-003, like the /admin/policy
#      screen), and `tobby scan exceptions` offline (configuration) and
#      --instance with the admin token.
#
# Every step checks its own manipulation before the verdict it prepares is
# read: B is proved to carry A's filesystem under another digest, each
# image is proved to carry the CVE in its own report, each configuration
# and document is proved to be on the node byte for byte, the expiry is
# proved past on the node's clock and the process proved not restarted.
#
# Topology:
#   tbc-m7c-source (container, tbc-net)  source registry, cookbook, channel
#   tbc-m7c-node   (container, tbc-net)  instance under test (mirror, auth on)
#   the crucible host                    API client, OCI client
#
# Host tooling on top of the crucible baseline: oras (the database the
# connected zone publishes is fetched with it) and the pinned Trivy release,
# from CRUCIBLE_TRIVY_ARCHIVE or fetched once and checked (as m6 a-scanning).
# M7C_EXPIRY_MARGIN (seconds, default 180) is how far ahead the short
# expiry of step 4 is set: a restart and a synchronization must fit in it.
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/c-exceptions.sh

# The jq programs are single-quoted on purpose: their $names are jq
# variables, bound with --arg.
# shellcheck disable=SC2016
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-c-exceptions.txt}"
# shellcheck source=../../lib.sh
. "$DIR/../../lib.sh"
# shellcheck source=c-lib.sh
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m7c-source
NODE=tbc-m7c-node
STORE=/var/lib/tobby
LOG=/var/log/tobby.log
ADMIN=root
ADMIN_PW="m7c-crucible-admin-pw"
CVE="CVE-2019-14697"
MARGIN="${M7C_EXPIRY_MARGIN:-180}"
VEX_DIR=/etc/tobby/vex
cleanup() {
    inc delete --force "$SOURCE" "$NODE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
m7_tooling oras
m7_build

# -- 0. Nodes and fixtures --------------------------------------------------------------
m7_launch "$SOURCE" tbc-connected-node
m7_launch "$NODE" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
S="$SOURCE_IP:8080"
# The canonical form of a repository on the source (ADR-0013): the port's
# colon is written "_".
CANON=$(printf '%s' "$S" | tr ':' '_')
m7_serve_source "$SOURCE"

m7_scanner_fixtures "$S"
m7_keypair

# Image A: the known-vulnerable image of m6 (Alpine 3.10.0, musl 1.1.22-r2).
REF_A="$S/team-a/vuln"
REF_B="$S/team-b/vuln"
VA=$(m7_seed push-vulnerable "$REF_A:1.0") || fail "seeding image A"
# Image B: the very same filesystem under another manifest — A's manifest
# with one annotation added — so it carries the same CVE under another
# digest. The seeder is deterministic (it would give B A's digest), so the
# variant is written by hand through the registry API, and proved.
m7_seed push-vulnerable "$REF_B:base" >/dev/null || fail "seeding image B's blobs"
curl -fsS -H 'Accept: application/vnd.oci.image.manifest.v1+json' -o "$WORK/manifest-a.json" \
    "http://$S/v2/team-b/vuln/manifests/$VA" || fail "reading A's manifest back from team-b/vuln"
[ "sha256:$(sha256sum <"$WORK/manifest-a.json" | cut -d' ' -f1)" = "$VA" ] ||
    fail "staging: the manifest read back is not A's ($VA)"
jq -cj '. + {annotations: {"org.opencontainers.image.description": "m7c image B: the filesystem of image A"}}' \
    "$WORK/manifest-a.json" >"$WORK/manifest-b.json"
VB="sha256:$(sha256sum <"$WORK/manifest-b.json" | cut -d' ' -f1)"
CODE=$(curl -s -o /dev/null -D "$WORK/put.headers" -w '%{http_code}' -X PUT \
    -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
    --data-binary @"$WORK/manifest-b.json" "http://$S/v2/team-b/vuln/manifests/1.0")
[ "$CODE" = "201" ] || fail "pushing image B's manifest answered $CODE"
GOT=$(tr -d '\r' <"$WORK/put.headers" | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}')
[ "$GOT" = "$VB" ] || fail "staging: the registry stored image B as '$GOT', not $VB"
curl -fsS -H 'Accept: application/vnd.oci.image.manifest.v1+json' -o "$WORK/manifest-b.back" \
    "http://$S/v2/team-b/vuln/manifests/1.0" || fail "reading image B back"
[ "sha256:$(sha256sum <"$WORK/manifest-b.back" | cut -d' ' -f1)" = "$VB" ] ||
    fail "staging: team-b/vuln:1.0 does not serve image B"
[ "$VA" != "$VB" ] || fail "staging: images A and B share a digest"
[ "$(jq -cS '{config, layers}' "$WORK/manifest-a.json")" = "$(jq -cS '{config, layers}' "$WORK/manifest-b.json")" ] ||
    fail "staging: image B does not carry image A's config and layers"
check "fixtures: image A $VA ($REF_A) and image B $VB ($REF_B) — same config and layers, two digests, B served back by tag"

m7_publish "$S" tobby-system "$(m7_system_recipe)" sign >/dev/null
m7_publish "$S" vuln-a "$(m7_image_recipe vuln-a "$REF_A" "$VA")" sign >/dev/null
m7_publish "$S" vuln-b "$(m7_image_recipe vuln-b "$REF_B" "$VB")" sign >/dev/null
check "fixtures: tobby-system, vuln-a and vuln-b published and signed (cosign verifies each under the zone key)"

inc file push "$WORK/cosign.pub" "$NODE/etc/tobby/trust.pub" --create-dirs >/dev/null
inc exec "$NODE" -- mkdir -p "$VEX_DIR"

# -- helpers ------------------------------------------------------------------------------
BASE="http://$NODE_IP:8080"
API="$BASE/api/v1"
A_SCAN=vuln-a@1.0.0/app
A_RECIPE=vuln-a@1.0.0/recipe
B_SCAN=vuln-b@1.0.0/app
B_RECIPE=vuln-b@1.0.0/recipe
FAR=$(date -u -d '+30 days' +%Y-%m-%dT%H:%M:%SZ)

# on_node <host file> <node path>: pushes a file and proves it landed byte
# for byte — a configuration or a document the node does not hold would
# make every verdict after it a verdict on something else.
on_node() {
    inc file push "$1" "$NODE$2" >/dev/null || fail "pushing $2 to the node"
    [ "$(sha256sum <"$1" | cut -d' ' -f1)" = "$(inc exec "$NODE" -- sha256sum "$2" | cut -d' ' -f1)" ] ||
        fail "staging: $2 on the node is not the file written"
}

# node_config <scan lines>: the configuration of the instance under test, a
# blocking policy and the given scan lines (two-space indented, under scan:).
node_config() {
    cat >"$WORK/config.yaml" <<EOF
mode: mirror
storage:
  root: $STORE
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
registries:
  insecure:
    - $S
  substitutions:
    ghcr.io: $S
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: zone
      keyFile: /etc/tobby/trust.pub
scan:
  mode: block
$1
EOF
    on_node "$WORK/config.yaml" /etc/tobby/config.yaml
}

# exception <id> <expires|""> <justification|""> [<scope key> <scope value>]:
# a scan.exceptions list of one entry; an empty field is left out.
exception() {
    printf '  exceptions:\n    - id: %s\n' "$1"
    if [ -n "$3" ]; then printf '      justification: %s\n' "$3"; fi
    if [ -n "$2" ]; then printf '      expires: %s\n' "$2"; fi
    if [ $# -ge 5 ]; then printf '      scope:\n        %s: "%s"\n' "$4" "$5"; fi
}

# retriever [unbounded]: tobby-system first (it arms the scanner in the
# same run), then the two images; "unbounded" adds a Retriever exception
# for the CVE without expiry (the v1alpha1 format allows it).
retriever() {
    m7_retriever "$WORK/retriever.yaml" "$S/cookbook" tobby-system vuln-a vuln-b
    if [ "${1:-}" = unbounded ]; then
        printf '  scanPolicy:\n    exceptions:\n      - id: %s\n        justification: accepted for the zone, no expiry (m7c)\n' \
            "$CVE" >>"$WORK/retriever.yaml"
    fi
    on_node "$WORK/retriever.yaml" /etc/tobby/retriever.yaml
}

# The log since a mark: LOG0 is set by restart (startup records) and by
# sync_run (records of the run). logrec <jq condition> [<jq args>…] prints
# the matching JSON records, one per line.
log_mark() { LOG0=$(inc exec "$NODE" -- sh -c "wc -l <$LOG 2>/dev/null || echo 0" | tr -d ' '); }
logrec() {
    _c="$1"; shift
    inc exec "$NODE" -- sh -c "tail -n +$((LOG0 + 1)) $LOG" |
        jq -Rc --arg cve "$CVE" "$@" "fromjson? | select($_c)"
}

restart() {
    m7_stop "$NODE"
    log_mark
    m7_start "$NODE" "$BASE"
    PID=$(inc exec "$NODE" -- sh -c 'pgrep -f "[t]obby serve"')
}

# refused <label>: tobby serve, with the configuration in place, must exit
# by itself before the bound — a start that is not refused runs until the
# timeout kills it (124 or 143). The output is left in $WORK/refused.out.
refused() {
    m7_stop "$NODE"
    _rc=$(inc exec "$NODE" -- sh -c 'timeout 60 tobby serve >/tmp/m7c-refused.out 2>&1; echo $?' || true)
    inc exec "$NODE" -- cat /tmp/m7c-refused.out >"$WORK/refused.out" 2>/dev/null || true
    case "$_rc" in
        "" | 0 | 124 | 143) fail "$1: tobby serve was not refused (exit '$_rc'): $(tail -5 "$WORK/refused.out")" ;;
    esac
}
said() { grep -qF -- "$1" "$WORK/refused.out"; }

api() { curl -s -H "$AUTHZ" "$@"; }
policy() { curl -fsS -H "Authorization: Bearer $TOKEN" "$API/policy" >"$WORK/policy.json" || fail "GET /api/v1/policy as an administrator failed"; }
polq() { _f="$1"; shift; jq -e --arg cve "$CVE" "$@" "$_f" "$WORK/policy.json" >/dev/null; }

# sync_run: one synchronization through the API, waited for; a blocking
# policy refusing B makes the task fail, so done and failed both settle it.
sync_run() {
    log_mark
    _id=$(api -X POST -H 'Content-Type: application/json' -d '{"prune":false}' "$API/sync" | jq -r '.task.id // empty')
    [ -n "$_id" ] || fail "the API did not start a synchronization"
    SYNC_ST=$(m7_wait_task "$API" "$_id" "$WORK/sync.json" -H "$AUTHZ")
    SYNC_END=$(date -u +%s)
    case "$SYNC_ST" in done | failed) ;; *) fail "the synchronization ended $SYNC_ST: $(jq -c '.task' "$WORK/sync.json")" ;; esac
    itemq tobby-system@1.0.0/recipe '.status == "done" or .status == "skipped"' ||
        fail "staging: tobby-system did not cross: $(show tobby-system@1.0.0/recipe)"
}
# itemq <item> <jq condition> [<jq args>…]: the condition holds on that item.
itemq() {
    _n="$1"; _f="$2"; shift 2
    jq -e --arg n "$_n" --arg cve "$CVE" "$@" ".task.items[] | select(.name == \$n) | $_f" "$WORK/sync.json" >/dev/null
}
show() {
    jq -c --arg n "$1" '.task.items[] | select(.name == $n) | {status, error,
        verdict: .scan.evaluation.verdict, violations: .scan.evaluation.violations,
        reasons: .scan.evaluation.reasons, applied: .scan.evaluation.exceptions_applied,
        expired: .scan.evaluation.exceptions_expired, out_of_scope: .scan.evaluation.exceptions_out_of_scope}' "$WORK/sync.json"
}
# carries <item>: the item's own report lists the CVE among its findings,
# and among its violations unless an exception covered it — the proof that
# the image under judgement really carries it.
carries() {
    itemq "$1" '[.scan.findings[]?.id] | index($cve) != null' ||
        fail "staging: $1 was not found to carry $CVE: $(show "$1")"
}
# refused_for_cve <item> <recipe item>: refused on the CVE, the recipe withheld.
refused_for_cve() {
    carries "$1"
    itemq "$1" '.scan.evaluation.verdict == "block" and (.scan.evaluation.violations | index($cve) != null)' ||
        fail "FR-031: $1 is not refused for $CVE: $(show "$1")"
    itemq "$2" '.error.code == "TBY-SCN-005"' || fail "FR-031: $2 was not withheld with TBY-SCN-005: $(show "$2")"
}
# admitted <item> <recipe item>: carries the CVE, passes, the recipe crosses.
admitted() {
    carries "$1"
    itemq "$1" '.scan.evaluation.verdict == "pass" and ((.scan.evaluation.violations // []) | length == 0)' ||
        fail "R-15: $1 was not admitted: $(show "$1")"
    itemq "$2" '.status == "done" or .status == "skipped"' || fail "R-15: $2 did not cross: $(show "$2")"
}

# exc_metric <source> <state>: tobby_scan_exceptions for that pair, whatever
# the label order.
exc_metric() {
    curl -fsS "$BASE/metrics" | awk -v s="source=\"$1\"" -v t="state=\"$2\"" \
        'index($1, "tobby_scan_exceptions{") == 1 && index($1, s) && index($1, t) {print $2; exit}'
}
reblocked_total() { m7_metric "$(curl -fsS "$BASE/metrics")" tobby_scan_reblocked_items_total; }

# -- 1. Start refusals -------------------------------------------------------------------------
# A valid configuration first: the local administrator is created against it
# (the scan section is validated at every scope, `tobby user add` included).
retriever
node_config ""
if inc exec "$NODE" -- grep -q 'disabled: true' /etc/tobby/config.yaml; then
    fail "authentication is disabled on the instance under test"
fi
m7_admin "$NODE" "$ADMIN" "$ADMIN_PW"

node_config "$(exception "$CVE" "" "accepted risk without an end (m7c)")"
if ! inc exec "$NODE" -- grep -q "id: $CVE" /etc/tobby/config.yaml || inc exec "$NODE" -- grep -q 'expires:' /etc/tobby/config.yaml; then
    fail "staging: the configuration does not hold an exception without expires"
fi
refused "an exception without expires"
said "scan.exceptions[0] ($CVE): expires is required" ||
    fail "R-15: the refusal does not name the entry without expires: $(cat "$WORK/refused.out")"
check "R-15: an exception without expires refuses the start, naming scan.exceptions[0] ($CVE) and the remedy"

node_config "$(exception "$CVE" "$FAR" "")"
if ! inc exec "$NODE" -- grep -q 'expires:' /etc/tobby/config.yaml || inc exec "$NODE" -- grep -q 'justification:' /etc/tobby/config.yaml; then
    fail "staging: the configuration does not hold an exception without justification"
fi
refused "an exception without justification"
said "scan.exceptions[0] ($CVE): justification is required" ||
    fail "R-15: the refusal does not name the entry without justification: $(cat "$WORK/refused.out")"
check "R-15: an exception without justification refuses the start, naming scan.exceptions[0] ($CVE)"

# The repository scope is matched in canonical form: the host:port form a
# recipe writes is not a pattern that could ever match, and is refused.
node_config "$(exception "$CVE" "$FAR" "colon form (m7c)" repository "$S/team-a/*")"
refused "a repository scope with host:port"
if ! said "scan.exceptions[0].scope.repository:" || ! said "canonical form"; then
    fail "R-15: a repository scope with a colon was not refused as non-canonical: $(cat "$WORK/refused.out")"
fi
check "R-15: a repository scope written host:port refuses the start (canonical form required, port written _)"

# A malformed OpenVEX document: well-formed JSON, valid OpenVEX but for one
# field the specification does not define.
jq -n --arg id "https://vex.example/m7c/malformed" --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg cve "$CVE" --arg d "$VA" '{
    "@context": "https://openvex.dev/ns/v0.2.0", "@id": $id, author: "m7c crucible", timestamp: $ts, version: 1,
    statements: [{vulnerability: {name: $cve}, products: [{"@id": $d}], status: "not_affected",
                  justification: "vulnerable_code_not_in_execute_path"}],
    comment: "not an OpenVEX field"}' >"$WORK/malformed.json"
jq -e '.comment and (.statements | length == 1)' "$WORK/malformed.json" >/dev/null || fail "staging: the malformed document is not the one meant"
on_node "$WORK/malformed.json" "$VEX_DIR/malformed.json"
node_config "  vex:
    - path: $VEX_DIR/malformed.json
      expires: $FAR"
refused "a malformed OpenVEX document"
if ! said "TBY-SCN-027" || ! said "$VEX_DIR/malformed.json"; then
    fail "R-15: the malformed document did not refuse the start with TBY-SCN-027 naming it: $(cat "$WORK/refused.out")"
fi
said 'unknown field "comment"' || fail "R-15: the refusal does not say what is wrong in the document: $(cat "$WORK/refused.out")"
check "R-15: a malformed OpenVEX document refuses the start (TBY-SCN-027, the path, the unknown field)"

# -- 2. Scope by digest -------------------------------------------------------------------------
node_config "$(exception "$CVE" "$FAR" "image A only (m7c digest scope)" digest "$VA")"
restart
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/policy")
[ "$CODE" = "401" ] || fail "an anonymous API call answered $CODE: authentication is not on"
TOKEN=$(curl -fsS -u "$ADMIN:$ADMIN_PW" -H 'Content-Type: application/json' \
    -d '{"name":"m7c-admin","role":"admin"}' "$API/tokens" | jq -r '.secret // empty')
case "$TOKEN" in tby_*) ;; *) fail "creating the admin API token failed" ;; esac
VIEWER=$(curl -fsS -u "$ADMIN:$ADMIN_PW" -H 'Content-Type: application/json' \
    -d '{"name":"m7c-viewer","role":"viewer"}' "$API/tokens" | jq -r '.secret // empty')
case "$VIEWER" in tby_*) ;; *) fail "creating the viewer API token failed" ;; esac
AUTHZ="Authorization: Bearer $TOKEN"
check "instance under test: mirror, blocking, authentication on (anonymous 401), admin and viewer tokens issued"

sync_run
METRICS=$(curl -fsS "$BASE/metrics")
[ "$(m7_metric "$METRICS" tobby_scan_scanner_available)" = "1" ] || fail "staging: the channel-delivered scanner is not available"
[ "$(m7_metric "$METRICS" tobby_scan_db_present)" = "1" ] || fail "staging: the delivered database is not present"
admitted "$A_SCAN" "$A_RECIPE"
itemq "$A_SCAN" 'any(.scan.evaluation.exceptions_applied[]?; .id == $cve and .level == "instance" and .scope.digest == $d)' --arg d "$VA" ||
    fail "R-15: A's report does not name the digest-scoped exception it was admitted by: $(show "$A_SCAN")"
refused_for_cve "$B_SCAN" "$B_RECIPE"
itemq "$B_SCAN" 'any(.scan.evaluation.reasons[]?; .code == "TBY-SCN-026" and .params.id == $cve and .params.scope == $s and .params.reference == $r)' \
    --arg s "digest=$VA" --arg r "$REF_B@$VB" ||
    fail "R-15: B's report does not name the exception as out of scope (TBY-SCN-026, scope digest=$VA, reference $REF_B@$VB): $(show "$B_SCAN")"
itemq "$B_SCAN" 'any(.scan.evaluation.exceptions_out_of_scope[]?; .id == $cve and .scope.digest == $d)' --arg d "$VA" ||
    fail "R-15: B's report does not list the exception under exceptions_out_of_scope: $(show "$B_SCAN")"
[ -n "$(logrec '.digest == $d and .msg == "ingredient refused by the vulnerability policy" and .exceptions_out_of_scope == $cve' --arg d "$VB")" ] ||
    fail "R-15: B's refusal is not logged with exceptions_out_of_scope=$CVE"
[ -n "$(logrec '.digest == $d and .exceptions_applied == $cve and .verdict == "pass"' --arg d "$VA")" ] ||
    fail "R-15: A's scan is not logged with exceptions_applied=$CVE"
check "R-15 digest scope: A (carries $CVE) admitted by the exception scoped to its digest; B (carries $CVE) refused (TBY-SCN-005), the exception named out of scope (TBY-SCN-026, scope digest=$VA, reference $REF_B@$VB) in its report and its log"

# -- 3. Scope by repository and by recipe ----------------------------------------------------------
# scope_case <label> <scope key> <scope value>: A admitted, B refused out of
# scope, under an exception with that one criterion.
scope_case() {
    node_config "$(exception "$CVE" "$FAR" "image A only (m7c $1 scope)" "$2" "$3")"
    restart
    sync_run
    admitted "$A_SCAN" "$A_RECIPE"
    itemq "$A_SCAN" 'any(.scan.evaluation.exceptions_applied[]?; .id == $cve and .scope[$k] == $v)' --arg k "$2" --arg v "$3" ||
        fail "R-15: A's report does not name the $1-scoped exception: $(show "$A_SCAN")"
    refused_for_cve "$B_SCAN" "$B_RECIPE"
    itemq "$B_SCAN" 'any(.scan.evaluation.reasons[]?; .code == "TBY-SCN-026" and .params.id == $cve and .params.scope == $s and .params.reference == $r)' \
        --arg s "$2=$3" --arg r "$REF_B@$VB" ||
        fail "R-15: B's report does not name the $1-scoped exception as out of scope ($2=$3): $(show "$B_SCAN")"
    itemq "$B_SCAN" 'any(.scan.evaluation.exceptions_out_of_scope[]?; .id == $cve and .scope[$k] == $v)' --arg k "$2" --arg v "$3" ||
        fail "R-15: B's report does not list the $1-scoped exception under exceptions_out_of_scope: $(show "$B_SCAN")"
    [ -n "$(logrec '.digest == $d and .exceptions_out_of_scope == $cve' --arg d "$VB")" ] ||
        fail "R-15: B's refusal under the $1 scope is not logged with exceptions_out_of_scope=$CVE"
    check "R-15 $1 scope ($2: $3): A admitted, B refused with the exception named out of scope (TBY-SCN-026) in its report and its log"
}
scope_case repository repository "$CANON/team-a/*"
scope_case recipe recipe vuln-a
scope_case "recipe@version" recipe vuln-a@1.0.0

# A version that is not A's: the exception is out of scope for A too.
node_config "$(exception "$CVE" "$FAR" "another version of vuln-a (m7c)" recipe vuln-a@9.9.9)"
restart
sync_run
refused_for_cve "$A_SCAN" "$A_RECIPE"
itemq "$A_SCAN" 'any(.scan.evaluation.reasons[]?; .code == "TBY-SCN-026" and .params.scope == "recipe=vuln-a@9.9.9" and .params.reference == $r)' \
    --arg r "$REF_A@$VA" || fail "R-15: A is not refused with the exception for vuln-a@9.9.9 out of scope: $(show "$A_SCAN")"
check "R-15 recipe@version scope: an exception for vuln-a@9.9.9 does not admit vuln-a@1.0.0 (TBY-SCN-026)"

# -- 4. Expiry and re-blocking ---------------------------------------------------------------------
# The time moves through a short expiry, never through the clock: the
# exception expires MARGIN seconds from now, a synchronization runs before,
# another after.
EXP_EPOCH=$(($(date -u +%s) + MARGIN))
EXPIRES=$(date -u -d "@$EXP_EPOCH" +%Y-%m-%dT%H:%M:%SZ)
node_config "$(exception "$CVE" "$EXPIRES" "image A until $EXPIRES (m7c expiry)" digest "$VA")"
restart
PID_START=$PID
[ "$(exc_metric instance active)/$(exc_metric instance expired)" = "1/0" ] ||
    fail "R-15: before the expiry the gauge does not count one active instance exception: active=$(exc_metric instance active) expired=$(exc_metric instance expired)"
[ "$(reblocked_total)" = "0" ] || fail "staging: the reblock counter is not 0 on a fresh process"
sync_run
[ "$SYNC_END" -lt $((EXP_EPOCH - 5)) ] ||
    fail "staging: the synchronization before the expiry ended at $SYNC_END, the exception expires at $EXP_EPOCH: raise M7C_EXPIRY_MARGIN"
admitted "$A_SCAN" "$A_RECIPE"
itemq "$A_SCAN" 'any(.scan.evaluation.exceptions_applied[]?; .id == $cve and .expires == $e)' --arg e "$EXPIRES" ||
    fail "R-15: before its expiry the exception did not admit A: $(show "$A_SCAN")"
[ "$(reblocked_total)" = "0" ] || fail "R-15: an admission counted as a reblock"
policy
polq 'any(.exceptions[]; .id == $cve and .origin == "instance" and .state == "active" and .expires == $e)' --arg e "$EXPIRES" ||
    fail "R-15: the policy view does not state the exception active before $EXPIRES: $(jq -c '.exceptions' "$WORK/policy.json")"
check "R-15 before the expiry ($EXPIRES): A admitted, the exception active in the gauge and the policy view, no reblock"

# Past the expiry on the node's own clock, the same process still running.
i=0
while [ "$(inc exec "$NODE" -- date -u +%s)" -le $((EXP_EPOCH + 1)) ]; do
    i=$((i + 1))
    [ "$i" -gt $((MARGIN + 60)) ] && fail "staging: the node's clock never passed $EXPIRES"
    sleep 1
done
[ "$(inc exec "$NODE" -- sh -c 'pgrep -f "[t]obby serve"')" = "$PID_START" ] ||
    fail "staging: the instance under test restarted while the expiry passed"
i=0
until [ "$(exc_metric instance expired)" = "1" ] && [ "$(exc_metric instance active)" = "0" ]; do
    i=$((i + 1))
    [ "$i" -gt 30 ] && fail "R-15: past $EXPIRES, without a restart nor an evaluation, tobby_scan_exceptions does not count the exception expired: active=$(exc_metric instance active) expired=$(exc_metric instance expired)"
    sleep 1
done
policy
polq 'any(.exceptions[]; .id == $cve and .origin == "instance" and .state == "expired")' ||
    fail "R-15: past its expiry the policy view does not state the exception expired: $(jq -c '.exceptions' "$WORK/policy.json")"
check "R-15 past the expiry (node clock > $EXPIRES, same process $PID_START): tobby_scan_exceptions{source=instance,state=expired} 1 and active 0 before any evaluation, the policy view says expired"

sync_run
refused_for_cve "$A_SCAN" "$A_RECIPE"
itemq "$A_SCAN" 'any(.scan.evaluation.reasons[]?; .code == "TBY-SCN-021" and .params.id == $cve and .params.expires == $e and .params.level == "instance")' \
    --arg e "$EXPIRES" || fail "R-15: A's refusal does not name the expired exception (TBY-SCN-021): $(show "$A_SCAN")"
itemq "$A_SCAN" 'any(.scan.evaluation.exceptions_expired[]?; .id == $cve and .expires == $e)' --arg e "$EXPIRES" ||
    fail "R-15: A's report does not list the exception under exceptions_expired: $(show "$A_SCAN")"
[ -n "$(logrec '.digest == $d and .msg == "ingredient refused by the vulnerability policy" and .exceptions_expired == $cve and .reblocked == true' --arg d "$VA")" ] ||
    fail "R-15: A's refusal is not logged with exceptions_expired=$CVE reblocked=true"
AUDIT=$(logrec '.log_type == "audit" and .action == "scan.exception_expired" and .target == $t and .outcome == "denied" and .exception == $cve and .exception_source == "instance" and .exception_expires == $e' \
    --arg t "$REF_A@$VA" --arg e "$EXPIRES")
[ "$(printf '%s\n' "$AUDIT" | grep -c .)" = "1" ] ||
    fail "FR-094/R-15: want one scan.exception_expired audit record for $REF_A@$VA ($CVE, instance, $EXPIRES), got: $AUDIT"
[ -n "$(logrec '.log_type == "audit" and .action == "scan.block" and .target == $t' --arg t "$REF_A@$VA")" ] ||
    fail "FR-094: the re-blocking has no scan.block audit record beside it"
[ -z "$(logrec '.log_type == "audit" and .action == "scan.exception_expired" and .target == $t' --arg t "$REF_B@$VB")" ] ||
    fail "R-15: B, refused out of scope, is audited as re-blocked"
[ "$(reblocked_total)" = "1" ] || fail "R-15: tobby_scan_reblocked_items_total is $(reblocked_total) after one item re-blocked, want 1"
check "R-15 re-blocking: A refused again (TBY-SCN-005 + TBY-SCN-021, exceptions_expired), logged reblocked=true, one scan.exception_expired audit record (target $REF_A@$VA, outcome denied, exception $CVE, source instance, expires $EXPIRES) beside scan.block, tobby_scan_reblocked_items_total 1"

# -- 5. OpenVEX input ----------------------------------------------------------------------------------
VEX_ID="https://vex.example/m7c/site-1"
VEX_PATH="$VEX_DIR/site.openvex.json"
# vex_doc <@id> <statement fields (JSON)>: one statement on A, by digest and
# repository (pkg:oci, the digest percent-encoded, repository_url in the
# form a recipe writes).
vex_doc() {
    jq -n --arg id "$1" --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg cve "$CVE" \
        --arg p "pkg:oci/vuln@sha256%3A${VA#sha256:}?repository_url=$REF_A" --argjson f "$2" '{
        "@context": "https://openvex.dev/ns/v0.2.0", "@id": $id, author: "m7c crucible", timestamp: $ts, version: 1,
        statements: [{vulnerability: {name: $cve}, products: [{"@id": $p}], status: "not_affected"} + $f]}'
}
vex_doc "$VEX_ID" '{"justification": "vulnerable_code_not_in_execute_path"}' >"$WORK/site.openvex.json"
jq -e --arg cve "$CVE" '.statements[0] | .vulnerability.name == $cve and .status == "not_affected" and .justification == "vulnerable_code_not_in_execute_path" and (has("impact_statement") | not)' \
    "$WORK/site.openvex.json" >/dev/null || fail "staging: the OpenVEX document is not the one meant"
on_node "$WORK/site.openvex.json" "$VEX_PATH"
node_config "  vex:
    - path: $VEX_PATH
      expires: $FAR"
restart
[ -n "$(logrec '.msg == "OpenVEX document read" and .path == $p and .document_id == $id and .admitted == 1 and .ignored == 0 and .exceptions == 1' \
    --arg p "$VEX_PATH" --arg id "$VEX_ID")" ] || fail "R-15: the startup log does not say the document was read with one statement admitted"
[ "$(exc_metric vex active)" = "1" ] || fail "R-15: tobby_scan_exceptions{source=vex,state=active} is not 1"
sync_run
admitted "$A_SCAN" "$A_RECIPE"
itemq "$A_SCAN" 'any(.scan.evaluation.exceptions_applied[]?; .id == $cve and .vex == $p and (.justification | contains($id)) and .scope.digest == $d and .scope.repository == $r)' \
    --arg p "$VEX_PATH" --arg id "$VEX_ID" --arg d "$VA" --arg r "$CANON/team-a/vuln" ||
    fail "R-15: A's report does not name the OpenVEX document (vex, @id, scope digest and repository): $(show "$A_SCAN")"
refused_for_cve "$B_SCAN" "$B_RECIPE"
itemq "$B_SCAN" 'any(.scan.evaluation.exceptions_out_of_scope[]?; .id == $cve and .vex == $p)' --arg p "$VEX_PATH" ||
    fail "R-15: the VEX-born exception is not out of scope for B: $(show "$B_SCAN")"
policy
polq 'any(.exceptions[]; .id == $cve and .origin == "vex" and .document == $p and .state == "active") and .vex_documents[0].path == $p and .vex_documents[0].admitted == 1' \
    --arg p "$VEX_PATH" || fail "R-15: the policy view does not state the VEX-born exception: $(jq -c '{exceptions, vex_documents}' "$WORK/policy.json")"
check "R-15 OpenVEX: a justified not_affected statement on pkg:oci/vuln@<A's digest>?repository_url=$REF_A admits A (exceptions_applied[].vex=$VEX_PATH, justification naming $VEX_ID), not B; startup log admitted=1, gauge vex/active 1, policy view"

VEX_ID2="https://vex.example/m7c/impact-only"
VEX_PATH2="$VEX_DIR/impact-only.openvex.json"
vex_doc "$VEX_ID2" '{"impact_statement": "we believe the parser is not reached"}' >"$WORK/impact-only.openvex.json"
jq -e '.statements[0] | .status == "not_affected" and .impact_statement and (has("justification") | not)' \
    "$WORK/impact-only.openvex.json" >/dev/null || fail "staging: the impact-statement document is not the one meant"
on_node "$WORK/impact-only.openvex.json" "$VEX_PATH2"
node_config "  vex:
    - path: $VEX_PATH2
      expires: $FAR"
restart
[ -n "$(logrec '.msg == "OpenVEX statement adds no exception" and .level == "WARN" and .path == $p and .reason == "impact-statement-only" and .vulnerability == $cve' \
    --arg p "$VEX_PATH2")" ] || fail "R-15: the startup log does not say the impact-statement-only statement adds no exception"
[ -n "$(logrec '.msg == "OpenVEX document read" and .path == $p and .admitted == 0 and .ignored == 1 and .exceptions == 0' --arg p "$VEX_PATH2")" ] ||
    fail "R-15: the startup log does not count the statement ignored"
policy
polq '.vex_documents[0] | .path == $p and .document_id == $id and .admitted == 0 and .ignored == 1 and .exceptions == 0 and any(.notes[]?; .reason == "impact-statement-only")' \
    --arg p "$VEX_PATH2" --arg id "$VEX_ID2" || fail "R-15: the policy view does not show the statement ignored: $(jq -c '.vex_documents' "$WORK/policy.json")"
polq '[.exceptions[] | select(.origin == "vex")] | length == 0' || fail "R-15: an impact statement became an exception"
sync_run
refused_for_cve "$A_SCAN" "$A_RECIPE"
itemq "$A_SCAN" '(.scan.evaluation.exceptions_applied // []) | length == 0' || fail "R-15: an exception applied to A with an impact statement only: $(show "$A_SCAN")"
check "R-15 OpenVEX impact_statement only: nothing admitted (startup WARN reason=impact-statement-only, vex_documents[0] admitted 0 ignored 1), A refused"

# -- 6. A Retriever exception without expiry -------------------------------------------------------------
# One unrelated, recipe-scoped instance exception rides along so the policy
# view of step 7 shows both origins and a scope; it matches no finding.
OTHER_CVE="CVE-2000-0001"
retriever unbounded
if ! inc exec "$NODE" -- grep -q "id: $CVE" /etc/tobby/retriever.yaml || inc exec "$NODE" -- grep -q 'expires' /etc/tobby/retriever.yaml; then
    fail "staging: the Retriever does not hold an exception without expiry"
fi
node_config "$(exception "$OTHER_CVE" "$FAR" "policy-view companion (m7c)" recipe vuln-a)"
restart
policy
polq '.retriever_known == false' || fail "R-15: the policy view claims to know the Retriever before one was served"
sync_run
for it in "$A_SCAN $A_RECIPE" "$B_SCAN $B_RECIPE"; do
    # shellcheck disable=SC2086 # split on purpose: one "<item> <recipe item>" pair
    set -- $it
    admitted "$1" "$2"
    itemq "$1" 'any(.scan.evaluation.exceptions_applied[]?; .id == $cve and .level == "retriever" and (has("expires") | not))' ||
        fail "R-15: $1 was not admitted by the unbounded Retriever exception: $(show "$1")"
    itemq "$1" 'any(.scan.evaluation.reasons[]?; .code == "TBY-SCN-028" and .params.id == $cve)' ||
        fail "R-15: $1's report does not say the exception is unbounded (TBY-SCN-028): $(show "$1")"
done
[ -n "$(logrec '.level == "WARN" and .msg == "Retriever scan exception without expiry: applied, unbounded" and .code == "TBY-SCN-028" and .id == $cve')" ] ||
    fail "R-15: the run does not log the unbounded Retriever exception as a WARN with TBY-SCN-028"
policy
polq '.retriever_known == true and any(.exceptions[]; .id == $cve and .origin == "retriever" and .state == "unbounded" and (has("expires") | not))' ||
    fail "R-15: the policy view does not state the Retriever exception unbounded: $(jq -c '{retriever_known, exceptions}' "$WORK/policy.json")"
[ "$(exc_metric retriever active)" = "1" ] || fail "R-15: the unbounded Retriever exception is not counted active by tobby_scan_exceptions"
check "R-15 Retriever exception without expiry: A and B admitted with TBY-SCN-028, WARN logged (code=TBY-SCN-028), policy view origin=retriever state=unbounded, gauge retriever/active 1"

# -- 7. The effective policy: API (admin) and command line ---------------------------------------------------
polq '.mode == "block" and .blocking == true and .threshold == 7 and .hardening.disableUnitImport == false and .hardening.disableRegistryPush == false' ||
    fail "R-15/FR-098: the policy view does not state the mode, threshold and hardening switches: $(jq -c '{mode, blocking, threshold, hardening}' "$WORK/policy.json")"
polq 'any(.exceptions[]; .id == $o and .origin == "instance" and .state == "active" and .scope.recipe == "vuln-a" and .justification == "policy-view companion (m7c)")' \
    --arg o "$OTHER_CVE" || fail "R-15: the policy view does not state the instance exception with its scope: $(jq -c '.exceptions' "$WORK/policy.json")"
polq '.exceptions | length == 2' || fail "R-15: the policy view lists other exceptions than the two configured: $(jq -c '.exceptions' "$WORK/policy.json")"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/policy")
[ "$CODE" = "401" ] || fail "GET /api/v1/policy answered $CODE anonymously"
CODE=$(curl -s -o "$WORK/policy-viewer.json" -w '%{http_code}' -H "Authorization: Bearer $VIEWER" "$API/policy")
[ "$CODE" = "403" ] && [ "$(jq -r '.code' "$WORK/policy-viewer.json")" = "TBY-AUTH-003" ] ||
    fail "GET /api/v1/policy answered $CODE to a viewer, want 403 TBY-AUTH-003 (admin floor, like /admin/policy)"
check "R-15 GET /api/v1/policy (admin): mode block, blocking, threshold 7, hardening both false, the instance exception (scope recipe=vuln-a, active) and the Retriever one (unbounded); anonymous 401, viewer 403 TBY-AUTH-003"

inc exec "$NODE" -- sh -c 'tobby scan exceptions --output json' >"$WORK/cli-offline.json" 2>"$WORK/cli-offline.err" ||
    fail "tobby scan exceptions (offline) failed: $(cat "$WORK/cli-offline.err")"
jq -e --arg o "$OTHER_CVE" '.source == "configuration" and .retriever_known == false and (.exceptions | length == 1) and .exceptions[0].id == $o and .exceptions[0].origin == "instance" and .exceptions[0].scope.recipe == "vuln-a"' \
    "$WORK/cli-offline.json" >/dev/null || fail "R-15: tobby scan exceptions (offline) is not the configuration's view: $(jq -c . "$WORK/cli-offline.json")"
inc exec "$NODE" -- env TOBBY_API_TOKEN="$TOKEN" tobby scan exceptions --instance http://127.0.0.1:8080 --output json \
    >"$WORK/cli-online.json" 2>"$WORK/cli-online.err" || fail "tobby scan exceptions --instance failed: $(cat "$WORK/cli-online.err")"
jq -e '.source == "instance" and .instance == "http://127.0.0.1:8080" and .retriever_known == true' "$WORK/cli-online.json" >/dev/null ||
    fail "R-15: tobby scan exceptions --instance is not the instance's view: $(jq -c '{source, instance, retriever_known}' "$WORK/cli-online.json")"
policy
[ "$(jq -cS '.exceptions' "$WORK/cli-online.json")" = "$(jq -cS '.exceptions' "$WORK/policy.json")" ] ||
    fail "FR-061: tobby scan exceptions --instance and GET /api/v1/policy list different exceptions"
check "R-15 tobby scan exceptions: offline source=configuration, retriever_known=false (the instance exception only); --instance with an admin token source=instance, retriever_known=true, the very exceptions of GET /api/v1/policy"

cleanup
trap - EXIT
check "scenario m7 c-exceptions complete — report: $REPORT"
