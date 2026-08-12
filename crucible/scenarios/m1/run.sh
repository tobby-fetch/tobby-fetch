#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-1 reduced crucible scenario (ADR-0014, DEVELOPMENT-PLAN §2.1):
# registry push/pull across the topology, egress canary, and a media
# round-trip where the medium is a REAL detachable block device — the
# fidelity the containerized CI tier cannot provide.
#
# Topology:
#   tbc-m1-connected  (container, tbc-net)     tobby serve --mode=passthrough
#   tbc-m1-airgap     (container, tbc-airgap)  tobby serve --mode=mirror
#   medium            (loop-backed block dev)  attached, filled, moved
#
# Both nodes run as system containers at this milestone: no scenario step
# needs its own kernel yet. VM profiles (tbc-*-node) exist for the
# milestones that do (filesystem limits, power loss, RKE2).
#
# The medium is a host loop device passed through as a unix-block device:
# Incus cannot attach custom block-type storage volumes to containers (any
# version, verified through 7.x), and the disposable acceptance VM
# (ADR-0014) has no nested KVM, so Incus VMs are not an option there. The
# guests format and mount it themselves via seccomp mount interception
# (security.syscalls.intercept.mount.allowed=ext4). Requires root on the
# crucible host for losetup — same bar as Incus administration.
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
. "$DIR/../../lib.sh"

IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.22}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
MEDIUM_IMG="${CRUCIBLE_MEDIUM_IMG:-/tmp/tbc-m1-medium.img}"

report_start

# -- Build the binary under test --------------------------------------------
( cd "$DIR/../../.." && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -o /tmp/tobby-crucible ./cmd/tobby )
check "built tobby for linux/$ARCH"

# -- Fresh instances ---------------------------------------------------------
for inst in tbc-m1-connected tbc-m1-airgap; do
    inc delete --force "$inst" 2>/dev/null || true
done
# Release any medium leftover from a previous run.
if [ -f "$MEDIUM_IMG" ]; then
    losetup -j "$MEDIUM_IMG" 2>/dev/null | cut -d: -f1 |
        while read -r dev; do losetup -d "$dev" || true; done
    rm -f "$MEDIUM_IMG"
fi

inc launch "$IMAGE" tbc-m1-connected --profile tbc-connected-node \
    -c security.syscalls.intercept.mount=true \
    -c security.syscalls.intercept.mount.allowed=ext4
inc launch "$IMAGE" tbc-m1-airgap --profile tbc-airgap-node \
    -c security.syscalls.intercept.mount=true \
    -c security.syscalls.intercept.mount.allowed=ext4
check "instances launched (connected + air-gapped)"

# The medium: a real block device, attached raw to the connected node.
truncate -s 2G "$MEDIUM_IMG"
MEDIUM_DEV=$(losetup -f --show "$MEDIUM_IMG")
inc config device add tbc-m1-connected medium unix-block \
    source="$MEDIUM_DEV" path=/dev/sdb
check "block-device medium created ($MEDIUM_DEV) and attached to the connected node"

for inst in tbc-m1-connected tbc-m1-airgap; do
    inc file push /tmp/tobby-crucible "$inst/usr/bin/tobby"
    inc exec "$inst" -- chmod +x /usr/bin/tobby
done

# Format the medium from inside the guest and mount it — the store lives on
# the medium, exactly like a transportable workstation setup (FR-050).
# util-linux only on the connected node: the air-gapped node has no route to
# any package mirror (that is the point) and uses busybox mount -t ext4.
# root_owner: on-disk uids are host-view numbers; format with the host uid
# the container root maps to (/proc/self/uid_map) so the guests own the
# filesystem after the (unshifted) intercepted mount.
inc exec tbc-m1-connected -- sh -c '
    apk add --no-cache e2fsprogs util-linux >/dev/null 2>&1 || true
    base=$(awk "{print \$2; exit}" /proc/self/uid_map)
    mkfs.ext4 -q -E root_owner="$base:$base" /dev/sdb &&
        mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby
'
check "medium formatted (ext4) and mounted on the connected node"

# -- Connected side: serve and fill the store --------------------------------
# m1 exercises the registry/store mechanics: authentication is opted out
# EXPLICITLY (FR-075 — audited, never silent). The secure posture is the
# m2 scenario's object.
inc exec tbc-m1-connected -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/media/tobby/store \
        TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m1-connected http://127.0.0.1:8080/readyz
check "connected instance ready (store on the medium)"

# Push through the wire protocol from the host side via the instance's
# network address; oras/crane may not exist on the acceptance host, so the
# scenario uses the repository's own seeder through a host proxy port.
CONNECTED_IP=$(inc list tbc-m1-connected -c 4 -f csv | awk '{print $1}' | head -1)
( cd "$DIR/../../.." && go run ./test/topology/seed push "$CONNECTED_IP:8080/docker.io/library/sample:1.0.0" ) \
    >/tmp/tbc-m1-digest || fail "pushing into the connected store"
DIGEST=$(cat /tmp/tbc-m1-digest)
( cd "$DIR/../../.." && go run ./test/topology/seed pull "$CONNECTED_IP:8080/docker.io/library/sample:1.0.0" "$DIGEST" ) \
    >/dev/null || fail "pull-back from the connected store"
check "embedded registry round-trip on the connected node ($DIGEST)"

# -- Detach the medium, move it across the gap -------------------------------
inc exec tbc-m1-connected -- sh -c 'sync && umount /media/tobby'
inc config device remove tbc-m1-connected medium
check "medium unmounted and detached (transport begins)"

inc config device add tbc-m1-airgap medium unix-block \
    source="$MEDIUM_DEV" path=/dev/sdb
inc exec tbc-m1-airgap -- sh -c 'mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby'
check "medium re-attached and mounted on the air-gapped node"

# -- Egress canary: the gap must be structural (NFR-019) ---------------------
# busybox wget: the air-gapped node can never install curl — no route to any
# mirror, by construction.
if inc exec tbc-m1-airgap -- wget -q -T 5 -O /dev/null "http://$CONNECTED_IP:8080/healthz" >/dev/null 2>&1; then
    fail "air-gapped node reached the connected zone"
fi
if inc exec tbc-m1-airgap -- wget -q -T 5 -O /dev/null http://example.com/ >/dev/null 2>&1; then
    fail "air-gapped node reached the internet"
fi
check "egress canary failed as required (air gap proven by construction)"

# -- Air-gapped side: the transported store serves ---------------------------
inc exec tbc-m1-airgap -- sh -c '
    nohup env TOBBY_MODE=mirror TOBBY_STORAGE_ROOT=/media/tobby/store \
        TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m1-airgap http://127.0.0.1:8080/readyz
inc exec tbc-m1-airgap -- wget -q -O /dev/null \
    --header "Accept: application/vnd.oci.image.index.v1+json" \
    "http://127.0.0.1:8080/v2/docker.io/library/sample/manifests/$DIGEST" ||
    fail "transported content not servable inside the air-gapped zone"
check "media round-trip: content pushed in the connected zone serves across the air gap"

# -- Cleanup ------------------------------------------------------------------
for inst in tbc-m1-connected tbc-m1-airgap; do
    inc delete --force "$inst"
done
losetup -d "$MEDIUM_DEV" 2>/dev/null || true
rm -f "$MEDIUM_IMG"
check "scenario m1 complete — report: $REPORT"
