#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenario, track A (R-25): Tobby updates through its
# own channel, across a real air gap, exactly like the vulnerability
# database — by the standard mirror flow, under the SITE's signature.
#
#   1. Fixtures (connected zone): the recipe that carries Tobby itself
#      (examples/tobby.yaml in shape) — an image, the reference chart
#      packaged by helm, and the binaries of this very build as a
#      recipe-ready FileSet (four platforms) — at two versions: 0.6.0
#      signed by a "site" key, 0.6.1 signed by a "project" key only.
#   2. The mirror instance, trusting both keys, writes both onto a real
#      block-device medium.
#   3. The medium is detached and carried; the destination instance,
#      trusting the site key alone, verifies it: 0.6.0 is pushable, 0.6.1
#      is blocked on its signature (invariant 1 — the destination's trust
#      roots ignore the project key). The import pushes 0.6.0 only.
#   4. Inside the zone, the binary that crossed is byte-identical to the
#      one built, and a person deploys it: it runs, and states its version.
#   5. NFR-019: the binary carries no update mechanism at all; the update
#      is the recipe a person cooked, carried and deployed.
#
# Each staging step checks its own manipulation (the medium mounted, the
# fixture signed by the key it claims) before the verdict it prepares.
#
# Host tooling on top of the crucible baseline: helm (already required by
# m4), jq, curl, cosign (installed on demand like m3-m5).
#
# Run alone:   sh crucible/scenarios/m6/a-self-update.sh
# Dispatched:  ./crucible/run.sh m6
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m6}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m6-a-self-update.txt}"
. "$DIR/../../lib.sh"

IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.24}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
MEDIUM_IMG="${CRUCIBLE_MEDIUM_IMG:-/tmp/tbc-m6u-medium.img}"
ZONE="zone-b"
WORK="$(mktemp -d)"
INSTANCES="tbc-m6u-source tbc-m6u-connected tbc-m6u-dest tbc-m6u-isolated"
COSIGN="${COSIGN:-cosign}"

report_start

release_leftovers() {
    for inst in $INSTANCES; do inc delete --force "$inst" 2>/dev/null || true; done
    if [ -f "$MEDIUM_IMG" ]; then
        losetup -j "$MEDIUM_IMG" 2>/dev/null | cut -d: -f1 |
            while read -r dev; do losetup -d "$dev" 2>/dev/null || true; done
        rm -f "$MEDIUM_IMG"
    fi
}
cleanup() { release_leftovers; rm -rf "$WORK"; }

# -- 0. Tooling and the binaries under test ----------------------------------
command -v jq >/dev/null 2>&1 || fail "jq not found"
command -v helm >/dev/null 2>&1 || fail "helm not found"
if ! command -v "$COSIGN" >/dev/null 2>&1; then
    GOBIN=/usr/local/bin go install github.com/sigstore/cosign/v2/cmd/cosign@latest >/dev/null 2>&1 ||
        fail "installing cosign"
    COSIGN=/usr/local/bin/cosign
fi
for plat in linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
    os=${plat%/*}; arch=${plat#*/}; ext=""; [ "$os" = windows ] && ext=.exe
    ( cd "$ROOT" && GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -o "$WORK/tobby-$os-$arch$ext" ./cmd/tobby ) ||
        fail "building tobby for $plat"
done
cp "$WORK/tobby-linux-$ARCH" "$WORK/tobby"
check "built tobby for the four release platforms"

# -- 1. Nodes and medium --------------------------------------------------------
release_leftovers
for spec in "tbc-m6u-source tbc-connected-node" "tbc-m6u-connected tbc-connected-node" \
            "tbc-m6u-dest tbc-airgap-node" "tbc-m6u-isolated tbc-airgap-node"; do
    set -- $spec
    inc launch "$IMAGE" "$1" --profile "$2" \
        -c security.syscalls.intercept.mount=true \
        -c security.syscalls.intercept.mount.allowed=ext4 >/dev/null
done
truncate -s 2G "$MEDIUM_IMG"; MEDIUM_DEV=$(losetup -f --show "$MEDIUM_IMG")
inc config device add tbc-m6u-connected medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
for inst in $INSTANCES; do
    inc file push "$WORK/tobby" "$inst/usr/bin/tobby" >/dev/null
    inc exec "$inst" -- chmod +x /usr/bin/tobby
done
inc exec tbc-m6u-connected -- sh -c '
    n=0
    until apk add --no-cache e2fsprogs >/dev/null 2>&1; do
        n=$((n + 1)); [ "$n" -ge 30 ] && { echo "no package mirror reachable" >&2; exit 1; }; sleep 2
    done
    base=$(awk "{print \$2; exit}" /proc/self/uid_map)
    mkfs.ext4 -q -E root_owner="$base:$base" /dev/sdb && mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby
' || fail "formatting and mounting the medium"
inc exec tbc-m6u-connected -- mountpoint -q /media/tobby || fail "staging: the medium is not mounted"
SOURCE_IP=$(instance_ip tbc-m6u-source)
if inc exec tbc-m6u-isolated -- wget -q -T 5 -O /dev/null "http://$SOURCE_IP:8080/healthz" >/dev/null 2>&1; then
    fail "the isolated node reached the connected zone: there is no air gap"
fi
check "nodes launched, medium mounted, egress canary failed as required"

# -- 2. Fixtures: Tobby at two versions, signed by two different keys ---------
inc exec tbc-m6u-source -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m6u-source http://127.0.0.1:8080/healthz
S="$SOURCE_IP:8080"
seed() { ( cd "$ROOT" && go run ./test/topology/seed "$@" ); }
( cd "$WORK" && mkdir site project &&
  ( cd site && COSIGN_PASSWORD= "$COSIGN" generate-key-pair >/dev/null 2>&1 ) &&
  ( cd project && COSIGN_PASSWORD= "$COSIGN" generate-key-pair >/dev/null 2>&1 ) ) || fail "generating the two keys"
for v in 0.6.0 0.6.1; do
    IMG=$(seed push "$S/ghcr.io/tobby-fetch/tobby-fetch:v$v") || fail "publishing the image $v"
    BIN=$(seed push-binaries "$S/ghcr.io/tobby-fetch/binaries/tobby:v$v" \
        "linux/amd64=$WORK/tobby-linux-amd64" "linux/arm64=$WORK/tobby-linux-arm64" \
        "windows/amd64=$WORK/tobby-windows-amd64.exe" "windows/arm64=$WORK/tobby-windows-arm64.exe") ||
        fail "publishing the binaries $v"
    rm -rf "$WORK/chart" && cp -r "$ROOT/deploy/charts/tobby" "$WORK/chart"
    sed -i "s/^version:.*/version: $v/" "$WORK/chart/Chart.yaml"
    helm package "$WORK/chart" -d "$WORK" >/dev/null || fail "packaging the chart $v"
    CHART=$(helm push "$WORK/tobby-$v.tgz" "oci://$S/ghcr.io/tobby-fetch/charts" --plain-http 2>&1 |
        awk '/^Digest:/{print $2}') || true
    [ -n "$CHART" ] || fail "pushing the chart $v"
    cat >"$WORK/recipe-$v.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: tobby
  version: $v
spec:
  ingredients:
    - name: tobby-image
      kind: ContainerImage
      ref: ghcr.io/tobby-fetch/tobby-fetch
      version: v$v
      digest: $IMG
    - name: tobby-chart
      kind: HelmChart
      ref: ghcr.io/tobby-fetch/charts/tobby
      version: $v
      digest: $CHART
      vendorDependencies: false
    - name: tobby-binaries
      kind: FileSet
      ref: ghcr.io/tobby-fetch/binaries/tobby
      version: v$v
      digest: $BIN
      platforms: [linux/amd64, linux/arm64, windows/amd64, windows/arm64]
EOF
    key=site; [ "$v" = 0.6.1 ] && key=project
    dgst=$(seed push-recipe "$S/ghcr.io/cookbook/tobby:$v" "$WORK/recipe-$v.yaml") || fail "publishing recipe $v"
    COSIGN_PASSWORD= "$COSIGN" sign --key "$WORK/$key/cosign.key" --yes --allow-insecure-registry \
        --use-signing-config=false --new-bundle-format=false --tlog-upload=false \
        "$S/ghcr.io/cookbook/tobby@$dgst" >/dev/null 2>&1 || fail "signing recipe $v"
    # Staging: the signature verifies with the key it claims, and not with the other.
    other=project; [ "$key" = project ] && other=site
    COSIGN_PASSWORD= "$COSIGN" verify --key "$WORK/$key/cosign.pub" --allow-insecure-registry --insecure-ignore-tlog \
        "$S/ghcr.io/cookbook/tobby@$dgst" >/dev/null 2>&1 || fail "staging: recipe $v does not verify with the $key key"
    if COSIGN_PASSWORD= "$COSIGN" verify --key "$WORK/$other/cosign.pub" --allow-insecure-registry --insecure-ignore-tlog \
        "$S/ghcr.io/cookbook/tobby@$dgst" >/dev/null 2>&1; then
        fail "staging: recipe $v also verifies with the $other key"
    fi
done
check "tobby 0.6.0 signed by the site key, 0.6.1 by the project key only (each checked against both)"

# -- 3. The standard mirror flow ----------------------------------------------
cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: $ZONE
spec:
  cookbook: ghcr.io/cookbook
  recipes:
    - name: tobby
      version: "0.6.0"
    - name: tobby
      version: "0.6.1"
EOF
inc file push "$WORK/retriever.yaml" tbc-m6u-connected/etc/tobby/retriever.yaml --create-dirs >/dev/null
inc file push "$WORK/site/cosign.pub" tbc-m6u-connected/etc/tobby/site.pub >/dev/null
inc file push "$WORK/project/cosign.pub" tbc-m6u-connected/etc/tobby/project.pub >/dev/null
inc exec tbc-m6u-connected -- sh -c "cat >/etc/tobby/config.yaml <<'EOF'
mode: mirror
zone: $ZONE
storage:
  root: /media/tobby/store
state:
  root: /var/lib/tobby-state
auth:
  disabled: true
registries:
  insecure: [$S]
  substitutions:
    ghcr.io: $S
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: site
      keyFile: /etc/tobby/site.pub
    - name: project
      keyFile: /etc/tobby/project.pub
EOF"
T=$(inc exec tbc-m6u-connected -- sh -c 'tobby sync --wait --output json 2>/dev/null' || true)
echo "$T" | jq -e '[.task.items[] | select(.status == "failed")] | length == 0' >/dev/null ||
    fail "the mirror synchronization failed: $(echo "$T" | jq -c '[.task.items[] | select(.status=="failed") | {name, code: .error.code}]')"
inc exec tbc-m6u-connected -- cat /media/tobby/store/meta/media.json | jq -e '[.recipes[] | select(.name=="tobby")] | length == 2' >/dev/null ||
    fail "the medium does not carry both versions"
inc exec tbc-m6u-connected -- sh -c 'sync && umount /media/tobby'
inc config device remove tbc-m6u-connected medium >/dev/null
check "mirror flow: both versions written on the medium; medium detached"

# -- 4. The isolated zone, trusting its site's key alone -----------------------
DEST_IP=$(instance_ip tbc-m6u-dest)
inc exec tbc-m6u-dest -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:5000 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m6u-dest http://127.0.0.1:5000/healthz
inc config device add tbc-m6u-isolated medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
inc exec tbc-m6u-isolated -- sh -c 'mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby && test -f /media/tobby/store/meta/media.json' ||
    fail "staging: the medium did not mount on the isolated node"
inc file push "$WORK/site/cosign.pub" tbc-m6u-isolated/etc/tobby/site.pub --create-dirs >/dev/null
inc exec tbc-m6u-isolated -- sh -c "cat >/etc/tobby/config.yaml <<'EOF'
mode: mirror
zone: $ZONE
storage:
  root: /media/tobby/store
state:
  root: /var/lib/tobby-state
auth:
  disabled: true
registries:
  insecure: [$DEST_IP:5000]
trust:
  roots:
    - name: site
      keyFile: /etc/tobby/site.pub
destination:
  registry: $DEST_IP:5000
  cookbook: cookbook
EOF"
V=$(inc exec tbc-m6u-isolated -- sh -c 'tobby media verify --output json 2>/dev/null' || true)
echo "$V" | jq -e '.recipes[] | select(.name=="tobby" and .version=="0.6.0") | .pushable == true' >/dev/null ||
    fail "the site-signed release is not pushable: $(echo "$V" | jq -c '.recipes')"
echo "$V" | jq -e '.recipes[] | select(.name=="tobby" and .version=="0.6.1") | .pushable == false' >/dev/null ||
    fail "invariant 1: a release signed by the project key alone is pushable in the zone"
inc exec tbc-m6u-isolated -- sh -c 'tobby media import --output json' >"$WORK/import.json" 2>/dev/null || true
TAGS=$(curl -fsS "http://$DEST_IP:5000/v2/cookbook/tobby/tags/list" | jq -r '.tags | join(",")')
case ",$TAGS," in *,0.6.0,*) ;; *) fail "0.6.0 did not reach the zone cookbook (tags: $TAGS)" ;; esac
case ",$TAGS," in *,0.6.1,*) fail "invariant 1: 0.6.1 reached the zone cookbook" ;; esac
check "verification: 0.6.0 pushable, 0.6.1 blocked on its signature; the zone cookbook holds 0.6.0 only"

# -- 5. The binary that crossed is the one built, and it runs -----------------
ACCEPT='Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json'
R="http://$DEST_IP:5000/v2/ghcr.io/tobby-fetch/binaries/tobby"
CHILD=$(curl -fsS -H "$ACCEPT" "$R/manifests/v0.6.0" |
    jq -r --arg a "$ARCH" '.manifests[] | select(.platform.os=="linux" and .platform.architecture==$a) | .digest')
[ -n "$CHILD" ] || fail "no linux/$ARCH child in the zone's binaries FileSet"
LAYER=$(curl -fsS -H "$ACCEPT" "$R/manifests/$CHILD" | jq -r '.layers[0].digest')
curl -fsS "$R/blobs/$LAYER" | tar -xzf - -C "$WORK" tobby 2>/dev/null && mv "$WORK/tobby" "$WORK/tobby-arrived" ||
    fail "extracting the binary that crossed"
[ "$(sha256sum <"$WORK/tobby-arrived")" = "$(sha256sum <"$WORK/tobby-linux-$ARCH")" ] ||
    fail "the binary that crossed is not byte-identical to the one built"
inc file push "$WORK/tobby-arrived" tbc-m6u-isolated/opt/tobby-0.6.0 >/dev/null
inc exec tbc-m6u-isolated -- sh -c 'chmod +x /opt/tobby-0.6.0 && /opt/tobby-0.6.0 version --output json' | jq -e '.version != null' >/dev/null ||
    fail "the binary that crossed does not run"
check "the linux/$ARCH binary that crossed is byte-identical to the build, and runs once deployed by hand"

# -- 6. NFR-019: there is nothing that looks for a newer version -------------
# The gap makes an attempt impossible (the canary above); this checks there
# is no such mechanism to attempt one: the deployed binary knows no update
# command, and the update was the recipe a person carried.
if inc exec tbc-m6u-isolated -- /opt/tobby-0.6.0 update >/dev/null 2>&1; then
    fail "NFR-019: the binary carries an update command"
fi
check "NFR-019: no self-update mechanism — the update is the recipe a person carried and deployed"

cleanup
check "scenario m6/a-self-update complete — report: $CRUCIBLE_REPORT"
