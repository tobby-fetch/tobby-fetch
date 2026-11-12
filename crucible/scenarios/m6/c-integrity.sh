#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenario, track C — R-31, on-demand store integrity
# (FR-057, DEVELOPMENT-PLAN §2.6), on real nodes and a real filesystem:
# the verification as the operator meets it, from the command line and from
# the running instance, on a store damaged the four ways the requirement
# names — and the property it ends on: the store is byte-identical
# afterwards.
#
#   1. Fixtures: a source registry holds one image per delivery and three
#      recipes, two signed by key A and one by key B. Three, because the
#      signature defect needs a recipe whose key is "no longer a trust
#      root", and that has to be a recipe the store ACCEPTED first.
#   2. A mirror instance synchronizes them by hand under roots [A, B]; the
#      store is then a transportable one (it carries a media manifest).
#   3. An intact store verifies clean, from the API and the screen, and the
#      CLI run on the stopped store exits 0 leaving every byte alone.
#   4. The damage — each manipulation CHECKED before a verdict is asked:
#        - a blob truncated (its bytes no longer hash to its directory),
#        - a blob a manifest references removed (gone from disk),
#        - an orphan planted (a well-formed blob nothing references),
#        - key B dropped from the trust roots (recipe three still carries
#          B's perfectly valid signature; cosign agrees, and agrees that
#          A does not verify it).
#   5. `tobby store verify` exits 4, and its report names each defect with
#      its object; the store is byte-identical before and after the run.
#   6. The running instance, restarted on the damaged store, produces the
#      same defects from POST /api/v1/store/verify and from /integrity; the
#      metrics count the verification; one at a time is enforced.
#
# Topology:
#   tbc-m6c-source (container, tbc-net)  source registry + cookbook
#   tbc-m6c-node   (container, tbc-net)  mirror instance, the store under test
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m6/run.sh):  sh crucible/scenarios/m6/c-integrity.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m6}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m6-c-integrity.txt}"
. "$DIR/../../lib.sh"
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m6c-source
NODE=tbc-m6c-node
STORE=/var/lib/tobby
cleanup() {
    inc delete --force "$SOURCE" "$NODE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
c_tooling
c_build "$WORK"

# -- 1. Nodes and fixtures ------------------------------------------------------
c_launch "$SOURCE" tbc-connected-node
c_launch "$NODE" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
c_serve_source "$SOURCE"

c_keypair a
c_keypair b
IMG_APP=$(c_seed_image "$SOURCE_IP:8080/library/app:1.0.0")
IMG_TOOL=$(c_seed_image "$SOURCE_IP:8080/library/tool:1.0.0")
IMG_OLD=$(c_seed_image "$SOURCE_IP:8080/library/old:1.0.0")
c_publish "$SOURCE_IP:8080" int-app app "$SOURCE_IP:8080/library/app" "$IMG_APP" a >/dev/null
c_publish "$SOURCE_IP:8080" int-tool tool "$SOURCE_IP:8080/library/tool" "$IMG_TOOL" a >/dev/null
D_OLD=$(c_publish "$SOURCE_IP:8080" int-old old "$SOURCE_IP:8080/library/old" "$IMG_OLD" b)
# The manipulation the signature defect rests on, proved before it is used:
# int-old is signed by B and by nobody else.
c_verifies b "$SOURCE_IP:8080/cookbook/int-old@$D_OLD" ||
    fail "int-old does not verify under key B: the fixture is wrong"
if c_verifies a "$SOURCE_IP:8080/cookbook/int-old@$D_OLD"; then
    fail "int-old verifies under key A: it was not signed by B alone"
fi
check "three deliveries published: int-app and int-tool signed by A, int-old signed by B alone (cosign agrees)"

cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-c
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: int-app
      version: "1.0.0"
    - name: int-tool
      version: "1.0.0"
    - name: int-old
      version: "1.0.0"
EOF
inc file push "$WORK/retriever.yaml" "$NODE/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/a.pub" "$NODE/etc/tobby/a.pub" --create-dirs >/dev/null
inc file push "$WORK/b.pub" "$NODE/etc/tobby/b.pub" --create-dirs >/dev/null
TRUST_AB='  roots:
    - name: a
      keyFile: /etc/tobby/a.pub
    - name: b
      keyFile: /etc/tobby/b.pub'
TRUST_A='  roots:
    - name: a
      keyFile: /etc/tobby/a.pub'

# -- 2. A mirror store, filled by hand ------------------------------------------
c_config "$NODE" mirror "$STORE" "$SOURCE_IP" "$TRUST_AB"
c_start "$NODE"
inc exec "$NODE" -- sh -c 'tobby sync --wait' >/dev/null 2>&1 || fail "the manual synchronization failed"
inc exec "$NODE" -- sh -c "test -f $STORE/meta/media.json" || fail "the mirror store carries no media manifest"
check "the mirror store holds three recipes and a media manifest (a transportable store)"

# -- 3. An intact store verifies clean ------------------------------------------
c_api "$NODE_IP" POST /api/v1/store/verify >"$WORK/started.json"
jq -e '.task.type == "store-verify"' "$WORK/started.json" >/dev/null ||
    fail "the API did not start a verification: $(cat "$WORK/started.json")"
c_wait_verification "$NODE_IP" "$WORK/verification.json"
jq -e '.verification.verdict == "clean" and .verification.store.transportable == true and .verification.checked.recipes == 3' \
    "$WORK/verification.json" >/dev/null ||
    fail "an intact store did not verify clean: $(jq -c '.verification' "$WORK/verification.json")"
jq -e '[.verification.recipes[] | select(.signature != "verified" or .complete != true)] | length == 0' \
    "$WORK/verification.json" >/dev/null || fail "an intact recipe is reported unverified or incomplete"
inc exec "$NODE" -- sh -c 'wget -q -O - http://127.0.0.1:8080/integrity' | grep -q 'data-verdict="clean"' ||
    fail "the Store integrity screen does not show the clean verdict"
check "FR-057: an intact transportable store verifies clean from the API and the screen"

# The command line, on the stopped store: exit 0, and not one byte moved.
c_stop "$NODE"
BEFORE=$(c_fingerprint "$NODE" "$STORE")
inc exec "$NODE" -- sh -c 'tobby store verify --config /etc/tobby/config.yaml --output json' \
    >"$WORK/cli-clean.json" 2>/dev/null || fail "tobby store verify exited non-zero on an intact store"
jq -e '.verdict == "clean" and .defect_count == 0' "$WORK/cli-clean.json" >/dev/null || fail "the CLI report is not clean"
AFTER=$(c_fingerprint "$NODE" "$STORE")
[ "$BEFORE" = "$AFTER" ] || fail "FR-057: verifying an intact store changed it ($BEFORE -> $AFTER)"
check "FR-057: tobby store verify exits 0 on the intact store and leaves it byte-identical"

# -- 4. The damage, each manipulation checked ------------------------------------
MANIFEST=$(inc exec "$NODE" -- sh -c "cat $STORE/meta/media.json")
ING_APP=$(echo "$MANIFEST" | jq -r '.recipes[] | select(.name=="int-app") | .ingredients[0].digest')
ING_TOOL=$(echo "$MANIFEST" | jq -r '.recipes[] | select(.name=="int-tool") | .ingredients[0].digest')

# 4a. A truncated blob: the ingredient's own manifest — small, certainly
# present, certainly addressed by the digest the recipe pins.
TRUNC_FILE=$(c_blob_file "$NODE" "$STORE" "$ING_APP")
[ -n "$TRUNC_FILE" ] || fail "could not locate the blob of int-app's ingredient"
inc exec "$NODE" -- sh -c "sz=\$(wc -c <'$TRUNC_FILE'); truncate -s \$((sz - 8)) '$TRUNC_FILE'"
# The manipulation, verified: the file no longer hashes to its own name.
HEX=$(echo "$ING_APP" | cut -d: -f2)
NOW=$(inc exec "$NODE" -- sh -c "sha256sum '$TRUNC_FILE'" | awk '{print $1}')
[ "$NOW" != "$HEX" ] || fail "the truncation changed nothing: the blob still hashes to $HEX"
TRUNC_REL=${TRUNC_FILE#"$STORE"/}
check "manipulation 1 verified: $TRUNC_REL no longer hashes to its digest"

# 4b. A blob a manifest references, removed. The tool ingredient is an
# index; its first child manifest names a layer blob, which is removed from
# disk.
INDEX_FILE=$(c_blob_file "$NODE" "$STORE" "$ING_TOOL")
CHILD=$(inc exec "$NODE" -- sh -c "cat '$INDEX_FILE'" | jq -r '.manifests[0].digest')
CHILD_FILE=$(c_blob_file "$NODE" "$STORE" "$CHILD")
[ -n "$CHILD_FILE" ] || fail "could not locate the child manifest of int-tool's ingredient"
LAYER=$(inc exec "$NODE" -- sh -c "cat '$CHILD_FILE'" | jq -r '.layers[0].digest')
LAYER_FILE=$(c_blob_file "$NODE" "$STORE" "$LAYER")
[ -n "$LAYER_FILE" ] || fail "could not locate the layer blob to remove"
inc exec "$NODE" -- sh -c "rm -rf \"\$(dirname '$LAYER_FILE')\""
[ -z "$(c_blob_file "$NODE" "$STORE" "$LAYER")" ] || fail "the referenced blob is still on disk"
check "manipulation 2 verified: $LAYER, referenced by a manifest of int-tool, is gone from disk"

# 4c. An orphan: a well-formed blob, in the right place, that nothing
# references. It must hash to its own name — a malformed one would be
# reported as damaged instead, which is a different defect.
ORPHAN_HEX=$(printf 'an orphaned blob planted by the crucible' | sha256sum | awk '{print $1}')
ORPHAN_REL="docker/registry/v2/blobs/sha256/$(echo "$ORPHAN_HEX" | cut -c1-2)/$ORPHAN_HEX/data"
inc exec "$NODE" -- sh -c "
    mkdir -p \"\$(dirname '$STORE/$ORPHAN_REL')\"
    printf 'an orphaned blob planted by the crucible' >'$STORE/$ORPHAN_REL'"
GOT=$(inc exec "$NODE" -- sh -c "sha256sum '$STORE/$ORPHAN_REL'" | awk '{print $1}')
[ "$GOT" = "$ORPHAN_HEX" ] || fail "the planted orphan does not hash to its own name"
check "manipulation 3 verified: a well-formed orphan blob is planted at $ORPHAN_REL"

# 4d. Key B is no longer a trust root. The manipulation is the
# configuration, so it is checked from the configuration and from the
# signature: B is gone from the file, and the signature on int-old is still
# valid under B (verified in step 1) and not under A.
c_config "$NODE" mirror "$STORE" "$SOURCE_IP" "$TRUST_A"
if inc exec "$NODE" -- sh -c 'grep -q "b.pub" /etc/tobby/config.yaml'; then
    fail "key B is still configured as a trust root"
fi
inc exec "$NODE" -- sh -c 'grep -q "a.pub" /etc/tobby/config.yaml' || fail "key A vanished from the trust roots"
check "manipulation 4 verified: key B is no longer a trust root; int-old's signature is valid under B and not under A"

# -- 5. The verdict ----------------------------------------------------------------
BEFORE=$(c_fingerprint "$NODE" "$STORE")
RC=0
inc exec "$NODE" -- sh -c 'tobby store verify --config /etc/tobby/config.yaml --output json' \
    >"$WORK/cli-damaged.json" 2>/dev/null || RC=$?
[ "$RC" = "4" ] || fail "FR-057/FR-066: tobby store verify exited $RC on a damaged store, want 4 (verification class)"
jq -e '.verdict == "defects"' "$WORK/cli-damaged.json" >/dev/null || fail "the verdict is not 'defects'"
jq -e --arg p "$TRUNC_REL" '[.defects[] | select(.code=="TBY-INT-001" and .path==$p)] | length == 1' \
    "$WORK/cli-damaged.json" >/dev/null ||
    fail "the truncated blob is not named by a TBY-INT-001 defect: $(jq -c '[.defects[] | {code, path}]' "$WORK/cli-damaged.json")"
jq -e --arg l "$LAYER" '[.defects[] | select(.code=="TBY-INT-002" and (.params.missing | contains($l)))] | length >= 1' \
    "$WORK/cli-damaged.json" >/dev/null || fail "the missing referenced blob is not named by a TBY-INT-002 defect"
jq -e '[.defects[] | select(.code=="TBY-SIG-001" and .path=="int-old@1.0.0")] | length == 1' \
    "$WORK/cli-damaged.json" >/dev/null ||
    fail "the recipe whose key is no longer a root is not named by a TBY-SIG-001 defect"
jq -e --arg p "$ORPHAN_REL" '[.findings[] | select(.code=="TBY-INT-003" and .path==$p)] | length == 1' \
    "$WORK/cli-damaged.json" >/dev/null || fail "the planted orphan is not listed"
jq -e '[.defects[] | select(.code=="TBY-INT-003")] | length == 0' "$WORK/cli-damaged.json" >/dev/null ||
    fail "an orphan is reported as a blocking defect"
jq -e '.started_at != null and .finished_at != null and .run_id != ""' "$WORK/cli-damaged.json" >/dev/null ||
    fail "the report is not timestamped and correlated"
AFTER=$(c_fingerprint "$NODE" "$STORE")
[ "$BEFORE" = "$AFTER" ] || fail "FR-057: the verification repaired or deleted something ($BEFORE -> $AFTER)"
check "FR-057: exit 4; the report names the truncated blob, the incomplete manifest, the refused signature and the orphan; the store is byte-identical"

# -- 6. The same verification from the running instance -----------------------------
c_start "$NODE"
c_api "$NODE_IP" POST /api/v1/store/verify >"$WORK/started2.json"
jq -e '.task.id != null' "$WORK/started2.json" >/dev/null ||
    fail "the API refused to start a verification on the damaged store: $(cat "$WORK/started2.json")"
c_wait_verification "$NODE_IP" "$WORK/verification2.json"
[ "$(jq -c '[.verification.defects[] | .code + " " + .path] | sort' "$WORK/verification2.json")" = \
  "$(jq -c '[.defects[] | .code + " " + .path] | sort' "$WORK/cli-damaged.json")" ] ||
    fail "FR-061: the API and the command line report different defects"
jq -e '.status == "failed"' "$WORK/verification2.json" >/dev/null || fail "a store with defects is not a failed task"
SCREEN=$(inc exec "$NODE" -- sh -c 'wget -q -O - http://127.0.0.1:8080/integrity')
echo "$SCREEN" | grep -q 'data-verdict="defects"' || fail "the screen does not show the defects verdict"
echo "$SCREEN" | grep -q "TBY-INT-001" || fail "the screen does not name the damaged blob"
METRICS=$(inc exec "$NODE" -- sh -c 'wget -q -O - http://127.0.0.1:8080/metrics')
echo "$METRICS" | grep -q 'tobby_integrity_verifications_total{verdict="defects"} 1' ||
    fail "FR-091: the metric does not count the verification"
echo "$METRICS" | grep -q 'tobby_integrity_last_verification_timestamp_seconds' ||
    fail "FR-091: the last-verification timestamp is not exposed"
inc exec "$NODE" -- sh -c 'grep -q "\"action\":\"store.verify\"" /var/log/tobby.log' ||
    fail "FR-094: no store.verify audit event"
check "FR-061: the running instance reports the same defects through the API and the screen; the metric and the audit event record it"

# One at a time, observed: two requests back to back cannot both be queued
# behind each other. The first is accepted (the previous verification has
# finished), the second arrives while the first is pending or running — or,
# on a fast disk, after it finished, which is the one outcome that proves
# nothing and is therefore not accepted as a pass on its own: the Go suite
# pins the race deterministically (TestOneVerificationAtATime). What this
# node adds is that a refusal, when it happens, is the 409 of the contract.
CODE1=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$NODE_IP:8080/api/v1/store/verify")
CODE2=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$NODE_IP:8080/api/v1/store/verify")
[ "$CODE1" = "201" ] || fail "FR-057: a verification after the first finished was refused ($CODE1)"
case "$CODE2" in
    201 | 409) ;;
    *) fail "FR-057: a second request answered $CODE2, want 201 (the first had already finished) or 409 (TBY-INT-004)" ;;
esac
check "FR-057: a second verification asked for while one is in flight is refused with 409, never queued behind it ($CODE1/$CODE2)"

check "scenario m6 c-integrity complete — report: $REPORT"
