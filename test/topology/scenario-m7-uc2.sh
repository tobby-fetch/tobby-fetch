#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 topology scenario (ADR-0014 tier 1): UC2, the mirror
# synchronization into an air-gapped zone, end to end through the real
# surfaces and the real commands — the exit criterion "the e2e suite covers
# both operating modes" for the mirror mode.
#
#   1. A signed recipe (REAL cosign) and its image sit in a source registry;
#      a mirror instance whose store lives on the "medium" synchronizes
#      them with `tobby sync --wait` (FR-014 manual trigger, FR-066) and
#      writes the media manifest (FR-054) for the destination zone.
#   2. Transport: the medium is COPIED onto another volume, mounted at
#      another path on the isolated side, and the original is destroyed —
#      the store must be self-contained (FR-050).
#   3. The isolated side has no route to the connected one (egress canary).
#   4. A damaged copy (one blob truncated) is blocked whole by `tobby media
#      verify`, with the offending file named, and `media import` pushes
#      nothing from it (R-19).
#   5. The intact copy verifies, `tobby media import` pushes it into the
#      zone registry, and a standard client INSIDE the zone reads the
#      pinned digest back.
#
# What this tier does not prove: a real detachable block device, which is
# the crucible's (crucible/scenarios/m5). Requires: docker (compose v2),
# go, jq and cosign (taken from the environment, the pinned build
# otherwise). Run from the repository root, on either architecture:
#   sh test/topology/scenario-m7-uc2.sh
set -eu

TOPO_DIR="$(dirname "$0")"
COMPOSE="docker compose -f $TOPO_DIR/compose-m7.yaml"
GEN="$TOPO_DIR/.m7"
REPORT="${TOPOLOGY_REPORT:-/tmp/tobby-topology-m7-uc2-report.txt}"
SOURCE_PORT=5320
NODE_PORT=5321
ZONE="zone-b"
RELOCATED="source-registry_5000/library/sample"

: >"$REPORT"
step() { printf '%s\n' "$*" | tee -a "$REPORT"; }
cleanup() { $COMPOSE down -v >/dev/null 2>&1 || true; rm -rf "$GEN"; }
fail() { step "FAIL $*"; cleanup; exit 1; }

step "== milestone-7 UC2 topology scenario $(date -u +%Y-%m-%dT%H:%M:%SZ) =="

COSIGN="${COSIGN:-cosign}"
if ! command -v "$COSIGN" >/dev/null 2>&1; then
    GOBIN="$(pwd)/$TOPO_DIR/.tools/bin" go install github.com/sigstore/cosign/v3/cmd/cosign@v3.0.4 ||
        fail "cosign unavailable and go install failed"
    COSIGN="$(pwd)/$TOPO_DIR/.tools/bin/cosign"
fi

GOOS=linux CGO_ENABLED=0 go build -trimpath -o "$TOPO_DIR/tobby" ./cmd/tobby || fail "building tobby"
docker build -q -t tobby-topology:local -f "$TOPO_DIR/Dockerfile.test" "$TOPO_DIR" >/dev/null ||
    fail "building the topology image"
rm -f "$TOPO_DIR/tobby"
step "ok   build: binary ($(uname -m) host), topology image"

$COMPOSE down -v >/dev/null 2>&1 || true
rm -rf "$GEN"
mkdir -p "$GEN"

# -- 1. Fixtures: image, key pair, one signed recipe -------------------------
$COMPOSE up -d source-registry >/dev/null 2>&1 || $COMPOSE up -d source-registry ||
    fail "starting the source registry"
i=0
until curl -fsS "http://127.0.0.1:$SOURCE_PORT/v2/" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the source registry never came up"
    sleep 0.2
done
IMG_DIGEST=$(go run ./test/topology/seed push "127.0.0.1:$SOURCE_PORT/library/sample:3.0.0") ||
    fail "seeding the sample image"
(cd "$GEN" && COSIGN_PASSWORD= "$COSIGN" generate-key-pair >/dev/null 2>&1) ||
    fail "generating the key pair"
cat >"$GEN/sample-app.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: sample-app
  version: 3.0.0
spec:
  ingredients:
    - name: sample
      kind: ContainerImage
      ref: source-registry:5000/library/sample
      version: 3.0.0
      digest: $IMG_DIGEST
EOF
RECIPE_DIGEST=$(go run ./test/topology/seed push-recipe \
    "127.0.0.1:$SOURCE_PORT/cookbook/sample-app:3.0.0" "$GEN/sample-app.yaml") ||
    fail "publishing the recipe"
COSIGN_PASSWORD= "$COSIGN" sign --key "$GEN/cosign.key" --yes --allow-insecure-registry \
    --use-signing-config=false --new-bundle-format=false --tlog-upload=false \
    "127.0.0.1:$SOURCE_PORT/cookbook/sample-app@$RECIPE_DIGEST" >/dev/null 2>&1 ||
    fail "cosign signing the recipe"
step "ok   fixtures: image $IMG_DIGEST, recipe signed"

# -- 2. Configurations -------------------------------------------------------
PUB_KEY_INDENTED=$(sed 's/^/        /' "$GEN/cosign.pub")
cat >"$GEN/connected.yaml" <<EOF
mode: mirror
zone: $ZONE
storage:
  root: /media/store
state:
  root: /var/lib/tobby/state
server:
  addr: :8080
auth:
  disabled: true
registries:
  insecure:
    - source-registry:5000
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: topology-m7
      key: |
$PUB_KEY_INDENTED
EOF
# The Retriever's name is the zone the medium is addressed to (FR-054).
cat >"$GEN/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: $ZONE
spec:
  cookbook: source-registry:5000/cookbook
  recipes:
    - name: sample-app
      version: "3.0.0"
EOF
# The destination side: the store is on /m2 (not /media), the zone registry
# is the only thing it can reach, and its trust root is its OWN.
cat >"$GEN/zone.yaml" <<EOF
mode: mirror
zone: $ZONE
storage:
  root: /m2/store
state:
  root: /var/lib/tobby/state
server:
  addr: :8080
auth:
  disabled: true
registries:
  insecure:
    - zone-registry:5000
destination:
  registry: zone-registry:5000
  cookbook: cookbook
trust:
  roots:
    - name: topology-m7
      key: |
$PUB_KEY_INDENTED
EOF
step "ok   configurations generated"

# -- 3. Connected side: manual synchronization onto the medium ---------------
$COMPOSE up -d tobby-connected >/dev/null 2>&1 || $COMPOSE up -d tobby-connected ||
    fail "starting the connected instance"
i=0
until curl -fsS "http://127.0.0.1:$NODE_PORT/readyz" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 100 ] && fail "the connected instance never became ready"
    sleep 0.2
done
$COMPOSE exec -T tobby-connected tobby sync --config /etc/tobby/connected.yaml --wait >/dev/null 2>&1 ||
    fail "tobby sync --wait failed"
MANIFEST=$($COMPOSE run --rm -T courier 'cat /a/store/meta/media.json') ||
    fail "FR-054: the synchronization wrote no media manifest"
printf '%s' "$MANIFEST" | jq -e --arg z "$ZONE" '.zone == $z and (.recipes | length) == 1' >/dev/null ||
    fail "FR-054: the manifest does not describe one delivery for $ZONE: $(printf %s "$MANIFEST" | head -c 250)"
step "ok   mirror synchronized by 'tobby sync --wait'; media manifest written for $ZONE"

# -- 4. Transport: copy, then destroy the original ---------------------------
$COMPOSE stop tobby-connected >/dev/null 2>&1
$COMPOSE run --rm -T courier 'cp -a /a/. /b/ && cp -a /a/. /c/' >/dev/null 2>&1 ||
    fail "copying the medium"
$COMPOSE rm -sfv tobby-connected >/dev/null 2>&1 || true
$COMPOSE stop source-registry >/dev/null 2>&1
$COMPOSE run --rm -T courier 'rm -rf /a/store /a/..?* /a/.[!.]* /a/*' >/dev/null 2>&1 || true
$COMPOSE run --rm -T courier 'test -f /b/store/meta/media.json' >/dev/null 2>&1 ||
    fail "the copy of the medium has no manifest"
step "ok   medium copied; original and source registry are gone"

# -- 5. The gap is structural -----------------------------------------------
$COMPOSE up -d zone-registry >/dev/null 2>&1 || $COMPOSE up -d zone-registry ||
    fail "starting the zone registry"
if $COMPOSE run --rm canary "curl -m 5 -fsS http://source-registry:5000/v2/" >/dev/null 2>&1; then
    fail "the isolated side reached the connected zone"
fi
if $COMPOSE run --rm canary "curl -m 5 -fsS https://example.com/" >/dev/null 2>&1; then
    fail "the isolated side reached the internet"
fi
i=0
until $COMPOSE run --rm canary "curl -m 3 -fsS http://zone-registry:5000/v2/" >/dev/null 2>&1; do
    i=$((i + 1)); [ "$i" -gt 50 ] && fail "the zone registry never came up"
    sleep 0.3
done
step "ok   egress canary failed as required; the zone registry is the only neighbour"

# -- 6. A damaged copy is blocked whole and pushes nothing (R-19) ------------
$COMPOSE run --rm -T courier '
    f=$(find /c/store/docker/registry/v2/blobs -name data -type f -size +1k | head -n 1)
    [ -n "$f" ] || exit 1
    sz=$(wc -c <"$f")
    truncate -s $((sz - 8)) "$f"
' >/dev/null 2>&1 || fail "damaging the second copy"
set +e
BAD=$($COMPOSE run --rm -T tobby-zone-damaged media verify --config /etc/tobby/zone.yaml --output json 2>/dev/null)
BAD_RC=$?
set -e
[ "$BAD_RC" -ne 0 ] || fail "R-19: a damaged medium verified clean"
printf '%s' "$BAD" | jq -e '.recipes[] | select(.name == "sample-app") | .pushable == false' >/dev/null ||
    fail "R-19: the damaged delivery is still pushable"
printf '%s' "$BAD" | jq -e '.recipes[] | select(.name == "sample-app") | .reason.path != null and .reason.path != ""' >/dev/null ||
    fail "R-19/FR-054: the blocked delivery does not name the offending file"
$COMPOSE run --rm -T tobby-zone-damaged media import --config /etc/tobby/zone.yaml --output json >/dev/null 2>&1 || true
CATALOG=$($COMPOSE run --rm canary "curl -m 5 -fsS http://zone-registry:5000/v2/_catalog")
printf '%s' "$CATALOG" | jq -e '(.repositories // []) | length == 0' >/dev/null ||
    fail "R-19: a blocked delivery reached the zone registry: $CATALOG"
step "ok   damaged copy blocked whole (exit $BAD_RC, offending file named), nothing pushed"

# -- 7. The intact copy verifies and is imported -----------------------------
$COMPOSE run --rm -T tobby-zone media verify --config /etc/tobby/zone.yaml --output json \
    >"$GEN/verify.json" 2>/dev/null || fail "the intact medium did not verify (exit $?)"
jq -e '.recipes[] | select(.name == "sample-app") | .pushable == true' "$GEN/verify.json" >/dev/null ||
    fail "the intact delivery is not pushable"
$COMPOSE run --rm -T tobby-zone media import --config /etc/tobby/zone.yaml --output json \
    >"$GEN/import.json" 2>/dev/null || fail "tobby media import failed (exit $?)"
CATALOG=$($COMPOSE run --rm canary "curl -m 5 -fsS http://zone-registry:5000/v2/_catalog")
printf '%s' "$CATALOG" | jq -e --arg r "$RELOCATED" '.repositories | index($r) != null' >/dev/null ||
    fail "the relocated ingredient is not in the zone registry: $CATALOG"
printf '%s' "$CATALOG" | jq -e '[.repositories[] | select(contains("cookbook"))] | length > 0' >/dev/null ||
    fail "FR-034: the signed recipe did not land in the zone cookbook: $CATALOG"
step "ok   intact copy verified and imported into the zone registry and cookbook"

# -- 8. A standard client inside the zone reads the pinned digest back -------
GOT=$($COMPOSE run --rm canary "curl -m 5 -fsS -D - -o /dev/null \
    -H 'Accept: application/vnd.oci.image.index.v1+json' \
    http://zone-registry:5000/v2/$RELOCATED/manifests/3.0.0" | tr -d '\r' |
    awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}') ||
    fail "a standard client inside the zone cannot read $RELOCATED"
[ "$GOT" = "$IMG_DIGEST" ] || fail "digest read in the zone ($GOT) differs from the pinned one ($IMG_DIGEST)"
step "ok   standard client in the zone reads the pinned digest ($GOT)"

cleanup
step "== scenario m7-uc2 PASSED — raw report: $REPORT =="
