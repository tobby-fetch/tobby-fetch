#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario — hardening seen from the wire, on an
# instance with authentication ON behind its own TLS listener: the two
# FR-098 hardening switches (R-17, track C) on every surface they close, and
# the 7.3 hardening (NFR-012, NFR-022, NFR-023, track D) that every client
# meets.
#
#   1. Defaults (both switches off), which is also the CONTROL of every
#      later refusal: the configuration dump, the startup record and
#      tobby_hardening_enabled say "off"; a unit import, a packing (API and
#      command line) and a recipe synchronization succeed; and the 7.3
#      hardening:
#        - security headers: every route class (UI page, API, /v2/, /files/,
#          /help, probes, /metrics) answers with the headers NFR-022 assigns
#          it; HSTS is present behind TLS; authenticated responses are
#          no-store;
#        - CSRF: a cookie-authenticated state-changing request without a
#          token, with a forged token, from a cross-site Origin or a
#          cross-site Sec-Fetch-Site is refused with TBY-AUTH-004 (UI and
#          API); the same request with the session's token, and with a
#          bearer token, succeeds — and only those two took effect;
#        - input bounds: an oversized body on each state-changing API route
#          is refused with TBY-SRV-004, the tighter per-endpoint bounds too;
#          the instance stays healthy;
#        - the five OCI clients of FR-041 (docker, podman, helm, oras,
#          skopeo) pass their push/pull round trip with the headers in place.
#   2. hardening.disableUnitImport: stated by the dump, the startup record
#      and its warning, the metric; the import screen (shown disabled,
#      naming the setting), its inspection and its submission, the import
#      API (inspect and create), the FileSets screen and API, the OCI layout
#      screen and API, `tobby fileset pack` and `tobby import` (exit 3)
#      refuse with TBY-POL-005 naming the
#      setting, nothing is written, no task is created, each refusal is one
#      hardening.refused audit event; a recipe synchronization still works.
#   3. hardening.disableRegistryPush: stated the same way, and on the content
#      screen; `docker push`, `helm push` and `oras push` as an
#      ADMINISTRATOR, and raw blob-upload, manifest-put and manifest-delete
#      requests, are refused with DENIED carrying TBY-POL-006, nothing lands
#      and nothing is deleted; each refusal is a hardening.refused audit
#      event naming the administrator; docker, podman, helm, oras and skopeo
#      pulls and a recipe synchronization still work.
#
# The instance runs the binary with server.tls (a throw-away CA); the
# reference chart's rendering is exercised by m4 and the crown. The OCI
# conformance suite, coverage, fuzzing and the arm64 e2e run in CI
# (DEVELOPMENT-PLAN §3), not here.
#
# Topology:
#   tbc-m7d-source (container, tbc-net)  source registry + cookbook
#   tbc-m7d-node   (container, tbc-net)  instance under test, TLS :8443
#   tbc-m7d-cli    (container, tbc-net)  docker and podman (nested)
#   the crucible host                    helm, oras, skopeo, curl (API, browser)
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/d-hardening.sh
# $C (the CA option of every curl) is two words by construction, and the
# "a && b || fail" chains mean "any step failing is the failure".
# shellcheck disable=SC2086,SC2015
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-d-hardening.txt}"
# shellcheck source=../../lib.sh
. "$DIR/../../lib.sh"
# shellcheck source=c-lib.sh
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m7d-source
NODE=tbc-m7d-node
CLI=tbc-m7d-cli
STORE=/var/lib/tobby
LOG=/var/log/tobby.log
ADMIN=root
ADMIN_PW="m7d-crucible-admin-pw"
U_SET=hardening.disableUnitImport
P_SET=hardening.disableRegistryPush
cleanup() {
    inc delete --force "$SOURCE" "$NODE" "$CLI" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
m7_tooling openssl helm oras skopeo
m7_build

# -- 0. Nodes, PKI, fixtures ------------------------------------------------------------
m7_launch "$SOURCE" tbc-connected-node
m7_launch "$NODE" tbc-connected-node
# docker and podman need a nested container runtime context; nothing is
# ever RUN by them here — they pull, tag and push.
m7_launch "$CLI" tbc-connected-node security.nesting=true
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
instance_ip "$CLI" >/dev/null
S="$SOURCE_IP:8080"
R="$NODE_IP:8443"
BASE="https://$R"
API="$BASE/api/v1"
CA="$WORK/ca.crt"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=tobby-crucible-m7d-ca" \
    -keyout "$WORK/ca.key" -out "$CA" >/dev/null 2>&1 || fail "generating the CA"
openssl req -newkey rsa:2048 -nodes -subj "/CN=$NODE_IP" -keyout "$WORK/tls.key" -out "$WORK/tls.csr" >/dev/null 2>&1 &&
    printf 'subjectAltName=IP:%s\n' "$NODE_IP" >"$WORK/tls.ext" &&
    openssl x509 -req -in "$WORK/tls.csr" -CA "$CA" -CAkey "$WORK/ca.key" -CAcreateserial -days 2 \
        -extfile "$WORK/tls.ext" -out "$WORK/tls.crt" >/dev/null 2>&1 ||
    fail "issuing the instance certificate"
chmod 0644 "$WORK/tls.key"
mkdir -p "$WORK/certs" && cp "$CA" "$WORK/certs/ca.crt"
check "throw-away PKI: a CA and the instance certificate for IP $NODE_IP"

m7_serve_source "$SOURCE"
m7_keypair
hard_recipe() { # <name>: one image and its signed recipe; prints the image digest
    # Not _d: m7_publish runs in this shell and sets _d to the RECIPE's
    # digest, which the first m7 pass then took for the image's.
    _hd=$(m7_seed push "$S/library/$1:1.0") || fail "seeding $1"
    m7_publish "$S" "$1" "$(m7_image_recipe "$1" "$S/library/$1" "$_hd")" sign >/dev/null
    printf '%s' "$_hd"
}
DIG_hard_a=$(hard_recipe hard-a)
DIG_hard_b=$(hard_recipe hard-b)
DIG_hard_c=$(hard_recipe hard-c)
IMP_DIG=$(m7_seed push "$S/library/hard-imp:1.0.0") || fail "seeding the unit-import source"
check "fixtures: three signed recipes (hard-a, hard-b, hard-c), each verified by cosign, and a unit-import source image"

inc file push "$WORK/cosign.pub" "$NODE/etc/tobby/trust.pub" --create-dirs >/dev/null
inc file push "$WORK/tls.crt" "$NODE/etc/tobby/tls.crt" >/dev/null
inc file push "$WORK/tls.key" "$NODE/etc/tobby/tls.key" >/dev/null
# shellcheck disable=SC2016 # expanded by the node's shell, not by this one
inc exec "$NODE" -- sh -c 'for d in notes notes2 cli0 cli2; do mkdir -p /srv/pack/$d && printf "%s\n" "$d" >/srv/pack/$d/README; done'

# hconfig <disableUnitImport> <disableRegistryPush>: the configuration of
# the instance under test; the switches are the only thing that varies.
hconfig() {
    inc exec "$NODE" -- sh -c "cat >/etc/tobby/config.yaml <<'EOF'
mode: mirror
storage:
  root: $STORE
state:
  root: /var/lib/tobby-state
server:
  addr: :8443
  tls:
    certFile: /etc/tobby/tls.crt
    keyFile: /etc/tobby/tls.key
registries:
  insecure:
    - $S
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: zone
      keyFile: /etc/tobby/trust.pub
files:
  packRoots:
    - /srv/pack
hardening:
  disableUnitImport: $1
  disableRegistryPush: $2
EOF"
}
C="--cacert $CA"
start() { m7_start "$NODE" "$BASE" --cacert "$CA"; }
restart() { m7_stop "$NODE"; start; }
retriever() { # <recipe…>
    m7_retriever "$WORK/retriever.yaml" "$S/cookbook" "$@"
    inc file push "$WORK/retriever.yaml" "$NODE/etc/tobby/retriever.yaml" --create-dirs >/dev/null
}
api() { curl -s $C -H "$AUTHZ" "$@"; }
basic() { curl -s $C -u "$ADMIN:$ADMIN_PW" "$@"; }
# code_of <file>: the taxonomy code of an RFC 9457 document.
code_of() { jq -r '.code // empty' "$1" 2>/dev/null; }
digest_of() { # <repo> <ref>: the digest /v2/ serves, empty when absent
    basic -I -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
        "$BASE/v2/$1/manifests/$2" | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}'
}
sync_ok() { # <label>: a synchronization through the API, which must succeed
    _id=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/sync" | jq -r '.task.id // empty')
    [ -n "$_id" ] || fail "$1: the API did not start a synchronization"
    _st=$(m7_wait_task "$API" "$_id" "$WORK/sync.json" $C -H "$AUTHZ")
    [ "$_st" = "done" ] || fail "$1: the synchronization ended $_st: $(jq -c '[.task.items[] | {name, status, error}]' "$WORK/sync.json")"
}
recipe_landed() { # <recipe> <digest>: its ingredient is in the store, class recipe
    _repo=$(inc exec "$NODE" -- cat "$STORE/meta/recipes.json" |
        jq -r --arg k "$1@1.0.0" '.recipes[$k].ingredients[] | select(.name == "app") | .repo')
    [ -n "$_repo" ] || fail "the recipe graph does not record $1"
    api "$API/content/$_repo" >"$WORK/landed.json"
    jq -e --arg d "$2" '.provenance.class == "recipe" and ([.tags[] | select(.digest == $d)] | length == 1)' "$WORK/landed.json" >/dev/null ||
        fail "$1 did not land in the store at its pinned digest $2 as class recipe: $(head -c 800 "$WORK/landed.json")"
    printf '%s' "$_repo"
}
startup_says() { # <unit> <push>: the LAST startup record of the switches
    inc exec "$NODE" -- sh -c "grep -F '\"msg\":\"hardening switches\"' $LOG | tail -1" |
        jq -e --argjson u "$1" --argjson p "$2" '.[env.U] == $u and .[env.P] == $p' >/dev/null
}
export U="$U_SET" P="$P_SET"
dump_says() { # <unit> <push>
    inc exec "$NODE" -- tobby config dump --output json 2>/dev/null |
        jq -e --argjson u "$1" --argjson p "$2" '.hardening.disableUnitImport == $u and .hardening.disableRegistryPush == $p' >/dev/null
}
gauge_says() { # <unit 0|1> <push 0|1>
    _m=$(curl -fsS $C "$BASE/metrics")
    [ "$(m7_metric "$_m" 'tobby_hardening_enabled{setting="disableUnitImport"}')" = "$1" ] &&
        [ "$(m7_metric "$_m" 'tobby_hardening_enabled{setting="disableRegistryPush"}')" = "$2" ]
}
refusals() { # <setting>: hardening.refused audit records naming it, so far
    inc exec "$NODE" -- sh -c "grep -F '\"action\":\"hardening.refused\"' $LOG | grep -F '\"setting\":\"$1\"' | grep -cF '\"outcome\":\"denied\"' || true"
}

# login: a browser session of the administrator in $JAR. Sessions live in
# the process: every restart needs a new one.
JAR="$WORK/jar"
login() {
    rm -f "$JAR"
    _c=$(curl -s $C -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' --data-urlencode "username=$ADMIN" \
        --data-urlencode "password=$ADMIN_PW" --data-urlencode next=/ "$BASE/login")
    [ "$_c" = "303" ] || fail "signing in to the web interface answered $_c"
}

# The OCI client node: docker and podman, trusting the scenario CA.
m7_apk "$CLI" ca-certificates docker podman
inc exec "$CLI" -- sh -c "mkdir -p /etc/docker/certs.d/$R /etc/containers/certs.d/$R"
inc file push "$CA" "$CLI/etc/docker/certs.d/$R/ca.crt" >/dev/null
inc file push "$CA" "$CLI/etc/containers/certs.d/$R/ca.crt" >/dev/null
inc exec "$CLI" -- sh -c 'nohup dockerd --iptables=false --ip6tables=false --bridge=none --storage-driver=vfs \
    >/var/log/dockerd.log 2>&1 &'
i=0
until inc exec "$CLI" -- docker info >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -gt 60 ] && fail "dockerd never came up on $CLI: $(inc exec "$CLI" -- tail -5 /var/log/dockerd.log)"
    sleep 1
done
D="inc exec $CLI -- docker"
PM="inc exec $CLI -- podman --storage-driver=vfs --events-backend=file --cgroup-manager=cgroupfs"
check "OCI client node: dockerd up (vfs, no bridge), podman installed, both trusting the scenario CA"

# -- 1. Defaults: the controls, and the 7.3 hardening -------------------------------------
retriever hard-a
hconfig false false
m7_admin "$NODE" "$ADMIN" "$ADMIN_PW"
start
TOKEN=$(basic -H 'Content-Type: application/json' -d '{"name":"m7d","role":"admin"}' "$API/tokens" | jq -r '.secret // empty')
case "$TOKEN" in tby_*) ;; *) fail "creating the admin API token failed" ;; esac
AUTHZ="Authorization: Bearer $TOKEN"
CODE=$(curl -s $C -o /dev/null -w '%{http_code}' "$API/content")
[ "$CODE" = "401" ] || fail "an anonymous API call answered $CODE: authentication is not on"
dump_says false false || fail "FR-098: the configuration dump does not show both switches off"
startup_says false false || fail "FR-098: the startup record does not state both switches off"
gauge_says 0 0 || fail "FR-098: tobby_hardening_enabled is not 0 for both settings"
check "defaults: TLS listener, authentication on; dump, startup record and metric state both switches off"

# 1a. The controls: every surface the switches will close works now.
api -G --data-urlencode "ref=$S/library/hard-imp:1.0.0" "$API/import/inspect" >"$WORK/inspect.json"
PLATFORMS=$(jq -c '[.platforms[] | {name: .name, digest: .digest}]' "$WORK/inspect.json") ||
    fail "control: the inspection failed: $(cat "$WORK/inspect.json")"
IMPORT_BODY="{\"reference\":\"$S/library/hard-imp:1.0.0\",\"platforms\":$PLATFORMS}"
IMP_ID=$(api -H 'Content-Type: application/json' -d "$IMPORT_BODY" "$API/import" | jq -r '.task.id // empty')
[ -n "$IMP_ID" ] || fail "control: the import API did not start a task"
[ "$(m7_wait_task "$API" "$IMP_ID" "$WORK/import.json" $C -H "$AUTHZ")" = "done" ] || fail "control: the unit import failed"
IMP_REPO="$(printf '%s' "$S" | tr ':' '_')/library/hard-imp"
[ "$(api "$API/content/$IMP_REPO/-/tags/1.0.0" | jq -r '.digest')" = "$IMP_DIG" ] || fail "control: the unit import did not land"
CODE=$(api -o "$WORK/pack.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"source":"/srv/pack/notes","name":"notes","version":"1.0.0"}' "$API/filesets/pack")
[ "$CODE" = "201" ] || fail "control: the pack API answered $CODE: $(cat "$WORK/pack.json")"
# The command line writes the store directly (ADR-0005), beside the running
# instance: its landing is read on disk, where it wrote.
repo_on_disk() { inc exec "$NODE" -- test -d "$STORE/docker/registry/v2/repositories/$1/_manifests"; }
CLI_DIG=$(inc exec "$NODE" -- tobby fileset pack /srv/pack/cli0 cli0:1.0.0 2>/dev/null) || fail "control: tobby fileset pack failed"
case "$CLI_DIG" in sha256:*) ;; *) fail "control: tobby fileset pack printed no digest ('$CLI_DIG')" ;; esac
repo_on_disk localhost/filesets/cli0 || fail "control: the CLI packing did not land in the store"
sync_ok "control"
REPO_A=$(recipe_landed hard-a "$DIG_hard_a")
check "controls: unit import, API and CLI packing, and a recipe synchronization succeed with both switches off"

# 1b. NFR-022, security headers per route class.
login
headers() { curl -s $C -o /dev/null -D - "$@" | tr -d '\r'; }
hval() { printf '%s\n' "$1" | awk -v k="$2" 'index(tolower($0), tolower(k) ": ") == 1 {sub(/^[^:]*: /, ""); print; exit}'; }
need() { # <label> <headers> <name> <substring>
    _v=$(hval "$2" "$3")
    case "$_v" in *"$4"*) ;; *) fail "NFR-022: $1 answers $3='$_v', want it to contain '$4'" ;; esac
}
common() { # <label> <headers>
    need "$1" "$2" X-Content-Type-Options nosniff
    need "$1" "$2" Referrer-Policy same-origin
    need "$1" "$2" Strict-Transport-Security max-age=
}
for path in /content /help; do
    H=$(headers -b "$JAR" "$BASE$path")
    need "UI $path" "$H" Content-Type text/html
    common "UI $path" "$H"
    need "UI $path" "$H" X-Frame-Options DENY
    for d in "frame-ancestors 'none'" "object-src 'none'" "base-uri 'none'" "form-action 'self'"; do
        need "UI $path" "$H" Content-Security-Policy "$d"
    done
    need "UI $path" "$H" Cache-Control no-store
done
H=$(headers -H "$AUTHZ" "$API/content")
common "API" "$H"; need "API" "$H" Content-Security-Policy "default-src 'none'"; need "API" "$H" Cache-Control no-store
H=$(headers -u "$ADMIN:$ADMIN_PW" "$BASE/v2/")
common "/v2/" "$H"; need "/v2/" "$H" Cache-Control no-store
H=$(headers -u "$ADMIN:$ADMIN_PW" "$BASE/files/")
common "/files/" "$H"; need "/files/" "$H" Content-Security-Policy "sandbox"; need "/files/" "$H" Content-Security-Policy "script-src 'none'"
for path in /healthz /readyz /metrics; do
    H=$(headers "$BASE$path")
    common "$path" "$H"; need "$path" "$H" Content-Security-Policy "default-src 'none'"
done
check "NFR-022: UI pages, API, /v2/, /files/, /help, probes and /metrics carry their class's headers; HSTS behind TLS; authenticated responses no-store"

# 1c. NFR-012, CSRF over the wire. The session's own token is read from a
# page of the session; the control requests prove it is the right one.
curl -s $C -b "$JAR" -o "$WORK/page.html" "$BASE/content"
CSRF=$(m7_csrf "$WORK/page.html")
[ -n "$CSRF" ] || fail "no anti-forgery token found in a page of the session"
tok_body() { printf '{"name":"%s","role":"viewer"}' "$1"; }
cookie_post() { # <name> [<header>…]: POST /api/v1/tokens with the cookie only
    _n="$1"; shift
    curl -s $C -b "$JAR" -o "$WORK/csrf-$_n.json" -w '%{http_code}' -H 'Content-Type: application/json' "$@" \
        -d "$(tok_body "csrf-$_n")" "$API/tokens"
}
for spec in "none" "forged:X-CSRF-Token: forged-token" "origin:Origin: https://evil.example" "fetchsite:Sec-Fetch-Site: cross-site"; do
    name=${spec%%:*}
    case "$name" in
        none) CODE=$(cookie_post none) ;;
        forged) CODE=$(cookie_post forged -H "${spec#*:}") ;;
        *) CODE=$(cookie_post "$name" -H "X-CSRF-Token: $CSRF" -H "${spec#*:}") ;;
    esac
    [ "$CODE" = "403" ] && [ "$(code_of "$WORK/csrf-$name.json")" = "TBY-AUTH-004" ] ||
        fail "NFR-012: a cookie-authenticated change ($name) answered $CODE $(code_of "$WORK/csrf-$name.json"), want 403 TBY-AUTH-004"
done
[ "$(cookie_post session -H "X-CSRF-Token: $CSRF")" = "201" ] ||
    fail "NFR-012 control: the same change with the session's token was refused: $(cat "$WORK/csrf-session.json")"
CODE=$(api -o "$WORK/csrf-bearer.json" -w '%{http_code}' -H 'Content-Type: application/json' -d "$(tok_body csrf-bearer)" "$API/tokens")
[ "$CODE" = "201" ] || fail "NFR-012: the same change with a bearer token answered $CODE"
api "$API/tokens" | jq -e '[.tokens[].name | select(startswith("csrf-"))] | sort == ["csrf-bearer", "csrf-session"]' >/dev/null ||
    fail "NFR-012: a refused change took effect: $(api "$API/tokens" | jq -c '[.tokens[].name]')"
CODE=$(curl -s $C -b "$JAR" -o "$WORK/ui-csrf.html" -w '%{http_code}' --data-urlencode "ref=$S/library/hard-imp:1.0.0" "$BASE/import")
[ "$CODE" = "403" ] && grep -q TBY-AUTH-004 "$WORK/ui-csrf.html" ||
    fail "NFR-012: a UI form posted without its token answered $CODE without TBY-AUTH-004"
check "NFR-012: cookie-only, forged-token, cross-site Origin and Sec-Fetch-Site changes refused (TBY-AUTH-004, API and UI); session token and bearer succeed, and only they took effect"

# 1d. NFR-023, bounded inputs: over the surface-wide bound (16 MiB) on every
# state-changing route, and over two endpoint bounds.
head -c $((16 * 1024 * 1024 + 1)) /dev/zero >"$WORK/big.body"
head -c $((64 * 1024 + 1)) /dev/zero >"$WORK/import.body"
head -c $((8 * 1024 + 1)) /dev/zero >"$WORK/pack.body"
oversized() { # <method> <path> <body file>: must answer 413 TBY-SRV-004
    _c=$(api -o "$WORK/bound.json" -w '%{http_code}' -X "$1" -H 'Content-Type: application/json' \
        --data-binary "@$3" "$API$2")
    [ "$_c" = "413" ] && [ "$(code_of "$WORK/bound.json")" = "TBY-SRV-004" ] ||
        fail "NFR-023: an oversized body on $1 $2 answered $_c $(code_of "$WORK/bound.json"), want 413 TBY-SRV-004"
}
for route in "DELETE /accounts/nobody" "DELETE /content/no/such/repo" "DELETE /retriever/interval" \
    "PATCH /accounts/nobody" "POST /account/password" "POST /accounts" "POST /filesets/pack" "POST /import" \
    "POST /media/import" "POST /media/verify" "POST /oci-layout/export" "POST /oci-layout/import" \
    "POST /oci-layout/plan" "POST /plan" "POST /recipes/publish" "POST /store/reset" "POST /store/verify" \
    "POST /store/verify/cancel" "POST /sync" "POST /tokens" "POST /tokens/nobody/revoke" \
    "PUT /network/certificate" "PUT /retriever/interval"; do
    oversized "${route%% *}" "${route#* }" "$WORK/big.body"
done
oversized POST /import "$WORK/import.body"
oversized POST /filesets/pack "$WORK/pack.body"
[ "$(curl -s $C -o /dev/null -w '%{http_code}' "$BASE/readyz")" = "200" ] || fail "NFR-023: the instance is not ready after the oversized bodies"
check "NFR-023: an oversized body is refused with TBY-SRV-004 on all 23 state-changing API routes, and over the import and pack bounds; the instance stays ready"

# 1e. FR-041 with the headers in place: five clients, push then pull, each
# push read back from /v2/ before the next step relies on it.
inc exec "$CLI" -- sh -c "printf '%s\n' '$ADMIN_PW' | docker login $R -u $ADMIN --password-stdin" >/dev/null 2>&1 ||
    fail "docker login as the administrator failed"
inc exec "$CLI" -- sh -c "tar -C /etc -cf - hostname os-release | docker import - m7d/local:1" >/dev/null ||
    fail "building the local docker image"
$D tag m7d/local:1 "$R/m7d/docker:1.0.0"
$D push "$R/m7d/docker:1.0.0" >/dev/null 2>&1 || fail "FR-041: docker push failed"
DOCKER_DIG=$(digest_of m7d/docker 1.0.0)
[ -n "$DOCKER_DIG" ] || fail "FR-041: the docker push is not served back"
$D rmi "$R/m7d/docker:1.0.0" >/dev/null 2>&1 || true
$D pull "$R/m7d/docker:1.0.0" >/dev/null 2>&1 || fail "FR-041: docker pull failed"
# The same global flags as $PM: a bare `podman login` would initialise the
# storage database with the overlay driver, and every later vfs call would
# then refuse it ("database graph driver overlay does not match vfs").
inc exec "$CLI" -- sh -c "printf '%s\n' '$ADMIN_PW' | podman --storage-driver=vfs --events-backend=file --cgroup-manager=cgroupfs login $R -u $ADMIN --password-stdin" >/dev/null 2>&1 ||
    fail "podman login failed"
$PM pull "$R/m7d/docker:1.0.0" >"$WORK/podman.out" 2>&1 || fail "FR-041: podman pull failed: $(tail -3 "$WORK/podman.out")"
$PM tag "$R/m7d/docker:1.0.0" "$R/m7d/podman:1.0.0" >/dev/null 2>&1
$PM push "$R/m7d/podman:1.0.0" >"$WORK/podman.out" 2>&1 || fail "FR-041: podman push failed: $(tail -3 "$WORK/podman.out")"
[ -n "$(digest_of m7d/podman 1.0.0)" ] || fail "FR-041: the podman push is not served back"
$PM rmi "$R/m7d/podman:1.0.0" >/dev/null 2>&1 || true
$PM pull "$R/m7d/podman:1.0.0" >"$WORK/podman.out" 2>&1 || fail "FR-041: podman pull of its own push failed: $(tail -3 "$WORK/podman.out")"

AUTH64=$(printf '%s:%s' "$ADMIN" "$ADMIN_PW" | base64 | tr -d '\n')
printf '{"auths":{"%s":{"auth":"%s"}}}\n' "$R" "$AUTH64" >"$WORK/helm-registry.json"
export HELM_REGISTRY_CONFIG="$WORK/helm-registry.json"
mkdir -p "$WORK/chart/m7d"
printf 'apiVersion: v2\nname: m7d\nversion: 0.1.0\n' >"$WORK/chart/m7d/Chart.yaml"
helm package "$WORK/chart/m7d" -d "$WORK" >/dev/null || fail "packaging the chart"
helm push "$WORK/m7d-0.1.0.tgz" "oci://$R/m7d/charts" --ca-file "$CA" >/dev/null 2>&1 || fail "FR-041: helm push failed"
[ -n "$(digest_of m7d/charts/m7d 0.1.0)" ] || fail "FR-041: the helm push is not served back"
# helm pull writes into -d and does not create it.
mkdir -p "$WORK/helm-out"
helm pull "oci://$R/m7d/charts/m7d" --version 0.1.0 --ca-file "$CA" -d "$WORK/helm-out" >"$WORK/helm.out" 2>&1 ||
    fail "FR-041: helm pull failed: $(tail -3 "$WORK/helm.out")"
printf 'an oras artifact\n' >"$WORK/oras.txt"
( cd "$WORK" && oras push --ca-file "$CA" -u "$ADMIN" -p "$ADMIN_PW" "$R/m7d/oras:1.0.0" oras.txt:text/plain >/dev/null 2>&1 ) ||
    fail "FR-041: oras push failed"
[ -n "$(digest_of m7d/oras 1.0.0)" ] || fail "FR-041: the oras push is not served back"
oras pull --ca-file "$CA" -u "$ADMIN" -p "$ADMIN_PW" -o "$WORK/oras-out" "$R/m7d/oras:1.0.0" >/dev/null 2>&1 &&
    cmp -s "$WORK/oras.txt" "$WORK/oras-out/oras.txt" || fail "FR-041: oras pull failed or returned other bytes"
SK="--src-cert-dir $WORK/certs --dest-cert-dir $WORK/certs --src-creds $ADMIN:$ADMIN_PW --dest-creds $ADMIN:$ADMIN_PW"
skopeo copy $SK "docker://$R/m7d/docker:1.0.0" "docker://$R/m7d/skopeo:1.0.0" >/dev/null 2>&1 ||
    fail "FR-041: skopeo copy (pull and push) failed"
[ "$(digest_of m7d/skopeo 1.0.0)" = "$DOCKER_DIG" ] || fail "FR-041: the skopeo push is not served back at the same digest"
skopeo inspect --cert-dir "$WORK/certs" --creds "$ADMIN:$ADMIN_PW" "docker://$R/m7d/skopeo:1.0.0" >/dev/null 2>&1 ||
    fail "FR-041: skopeo inspect failed"
check "FR-041/NFR-022: docker, podman, helm, oras and skopeo push and pull through the TLS listener with the headers in place"

# -- 2. hardening.disableUnitImport ------------------------------------------------------
hconfig true false
inc exec "$NODE" -- grep -q 'disableUnitImport: true' /etc/tobby/config.yaml || fail "the switch is not in the configuration"
restart
login
dump_says true false || fail "FR-098: the configuration dump does not show $U_SET on"
startup_says true false || fail "FR-098: the startup record does not state $U_SET on"
inc exec "$NODE" -- sh -c "grep -F 'OCI layout imports and FileSet packing are disabled' $LOG | grep -qF '\"setting\":\"$U_SET\"'" ||
    fail "FR-098: no startup warning names $U_SET"
gauge_says 1 0 || fail "FR-098: tobby_hardening_enabled does not read disableUnitImport=1, disableRegistryPush=0"
check "FR-098: $U_SET stated by the dump, the startup record and its warning, and the metric"

TASKS_BEFORE=$(api "$API/tasks?type=unit-import" | jq -r '.total')
LAYOUT_TASKS_BEFORE=$(api "$API/tasks?type=layout-import" | jq -r '.total')
REFUSED_BEFORE=$(refusals "$U_SET")
# The import screen: shown, disabled, naming the setting.
curl -s $C -b "$JAR" -o "$WORK/import.html" "$BASE/import"
grep -q 'data-hardening="disableUnitImport"' "$WORK/import.html" && grep -q "$U_SET" "$WORK/import.html" &&
    grep -q 'disabled aria-describedby="import-hardened"' "$WORK/import.html" ||
    fail "FR-098: the import screen is not shown disabled naming $U_SET"
# Its inspection (the htmx step 2) and its submission.
CODE=$(curl -s $C -b "$JAR" -o "$WORK/ui-inspect.html" -w '%{http_code}' -H 'HX-Request: true' -G \
    --data-urlencode "ref=$S/library/hard-imp:1.0.0" "$BASE/import")
[ "$CODE" = "403" ] && grep -q TBY-POL-005 "$WORK/ui-inspect.html" && grep -q "$U_SET" "$WORK/ui-inspect.html" ||
    fail "FR-098: the screen's inspection answered $CODE without TBY-POL-005 naming the setting"
curl -s $C -b "$JAR" -o "$WORK/page.html" "$BASE/content"
CSRF=$(m7_csrf "$WORK/page.html")
CODE=$(curl -s $C -b "$JAR" -o "$WORK/ui-submit.html" -w '%{http_code}' --data-urlencode "csrf=$CSRF" \
    --data-urlencode "ref=$S/library/hard-imp:1.0.0" --data-urlencode "platform=linux/amd64" "$BASE/import")
[ "$CODE" = "403" ] && grep -q TBY-POL-005 "$WORK/ui-submit.html" ||
    fail "FR-098: the screen's submission answered $CODE without TBY-POL-005"
# The API, inspect and create.
CODE=$(api -o "$WORK/a-inspect.json" -w '%{http_code}' -G --data-urlencode "ref=$S/library/hard-imp:1.0.0" "$API/import/inspect")
[ "$CODE" = "403" ] && [ "$(code_of "$WORK/a-inspect.json")" = "TBY-POL-005" ] &&
    jq -e --arg s "$U_SET" '.probable_cause | contains($s)' "$WORK/a-inspect.json" >/dev/null ||
    fail "FR-098: the inspect API answered $CODE $(code_of "$WORK/a-inspect.json") without the setting"
CODE=$(api -o "$WORK/a-import.json" -w '%{http_code}' -H 'Content-Type: application/json' -d "$IMPORT_BODY" "$API/import")
[ "$CODE" = "403" ] && [ "$(code_of "$WORK/a-import.json")" = "TBY-POL-005" ] ||
    fail "FR-098: the import API answered $CODE $(code_of "$WORK/a-import.json"), want 403 TBY-POL-005"
# The FileSets screen and API.
curl -s $C -b "$JAR" -o "$WORK/filesets.html" "$BASE/filesets"
grep -q 'data-hardening="disableUnitImport"' "$WORK/filesets.html" && grep -q 'disabled aria-describedby="pack-hardened"' "$WORK/filesets.html" ||
    fail "FR-098: the FileSets screen does not show packing disabled naming the setting"
api "$API/filesets" | jq -e --arg s "$U_SET" '.packEnabled == false and .packDisabledBy == $s' >/dev/null ||
    fail "FR-098: the FileSets API does not say packing is disabled by $U_SET"
CODE=$(api -o "$WORK/a-pack.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"source":"/srv/pack/notes2","name":"notes2","version":"1.0.0"}' "$API/filesets/pack")
[ "$CODE" = "403" ] && [ "$(code_of "$WORK/a-pack.json")" = "TBY-POL-005" ] ||
    fail "FR-098: the pack API answered $CODE $(code_of "$WORK/a-pack.json"), want 403 TBY-POL-005"
# The OCI layout import is a unit import too (B-071): the screen shown
# disabled, its submission, the API and the command line refuse. The
# refusal comes before the layout is read, so the path need not exist.
curl -s $C -b "$JAR" -o "$WORK/layout.html" "$BASE/admin/oci-layout"
grep -q 'data-hardening="disableUnitImport"' "$WORK/layout.html" &&
    grep -q 'aria-describedby="layout-import-hardened"' "$WORK/layout.html" ||
    fail "FR-098: the OCI layout screen does not show its import disabled naming the setting"
CSRF=$(m7_csrf "$WORK/layout.html")
CODE=$(curl -s $C -b "$JAR" -o "$WORK/ui-layout.html" -w '%{http_code}' --data-urlencode "csrf=$CSRF" \
    --data-urlencode "input=/srv/pack/none.tar" "$BASE/admin/oci-layout/import")
[ "$CODE" = "403" ] && grep -q TBY-POL-005 "$WORK/ui-layout.html" ||
    fail "FR-098: the OCI layout screen's import answered $CODE without TBY-POL-005"
CODE=$(api -o "$WORK/a-layout.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"input":"/srv/pack/none.tar"}' "$API/oci-layout/import")
[ "$CODE" = "403" ] && [ "$(code_of "$WORK/a-layout.json")" = "TBY-POL-005" ] ||
    fail "FR-098: the OCI layout import API answered $CODE $(code_of "$WORK/a-layout.json"), want 403 TBY-POL-005"
RC=0
inc exec "$NODE" -- tobby import /srv/pack/none.tar >/dev/null 2>"$WORK/cli-layout.err" || RC=$?
[ "$RC" = "3" ] && grep -q TBY-POL-005 "$WORK/cli-layout.err" && grep -q '"action":"hardening.refused"' "$WORK/cli-layout.err" ||
    fail "FR-098: tobby import exited $RC under $U_SET, want 3 with TBY-POL-005 and its hardening.refused record: $(cat "$WORK/cli-layout.err")"
[ "$(api "$API/tasks?type=layout-import" | jq -r '.total')" = "$LAYOUT_TASKS_BEFORE" ] ||
    fail "FR-098: a refused OCI layout import created a task"
# The command line, beside the running instance, reading the same file.
RC=0
inc exec "$NODE" -- tobby fileset pack /srv/pack/cli2 cli2:1.0.0 >/dev/null 2>"$WORK/cli-pack.err" || RC=$?
[ "$RC" = "3" ] || fail "FR-098: tobby fileset pack exited $RC under $U_SET, want 3 (policy)"
grep -q TBY-POL-005 "$WORK/cli-pack.err" && grep -q '"action":"hardening.refused"' "$WORK/cli-pack.err" &&
    grep -q "\"setting\":\"$U_SET\"" "$WORK/cli-pack.err" ||
    fail "FR-098: the command line's refusal does not carry TBY-POL-005 and its hardening.refused record: $(cat "$WORK/cli-pack.err")"
# Nothing was written, nothing was queued, each refusal is one record.
for repo in localhost/filesets/notes2 localhost/filesets/cli2; do
    if repo_on_disk "$repo"; then fail "FR-098: $repo was written despite the refusal"; fi
done
[ "$(api -o /dev/null -w '%{http_code}' "$API/content/localhost/filesets/notes2")" = "404" ] ||
    fail "FR-098: the refused packing is listed by the content API"
[ "$(api "$API/tasks?type=unit-import" | jq -r '.total')" = "$TASKS_BEFORE" ] || fail "FR-098: a refused unit import created a task"
N=$(( $(refusals "$U_SET") - REFUSED_BEFORE ))
[ "$N" = "7" ] || fail "FR-094/FR-098: $N hardening.refused records for the seven refusals of the instance (screen inspect and submit, API inspect, import and pack, OCI layout screen and API), want 7"
check "FR-098: the import screen (disabled, inspect, submit), the import API, the FileSets screen and API, the OCI layout screen and API, tobby fileset pack and tobby import (exit 3) refuse with TBY-POL-005 naming $U_SET; nothing written or queued; one hardening.refused record each"

retriever hard-a hard-b
sync_ok "$U_SET"
recipe_landed hard-b "$DIG_hard_b" >/dev/null
check "FR-098: a recipe synchronization still writes the store under $U_SET (hard-b landed, class recipe)"

# -- 3. hardening.disableRegistryPush ------------------------------------------------------
hconfig false true
inc exec "$NODE" -- grep -q 'disableRegistryPush: true' /etc/tobby/config.yaml || fail "the switch is not in the configuration"
restart
login
dump_says false true || fail "FR-098: the configuration dump does not show $P_SET on"
startup_says false true || fail "FR-098: the startup record does not state $P_SET on"
inc exec "$NODE" -- sh -c "grep -F 'the embedded registry is read-only' $LOG | grep -qF '\"setting\":\"$P_SET\"'" ||
    fail "FR-098: no startup warning names $P_SET"
gauge_says 0 1 || fail "FR-098: tobby_hardening_enabled does not read disableUnitImport=0, disableRegistryPush=1"
curl -s $C -b "$JAR" -o "$WORK/content.html" "$BASE/content"
grep -q 'data-hardening="disableRegistryPush"' "$WORK/content.html" && grep -q "$P_SET" "$WORK/content.html" ||
    fail "FR-098: the content screen does not state that the registry is read-only, naming $P_SET"
check "FR-098: $P_SET stated by the dump, the startup record and its warning, the metric and the content screen"

REFUSED_BEFORE=$(refusals "$P_SET")
denied_by_policy() { # <label> <output file>
    grep -qi 'denied' "$2" && grep -q 'TBY-POL-006' "$2" ||
        fail "FR-098: $1 was not refused with DENIED carrying TBY-POL-006: $(tail -3 "$2")"
}
$D tag m7d/local:1 "$R/m7d/late:1.0.0"
if $D push "$R/m7d/late:1.0.0" >"$WORK/docker-push.out" 2>&1; then fail "FR-098: docker push by the administrator succeeded under $P_SET"; fi
denied_by_policy "docker push (administrator)" "$WORK/docker-push.out"
if helm push "$WORK/m7d-0.1.0.tgz" "oci://$R/m7d/late-charts" --ca-file "$CA" >"$WORK/helm-push.out" 2>&1; then
    fail "FR-098: helm push by the administrator succeeded under $P_SET"
fi
denied_by_policy "helm push (administrator)" "$WORK/helm-push.out"
if ( cd "$WORK" && oras push --ca-file "$CA" -u "$ADMIN" -p "$ADMIN_PW" "$R/m7d/late-oras:1.0.0" oras.txt:text/plain ) \
    >"$WORK/oras-push.out" 2>&1; then
    fail "FR-098: oras push by the administrator succeeded under $P_SET"
fi
denied_by_policy "oras push (administrator)" "$WORK/oras-push.out"
for repo in m7d/late m7d/late-charts/m7d m7d/late-oras; do
    [ "$(api -o /dev/null -w '%{http_code}' "$API/content/$repo")" = "404" ] || fail "FR-098: $repo landed despite the refusal"
done
# The wire itself: every write method, as the administrator.
wire() { # <label> <curl args…>: must answer 403 with the OCI DENIED error carrying TBY-POL-006
    _l="$1"; shift
    _c=$(basic -o "$WORK/wire.json" -w '%{http_code}' "$@")
    [ "$_c" = "403" ] && jq -e '.errors[0].code == "DENIED" and (.errors[0].message | startswith("TBY-POL-006"))' "$WORK/wire.json" >/dev/null ||
        fail "FR-098: $_l answered $_c $(cat "$WORK/wire.json"), want 403 DENIED TBY-POL-006"
}
wire "a blob upload" -X POST "$BASE/v2/m7d/raw/blobs/uploads/"
wire "a manifest put" -X PUT -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' -d '{}' "$BASE/v2/m7d/raw/manifests/1.0.0"
wire "a manifest delete" -X DELETE "$BASE/v2/m7d/docker/manifests/$DOCKER_DIG"
[ "$(digest_of m7d/docker 1.0.0)" = "$DOCKER_DIG" ] || fail "FR-098: the refused delete removed the manifest"
N=$(( $(refusals "$P_SET") - REFUSED_BEFORE ))
[ "$N" -ge 6 ] || fail "FR-094/FR-098: $N hardening.refused records for six refused pushes, want at least 6"
inc exec "$NODE" -- sh -c "grep -F '\"action\":\"hardening.refused\"' $LOG | grep -F '\"setting\":\"$P_SET\"' | grep -qF '\"actor\":\"$ADMIN\"'" ||
    fail "FR-094/FR-098: no hardening.refused record names the administrator"
check "FR-098: docker, helm and oras pushes by the administrator, and raw upload, put and delete, refused with DENIED/TBY-POL-006; nothing landed or vanished; each refusal audited, naming the administrator"

# Pulls are untouched.
$D rmi "$R/m7d/docker:1.0.0" >/dev/null 2>&1 || true
$D pull "$R/m7d/docker:1.0.0" >/dev/null 2>&1 || fail "FR-098: docker pull failed under $P_SET"
$D pull "$R/$REPO_A@$DIG_hard_a" >/dev/null 2>&1 || fail "FR-098: docker pull of the recipe-managed image failed under $P_SET"
$PM rmi "$R/m7d/podman:1.0.0" >/dev/null 2>&1 || true
$PM pull "$R/m7d/podman:1.0.0" >"$WORK/podman.out" 2>&1 || fail "FR-098: podman pull failed under $P_SET: $(tail -3 "$WORK/podman.out")"
rm -rf "$WORK/helm-out" "$WORK/oras-out"
mkdir -p "$WORK/helm-out"
helm pull "oci://$R/m7d/charts/m7d" --version 0.1.0 --ca-file "$CA" -d "$WORK/helm-out" >"$WORK/helm.out" 2>&1 ||
    fail "FR-098: helm pull failed under $P_SET: $(tail -3 "$WORK/helm.out")"
oras pull --ca-file "$CA" -u "$ADMIN" -p "$ADMIN_PW" -o "$WORK/oras-out" "$R/m7d/oras:1.0.0" >/dev/null 2>&1 ||
    fail "FR-098: oras pull failed under $P_SET"
skopeo inspect --cert-dir "$WORK/certs" --creds "$ADMIN:$ADMIN_PW" "docker://$R/m7d/skopeo:1.0.0" >/dev/null 2>&1 ||
    fail "FR-098: skopeo inspect failed under $P_SET"
retriever hard-a hard-b hard-c
sync_ok "$P_SET"
recipe_landed hard-c "$DIG_hard_c" >/dev/null
check "FR-098: docker (pushed and recipe-managed), podman, helm, oras and skopeo pulls, and a recipe synchronization (hard-c landed), still work under $P_SET"

cleanup
trap - EXIT
check "scenario m7 d-hardening complete — report: $REPORT"
