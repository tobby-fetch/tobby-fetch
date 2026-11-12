#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenario, track C — the trust-root compartments named
# in the error (DEVELOPMENT-PLAN §2.6, exit criterion delivered at J3 and
# never proven until now; FR-033, RECIPE-SPEC §12.3).
#
# A trust scope can restrict the roots that apply inside its perimeter. The
# criterion is that content signed by a key that is VALID FOR ANOTHER SCOPE
# is refused, and that the refusal names the scope that refused it
# (TBY-SIG-001, parameter "scope") — so an operator reads which perimeter to
# fix instead of hunting through every root.
#
#   1. Two keys, A and B, both configured as trust roots. Two scopes: tenant-a
#      lists only root A, tenant-b lists only root B.
#   2. Three recipes, all signed by key B:
#        tenant-a-app   inside scope tenant-a  — B is a valid root, but not
#                       one this scope lists: REFUSED, naming tenant-a;
#        tenant-b-app   inside scope tenant-b  — B is listed: admitted;
#        other-app      under no scope (signed by A, a root) — admitted.
#      The manipulation is checked before the verdict is asked: cosign, a
#      tool nobody here wrote, agrees that tenant-a-app verifies under B and
#      NOT under A — so the refusal can only come from the scope.
#   3. A manual synchronization: the refused recipe fails with TBY-SIG-001
#      carrying scope "tenant-a" on its task item, the others land, and the
#      refused recipe's content is absent from the store. The task page
#      renders the scope in the message.
#
# Topology:
#   tbc-m6s-source (container, tbc-net)  source registry + cookbook
#   tbc-m6s-node   (container, tbc-net)  mirror instance under test
#
# Run on its own:  sh crucible/scenarios/m6/c-trust-scopes.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m6}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m6-c-trust-scopes.txt}"
. "$DIR/../../lib.sh"
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m6s-source
NODE=tbc-m6s-node
STORE=/var/lib/tobby
cleanup() {
    inc delete --force "$SOURCE" "$NODE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
c_tooling
c_build "$WORK"

# -- 1. Nodes, keys, fixtures ----------------------------------------------------
c_launch "$SOURCE" tbc-connected-node
c_launch "$NODE" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
c_serve_source "$SOURCE"

c_keypair a
c_keypair b
IMG_TENANT_A=$(c_seed_image "$SOURCE_IP:8080/library/tenant-a:1.0.0")
IMG_TENANT_B=$(c_seed_image "$SOURCE_IP:8080/library/tenant-b:1.0.0")
IMG_OTHER=$(c_seed_image "$SOURCE_IP:8080/library/other:1.0.0")
D_TENANT_A=$(c_publish "$SOURCE_IP:8080" tenant-a-app tenant-a "$SOURCE_IP:8080/library/tenant-a" "$IMG_TENANT_A" b)
c_publish "$SOURCE_IP:8080" tenant-b-app tenant-b "$SOURCE_IP:8080/library/tenant-b" "$IMG_TENANT_B" b >/dev/null
c_publish "$SOURCE_IP:8080" other-app other "$SOURCE_IP:8080/library/other" "$IMG_OTHER" a >/dev/null

# The manipulation, proved before a verdict is asked for: tenant-a-app is
# validly signed by B, and A does not verify it. Whatever refuses it, then,
# is the scope — not a broken signature, not an unknown key.
c_verifies b "$SOURCE_IP:8080/cookbook/tenant-a-app@$D_TENANT_A" ||
    fail "tenant-a-app does not verify under key B: the fixture is wrong"
if c_verifies a "$SOURCE_IP:8080/cookbook/tenant-a-app@$D_TENANT_A"; then
    fail "tenant-a-app verifies under key A: the refusal could not be the scope's"
fi
check "tenant-a-app is signed by B, valid for the zone, and cosign confirms A does not verify it"

cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-c
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: tenant-a-app
      version: "1.0.0"
    - name: tenant-b-app
      version: "1.0.0"
    - name: other-app
      version: "1.0.0"
EOF
inc file push "$WORK/retriever.yaml" "$NODE/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/a.pub" "$NODE/etc/tobby/a.pub" --create-dirs >/dev/null
inc file push "$WORK/b.pub" "$NODE/etc/tobby/b.pub" --create-dirs >/dev/null

# Scopes match the CANONICAL nominal repository (ADR-0013): the port's colon
# is written "_" because ":" cannot appear in a repository path.
CANON=$(printf '%s' "$SOURCE_IP:8080" | tr ':' '_')
TRUST="  roots:
    - name: a
      keyFile: /etc/tobby/a.pub
    - name: b
      keyFile: /etc/tobby/b.pub
  scopes:
    - name: tenant-a
      repositories: [\"$CANON/cookbook/tenant-a-*\"]
      roots: [a]
    - name: tenant-b
      repositories: [\"$CANON/cookbook/tenant-b-*\"]
      roots: [b]"
c_config "$NODE" mirror "$STORE" "$SOURCE_IP" "$TRUST"
# The configuration is the other half of the manipulation: both scopes are
# declared, and each lists exactly one root.
inc exec "$NODE" -- sh -c 'grep -q "name: tenant-a" /etc/tobby/config.yaml && grep -q "roots: \[a\]" /etc/tobby/config.yaml' ||
    fail "scope tenant-a is not configured with root a only"
inc exec "$NODE" -- sh -c 'grep -q "name: tenant-b" /etc/tobby/config.yaml && grep -q "roots: \[b\]" /etc/tobby/config.yaml' ||
    fail "scope tenant-b is not configured with root b only"
check "two scopes declared: tenant-a lists root a only, tenant-b lists root b only; both keys are configured roots"

# -- 2. The synchronization ---------------------------------------------------------
c_start "$NODE"
inc exec "$NODE" -- sh -c 'tobby sync --wait --output json' >"$WORK/sync.json" 2>/dev/null || true
jq -e '.task.id != null and .task.type == "sync"' "$WORK/sync.json" >/dev/null ||
    fail "the synchronization produced no task document: $(cat "$WORK/sync.json")"

# The refusal names the scope.
jq -e '[.task.items[] | select(.name == "tenant-a-app@1.0.0" and .status == "failed")] | length == 1' "$WORK/sync.json" >/dev/null ||
    fail "tenant-a-app was not refused: $(jq -c '[.task.items[] | {name, status}]' "$WORK/sync.json")"
jq -e '.task.items[] | select(.name == "tenant-a-app@1.0.0") | .error.code == "TBY-SIG-001"' "$WORK/sync.json" >/dev/null ||
    fail "the refusal is not TBY-SIG-001"
jq -e '.task.items[] | select(.name == "tenant-a-app@1.0.0") | .error.params.scope == "tenant-a"' "$WORK/sync.json" >/dev/null ||
    fail "plan §2.6: the refusal does not name the scope that refused it: $(jq -c '.task.items[] | select(.name == "tenant-a-app@1.0.0") | .error' "$WORK/sync.json")"
check "plan §2.6: content signed by a key valid for another scope is refused with TBY-SIG-001, naming scope tenant-a"

# The scope that lists B admits the same key, and the unscoped recipe is
# unaffected: the compartment refuses what is outside it and nothing else.
jq -e '[.task.items[] | select(.name | startswith("tenant-b-app@1.0.0/")) | .status] | all(. == "done")' "$WORK/sync.json" >/dev/null ||
    fail "tenant-b-app, signed by the root its scope lists, was not admitted"
jq -e '[.task.items[] | select(.name | startswith("other-app@1.0.0/")) | .status] | all(. == "done")' "$WORK/sync.json" >/dev/null ||
    fail "other-app, under no scope, was not admitted"
check "the scope listing key B admits tenant-b-app; the unscoped recipe is admitted: the refusal is the compartment's, not a general one"

# Nothing of the refused recipe landed.
c_api "$NODE_IP" GET /api/v1/recipes >"$WORK/recipes.json"
jq -e '[.recipes[].name] | sort == ["other-app", "tenant-b-app"]' "$WORK/recipes.json" >/dev/null ||
    fail "the recipe graph holds $(jq -c '[.recipes[].name] | sort' "$WORK/recipes.json"), want only the two admitted recipes"
inc exec "$NODE" -- sh -c "! find $STORE/docker/registry/v2/repositories -type d -path '*cookbook/tenant-a-app*' | grep -q ." ||
    fail "the refused recipe's artifact was stored anyway"
check "the refused recipe left nothing in the store or the recipe graph"

# The operator reads it on the task page, in words.
TASK_ID=$(jq -r '.task.id' "$WORK/sync.json")
PAGE=$(inc exec "$NODE" -- sh -c "wget -q -O - http://127.0.0.1:8080/tasks/$TASK_ID")
echo "$PAGE" | grep -q "tenant-a" || fail "the task page does not name the scope in the refusal"
check "R-03: the task page renders the scope in the refusal message"

check "scenario m6 c-trust-scopes complete — report: $REPORT"
