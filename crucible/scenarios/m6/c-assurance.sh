#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenario, track C — R-22, the reduced-assurance
# marker (FR-037), across the whole air-gap trip: set where a relaxed scope
# admits content, carried on every surface that reports it, written into the
# medium's manifest per recipe, and re-read and re-displayed on the far side
# — as a CLAIM, beside the destination's own verdict, never instead of it —
# without a single byte of metadata reaching the destination registry.
#
#   1. Fixtures: a source registry holds two recipes — lab-app, UNSIGNED,
#      and signed-app, signed by key A. The mirror instance trusts root A and
#      declares the scope "lab", which admits unsigned recipes under
#      lab-*. The unsigned state is checked from the registry itself
#      (no signature tag exists) before anything depends on it.
#   2. Source side: after a manual synchronization, lab-app — and every item
#      it reaches — carries the marker in the task report, the recipe API,
#      the UI, a log field and the metric; signed-app carries none anywhere.
#   3. The manifest records the marker for lab-app and only lab-app.
#   4. The medium crosses the gap on a real block device.
#   5. Destination, strict (no scope of its own): lab-app is blocked —
#      TBY-SIG-001 — and the source's claim is shown anyway.
#   6. Destination with its OWN scope ("dest-lab"): lab-app is admitted
#      under THIS instance's marker, the source's claim beside it.
#   7. The manifest is rewritten to claim nothing (the manipulation is
#      checked): the destination still marks lab-app, because its own trust
#      verdict decides — a manifest claiming full assurance hides nothing.
#   8. Import: the delivery lands, its items and the medium's log carry the
#      destination's marker, and the zone registry holds exactly what the
#      source held — the same digests, no marker artifact, no extra tag
#      (RECIPE-SPEC §7.1: transferred content stays bit-exact).
#
# Topology:
#   tbc-m6a-source    (container, tbc-net)     source registry + cookbook
#   tbc-m6a-connected (container, tbc-net)     mirror instance, store ON the medium
#   tbc-m6a-dest      (container, tbc-airgap)  the isolated zone's registry
#   tbc-m6a-isolated  (container, tbc-airgap)  destination instance, on the medium
#   medium            (loop-backed block dev)  attached, filled, detached, moved
#
# Run on its own:  sh crucible/scenarios/m6/c-assurance.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m6}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m6-c-assurance.txt}"
. "$DIR/../../lib.sh"
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m6a-source
CONNECTED=tbc-m6a-connected
DEST=tbc-m6a-dest
ISOLATED=tbc-m6a-isolated
INSTANCES="$SOURCE $CONNECTED $DEST $ISOLATED"
MEDIUM_IMG="${CRUCIBLE_MEDIUM_IMG:-/tmp/tbc-m6a-medium.img}"
ZONE="zone-c"

release_leftovers() {
    for inst in $INSTANCES; do inc delete --force "$inst" 2>/dev/null || true; done
    if [ -f "$MEDIUM_IMG" ]; then
        losetup -j "$MEDIUM_IMG" 2>/dev/null | cut -d: -f1 |
            while read -r dev; do losetup -d "$dev" 2>/dev/null || true; done
        rm -f "$MEDIUM_IMG"
    fi
}
cleanup() {
    release_leftovers
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
c_tooling
c_build "$WORK"

# -- 1. Nodes, medium, fixtures -----------------------------------------------------
release_leftovers
for spec in "$SOURCE tbc-connected-node" "$CONNECTED tbc-connected-node" \
            "$DEST tbc-airgap-node" "$ISOLATED tbc-airgap-node"; do
    # shellcheck disable=SC2086 # a pair of words by construction
    set -- $spec
    c_launch "$1" "$2"
done
truncate -s 1G "$MEDIUM_IMG"
MEDIUM_DEV=$(losetup -f --show "$MEDIUM_IMG")
inc config device add "$CONNECTED" medium unix-block source="$MEDIUM_DEV" path=/dev/sdb
# shellcheck disable=SC2016 # expanded by the node's shell, not by this one
inc exec "$CONNECTED" -- sh -c '
    n=0
    until apk add --no-cache e2fsprogs util-linux >/dev/null 2>&1; do
        n=$((n + 1))
        [ "$n" -ge 30 ] && { echo "apk add: no package mirror reachable" >&2; exit 1; }
        sleep 2
    done
    base=$(awk "{print \$2; exit}" /proc/self/uid_map)
    mkfs.ext4 -q -E root_owner="$base:$base" /dev/sdb &&
    mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby
' || fail "formatting and mounting the medium on the connected node"
check "the medium ($MEDIUM_DEV) is a real block device, formatted and mounted on the connected node"

SOURCE_IP=$(instance_ip "$SOURCE")
DEST_IP=$(instance_ip "$DEST")
CONNECTED_IP=$(instance_ip "$CONNECTED")
instance_ip "$ISOLATED" >/dev/null # waits until the node has its address
if inc exec "$ISOLATED" -- wget -q -T 5 -O /dev/null "http://$SOURCE_IP:8080/healthz" >/dev/null 2>&1; then
    fail "the isolated node reached the connected zone: there is no air gap to rehearse"
fi
check "egress canary failed as required (air gap proven by construction)"

c_serve_source "$SOURCE"
c_keypair a
IMG_LAB=$(c_seed_image "$SOURCE_IP:8080/library/lab:1.0.0")
IMG_SIGNED=$(c_seed_image "$SOURCE_IP:8080/library/signed:1.0.0")
D_LAB=$(c_publish "$SOURCE_IP:8080" lab-app lab "$SOURCE_IP:8080/library/lab" "$IMG_LAB")
D_SIGNED=$(c_publish "$SOURCE_IP:8080" signed-app signed "$SOURCE_IP:8080/library/signed" "$IMG_SIGNED" a)

# The manipulation, verified: lab-app has NO signature in the source
# registry. Its absence is read from the registry, not assumed from the
# script having skipped a step.
LAB_SIG_HEX=$(printf '%s' "$D_LAB" | cut -d: -f2)
CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://$SOURCE_IP:8080/v2/cookbook/lab-app/manifests/sha256-$LAB_SIG_HEX.sig")
[ "$CODE" = "404" ] || fail "lab-app has a signature tag in the source registry (HTTP $CODE): it is not unsigned"
if c_verifies a "$SOURCE_IP:8080/cookbook/lab-app@$D_LAB"; then
    fail "lab-app verifies under key A: it is not unsigned"
fi
c_verifies a "$SOURCE_IP:8080/cookbook/signed-app@$D_SIGNED" || fail "signed-app does not verify under key A: the fixture is wrong"
check "lab-app is unsigned (no signature tag, cosign agrees); signed-app verifies under key A"

cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: $ZONE
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: lab-app
      version: "1.0.0"
    - name: signed-app
      version: "1.0.0"
EOF
inc file push "$WORK/retriever.yaml" "$CONNECTED/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/a.pub" "$CONNECTED/etc/tobby/a.pub" --create-dirs >/dev/null
inc file push "$WORK/a.pub" "$ISOLATED/etc/tobby/a.pub" --create-dirs >/dev/null

CANON=$(printf '%s' "$SOURCE_IP:8080" | tr ':' '_')
trust_with_scope() { # <scope name>
    printf '  roots:\n    - name: a\n      keyFile: /etc/tobby/a.pub\n  scopes:\n    - name: %s\n      repositories: ["%s/cookbook/lab-*"]\n      allowUnsigned: true' "$1" "$CANON"
}
trust_strict() {
    printf '  roots:\n    - name: a\n      keyFile: /etc/tobby/a.pub'
}

# -- 2. Source side: the marker on every surface -------------------------------------
c_config "$CONNECTED" mirror /media/tobby/store "$SOURCE_IP" "$(trust_with_scope lab)"
inc exec "$CONNECTED" -- sh -c 'grep -q "allowUnsigned: true" /etc/tobby/config.yaml' ||
    fail "the source-side scope does not allow unsigned recipes"
c_start "$CONNECTED"
inc exec "$CONNECTED" -- sh -c 'tobby sync --wait --output json' >"$WORK/sync.json" 2>/dev/null ||
    fail "the manual synchronization failed: $(cat "$WORK/sync.json")"

jq -e '[.items[] | select(.name | startswith("lab-app@1.0.0"))] | length >= 2' "$WORK/sync.json" >/dev/null ||
    fail "the task carries too few items of lab-app to prove anything: $(jq -c '[.items[].name]' "$WORK/sync.json")"
jq -e '[.items[] | select(.name | startswith("lab-app@1.0.0")) | .assurance] | all(. != null and .level == "reduced" and .scope == "lab" and .reasons == ["unsigned"])' \
    "$WORK/sync.json" >/dev/null ||
    fail "FR-037: an item of the unsigned recipe lacks the marker in the task report: $(jq -c '[.items[] | {name, assurance}]' "$WORK/sync.json")"
jq -e '[.items[] | select(.name | startswith("signed-app@1.0.0")) | has("assurance")] | any | not' "$WORK/sync.json" >/dev/null ||
    fail "FR-037: an item of the signed recipe carries a marker"
check "FR-037: every item of lab-app carries the marker in the task report; signed-app's carry none"

c_api "$CONNECTED_IP" GET /api/v1/recipes >"$WORK/recipes.json"
jq -e '[.recipes[] | select(.name == "lab-app") | .assurance.scope] == ["lab"]' "$WORK/recipes.json" >/dev/null ||
    fail "FR-037: the recipe API does not carry the marker for lab-app"
jq -e '[.recipes[] | select(.name == "signed-app") | has("assurance")] | any | not' "$WORK/recipes.json" >/dev/null ||
    fail "FR-037: the recipe API marks the signed recipe"
RECIPES_PAGE=$(inc exec "$CONNECTED" -- sh -c 'wget -q -O - http://127.0.0.1:8080/recipes')
[ "$(echo "$RECIPES_PAGE" | grep -c 'data-assurance="reduced"')" = "1" ] ||
    fail "FR-037: the recipes screen does not mark exactly one recipe"
TASK_ID=$(jq -r '.id' "$WORK/sync.json")
TASK_PAGE=$(inc exec "$CONNECTED" -- sh -c "wget -q -O - http://127.0.0.1:8080/tasks/$TASK_ID")
echo "$TASK_PAGE" | grep -q 'data-assurance="reduced"' || fail "FR-037: the task page does not mark the items of lab-app"
check "FR-037: the API and the UI (recipes, task detail) carry the marker for lab-app and for no other recipe"

inc exec "$CONNECTED" -- sh -c 'grep "\"recipe\":\"lab-app\"" /var/log/tobby.log | grep -q "\"assurance\":\"reduced\""' ||
    fail "FR-037: the log records of lab-app carry no assurance field"
inc exec "$CONNECTED" -- sh -c 'grep "\"assurance_scope\":\"lab\"" /var/log/tobby.log >/dev/null' ||
    fail "FR-037: the log field does not name the scope"
if inc exec "$CONNECTED" -- sh -c 'grep "\"recipe\":\"signed-app\"" /var/log/tobby.log | grep -q "\"assurance\""'; then
    fail "FR-037: a log record of the signed recipe carries the assurance field"
fi
METRICS=$(inc exec "$CONNECTED" -- sh -c 'wget -q -O - http://127.0.0.1:8080/metrics')
echo "$METRICS" | grep -q 'tobby_assurance_reduced_recipes{scope="lab"} 1' ||
    fail "FR-091: the metric does not count the recipe under reduced assurance: $(echo "$METRICS" | grep tobby_assurance || echo absent)"
check "FR-037: the log field (with its scope) and the tobby_assurance_reduced_recipes metric carry the marker"

# -- 3. The manifest ----------------------------------------------------------------------
inc exec "$CONNECTED" -- sh -c 'tobby sync --wait' >/dev/null 2>&1 || fail "the second synchronization failed"
MANIFEST=$(inc exec "$CONNECTED" -- sh -c 'cat /media/tobby/store/meta/media.json')
echo "$MANIFEST" | jq -e '[.recipes[] | select(.name == "lab-app") | .assurance] == [{"level":"reduced","scope":"lab","reasons":["unsigned"]}]' >/dev/null ||
    fail "FR-037: the manifest does not record the marker for lab-app: $(echo "$MANIFEST" | jq -c '[.recipes[] | {name, assurance}]')"
echo "$MANIFEST" | jq -e '[.recipes[] | select(.name == "signed-app") | has("assurance")] | any | not' >/dev/null ||
    fail "FR-037: the manifest marks the signed recipe (the field must be omitted, not null)"
check "FR-037/FR-054: the media manifest records the marker per recipe, and omits it for a recipe of full assurance"

# -- 4. The trip ----------------------------------------------------------------------------
c_stop "$CONNECTED"
inc exec "$CONNECTED" -- sh -c 'sync && umount /media/tobby'
inc config device remove "$CONNECTED" medium >/dev/null
inc config device add "$ISOLATED" medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
inc exec "$ISOLATED" -- sh -c 'mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby'
check "the medium is re-attached and mounted on the isolated node"

inc exec "$DEST" -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:5000 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready "$DEST" http://127.0.0.1:5000/healthz

write_destination_config() { # <trust yaml>
    inc exec "$ISOLATED" -- sh -c "mkdir -p /etc/tobby && cat >/etc/tobby/config.yaml <<'EOF'
mode: mirror
zone: $ZONE
storage:
  root: /media/tobby/store
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
auth:
  disabled: true
registries:
  insecure:
    - $DEST_IP:5000
destination:
  registry: $DEST_IP:5000
  cookbook: cookbook
trust:
$1
EOF"
}
verify_medium() { # <output file>
    inc exec "$ISOLATED" -- sh -c 'tobby media verify --output json' >"$1" 2>/dev/null || true
    jq -e '.recipes != null' "$1" >/dev/null || fail "tobby media verify produced no report: $(cat "$1")"
}

# -- 5. Destination, strict: blocked, and the claim is shown anyway --------------------------
write_destination_config "$(trust_strict)"
inc exec "$ISOLATED" -- sh -c '! grep -q "allowUnsigned" /etc/tobby/config.yaml' ||
    fail "the strict destination still allows unsigned recipes"
verify_medium "$WORK/verify-strict.json"
jq -e '.recipes[] | select(.name == "lab-app") | .pushable == false and .reason.code == "TBY-SIG-001"' "$WORK/verify-strict.json" >/dev/null ||
    fail "a strict destination admitted the unsigned recipe: $(jq -c '[.recipes[] | {name, pushable, reason}]' "$WORK/verify-strict.json")"
jq -e '.recipes[] | select(.name == "lab-app") | .sourceAssurance.scope == "lab"' "$WORK/verify-strict.json" >/dev/null ||
    fail "FR-037: the source's claim is not re-displayed for a recipe the destination blocks"
jq -e '.recipes[] | select(.name == "signed-app") | .pushable == true and (has("assurance") | not) and (has("sourceAssurance") | not)' \
    "$WORK/verify-strict.json" >/dev/null || fail "the signed recipe is not pushable and unmarked on the destination"
check "FR-037: a strict destination blocks lab-app (TBY-SIG-001) and still shows the source's claim; signed-app is pushable and unmarked"

# -- 6. Destination with its own scope: two markers, two scopes -------------------------------
write_destination_config "$(trust_with_scope dest-lab)"
inc exec "$ISOLATED" -- sh -c 'grep -q "name: dest-lab" /etc/tobby/config.yaml' || fail "the destination's own scope is not configured"
verify_medium "$WORK/verify-own.json"
jq -e '.recipes[] | select(.name == "lab-app") | .pushable == true and .assurance.scope == "dest-lab" and .sourceAssurance.scope == "lab"' \
    "$WORK/verify-own.json" >/dev/null ||
    fail "FR-037: the report does not show this instance's marker (dest-lab) beside the source's claim (lab): $(jq -c '[.recipes[] | {name, assurance, sourceAssurance}]' "$WORK/verify-own.json")"
check "FR-037: with a scope of its own the destination admits lab-app under ITS marker (dest-lab), the source's claim (lab) beside it"

# -- 7. A manifest that claims nothing hides nothing --------------------------------------------
# shellcheck disable=SC2016 # expanded by the node's shell, not by this one
inc exec "$ISOLATED" -- sh -c '
    f=/media/tobby/store/meta/media.json
    jq "del(.recipes[].assurance)" "$f" >"$f.new" && mv "$f.new" "$f"
' 2>/dev/null || {
    # busybox images ship no jq: do it from the host and push the result back.
    inc file pull "$ISOLATED/media/tobby/store/meta/media.json" "$WORK/media.json" >/dev/null
    jq 'del(.recipes[].assurance)' "$WORK/media.json" >"$WORK/media-nothing.json"
    inc file push "$WORK/media-nothing.json" "$ISOLATED/media/tobby/store/meta/media.json" >/dev/null
}
inc file pull "$ISOLATED/media/tobby/store/meta/media.json" "$WORK/media-check.json" >/dev/null
jq -e '[.recipes[] | has("assurance")] | any | not' "$WORK/media-check.json" >/dev/null ||
    fail "the manifest still carries a claim: the manipulation did not happen"
verify_medium "$WORK/verify-silent.json"
jq -e '.recipes[] | select(.name == "lab-app") | .assurance.scope == "dest-lab" and (has("sourceAssurance") | not)' "$WORK/verify-silent.json" >/dev/null ||
    fail "FR-037: a manifest claiming full assurance hid the destination's own marker: $(jq -c '[.recipes[] | {name, assurance, sourceAssurance}]' "$WORK/verify-silent.json")"
check "FR-037: with the claim removed from the manifest, the destination still marks lab-app from its own verdict"

# -- 8. Import: the zone registry holds exactly what the source held ------------------------------
inc exec "$ISOLATED" -- sh -c 'tobby media import --output json' >"$WORK/import.json" 2>/dev/null || true
jq -e '[.items[] | select(.name | contains("lab-app@1.0.0")) | .assurance.scope] | length >= 1 and all(. == "dest-lab")' "$WORK/import.json" >/dev/null ||
    fail "FR-037: the import task's items for lab-app do not carry the destination's marker: $(jq -c '[.items[] | {name, status, assurance}]' "$WORK/import.json")"
inc exec "$ISOLATED" -- sh -c 'grep -q "delivery under reduced assurance" /media/tobby/store/_tobby/logs/operations.log' ||
    fail "FR-037: the medium's own log does not name the delivery under reduced assurance"

# Bit-exact (RECIPE-SPEC §7.1): same digests on both sides, and the zone
# cookbook repository holds the recipe's tag and nothing a marker could have
# added.
dest_digest() { # <repo> <tag>
    curl -sI -H 'Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json' \
        "http://$DEST_IP:5000/v2/$1/manifests/$2" | tr -d '\r' | awk -F': ' 'tolower($1) == "docker-content-digest" {print $2}'
}
[ "$(dest_digest cookbook/lab-app 1.0.0)" = "$D_LAB" ] ||
    fail "RECIPE-SPEC §7.1: the recipe artifact in the zone registry is not bit-exact (got $(dest_digest cookbook/lab-app 1.0.0), want $D_LAB)"
[ "$(dest_digest "$CANON/library/lab" 1.0.0)" = "$IMG_LAB" ] ||
    fail "RECIPE-SPEC §7.1: the ingredient in the zone registry is not bit-exact"
TAGS=$(curl -s "http://$DEST_IP:5000/v2/cookbook/lab-app/tags/list" | jq -c '.tags | map(select(startswith("sha256-") | not)) | sort')
[ "$TAGS" = '["1.0.0"]' ] || fail "the zone cookbook repository holds tags $TAGS, want only the recipe's version"
for repo in cookbook/lab-app "$CANON/library/lab"; do
    if curl -s "http://$DEST_IP:5000/v2/$repo/tags/list" | grep -qi "assur"; then
        fail "RECIPE-SPEC §7.1: a marker reached the destination registry ($repo)"
    fi
done
check "RECIPE-SPEC §7.1: the zone registry holds the same digests as the source, one version tag, and no trace of any marker"

check "scenario m6 c-assurance complete — report: $REPORT"
