#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track C — provenance and trust, content by
# content (FR-098, R-17), on an instance with authentication ON, the pinned
# scanner and its database delivered by the tobby-system channel:
#
#   1. One store receives a recipe-managed repository (signed recipe), one
#      admitted under a relaxed scope (unsigned recipe, scope "lab"), one
#      unit import, one packed FileSet and one repository pushed by a
#      standard client. Each arrival is CHECKED on its own before any class
#      is read: the synchronization ran with the scanner armed and scanned
#      the images, the imported and packed tags hold the digests their
#      operations reported, the pushed manifest is served back by digest.
#   2. Each shows its class in the API (listing and repository detail) and
#      in the content browser (listing and repository page), the relaxed
#      one with its reduced-assurance marker; the scanned tags carry their
#      scan date — the very date of the task report that scanned them —
#      with the database age; the unscanned ones carry none.
#   3. The unmanaged-content metric counts two unit-import and one seed.
#   4. The log records of the push, the unit import and the packing carry
#      the provenance field.
#
# The two hardening switches of FR-098 (disableUnitImport,
# disableRegistryPush) are proved on every surface by d-hardening.sh.
#
# Topology:
#   tbc-m7p-source (container, tbc-net)  source registry, cookbook, channel
#   tbc-m7p-node   (container, tbc-net)  instance under test (mirror, auth on)
#   the crucible host                    API client, browser (curl), OCI client
#
# Host tooling on top of the crucible baseline: oras (the database the
# connected zone publishes is fetched with it) and the pinned Trivy release,
# from CRUCIBLE_TRIVY_ARCHIVE or fetched once and checked (as m6 a-scanning).
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/c-provenance.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-c-provenance.txt}"
# shellcheck source=../../lib.sh
. "$DIR/../../lib.sh"
# shellcheck source=c-lib.sh
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m7p-source
NODE=tbc-m7p-node
STORE=/var/lib/tobby
LOG=/var/log/tobby.log
ADMIN=root
ADMIN_PW="m7p-crucible-admin-pw"
cleanup() {
    inc delete --force "$SOURCE" "$NODE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
m7_tooling oras
m7_build

# -- 1. Nodes and fixtures ------------------------------------------------------------
m7_launch "$SOURCE" tbc-connected-node
m7_launch "$NODE" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
S="$SOURCE_IP:8080"
CANON=$(printf '%s' "$S" | tr ':' '_')
m7_serve_source "$SOURCE"

m7_scanner_fixtures "$S"
m7_keypair
APP_DIG=$(m7_seed push "$S/docker.io/example/prov-app:1.0") || fail "seeding prov-app"
LAB_DIG=$(m7_seed push "$S/docker.io/example/prov-lab:1.0") || fail "seeding prov-lab"
IMP_DIG=$(m7_seed push "$S/library/prov-imp:1.0.0") || fail "seeding the unit-import source"
m7_publish "$S" tobby-system "$(m7_system_recipe)" sign >/dev/null
m7_publish "$S" prov-app "$(m7_image_recipe prov-app docker.io/example/prov-app "$APP_DIG")" sign >/dev/null
LAB_RD=$(m7_publish "$S" lab-app "$(m7_image_recipe lab-app docker.io/example/prov-lab "$LAB_DIG")")
# The relaxed scope is only a relaxation if lab-app really is unsigned: read
# from the registry (no signature tag) and from cosign, not assumed.
CODE=$(curl -s -o /dev/null -w '%{http_code}' \
    "http://$S/v2/cookbook/lab-app/manifests/sha256-$(printf '%s' "$LAB_RD" | cut -d: -f2).sig")
[ "$CODE" = "404" ] || fail "lab-app has a signature tag in the source registry (HTTP $CODE): it is not unsigned"
if "$COSIGN" verify --key "$WORK/cosign.pub" --allow-insecure-registry --insecure-ignore-tlog=true \
    "$S/cookbook/lab-app@$LAB_RD" >/dev/null 2>&1; then
    fail "lab-app verifies under the zone key: it is not unsigned"
fi
check "fixtures: tobby-system and prov-app signed (cosign agrees), lab-app unsigned (no signature tag, cosign refuses)"

m7_retriever "$WORK/retriever.yaml" "$S/cookbook" tobby-system prov-app lab-app
inc file push "$WORK/retriever.yaml" "$NODE/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/cosign.pub" "$NODE/etc/tobby/trust.pub" --create-dirs >/dev/null
inc exec "$NODE" -- sh -c "mkdir -p /srv/pack/notes && printf 'release notes\n' >/srv/pack/notes/README"
inc exec "$NODE" -- sh -c "cat >/etc/tobby/config.yaml <<'EOF'
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
    docker.io: $S
    ghcr.io: $S
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: zone
      keyFile: /etc/tobby/trust.pub
  scopes:
    - name: lab
      repositories: [\"$CANON/cookbook/lab-*\"]
      allowUnsigned: true
scan:
  mode: warn
files:
  packRoots:
    - /srv/pack
EOF"
# No zone: a mirror on the source side (the FR-054 serving gate is for a
# destination holding a medium, and this store gets a media manifest).
if inc exec "$NODE" -- grep -q '^zone:' /etc/tobby/config.yaml; then
    fail "the instance under test declares a zone: its /v2/ would be gated"
fi
if inc exec "$NODE" -- grep -q 'disabled: true' /etc/tobby/config.yaml; then
    fail "authentication is disabled on the instance under test"
fi
m7_admin "$NODE" "$ADMIN" "$ADMIN_PW"
BASE="http://$NODE_IP:8080"
API="$BASE/api/v1"
m7_start "$NODE" "$BASE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/content")
[ "$CODE" = "401" ] || fail "an anonymous API call answered $CODE: authentication is not on"
TOKEN=$(curl -fsS -u "$ADMIN:$ADMIN_PW" -H 'Content-Type: application/json' \
    -d '{"name":"m7p","role":"admin"}' "$API/tokens" | jq -r '.secret // empty')
case "$TOKEN" in tby_*) ;; *) fail "creating the admin API token failed" ;; esac
AUTHZ="Authorization: Bearer $TOKEN"
api() { curl -s -H "$AUTHZ" "$@"; }
check "instance under test: mirror, authentication on (anonymous 401), admin token issued"

# 1a. The recipe synchronization, scanner armed in the same run (tobby-system
# first). Its own result is checked before any class is read.
T0=$(date -u +%s)
SYNC_ID=$(api -X POST -H 'Content-Type: application/json' -d '{}' "$API/sync" | jq -r '.task.id // empty')
[ -n "$SYNC_ID" ] || fail "the API did not start a synchronization"
ST=$(m7_wait_task "$API" "$SYNC_ID" "$WORK/sync.json" -H "$AUTHZ")
T1=$(date -u +%s)
[ "$ST" = "done" ] || fail "the synchronization ended $ST: $(jq -c '[.task.items[] | {name, status, error}]' "$WORK/sync.json")"
METRICS=$(curl -fsS "$BASE/metrics")
[ "$(m7_metric "$METRICS" tobby_scan_scanner_available)" = "1" ] || fail "staging: the channel-delivered scanner is not available"
[ "$(m7_metric "$METRICS" tobby_scan_db_present)" = "1" ] || fail "staging: the delivered database is not present"
item() { jq -r --arg n "$1" ".task.items[] | select(.name == \$n) | $2" "$WORK/sync.json"; }
for it in prov-app@1.0.0/app lab-app@1.0.0/app; do
    [ -n "$(item "$it" '.scan.scanned_at // empty')" ] ||
        fail "staging: $it was not scanned: $(item "$it" '.scan')"
done
[ "$(item lab-app@1.0.0/app '.assurance.scope // empty')" = "lab" ] ||
    fail "staging: lab-app was not admitted under the relaxed scope: $(item lab-app@1.0.0/app '.assurance')"
RECIPES=$(inc exec "$NODE" -- cat "$STORE/meta/recipes.json")
APP_REPO=$(printf '%s' "$RECIPES" | jq -r '.recipes["prov-app@1.0.0"].ingredients[] | select(.name == "app") | .repo')
LAB_REPO=$(printf '%s' "$RECIPES" | jq -r '.recipes["lab-app@1.0.0"].ingredients[] | select(.name == "app") | .repo')
[ -n "$APP_REPO" ] && [ -n "$LAB_REPO" ] && [ "$APP_REPO" != "$LAB_REPO" ] ||
    fail "staging: the recipe graph does not record two distinct ingredient repositories ($APP_REPO, $LAB_REPO)"
for pair in "$APP_REPO $APP_DIG" "$LAB_REPO $LAB_DIG"; do
    # shellcheck disable=SC2086 # split on purpose: one "<repo> <digest>" pair
    set -- $pair
    api "$API/content/$1" | jq -e --arg d "$2" '[.tags[] | select(.digest == $d)] | length == 1' >/dev/null ||
        fail "staging: $1 does not hold the pinned digest $2"
done
check "1a: synchronization done with the scanner armed; prov-app and lab-app (scope lab) scanned and stored at their pinned digests ($APP_REPO, $LAB_REPO)"

# 1b. A unit import through the API (FR-023).
IMP_REF="$S/library/prov-imp:1.0.0"
IMP_REPO="$CANON/library/prov-imp"
api -G --data-urlencode "ref=$IMP_REF" "$API/import/inspect" >"$WORK/inspect.json"
PLATFORMS=$(jq -c '[.platforms[] | {name: .name, digest: .digest}]' "$WORK/inspect.json") ||
    fail "the inspection of $IMP_REF failed: $(cat "$WORK/inspect.json")"
[ "$(printf '%s' "$PLATFORMS" | jq 'length')" -ge 1 ] || fail "the inspection of $IMP_REF found no platform"
IMP_ID=$(api -H 'Content-Type: application/json' -d "{\"reference\":\"$IMP_REF\",\"platforms\":$PLATFORMS}" \
    "$API/import" | jq -r '.task.id // empty')
[ -n "$IMP_ID" ] || fail "the API did not start the unit import"
ST=$(m7_wait_task "$API" "$IMP_ID" "$WORK/import.json" -H "$AUTHZ")
[ "$ST" = "done" ] || fail "the unit import ended $ST: $(cat "$WORK/import.json")"
[ "$(api "$API/content/$IMP_REPO/-/tags/1.0.0" | jq -r '.digest')" = "$IMP_DIG" ] ||
    fail "staging: the unit import did not land at $IMP_REPO:1.0.0 with the source digest $IMP_DIG"
check "1b: unit import done, $IMP_REPO:1.0.0 holds the source digest"

# 1c. A FileSet packed on this instance (FR-048), through the API.
CODE=$(api -o "$WORK/pack.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -d '{"source":"/srv/pack/notes","name":"notes","version":"1.0.0"}' "$API/filesets/pack")
[ "$CODE" = "201" ] || fail "packing /srv/pack/notes answered $CODE: $(cat "$WORK/pack.json")"
PACK_REPO=$(jq -r '.fileset.repository' "$WORK/pack.json")
PACK_DIG=$(jq -r '.fileset.digest' "$WORK/pack.json")
[ "$(api "$API/content/$PACK_REPO/-/tags/1.0.0" | jq -r '.digest')" = "$PACK_DIG" ] ||
    fail "staging: the packed FileSet is not at $PACK_REPO:1.0.0 with the digest the packing reported"
check "1c: FileSet packed, $PACK_REPO:1.0.0 holds $PACK_DIG"

# 1d. A repository pushed through /v2/ by a standard client, as the admin.
SEED_REPO=seeded/app
SEED_DIG=$(SEED_AUTH="$ADMIN:$ADMIN_PW" m7_seed push "$NODE_IP:8080/$SEED_REPO:1.0.0") ||
    fail "pushing $SEED_REPO through /v2/"
GOT=$(curl -sI -u "$ADMIN:$ADMIN_PW" \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json' \
    "$BASE/v2/$SEED_REPO/manifests/1.0.0" | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}')
[ "$GOT" = "$SEED_DIG" ] || fail "staging: the pushed manifest is not served back (got '$GOT', want $SEED_DIG)"
check "1d: $SEED_REPO pushed by a standard client and served back by digest"

# -- 2. The class on every surface ----------------------------------------------------
api "$API/content" >"$WORK/list.json"
jq -e '.totalPages == 1' "$WORK/list.json" >/dev/null || fail "the listing spans several pages: this check reads only the first"
jq -e '[.repositories[].provenance] | all(. == "recipe" or . == "unit-import" or . == "seed")' "$WORK/list.json" >/dev/null ||
    fail "FR-098: a listed repository carries no provenance class: $(jq -c '[.repositories[] | {name, provenance}]' "$WORK/list.json")"
for pair in "$APP_REPO recipe" "$LAB_REPO recipe" "$IMP_REPO unit-import" "$PACK_REPO unit-import" "$SEED_REPO seed"; do
    # shellcheck disable=SC2086 # split on purpose: one "<repo> <class>" pair
    set -- $pair
    [ "$(jq -r --arg r "$1" '.repositories[] | select(.name == $r) | .provenance' "$WORK/list.json")" = "$2" ] ||
        fail "FR-098: the API listing does not class $1 as $2: $(jq -c '[.repositories[] | {name, provenance}]' "$WORK/list.json")"
done
check "FR-098: the API listing classes every repository, the five staged ones as recipe, recipe, unit-import, unit-import, seed"

detail() { api "$API/content/$1" >"$WORK/detail.json"; jq -e "$2" "$WORK/detail.json" >/dev/null; }
# A scanned tag's date, read back against the task report that scanned it.
epoch() { jq -r "$1 | sub(\"\\\\.[0-9]+\"; \"\") | sub(\"\\\\+00:00$\"; \"Z\") | fromdateiso8601" "$2"; }
for spec in "$APP_REPO $APP_DIG prov-app@1.0.0" "$LAB_REPO $LAB_DIG lab-app@1.0.0"; do
    # shellcheck disable=SC2086 # split on purpose: one "<repo> <digest> <recipe>" triple
    set -- $spec
    detail "$1" '.provenance.class == "recipe"' || fail "FR-098: the detail of $1 is not class recipe: $(jq -c '.provenance' "$WORK/detail.json")"
    jq -e --arg r "$3" '.provenance.recipes | index($r) != null' "$WORK/detail.json" >/dev/null ||
        fail "FR-098: the detail of $1 does not name its managing recipe $3"
    jq -e --arg d "$2" '.tags[] | select(.digest == $d) | .lastScan | .scannedAt != null and .databaseAgeSeconds > 0 and (.verdict | length > 0)' \
        "$WORK/detail.json" >/dev/null ||
        fail "FR-098: the scanned tag of $1 carries no scan date with its database age: $(jq -c '.tags' "$WORK/detail.json")"
    API_AT=$(epoch ".tags[] | select(.digest == \"$2\") | .lastScan.scannedAt" "$WORK/detail.json")
    TASK_AT=$(epoch ".task.items[] | select(.name == \"$3/app\") | .scan.scanned_at" "$WORK/sync.json")
    [ "$API_AT" -ge $((T0 - 5)) ] && [ "$API_AT" -le $((T1 + 5)) ] ||
        fail "FR-098: the scan date of $1 ($API_AT) is outside the synchronization that scanned it ($T0..$T1)"
    D=$((API_AT - TASK_AT)); [ "$D" -ge -1 ] && [ "$D" -le 1 ] ||
        fail "FR-098: the scan date of $1 ($API_AT) is not the task report's ($TASK_AT)"
    jq -e '.provenance.lastScan.scannedAt != null' "$WORK/detail.json" >/dev/null ||
        fail "FR-098: the repository detail of $1 does not summarize its last scan"
done
detail "$APP_REPO" '.provenance.assurance == null' || fail "FR-037: the signed recipe's repository carries a reduced-assurance marker"
detail "$LAB_REPO" '.provenance.assurance.level == "reduced" and .provenance.assurance.scope == "lab"' ||
    fail "FR-037/FR-098: the relaxed repository does not carry the marker of scope lab: $(jq -c '.provenance' "$WORK/detail.json")"
check "FR-098: recipe repositories name their recipe, the relaxed one carries the marker (scope lab); each scanned tag carries the scan date of its task report (±1 s) and the database age"

detail "$IMP_REPO" '.provenance.class == "unit-import" and ((.provenance.origin // "") == "")' ||
    fail "FR-098: the unit import is not class unit-import of network origin: $(jq -c '.provenance' "$WORK/detail.json")"
detail "$PACK_REPO" '.provenance.class == "unit-import" and .provenance.origin == "local-pack"' ||
    fail "FR-098: the packed FileSet is not class unit-import, origin local-pack: $(jq -c '.provenance' "$WORK/detail.json")"
jq -e '[.tags[] | has("lastScan")] | any | not' "$WORK/detail.json" >/dev/null ||
    fail "FR-098: the packed FileSet (never scanned, ADR-0017) shows a scan date"
detail "$SEED_REPO" '.provenance.class == "seed" and ([.tags[] | has("lastScan")] | any | not)' ||
    fail "FR-098: the pushed repository is not class seed, or shows a scan that never ran: $(jq -c '.' "$WORK/detail.json")"
check "FR-098: the API details class the unit import (network), the packed FileSet (local-pack) and the pushed repository (seed), none with an invented scan"

# The content browser, as a signed-in administrator.
JAR="$WORK/jar"
CODE=$(curl -s -c "$JAR" -b "$JAR" -o /dev/null -w '%{http_code}' --data-urlencode "username=$ADMIN" \
    --data-urlencode "password=$ADMIN_PW" --data-urlencode next=/ "$BASE/login")
[ "$CODE" = "303" ] || fail "signing in to the web interface answered $CODE"
curl -s -b "$JAR" -o "$WORK/content.html" "$BASE/content"
grep -q 'data-provenance=' "$WORK/content.html" || fail "the content browser did not render the listing (not signed in?)"
for pair in "$APP_REPO recipe" "$LAB_REPO recipe" "$IMP_REPO unit-import" "$PACK_REPO unit-import" "$SEED_REPO seed"; do
    # shellcheck disable=SC2086 # split on purpose: one "<repo> <class>" pair
    set -- $pair
    m7_row "$WORK/content.html" "$1" | grep -q "data-provenance=\"$2\"" ||
        fail "FR-098: the content browser does not show $1 as $2"
done
m7_row "$WORK/content.html" "$LAB_REPO" | grep -q 'data-assurance="reduced"' ||
    fail "FR-037/FR-098: the content browser does not mark the relaxed repository"
if m7_row "$WORK/content.html" "$APP_REPO" | grep -q 'data-assurance='; then
    fail "FR-037: the content browser marks the signed recipe's repository"
fi
for pair in "$APP_REPO $APP_DIG" "$LAB_REPO $LAB_DIG"; do
    # shellcheck disable=SC2086 # split on purpose: one "<repo> <digest>" pair
    set -- $pair
    VERDICT=$(api "$API/content/$1" | jq -r --arg d "$2" '.tags[] | select(.digest == $d) | .lastScan.verdict')
    curl -s -b "$JAR" -o "$WORK/repo.html" "$BASE/content/$1"
    grep -q 'data-provenance="recipe"' "$WORK/repo.html" || fail "FR-098: the page of $1 does not show class recipe"
    tr '\n' ' ' <"$WORK/repo.html" | grep -q "data-scan=\"$VERDICT\"><time datetime=\"" ||
        fail "FR-098: the page of $1 does not show the tag's scan ($VERDICT) with its date"
done
curl -s -b "$JAR" -o "$WORK/repo.html" "$BASE/content/$PACK_REPO"
grep -q 'data-provenance="unit-import"' "$WORK/repo.html" || fail "FR-098: the page of the packed FileSet does not show class unit-import"
curl -s -b "$JAR" -o "$WORK/repo.html" "$BASE/content/$IMP_REPO"
grep -q 'data-provenance="unit-import"' "$WORK/repo.html" || fail "FR-098: the page of the unit import does not show class unit-import"
curl -s -b "$JAR" -o "$WORK/repo.html" "$BASE/content/$SEED_REPO"
grep -q 'data-provenance="seed"' "$WORK/repo.html" || fail "FR-098: the page of the pushed repository does not show class seed"
if grep -q 'data-last-scan=' "$WORK/repo.html"; then fail "FR-098: the page of the pushed repository shows a scan"; fi
check "FR-098: the content browser shows each class (listing and repository pages), the marker on the relaxed one, and the scan date of the scanned tags"

# -- 3. The unmanaged-content metric -------------------------------------------------------
# Read at scrape time and cached up to 10 s: polled, never slept on blindly.
i=0
while :; do
    METRICS=$(curl -fsS "$BASE/metrics")
    UI_N=$(m7_metric "$METRICS" 'tobby_store_unmanaged_repositories{class="unit-import"}')
    SEED_N=$(m7_metric "$METRICS" 'tobby_store_unmanaged_repositories{class="seed"}')
    [ "$UI_N" = "2" ] && [ "$SEED_N" = "1" ] && break
    i=$((i + 1))
    [ "$i" -gt 30 ] && fail "FR-098: tobby_store_unmanaged_repositories counts unit-import=$UI_N seed=$SEED_N, want 2 and 1"
    sleep 1
done
[ "$(printf '%s\n' "$METRICS" | grep -c '^tobby_store_unmanaged_repositories{')" = "2" ] ||
    fail "FR-098: the unmanaged-content metric carries another class than unit-import and seed"
check "FR-098: tobby_store_unmanaged_repositories counts unit-import 2 and seed 1 (recipe content is not counted)"

# -- 4. The provenance log field ------------------------------------------------------------
logline() { inc exec "$NODE" -- sh -c "grep -F '$1' $LOG | grep -F '\"repository\":\"$2\"' | grep -qF '\"provenance\":\"$3\"'"; }
logline 'manifest pushed' "$SEED_REPO" seed || fail "FR-090/FR-098: the push of $SEED_REPO is not logged with provenance seed"
logline 'unit import recorded' "$IMP_REPO" unit-import || fail "FR-090/FR-098: the unit import is not logged with provenance unit-import"
logline 'fileset packed' "$PACK_REPO" unit-import || fail "FR-090/FR-098: the packing is not logged with provenance unit-import"
check "FR-090/FR-098: the push, the unit import and the packing are logged with their provenance"

cleanup
trap - EXIT
check "scenario m7 c-provenance complete — report: $REPORT"
