#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# One-time crucible provisioning (ADR-0014): Incus project, the two zone
# networks, and the instance profiles. Idempotent: safe to re-run.
#
# Requires: a Linux host with KVM, Incus initialized, and a snapshot-capable
# storage pool (zfs or btrfs recommended; override with CRUCIBLE_POOL).
set -eu

PROJECT="tobby-crucible"
POOL="${CRUCIBLE_POOL:-default}"

exists() { incus "$1" show "$2" --project "$PROJECT" >/dev/null 2>&1; }

# -- Project: everything the crucible creates lives here ---------------------
if ! incus project show "$PROJECT" >/dev/null 2>&1; then
    incus project create "$PROJECT" \
        -c features.images=false \
        -c features.profiles=true
    echo "created project $PROJECT"
fi

# -- Networks ----------------------------------------------------------------
# Connected zone: ordinary NATed bridge.
if ! incus network show tbc-net >/dev/null 2>&1; then
    incus network create tbc-net \
        ipv4.address=10.180.10.1/24 ipv4.nat=true \
        ipv6.address=none
    echo "created network tbc-net (connected zone)"
fi

# Air-gapped zone: bridge with NO uplink and NO NAT — the gap is the absence
# of a route, proven by the canary at every scenario run (NFR-019).
if ! incus network show tbc-airgap >/dev/null 2>&1; then
    incus network create tbc-airgap \
        ipv4.address=10.180.20.1/24 ipv4.nat=false \
        ipv6.address=none
    echo "created network tbc-airgap (air-gapped zone)"
fi

# ipv4.nat=false alone is NOT a gap: the host kernel still routes between
# its bridges, so tbc-airgap could reach tbc-net (and any host-reachable
# network). The ACL makes the gap structural — intra-zone traffic allowed,
# everything else rejected in both directions (default action when ACLs
# are applied).
if ! incus network acl show tbc-airgap-noegress >/dev/null 2>&1; then
    incus network acl create tbc-airgap-noegress
    incus network acl rule add tbc-airgap-noegress egress \
        destination=10.180.20.0/24 action=allow
    incus network acl rule add tbc-airgap-noegress ingress \
        source=10.180.20.0/24 action=allow
    echo "created network ACL tbc-airgap-noegress"
fi
if [ "$(incus network get tbc-airgap security.acls)" != "tbc-airgap-noegress" ]; then
    incus network set tbc-airgap security.acls=tbc-airgap-noegress
    echo "applied ACL tbc-airgap-noegress to tbc-airgap"
fi

# -- Profiles ----------------------------------------------------------------
# Fidelity nodes are VMs (own kernel: media semantics, future RKE2 nodes);
# fixtures are cheap system containers.
if ! incus profile show tbc-connected-node --project "$PROJECT" >/dev/null 2>&1; then
    incus profile create tbc-connected-node --project "$PROJECT"
    incus profile device add tbc-connected-node eth0 nic network=tbc-net --project "$PROJECT"
    incus profile device add tbc-connected-node root disk path=/ pool="$POOL" --project "$PROJECT"
    incus profile set tbc-connected-node limits.cpu=2 limits.memory=2GiB --project "$PROJECT"
    echo "created profile tbc-connected-node"
fi

if ! incus profile show tbc-airgap-node --project "$PROJECT" >/dev/null 2>&1; then
    incus profile create tbc-airgap-node --project "$PROJECT"
    incus profile device add tbc-airgap-node eth0 nic network=tbc-airgap --project "$PROJECT"
    incus profile device add tbc-airgap-node root disk path=/ pool="$POOL" --project "$PROJECT"
    incus profile set tbc-airgap-node limits.cpu=2 limits.memory=2GiB --project "$PROJECT"
    echo "created profile tbc-airgap-node"
fi

if ! incus profile show tbc-fixture --project "$PROJECT" >/dev/null 2>&1; then
    incus profile create tbc-fixture --project "$PROJECT"
    incus profile device add tbc-fixture eth0 nic network=tbc-net --project "$PROJECT"
    incus profile device add tbc-fixture root disk path=/ pool="$POOL" --project "$PROJECT"
    incus profile set tbc-fixture limits.cpu=1 limits.memory=512MiB --project "$PROJECT"
    echo "created profile tbc-fixture"
fi

echo "crucible ready (project $PROJECT, pool $POOL)"
echo "next: ./crucible/run.sh m1"
