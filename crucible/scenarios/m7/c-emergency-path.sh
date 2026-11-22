#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, tracks C and E — the emergency path (R-21,
# NFR-021), followed LITERALLY from the published procedure: the scenario
# extracts the commands from the documentation page, so a procedure that
# drifts from the product fails here.
#
#   1. A local recipe is written with the published SDK and schemas
#      (`recipe lint` validates it), its digests copied from the content
#      browser (FR-062) of the Tobby instance holding the in-house image.
#   2. It is signed with an "organization emergency key" by the exact cosign
#      command of the page; Tobby signs nothing.
#   3. The zone instance admits it only under the dedicated, declared
#      emergency trust scope: the scope is visible in the configuration dump,
#      the plan and the task report name it, and the same recipe outside the
#      scope is refused with TBY-SIG-001 naming the scope that reserves the
#      emergency key.
#   4. No other trust root accepts the emergency key, and the emergency
#      scope accepts no other root. Closing the path means removing the
#      scope AND the emergency root, as the page says (B-057): a root that
#      no scope lists joins the default set and verifies every cookbook, so
#      with the scope removed and the root kept the recipe is still admitted
#      — under no scope — and so is its copy in the normal cookbook. That is
#      expected, and proved here as the reason for the rule; with both
#      removed, the recipe is refused with TBY-SIG-001.
#
# The page's blocks are run as written. Three things are bound to this
# zone, and nothing else is changed: the example host names
# (registry.example.com, registry.build.example.com) become the zone
# registry's address — in its canonical form, port written "_", inside the
# trust patterns, as the page says to write them; the digest placeholder
# becomes the digest copied from the content browser; the comment standing
# for "the zone's usual entries" becomes the zone's one usual entry. The
# operator's cosign gets --allow-insecure-registry, because the fixture
# registry speaks plain HTTP, and the operator's `tobby` reads a
# configuration that says so (registries.insecure).
#
# Every step checks its own manipulation before the verdict it prepares is
# read: the digest copied from the content browser is the one the registry
# serves, the signature verifies under the emergency key and NOT under the
# release key, each configuration is checked in the instance's own dump.
#
# Topology:
#   tbc-m7c-airgap  (container, tbc-airgap)  zone instance under test, no egress
#   tbc-m7c-zonereg (container, tbc-airgap)  the zone's registry and cookbooks
#                                            (a Tobby instance, authentication
#                                            off: a fixture, never a deployment)
#   tbc-m7c-canary  (container, tbc-net)     the egress canary's target
#   the crucible host                        the operator's workstation: cosign,
#                                            recipe lint, tobby recipe push
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/c-emergency-path.sh

# The jq programs are single-quoted on purpose: their $names are jq
# variables, bound with --arg.
# shellcheck disable=SC2016
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-c-emergency-path.txt}"
# shellcheck source=../../lib.sh
. "$DIR/../../lib.sh"
# shellcheck source=c-lib.sh
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
OPS="$WORK/ops"
NODE=tbc-m7c-airgap
ZONEREG=tbc-m7c-zonereg
CANARY=tbc-m7c-canary
LOG=/var/log/tobby.log
ADMIN=root
ADMIN_PW="m7c-crucible-admin-pw"
DOC="$ROOT/website/src/content/docs/docs/recipes/emergency-path.md"
HOTFIX=hotfix-gateway@1.4.2-hotfix.1
GATEWAY=gateway@1.4.2
cleanup() {
    inc delete --force "$NODE" "$ZONEREG" "$CANARY" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

# doc_block <heading prefix> <fence language> [<n>]: the n-th fenced block
# of that language in the section opened by the heading, its indentation
# (a block inside a list item) removed. Empty when the page carries none.
doc_block() {
    awk -v h="$1" -v lang="$2" -v n="${3:-1}" '
        index($0, h) == 1 { sec = 1; next }
        !sec { next }
        !code && /^##/ { exit }
        { line = $0; sub(/^ +/, "", line) }
        !code && line == "```" lang { k++; if (k == n) { code = 1; ind = length($0) - length(line) }; next }
        code && line == "```" { exit }
        code { s = $0; if (substr(s, 1, ind) ~ /^ *$/) s = substr(s, ind + 1); print s }
    ' "$DOC"
}

report_start
m7_tooling go
m7_build
( cd "$ROOT" && go build -o "$WORK/bin/recipe" github.com/tobby-fetch/recipe-spec/cmd/recipe ) ||
    fail "building recipe lint from the published SDK (github.com/tobby-fetch/recipe-spec, the version go.mod pins)"
check "built recipe lint from the published SDK, at the version go.mod pins"

# -- 0. The procedure, from the page ------------------------------------------------------
[ -f "$DOC" ] || fail "the emergency-path page is not at $DOC"
B_KEYS=$(doc_block '### 1. ' sh)
B_TRUST=$(doc_block '### 2. ' yaml)
B_DUMP=$(doc_block '### 2. ' sh)
B_RECIPE=$(doc_block '### 3. ' yaml)
B_LINT=$(doc_block '### 4. ' sh)
B_PUSH=$(doc_block '### 5. ' sh 1)
B_VERIFY=$(doc_block '### 5. ' sh 2)
B_RETRIEVER=$(doc_block '### 6. ' yaml)
B_PLAN=$(doc_block '### 6. ' sh 1)
B_SYNC=$(doc_block '### 6. ' sh 2)
B_CLOSED=$(doc_block '## After the emergency' yaml)
# What each block must be, for the scenario to be following this page and
# not a page that moved under it.
want() { # <block> <label> <fixed string>…
    _b="$1"; _l="$2"; shift 2
    for _s in "$@"; do
        printf '%s\n' "$_b" | grep -qF -- "$_s" || fail "the page's $_l block no longer carries '$_s': $(printf '%s' "$_b" | tr '\n' ' ')"
    done
}
want "$B_KEYS" "key pair (step 1)" "cosign generate-key-pair" "emergency.key" "emergency.pub"
want "$B_TRUST" "trust (step 2)" "trust:" "org-release-key" "org-emergency-key" "scopes:" "name: emergency" "registry.example.com/emergency/**" "roots: [org-emergency-key]"
want "$B_DUMP" "configuration check (step 2)" "tobby config dump"
want "$B_RECIPE" "recipe (step 3)" "kind: Recipe" "name: hotfix-gateway" "version: 1.4.2-hotfix.1" "registry.build.example.com/platform/gateway" "sha256:REPLACE-WITH-THE-DIGEST-OF-THE-INDEX"
want "$B_LINT" "lint (step 4)" "recipe lint --profile cooked hotfix-gateway.yaml"
want "$B_PUSH" "publish and sign (step 5)" "DIGEST=\$(tobby recipe push hotfix-gateway.yaml" "cosign sign --key emergency.key" "--tlog-upload=false" "registry.example.com/emergency/hotfix-gateway@\${DIGEST}"
want "$B_VERIFY" "verification (step 5)" "cosign verify --key emergency.pub"
want "$B_RETRIEVER" "Retriever (step 6)" "cookbook: registry.example.com/cookbook" "name: hotfix-gateway" "cookbook: registry.example.com/emergency" "usual entries"
want "$B_PLAN" "plan (step 6)" "tobby sync --dry-run --retriever ./retriever.yaml"
want "$B_SYNC" "synchronization (step 6)" "tobby sync --wait"
want "$B_CLOSED" "closed trust (after the emergency)" "trust:" "org-release-key"
if printf '%s\n' "$B_CLOSED" | grep -q 'emergency\|scopes'; then
    fail "the page's closed configuration still carries the emergency scope or root: $(printf '%s' "$B_CLOSED" | tr '\n' ' ')"
fi
check "procedure extracted from website/…/recipes/emergency-path.md: key pair, trust, configuration check, recipe, lint, publish and sign, verification, Retriever, plan, synchronization, closed trust"

# -- 1. The zone: canary first --------------------------------------------------------------
m7_launch "$CANARY" tbc-connected-node
m7_launch "$ZONEREG" tbc-airgap-node
m7_launch "$NODE" tbc-airgap-node
CANARY_IP=$(instance_ip "$CANARY")
ZONEREG_IP=$(instance_ip "$ZONEREG")
NODE_IP=$(instance_ip "$NODE")
m7_serve_source "$CANARY"
curl -fsS -o /dev/null "http://$CANARY_IP:8080/healthz" || fail "staging: the canary does not answer the crucible host"
for inst in "$NODE" "$ZONEREG"; do
    if inc exec "$inst" -- wget -q -T 5 -O /dev/null "http://$CANARY_IP:8080/healthz" >/dev/null 2>&1; then
        fail "$inst reached the connected zone: there is no air gap to rehearse"
    fi
    if inc exec "$inst" -- wget -q -T 5 -O /dev/null http://example.com/ >/dev/null 2>&1; then
        fail "$inst reached the internet"
    fi
done
m7_serve_source "$ZONEREG"
inc exec "$NODE" -- wget -q -T 5 -O /dev/null "http://$ZONEREG_IP:8080/healthz" >/dev/null 2>&1 ||
    fail "staging: the zone instance does not reach the zone registry"
check "egress canary failed as required: $CANARY ($CANARY_IP) answers the crucible host, not $NODE nor $ZONEREG; no route to the internet; the zone instance reaches the zone registry ($ZONEREG_IP)"

ZR="$ZONEREG_IP:8080"
ZR_CANON=$(printf '%s' "$ZR" | tr ':' '_')
API="http://$NODE_IP:8080/api/v1"
BASE="http://$NODE_IP:8080"
printf 'registries:\n  insecure:\n    - %s\n' "$ZR" >"$WORK/publish.yaml"

# sub_cmd / sub_cfg: the page's examples bound to this zone (see the header).
sub_cmd() {
    sed -e "s|registry\.build\.example\.com|$ZR|g" -e "s|registry\.example\.com|$ZR|g" \
        -e "s|sha256:REPLACE-WITH-THE-DIGEST-OF-THE-INDEX|${HOT:-}|g"
}
sub_cfg() { sed -e "s|registry\.example\.com|$ZR_CANON|g"; }

# ops <shell text>: a block of the page, run as the operator runs it, in the
# operator's working directory. A script of its own, under sh -e: a command
# of the block that fails stops it there, whatever the caller's context (a
# subshell on the left of || would run on with -e silently ignored).
mkdir -p "$OPS"
ops() {
    {
        printf 'COSIGN_BIN=%s\nTOBBY_BIN=%s\nPUBLISH=%s\n' "'$COSIGN'" "'$WORK/tobby'" "'$WORK/publish.yaml'"
        printf 'PATH=%s:$PATH\nCOSIGN_PASSWORD=\nexport PATH COSIGN_PASSWORD\ncd %s\n' "'$WORK/bin'" "'$OPS'"
        # shellcheck disable=SC2016 # written for the operator's shell
        printf '%s\n' \
            'cosign() {' \
            '    _sub="$1"; shift' \
            '    case "$_sub" in' \
            '        sign | verify) command "$COSIGN_BIN" "$_sub" --allow-insecure-registry "$@" ;;' \
            '        *) command "$COSIGN_BIN" "$_sub" "$@" ;;' \
            '    esac' \
            '}' \
            'tobby() { "$TOBBY_BIN" "$@" --config "$PUBLISH"; }'
        printf '%s\n' "$1"
    } >"$WORK/ops.sh"
    sh -e "$WORK/ops.sh"
}

reg_digest() { # <repo> <tag>: what the zone registry serves under that tag, read from the host
    curl -sS -I -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json' \
        "http://$ZR/v2/$1/manifests/$2" 2>/dev/null | awk 'tolower($1) == "docker-content-digest:" {print $2}' | tr -d '\r'
}

# -- 2. Prepare once: the key pair (step 1) ----------------------------------------------------
ops "$B_KEYS" >"$WORK/keys.out" 2>&1 || fail "the page's key-pair commands failed: $(tr '\n' ' ' <"$WORK/keys.out")"
[ -s "$OPS/emergency.key" ] && [ -s "$OPS/emergency.pub" ] || fail "step 1 left no emergency.key / emergency.pub"
[ ! -e "$OPS/cosign.key" ] || fail "step 1 left cosign.key behind"
# The zone's normal release key: the organization's pipeline, not the page.
mkdir -p "$WORK/release"
( cd "$WORK/release" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1 ) || fail "generating the release key pair"
check "step 1: the emergency key pair generated by the page's commands (emergency.key, emergency.pub); a separate release key pair for the normal pipeline"

# -- 3. The zone registry: the images, the normal cookbook ------------------------------------------
ORIG=$(m7_seed push "$ZR/platform/gateway:1.4.2") || fail "seeding the gateway image"
PUSHED=$(m7_seed push "$ZR/platform/gateway:1.4.2-hotfix.1") || fail "seeding the patched gateway image"
[ "$ORIG" != "$PUSHED" ] || fail "staging: the patched image has the original's digest"

# publish_signed <yaml> <ref:tag> <key file>: the validating push, then a
# signature by digest, checked under the matching public key. Prints the digest.
publish_signed() {
    _d=$("$WORK/tobby" recipe push "$1" "$2" --config "$WORK/publish.yaml" 2>"$WORK/push.err") ||
        fail "publishing $2: $(tr '\n' ' ' <"$WORK/push.err")"
    _r="${2%:*}"
    COSIGN_PASSWORD='' "$COSIGN" sign --key "$3" --yes --allow-insecure-registry \
        --use-signing-config=false --tlog-upload=false "$_r@$_d" >/dev/null 2>&1 || fail "signing $_r@$_d"
    "$COSIGN" verify --key "${3%.key}.pub" --allow-insecure-registry --insecure-ignore-tlog=true \
        "$_r@$_d" >/dev/null 2>&1 || fail "staging: $_r@$_d does not verify under ${3%.key}.pub"
    printf '%s' "$_d"
}
cat >"$WORK/gateway.yaml" <<EOF
apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Recipe
metadata:
  name: gateway
  version: 1.4.2
spec:
  ingredients:
    - name: gateway
      kind: ContainerImage
      ref: $ZR/platform/gateway
      version: 1.4.2
      digest: $ORIG
      platforms: ["linux/amd64"]
EOF
publish_signed "$WORK/gateway.yaml" "$ZR/cookbook/gateway:1.4.2" "$WORK/release/cosign.key" >/dev/null
check "zone registry $ZR: platform/gateway 1.4.2 ($ORIG) and the in-house 1.4.2-hotfix.1 ($PUSHED); the normal cookbook carries gateway 1.4.2, signed with the release key"

# -- 4. The zone instance (step 2) ------------------------------------------------------------------
inc exec "$NODE" -- mkdir -p /etc/tobby/keys /var/lib/tobby /var/lib/tobby-state
inc file push "$WORK/release/cosign.pub" "$NODE/etc/tobby/keys/org-release.pub" >/dev/null
inc file push "$OPS/emergency.pub" "$NODE/etc/tobby/keys/emergency.pub" >/dev/null
# node_config <trust section>: the instance's configuration around it.
node_config() {
    {
        cat <<EOF
mode: mirror
zone: zone-ep
storage:
  root: /var/lib/tobby
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
registries:
  insecure:
    - $ZR
retriever:
  source: /etc/tobby/retriever.yaml
EOF
        printf '%s\n' "$1"
    } >"$WORK/config.yaml"
    inc file push "$WORK/config.yaml" "$NODE/etc/tobby/config.yaml" >/dev/null
    [ "$(sha256sum <"$WORK/config.yaml" | cut -d' ' -f1)" = "$(inc exec "$NODE" -- sha256sum /etc/tobby/config.yaml | cut -d' ' -f1)" ] ||
        fail "staging: the configuration on the node is not the one written"
}
TRUST_OPEN=$(printf '%s\n' "$B_TRUST" | sub_cfg)
node_config "$TRUST_OPEN"
# retriever <file>: the configured Retriever (the zone's desired state).
retriever() {
    inc file push "$1" "$NODE/etc/tobby/retriever.yaml" >/dev/null
    [ "$(sha256sum <"$1" | cut -d' ' -f1)" = "$(inc exec "$NODE" -- sha256sum /etc/tobby/retriever.yaml | cut -d' ' -f1)" ] ||
        fail "staging: the Retriever on the node is not the one written"
}
retriever_doc() { # <file> <recipe…>: a Retriever on the normal cookbook, each entry from it
    _f="$1"; shift
    {
        printf 'apiVersion: recipe.tobbyfetch.dev/v1alpha1\nkind: Retriever\nmetadata:\n  name: zone-ep\nspec:\n'
        printf '  cookbook: %s/cookbook\n  recipes:\n' "$ZR"
        for _e in "$@"; do printf '    - name: %s\n      version: "%s"\n' "${_e%@*}" "${_e#*@}"; done
    } >"$_f"
}
retriever_doc "$WORK/usual.yaml" "$GATEWAY"
retriever "$WORK/usual.yaml"

if inc exec "$NODE" -- grep -q 'disabled' /etc/tobby/config.yaml; then
    fail "staging: authentication is disabled on the zone instance"
fi
m7_admin "$NODE" "$ADMIN" "$ADMIN_PW"
m7_start "$NODE" "$BASE"
OPERATOR=$(curl -fsS -u "$ADMIN:$ADMIN_PW" -H 'Content-Type: application/json' \
    -d '{"name":"m7c-ep-operator","role":"operator"}' "$API/tokens" | jq -r '.secret // empty')
case "$OPERATOR" in tby_*) ;; *) fail "creating the operator API token failed" ;; esac
AUTHZ="Authorization: Bearer $OPERATOR"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/recipes")
[ "$CODE" = "401" ] || fail "an anonymous API call answered $CODE: authentication is not on"

# The page's own check of the result.
inc exec "$NODE" -- sh -c "$B_DUMP" >"$WORK/dump.txt" 2>&1 || fail "the page's configuration check failed: $(cat "$WORK/dump.txt")"
for s in "name: emergency" "- $ZR_CANON/emergency/**" "- org-emergency-key" "name: org-release-key"; do
    grep -qF -- "$s" "$WORK/dump.txt" || fail "R-21: the page's configuration check does not show '$s': $(tr '\n' ' ' <"$WORK/dump.txt")"
done
inc exec "$NODE" -- tobby config dump --output json >"$WORK/dump.json" 2>/dev/null || fail "tobby config dump --output json failed"
jq -e --arg p "$ZR_CANON/emergency/**" '.trust.scopes == [{"name": "emergency", "repositories": [$p], "roots": ["org-emergency-key"]}]
    and ([.trust.roots[].name] == ["org-release-key", "org-emergency-key"])' "$WORK/dump.json" >/dev/null ||
    fail "R-21: the effective configuration is not the page's trust section: $(jq -c '.trust' "$WORK/dump.json")"
check "step 2: zone instance (mirror, authentication on, anonymous 401) with the page's trust section; its configuration check shows the emergency scope ($ZR_CANON/emergency/**, roots [org-emergency-key]) beside both roots"

# The zone before the emergency: the usual entry, from the normal cookbook.
SYNC_OUT=$(inc exec "$NODE" -- env TOBBY_API_TOKEN="$OPERATOR" sh -c "$B_SYNC" 2>/dev/null) ||
    fail "the zone's usual synchronization failed: $SYNC_OUT"
check "the zone's usual synchronization (gateway 1.4.2, release key, default set) succeeds"

# -- 5. When the day comes: the recipe (steps 3 and 4) -------------------------------------------------
# The digest is copied from the content browser of the instance holding the
# image — its Content screen (the copy button) and GET /api/v1/content —
# and checked against what the registry serves before anything uses it.
curl -fsS "http://$ZR/content/platform/gateway" >"$WORK/content.html" || fail "the Content screen of $ZONEREG did not answer"
SCREEN=$(tr '\n' ' ' <"$WORK/content.html" | awk -v RS='<tr' 'index($0, ">1.4.2-hotfix.1<") {print; exit}' |
    grep -o 'data-copy="sha256:[0-9a-f]*"' | head -1 | sed 's/data-copy="\(.*\)"/\1/')
HOT=$(curl -fsS "http://$ZR/api/v1/content/platform/gateway" | jq -r '.tags[] | select(.tag == "1.4.2-hotfix.1") | .digest')
[ -n "$HOT" ] && [ "$HOT" = "$SCREEN" ] || fail "FR-062: the Content screen ('$SCREEN') and GET /api/v1/content ('$HOT') disagree on the digest"
[ "$HOT" = "$PUSHED" ] && [ "$HOT" = "$(reg_digest platform/gateway 1.4.2-hotfix.1)" ] ||
    fail "staging: the content browser's digest $HOT is not the image pushed ($PUSHED) and served"
printf '%s\n' "$B_RECIPE" | sub_cmd >"$OPS/hotfix-gateway.yaml"
if grep -Eq 'registry(\.build)?\.example\.com|REPLACE-WITH' "$OPS/hotfix-gateway.yaml"; then
    fail "staging: the recipe still carries an example value: $(tr '\n' ' ' <"$OPS/hotfix-gateway.yaml")"
fi
if ! grep -qF "ref: $ZR/platform/gateway" "$OPS/hotfix-gateway.yaml" || ! grep -qF "digest: $HOT" "$OPS/hotfix-gateway.yaml"; then
    fail "staging: the recipe does not pin $ZR/platform/gateway@$HOT"
fi
ops "$B_LINT" >"$WORK/lint.out" 2>&1 || fail "R-21: the page's lint refuses the recipe: $(tr '\n' ' ' <"$WORK/lint.out")"
# Control: the same lint refuses a draft, so its verdict above is a verdict.
grep -v '^ *digest:' "$OPS/hotfix-gateway.yaml" >"$OPS/draft.yaml"
if ( cd "$OPS" && "$WORK/bin/recipe" lint --profile cooked draft.yaml ) >/dev/null 2>&1; then
    fail "staging: recipe lint --profile cooked admits a recipe without digest: its verdict proves nothing"
fi
check "steps 3-4: the page's recipe, its digest copied from the content browser ($HOT: Content screen copy button = API = registry); the page's lint (published SDK, cooked profile) admits it and refuses its draft"

# -- 6. Publish, then sign by digest (step 5) -------------------------------------------------------------
ops "$(printf '%s\n' "$B_PUSH" | sub_cmd)
printf '%s' \"\$DIGEST\" >digest.txt" >"$WORK/push.out" 2>&1 || fail "R-21: the page's publish-and-sign commands failed: $(tr '\n' ' ' <"$WORK/push.out")"
DIGEST=$(cat "$OPS/digest.txt")
case "$DIGEST" in sha256:*) ;; *) fail "tobby recipe push printed no digest: '$DIGEST'" ;; esac
[ "$(reg_digest emergency/hotfix-gateway 1.4.2-hotfix.1)" = "$DIGEST" ] ||
    fail "staging: the emergency cookbook does not serve $DIGEST under hotfix-gateway:1.4.2-hotfix.1"
ops "DIGEST='$DIGEST'
$(printf '%s\n' "$B_VERIFY" | sub_cmd)" >"$WORK/verify.out" 2>&1 ||
    fail "R-21: the page's verification of the signature failed: $(tr '\n' ' ' <"$WORK/verify.out")"
if "$COSIGN" verify --key "$WORK/release/cosign.pub" --allow-insecure-registry --insecure-ignore-tlog=true \
    "$ZR/emergency/hotfix-gateway@$DIGEST" >/dev/null 2>&1; then
    fail "staging: the emergency recipe also verifies under the release key"
fi
check "step 5: the page's tobby recipe push published $ZR/emergency/hotfix-gateway:1.4.2-hotfix.1 ($DIGEST) and its cosign sign signed it by digest; the page's cosign verify accepts it under emergency.pub, the release key does not"

# The two negatives of step 4, published now: the very same recipe in the
# normal cookbook, signed with the emergency key; and a recipe in the
# emergency cookbook signed with the release key.
SAME=$(publish_signed "$OPS/hotfix-gateway.yaml" "$ZR/cookbook/hotfix-gateway:1.4.2-hotfix.1" "$OPS/emergency.key")
[ "$SAME" = "$DIGEST" ] || fail "staging: the recipe published in the normal cookbook is not the same artifact ($SAME, $DIGEST)"
sed 's/^  name: hotfix-gateway$/  name: hotfix-release-signed/' "$OPS/hotfix-gateway.yaml" >"$WORK/release-signed.yaml"
grep -q '^  name: hotfix-release-signed$' "$WORK/release-signed.yaml" || fail "staging: renaming the release-signed variant"
publish_signed "$WORK/release-signed.yaml" "$ZR/emergency/hotfix-release-signed:1.4.2-hotfix.1" "$WORK/release/cosign.key" >/dev/null
check "negatives published: the same recipe ($SAME) in the normal cookbook signed with the emergency key; hotfix-release-signed in the emergency cookbook signed with the release key"

# -- 7. Admit it (step 6) ------------------------------------------------------------------------------
{
    printf 'apiVersion: recipe.tobbyfetch.dev/v1alpha1\nkind: Retriever\nmetadata:\n  name: zone-ep\n'
    printf '%s\n' "$B_RETRIEVER" | sub_cmd | awk '/usual entries/ {
        match($0, /^ */); p = substr($0, 1, RLENGTH)
        print p "- name: gateway"; print p "  version: \"1.4.2\""; next } { print }'
} >"$WORK/retriever.yaml"
if ! grep -qF "cookbook: $ZR/emergency" "$WORK/retriever.yaml" || ! grep -q '^    - name: gateway$' "$WORK/retriever.yaml"; then
    fail "staging: the candidate Retriever is not the page's plus the usual entry: $(tr '\n' ' ' <"$WORK/retriever.yaml")"
fi
inc file push "$WORK/retriever.yaml" "$NODE/root/retriever.yaml" >/dev/null

# Plan before promoting: the page's command, in the directory holding the candidate.
RC=0
inc exec "$NODE" -- sh -c "cd /root && $B_PLAN" >"$WORK/plan.txt" 2>&1 || RC=$?
[ "$RC" = "5" ] || fail "R-21: the page's plan exited $RC, not 5 (changes planned): $(tr '\n' ' ' <"$WORK/plan.txt")"
# NAME REQUESTED RESOLVED SIGNATURE SCOPE TRANSFER
ROW_H=$(awk '$1 == "hotfix-gateway" {print $4 "/" $5}' "$WORK/plan.txt")
ROW_G=$(awk '$1 == "gateway" {print $4 "/" $5}' "$WORK/plan.txt")
[ "$ROW_H" = "verified/emergency" ] && [ "$ROW_G" = "verified/-" ] ||
    fail "R-21: the plan does not show hotfix-gateway verified under emergency and gateway verified under no scope: $(tr '\n' ' ' <"$WORK/plan.txt")"
inc exec "$NODE" -- sh -c "cd /root && tobby sync --dry-run --retriever ./retriever.yaml --output json" >"$WORK/plan.json" 2>/dev/null || true
jq -e '.outcome == "changes-planned"
    and any(.recipes[]; .name == "hotfix-gateway" and .signature == "verified" and .trust_scope == "emergency")
    and any(.recipes[]; .name == "gateway" and .signature == "verified" and (.trust_scope // "") == "")' "$WORK/plan.json" >/dev/null ||
    fail "R-21: the JSON plan does not carry the emergency scope: $(jq -c '[.recipes[]? | {name, signature, trust_scope}]' "$WORK/plan.json")"
check "step 6, plan: exit 5 (changes planned); hotfix-gateway verified, SCOPE emergency; gateway verified, no scope — the same in --output json (trust_scope)"

retriever "$WORK/retriever.yaml"
LINES0=$(inc exec "$NODE" -- sh -c "wc -l <$LOG" | tr -d ' ')
SYNC_OUT=$(inc exec "$NODE" -- env TOBBY_API_TOKEN="$OPERATOR" sh -c "$B_SYNC" 2>&1) ||
    fail "R-21: the page's synchronization failed: $SYNC_OUT"
TASK=$(printf '%s\n' "$SYNC_OUT" | awk '$1 == "task" && $3 == "on" {print $2; exit}')
[ -n "$TASK" ] || fail "the synchronization printed no task: $SYNC_OUT"
curl -fsS -H "$AUTHZ" "$API/tasks/$TASK" >"$WORK/task.json" || fail "reading task $TASK"
resq() { jq -e --arg r "$1" "[.task.resolutions[] | select(.recipe == \$r and (.ingredient // \"\") == \"\")] | length == 1 and (.[0] | $2)" "$WORK/task.json" >/dev/null; }
jq -e '.task.status == "done"' "$WORK/task.json" >/dev/null || fail "R-21: the synchronization task is not done: $(jq -c '.task.status' "$WORK/task.json")"
resq "$HOTFIX" ".trust_scope == \"emergency\" and .digest == \"$DIGEST\"" ||
    fail "R-21: the task report does not name the emergency scope on $HOTFIX: $(jq -c '.task.resolutions' "$WORK/task.json")"
resq "$GATEWAY" '(.trust_scope // "") == ""' || fail "R-21: the usual entry is reported under a scope: $(jq -c '.task.resolutions' "$WORK/task.json")"
jq -e --arg i "$HOTFIX/gateway" '.task.items[] | select(.name == $i) | .status == "done"' "$WORK/task.json" >/dev/null ||
    fail "R-21: the emergency ingredient did not cross: $(jq -c '[.task.items[] | {name, status}]' "$WORK/task.json")"
curl -fsS -H "$AUTHZ" "$API/recipes" >"$WORK/recipes.json" || fail "GET /api/v1/recipes"
jq -e 'any(.recipes[]; .name == "hotfix-gateway" and .version == "1.4.2-hotfix.1" and .verified == true and .trustScope == "emergency")
    and any(.recipes[]; .name == "gateway" and .verified == true and (.trustScope // "") == "")' "$WORK/recipes.json" >/dev/null ||
    fail "R-21: GET /api/v1/recipes does not record hotfix-gateway verified under emergency: $(jq -c '[.recipes[] | {name, version, verified, trustScope}]' "$WORK/recipes.json")"
curl -fsS -H "$AUTHZ" "$API/content/$ZR_CANON/platform/gateway" >"$WORK/zone-content.json" || fail "the zone instance does not hold $ZR_CANON/platform/gateway"
jq -e --arg d "$HOT" 'any(.tags[]; .tag == "1.4.2-hotfix.1" and .digest == $d)' "$WORK/zone-content.json" >/dev/null ||
    fail "R-21: the zone instance does not hold the patched image at $HOT: $(jq -c '.tags' "$WORK/zone-content.json")"
inc exec "$NODE" -- sh -c "tail -n +$((LINES0 + 1)) $LOG" >"$WORK/run.log"
jq -Rce --arg d "$DIGEST" 'fromjson? | select(.msg == "recipe verified" and .digest == $d and .trust_scope == "emergency")' "$WORK/run.log" >/dev/null ||
    fail "R-21: the run log does not record the recipe verified under the emergency scope"
jq -Rce 'fromjson? | select(.log_type == "audit" and .action == "sync.create" and .actor == "m7c-ep-operator" and .outcome == "success")' "$WORK/run.log" >/dev/null ||
    fail "FR-094: the synchronization has no sync.create audit record by the operator token"
check "step 6, synchronization (task $TASK, done): $HOTFIX verified under trust_scope emergency in the task report, the run log and GET /api/v1/recipes (trustScope emergency); the patched image held at $HOT; gateway under no scope; sync.create audited"

# -- 8. Outside the scope, and inside it with another key --------------------------------------------------
retriever_doc "$WORK/negatives.yaml" "$GATEWAY" "$HOTFIX"
printf '    - name: hotfix-release-signed\n      version: "1.4.2-hotfix.1"\n      cookbook: %s/emergency\n' "$ZR" >>"$WORK/negatives.yaml"
retriever "$WORK/negatives.yaml"
ID=$(curl -fsS -H "$AUTHZ" -H 'Content-Type: application/json' -d '{"prune":false}' "$API/sync" | jq -r '.task.id // empty')
[ -n "$ID" ] || fail "the API did not start a synchronization"
ST=$(m7_wait_task "$API" "$ID" "$WORK/neg.json" -H "$AUTHZ")
[ "$ST" = "failed" ] || fail "R-21: a synchronization carrying two wrongly signed recipes ended $ST"
itemq() { jq -e --arg n "$1" ".task.items[] | select(.name == \$n) | $2" "$WORK/neg.json" >/dev/null; }
itemq "$HOTFIX" '.status == "failed" and .error.code == "TBY-SIG-001" and (.error.params.scope // "") == ""
    and (.error.params.reserved | startswith("org-emergency-key (")) and .error.params.reservedto == "emergency"' ||
    fail "B-057: the emergency-signed recipe outside the scope is not refused naming org-emergency-key reserved to emergency: $(jq -c '[.task.items[] | {name, status, error}]' "$WORK/neg.json")"
itemq "hotfix-release-signed@1.4.2-hotfix.1" '.status == "failed" and .error.code == "TBY-SIG-001" and .error.params.scope == "emergency"
    and (.error.params.reserved | startswith("org-release-key (")) and (.error.params.reservedto // "") == ""' ||
    fail "FR-033: the release-signed recipe inside the emergency scope is not refused by that scope: $(jq -c '[.task.items[] | {name, status, error}]' "$WORK/neg.json")"
itemq "$GATEWAY/recipe" '.status == "done" or .status == "skipped"' || fail "the usual entry did not cross beside the refusals"
# The message as an operator reads it: the task screen, in a browser session.
JAR="$WORK/jar"
CODE=$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' --data-urlencode "username=$ADMIN" \
    --data-urlencode "password=$ADMIN_PW" --data-urlencode next=/ "$BASE/login")
[ "$CODE" = "303" ] || fail "signing in to the web interface answered $CODE"
curl -fsS -b "$JAR" "$BASE/tasks/$ID" >"$WORK/neg.html" || fail "the task screen of $ID did not answer"
for s in "org-emergency-key (sha256:" "reserved to the trust scope emergency" "by the trust scope emergency" "org-release-key (sha256:"; do
    grep -qF -- "$s" "$WORK/neg.html" || fail "TBY-SIG-001: the task screen does not say '$s'"
done
check "step 4 negatives (task $ID): the same recipe in the normal cookbook is refused, TBY-SIG-001 naming org-emergency-key reserved to the trust scope emergency; a release-signed recipe in the emergency cookbook is refused by the scope emergency (the release key does not apply there); the usual entry crosses; the task screen says both"

# -- 9. After the emergency: closing the path ------------------------------------------------------------------
# restart_with <trust section> <label>: the configuration, checked in the
# instance's own dump, then a fresh process.
restart_with() {
    m7_stop "$NODE"
    node_config "$1"
    inc exec "$NODE" -- tobby config dump --output json >"$WORK/dump.json" 2>/dev/null || fail "tobby config dump ($2)"
    m7_start "$NODE" "$BASE"
}
sync_task() { # <retriever file> <out>: one synchronization through the API, its status printed
    retriever "$1"
    _id=$(curl -fsS -H "$AUTHZ" -H 'Content-Type: application/json' -d '{"prune":false}' "$API/sync" | jq -r '.task.id // empty')
    [ -n "$_id" ] || fail "the API did not start a synchronization"
    m7_wait_task "$API" "$_id" "$2" -H "$AUTHZ"
}

# (a) The scope removed, the emergency root kept: the WRONG way to close.
TRUST_A=$(printf '%s\n' "$TRUST_OPEN" | awk '/^  scopes:/ {exit} {print}')
restart_with "$TRUST_A" "scope removed, root kept"
jq -e '((.trust.scopes // []) | length == 0) and ([.trust.roots[].name] == ["org-release-key", "org-emergency-key"])' "$WORK/dump.json" >/dev/null ||
    fail "staging: the configuration is not 'scope removed, both roots kept': $(jq -c '.trust' "$WORK/dump.json")"
ST=$(sync_task "$WORK/retriever.yaml" "$WORK/a1.json")
[ "$ST" = "done" ] || fail "B-057 (a): with the scope removed and the root kept, the page's Retriever ended $ST: $(jq -c '[.task.items[] | {name, error}]' "$WORK/a1.json")"
jq -e --arg r "$HOTFIX" '[.task.resolutions[] | select(.recipe == $r and (.ingredient // "") == "")] | length == 1 and ((.[0].trust_scope // "") == "")' "$WORK/a1.json" >/dev/null ||
    fail "B-057 (a): the emergency recipe is not admitted under no scope: $(jq -c '.task.resolutions' "$WORK/a1.json")"
retriever_doc "$WORK/a2.yaml" "$GATEWAY" "$HOTFIX"
ST=$(sync_task "$WORK/a2.yaml" "$WORK/a2.json")
[ "$ST" = "done" ] || fail "B-057 (a): the emergency-signed copy in the normal cookbook was refused ($ST) although the root is in the default set"
check "after (a), the scope removed and the emergency root kept: the emergency recipe is still admitted — under no scope — and so is its emergency-signed copy in the NORMAL cookbook: an unlisted root joins the default set (B-057). Expected; it is why the page closes the path by removing both"

# (b) The page's closed configuration: scope and root removed.
restart_with "$B_CLOSED" "scope and root removed"
jq -e '((.trust.scopes // []) | length == 0) and ([.trust.roots[].name] == ["org-release-key"])' "$WORK/dump.json" >/dev/null ||
    fail "staging: the configuration is not the page's closed one: $(jq -c '.trust' "$WORK/dump.json")"
ST=$(sync_task "$WORK/retriever.yaml" "$WORK/b.json")
[ "$ST" = "failed" ] || fail "R-21 (b): with the scope and the root removed, the page's Retriever ended $ST"
jq -e --arg n "$HOTFIX" '.task.items[] | select(.name == $n) | .status == "failed" and .error.code == "TBY-SIG-001"
    and (.error.params.scope // "") == "" and (.error.params.reserved // "") == ""' "$WORK/b.json" >/dev/null ||
    fail "R-21 (b): the emergency recipe is not refused with TBY-SIG-001: $(jq -c '[.task.items[] | {name, status, error}]' "$WORK/b.json")"
jq -e --arg n "$GATEWAY/recipe" '.task.items[] | select(.name == $n) | .status == "done" or .status == "skipped"' "$WORK/b.json" >/dev/null ||
    fail "R-21 (b): the usual entry did not cross once the path was closed"
check "after (b), the page's closed trust (org-release-key alone): the emergency recipe is refused with TBY-SIG-001 under the default set, no root of the zone verifying it; the usual entry still crosses"

# Tobby signs nothing: no private key ever reached a Tobby host.
for inst in "$NODE" "$ZONEREG"; do
    KEYS=$(inc exec "$inst" -- sh -c 'find / -xdev -name "*.key" 2>/dev/null' || true)
    [ -z "$KEYS" ] || fail "a private key lies on $inst: $KEYS"
done
check "no private key on the zone instance nor on the zone registry: the signatures were all made on the operator's workstation"

cleanup
trap - EXIT
check "scenario m7 c-emergency-path complete — report: $REPORT"
