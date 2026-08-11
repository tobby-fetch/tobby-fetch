#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Destroys everything the crucible created: instances, volumes, profiles,
# the project, and the two zone networks. The host's default project is
# never touched.
set -eu

PROJECT="tobby-crucible"

if incus project show "$PROJECT" >/dev/null 2>&1; then
    for inst in $(incus list --project "$PROJECT" -c n -f csv); do
        incus delete --force "$inst" --project "$PROJECT"
    done
    for vol in $(incus storage volume list --project "$PROJECT" -f csv 2>/dev/null | awk -F, '$1=="custom" {print $2}'); do
        pool=$(incus storage volume list --project "$PROJECT" -f csv | awk -F, -v v="$vol" '$2==v {print $NF; exit}')
        incus storage volume delete "${pool:-default}" "$vol" --project "$PROJECT" || true
    done
    for prof in tbc-connected-node tbc-airgap-node tbc-fixture; do
        incus profile delete "$prof" --project "$PROJECT" 2>/dev/null || true
    done
    incus project delete "$PROJECT"
    echo "deleted project $PROJECT"
fi

for net in tbc-net tbc-airgap; do
    incus network delete "$net" 2>/dev/null && echo "deleted network $net" || true
done
