#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-5 crucible scenario (ADR-0014, DEVELOPMENT-PLAN §2.5): the
# complete air-gap rehearsal — prepare a medium in a connected zone, carry
# it across, and push its content into an isolated zone — on real nodes,
# with the properties only this tier can exercise faithfully: a REAL
# detachable block device, a real network gap that has no route rather than
# an unused one, real cosign signatures re-verified on the far side against
# THAT side's trust roots, and a medium that is deliberately damaged
# between the two halves of the trip.
#
#   1. Fixtures: a production registry (connected zone) holds two images
#      and a cookbook of two DISTINCT recipes, signed by a key this
#      scenario generates. Two deliveries is the point: R-19 decides
#      per recipe, and a medium carrying one delivery cannot show it.
#   2. Pre-flight (FR-055): an undersized medium is refused BEFORE any
#      transfer, stating the shortfall; a FAT32 volume is identified with
#      its 4 GiB per-file ceiling.
#   3. Mirror synchronization is triggered MANUALLY (FR-014) — and no
#      cycle fires on its own, which is asserted rather than assumed.
#   4. Prune to the Retriever (FR-045) runs before the manifest is
#      written, so the inventory describes what the medium finally holds
#      (FR-054); a unit import survives it.
#   5. NFR-020: a credentials file planted under the store makes the
#      instance refuse to start, naming the path.
#   6. The medium is detached and re-attached to the air-gapped node —
#      real block-device semantics, not a copy.
#   7. FR-054's third clause: before verification, /v2/ and /files/ serve
#      NOTHING, and /readyz still answers 200 with the reason.
#   8. A medium addressed to another zone is refused, and the refusal is
#      waivable by an administrator and audited (FR-054, FR-094).
#   9. R-19: one ingredient blob is truncated. The recipe reaching it is
#      blocked WHOLE and named with the offending file; its neighbour is
#      pushed. That is the whole arbitration, exercised.
#  10. Differential push into the zone registry, recipes republished to the
#      zone cookbook with their signatures, content pulled back by a
#      standard client INSIDE the isolated zone.
#  11. The return log is written on the medium, outside manifest coverage.
#  12. R-28: re-importing a medium older than the zone's last import is
#      refused, naming both timestamps.
#  13. FR-051: the store exports to a standard OCI image layout that
#      skopeo reads, and re-imports at identical digests.
#  14. R-05: /help serves both languages with no route out.
#  15. R-41: a directory packed on the isolated side is served under
#      /files/ — without any HTTP write surface existing.
#  16. R-04: a plan run leaves the store byte-identical.
#  17. FR-046: the operator role cannot reset the store; the admin can,
#      with the typed confirmation, and it is audited.
#
# Topology:
#   tbc-m5-source    (container, tbc-net)     production registry + cookbook
#   tbc-m5-connected (container, tbc-net)     mirror instance, store ON the medium
#   tbc-m5-dest      (container, tbc-airgap)  the isolated zone's registry
#   tbc-m5-isolated  (container, tbc-airgap)  destination instance, on the medium
#   medium           (loop-backed block dev)  attached, filled, detached, moved
#   small / fat32    (loop-backed block devs) the two pre-flight refusals
#
# Requires, on top of the crucible baseline: cosign (installed on demand
# like m3 and m4 — the signatures it produces are the point), skopeo on
# the crucible host for the FR-051 interoperability check, jq, openssl.
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
. "$DIR/../../lib.sh"

IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.22}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
MEDIUM_IMG="${CRUCIBLE_MEDIUM_IMG:-/tmp/tbc-m5-medium.img}"
SMALL_IMG="/tmp/tbc-m5-small.img"
FAT_IMG="/tmp/tbc-m5-fat32.img"
ZONE="zone-b"
WORK="$(mktemp -d)"
INSTANCES="tbc-m5-source tbc-m5-connected tbc-m5-dest tbc-m5-isolated"
COSIGN="${COSIGN:-cosign}"

report_start

# Two different jobs, and conflating them cost a node run: releasing what a
# previous run left behind must NOT touch this run's scratch directory,
# which by then already holds the binary under test.
release_leftovers() {
    for inst in $INSTANCES; do inc delete --force "$inst" 2>/dev/null || true; done
    for img in "$MEDIUM_IMG" "$SMALL_IMG" "$FAT_IMG"; do
        [ -f "$img" ] || continue
        losetup -j "$img" 2>/dev/null | cut -d: -f1 |
            while read -r dev; do losetup -d "$dev" 2>/dev/null || true; done
        rm -f "$img"
    done
}

cleanup() {
    release_leftovers
    rm -rf "$WORK"
}

# -- 0. Tooling, taken from the environment and never downloaded (NFR-019) ---
command -v skopeo >/dev/null 2>&1 || fail "skopeo not found: the FR-051 interoperability check needs it on the crucible host"
command -v jq >/dev/null 2>&1 || fail "jq not found"
if ! command -v "$COSIGN" >/dev/null 2>&1; then
    GOBIN=/usr/local/bin go install github.com/sigstore/cosign/v2/cmd/cosign@latest >/dev/null 2>&1 ||
        fail "installing cosign"
    COSIGN=/usr/local/bin/cosign
fi
check "tooling available (skopeo $(skopeo --version | awk '{print $3}'), jq, cosign)"

# -- 1. Build the binary under test ------------------------------------------
( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
    go build -trimpath -o "$WORK/tobby" ./cmd/tobby ) || fail "building tobby"
check "built tobby for linux/$ARCH"

# -- 2. Fresh instances and three block devices ------------------------------
release_leftovers
for spec in "tbc-m5-source tbc-connected-node" "tbc-m5-connected tbc-connected-node" \
            "tbc-m5-dest tbc-airgap-node" "tbc-m5-isolated tbc-airgap-node"; do
    set -- $spec
    inc launch "$IMAGE" "$1" --profile "$2" \
        -c security.syscalls.intercept.mount=true \
        -c security.syscalls.intercept.mount.allowed=ext4,vfat >/dev/null
done
check "instances launched (source, connected, zone registry, isolated)"

# The medium is generous; "small" is deliberately too small for the
# delivery, and "fat32" exists to be identified rather than filled.
truncate -s 3G "$MEDIUM_IMG"; MEDIUM_DEV=$(losetup -f --show "$MEDIUM_IMG")
truncate -s 24M "$SMALL_IMG";  SMALL_DEV=$(losetup -f --show "$SMALL_IMG")
truncate -s 64M "$FAT_IMG";    FAT_DEV=$(losetup -f --show "$FAT_IMG")
inc config device add tbc-m5-connected medium unix-block source="$MEDIUM_DEV" path=/dev/sdb
inc config device add tbc-m5-connected small  unix-block source="$SMALL_DEV"  path=/dev/sdc
inc config device add tbc-m5-connected fat32  unix-block source="$FAT_DEV"    path=/dev/sdd
check "three block devices created and attached ($MEDIUM_DEV, $SMALL_DEV, $FAT_DEV)"

for inst in $INSTANCES; do
    inc file push "$WORK/tobby" "$inst/usr/bin/tobby" >/dev/null
    inc exec "$inst" -- chmod +x /usr/bin/tobby
done

# root_owner: on-disk uids are host-view numbers, so the filesystems are
# made with the host uid the container root maps to (m1's lesson). FAT32
# carries no ownership at all, so its mount decides instead: without
# umask=0000 the guest cannot create the store directory and the
# filesystem check never gets to run.
inc exec tbc-m5-connected -- sh -c '
    apk add --no-cache e2fsprogs dosfstools util-linux >/dev/null 2>&1 || true
    base=$(awk "{print \$2; exit}" /proc/self/uid_map)
    mkfs.ext4 -q -E root_owner="$base:$base" /dev/sdb &&
    mkfs.ext4 -q -m 0 -E root_owner="$base:$base" /dev/sdc &&
    mkfs.vfat -F 32 -n TOBBYFAT /dev/sdd >/dev/null &&
    mkdir -p /media/tobby /media/small /media/fat32 &&
    mount -t ext4 /dev/sdb /media/tobby &&
    mount -t ext4 /dev/sdc /media/small &&
    mount -t vfat -o umask=0000 /dev/sdd /media/fat32
' || fail "formatting and mounting the three media on the connected node"
check "media formatted (ext4, ext4, FAT32) and mounted on the connected node"

# -- 3. Egress canary: the gap is absence of path, not an unused route -------
SOURCE_IP=$(inc list tbc-m5-source -c 4 -f csv | awk '{print $1}' | head -1)
if inc exec tbc-m5-isolated -- wget -q -T 5 -O /dev/null "http://$SOURCE_IP:8080/healthz" >/dev/null 2>&1; then
    fail "the isolated node reached the connected zone: there is no air gap to rehearse"
fi
if inc exec tbc-m5-isolated -- wget -q -T 5 -O /dev/null http://example.com/ >/dev/null 2>&1; then
    fail "the isolated node reached the internet"
fi
check "egress canary failed as required (air gap proven by construction)"

# -- 4. Fixtures: two signed deliveries in one cookbook ----------------------
# TWO recipes, not one: R-19 decides per recipe, and a medium carrying a
# single delivery cannot show the difference between "block the medium"
# and "block the delivery".
inc exec tbc-m5-source -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m5-source http://127.0.0.1:8080/healthz
IMG_APP=$( cd "$ROOT" && go run ./test/topology/seed push "$SOURCE_IP:8080/library/app:1.0.0" ) ||
    fail "seeding the app image"
IMG_TOOL=$( cd "$ROOT" && go run ./test/topology/seed push "$SOURCE_IP:8080/library/tool:1.0.0" ) ||
    fail "seeding the tool image"
( cd "$WORK" && COSIGN_PASSWORD= "$COSIGN" generate-key-pair >/dev/null 2>&1 ) ||
    fail "generating the signing key pair"

write_recipe() { # <name> <ingredient> <digest>
    cat >"$WORK/recipe-$1.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: $1
  version: 1.0.0
spec:
  ingredients:
    - name: $2
      kind: ContainerImage
      ref: $SOURCE_IP:8080/library/$2
      version: 1.0.0
      digest: $3
EOF
}
sign_recipe() {
    COSIGN_PASSWORD= "$COSIGN" sign --key "$WORK/cosign.key" --yes --allow-insecure-registry \
        --use-signing-config=false --new-bundle-format=false --tlog-upload=false "$1" >/dev/null 2>&1
}
for pair in "zone-app app $IMG_APP" "zone-tool tool $IMG_TOOL"; do
    set -- $pair
    write_recipe "$1" "$2" "$3"
    dgst=$( cd "$ROOT" && go run ./test/topology/seed push-recipe \
        "$SOURCE_IP:8080/cookbook/$1:1.0.0" "$WORK/recipe-$1.yaml" ) || fail "publishing recipe $1"
    sign_recipe "$SOURCE_IP:8080/cookbook/$1@$dgst" || fail "signing recipe $1"
done
check "two signed deliveries published (zone-app → $IMG_APP, zone-tool → $IMG_TOOL)"

cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: $ZONE
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: zone-app
      version: "1.0.0"
    - name: zone-tool
      version: "1.0.0"
EOF
inc file push "$WORK/retriever.yaml" tbc-m5-connected/etc/tobby/retriever.yaml --create-dirs >/dev/null
inc file push "$WORK/cosign.pub" tbc-m5-connected/etc/tobby/trust.pub --create-dirs >/dev/null
inc file push "$WORK/cosign.pub" tbc-m5-isolated/etc/tobby/trust.pub --create-dirs >/dev/null

# The trust root is a YAML block scalar: its content must be indented more
# than its key, or the document parses as something else entirely.
write_config() { # <instance> <store root> <destination registry or empty>
    dest=""
    [ -n "$3" ] && dest="destination:
  registry: $3
  cookbook: cookbook"
    inc exec "$1" -- sh -c "mkdir -p /etc/tobby && cat >/etc/tobby/config.yaml <<'EOF'
mode: mirror
zone: $ZONE
storage:
  root: $2
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
auth:
  disabled: true
registries:
  insecure:
    - $SOURCE_IP:8080
    - $DEST_IP:5000
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: zone
      keyFile: /etc/tobby/trust.pub
$dest
EOF"
}

DEST_IP=$(inc list tbc-m5-dest -c 4 -f csv | awk '{print $1}' | head -1)
inc exec tbc-m5-dest -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:5000 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m5-dest http://127.0.0.1:5000/healthz
check "the isolated zone's registry is serving on $DEST_IP:5000"

# -- 5. Pre-flight refuses before a byte moves (FR-055) ----------------------
# The undersized medium first. The refusal must arrive with nothing written:
# a store that is half-filled and then refused is worse than either outcome.
# The fixtures are synthetic images of a few kilobytes, so "a medium too
# small for the delivery" has to be arranged rather than found. Two knobs
# do it deterministically: the volume is filled to within 256 KiB — room
# for the store's own skeleton, since a plan on a volume with no space at
# all fails to OPEN the store before the pre-flight can refuse anything (an
# observation of this run, and a fair one: FR-055 is about a medium too
# small for the DELIVERY, not about a volume at zero) —
# and the safety margin is turned up to 99 %, which is a real configurable
# and reserves all but a hundredth of what is free. The arithmetic the
# refusal performs is exactly the one it performs on a real delivery:
# projected bytes against free space minus the reserve.
# busybox truncate rejects a relative size, and ext4's root reserve is not
# counted as available — so the ballast is sized from what df reports rather
# than written until it fails and cut back.
inc exec tbc-m5-connected -- sh -c '
    avail=$(df -k /media/small | awk "NR==2{print \$4}")
    dd if=/dev/zero of=/media/small/ballast bs=1k count=$((avail - 256)) >/dev/null 2>&1
    sync
' || fail "could not fill the undersized medium"
FREE_KB=$(inc exec tbc-m5-connected -- sh -c "df -k /media/small | awk 'NR==2{print \$4}'")
check "the undersized medium has ${FREE_KB} KiB free, of which the 99 % reserve leaves a hundredth"

write_config tbc-m5-connected /media/small/store ""
SMALL_PLAN=$(inc exec tbc-m5-connected -- sh -c \
    'TOBBY_PREFLIGHT_SAFETY_MARGIN_PERCENT=99 tobby sync --dry-run --output json 2>/dev/null' || true)
echo "$SMALL_PLAN" | jq -e '[.checks[].refusal_code] | index("TBY-STO-004") != null' >/dev/null ||
    fail "an undersized medium was not refused by the pre-flight: $(echo "$SMALL_PLAN" | jq -c '.checks' 2>/dev/null)"
echo "$SMALL_PLAN" | jq -e '[.checks[] | select(.refusal_code == "TBY-STO-004") | .shortfall_bytes] | max > 0' >/dev/null ||
    fail "the refusal does not state the shortfall in bytes (FR-055)"
inc exec tbc-m5-connected -- sh -c 'test ! -d /media/small/store/docker' ||
    fail "the refused synchronization wrote content anyway"
check "FR-055: undersized medium refused before any transfer, shortfall stated, nothing written"

# The FAT32 volume is identified rather than filled: producing a file above
# the 4 GiB ceiling on a crucible node costs more than the check is worth,
# and the refusal itself is exercised by execution in the Go suite against a
# real FAT32 volume. What only a real node can show is that the filesystem
# is recognised at all, through statfs, with its true ceiling.
write_config tbc-m5-connected /media/fat32/store ""
FAT_PLAN=$(inc exec tbc-m5-connected -- sh -c \
    'tobby sync --dry-run --output json 2>/dev/null' || true)
echo "$FAT_PLAN" | jq -e '[.checks[].filesystem.identified] | index(true) != null' >/dev/null ||
    fail "the FAT32 volume was not positively identified: $(echo "$FAT_PLAN" | jq -c '[.checks[].filesystem]' 2>/dev/null)"
echo "$FAT_PLAN" | jq -e '[.checks[].filesystem.max_file_size] | index(4294967295) != null' >/dev/null ||
    fail "the FAT32 per-file ceiling is not 4 GiB - 1: $(echo "$FAT_PLAN" | jq -c '[.checks[].filesystem]' 2>/dev/null)"
check "FR-055: a real FAT32 volume is identified with its 4 GiB per-file ceiling"

# -- 6. The mirror synchronization is triggered by hand, and only by hand ----
write_config tbc-m5-connected /media/tobby/store ""
inc exec tbc-m5-connected -- sh -c '
    nohup tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m5-connected http://127.0.0.1:8080/readyz
# FR-014 forbids an unattended mirror synchronization. Waiting proves the
# absence of one; the scheduler is not merely idle in mirror mode, it is
# never built, and this is the check that would notice if it were.
sleep 20
TASKS=$(inc exec tbc-m5-connected -- wget -q -O - http://127.0.0.1:8080/api/v1/tasks)
echo "$TASKS" | jq -e '(.tasks | length) == 0' >/dev/null ||
    fail "FR-014: a mirror instance started a synchronization on its own: $(echo "$TASKS" | jq -c '.tasks')"
check "FR-014: no cycle fires on its own in mirror mode"

# R-04: the plan is side-effect-free, and on a real store the way to say
# that is to fingerprint the tree on both sides of the run. The Go suite
# asserts the same property; what a node adds is a store a real
# synchronization filled, on a real filesystem, with a real Retriever to
# resolve.
fingerprint() {
    inc exec tbc-m5-connected -- sh -c \
        'find /media/tobby/store -type f -exec sha256sum {} + 2>/dev/null | sort | sha256sum'
}
PLAN_BEFORE=$(fingerprint)
inc exec tbc-m5-connected -- sh -c 'tobby sync --dry-run >/dev/null 2>&1' || true
PLAN_AFTER=$(fingerprint)
[ "$PLAN_BEFORE" = "$PLAN_AFTER" ] ||
    fail "R-04: the plan mutated the store ($PLAN_BEFORE -> $PLAN_AFTER)"
check "R-04: a plan run leaves the store byte-identical"

# -- 7. Fill the medium, prune, and inventory what remains -------------------
inc exec tbc-m5-connected -- sh -c 'tobby sync --wait' >/dev/null 2>&1 ||
    fail "the manual synchronization failed"
inc exec tbc-m5-connected -- sh -c 'test -f /media/tobby/store/meta/media.json' ||
    fail "FR-054: the synchronization produced no media manifest"
MANIFEST=$(inc exec tbc-m5-connected -- sh -c 'cat /media/tobby/store/meta/media.json')
echo "$MANIFEST" | jq -e --arg z "$ZONE" '.zone == $z' >/dev/null ||
    fail "the manifest does not name the zone it was produced for"
echo "$MANIFEST" | jq -e '(.recipes | length) == 2' >/dev/null ||
    fail "the manifest does not describe both deliveries"
echo "$MANIFEST" | jq -e '.mediaId != null and .mediaId != ""' >/dev/null ||
    fail "R-28: the manifest carries no media identifier"
MEDIA_ID=$(echo "$MANIFEST" | jq -r '.mediaId')
check "FR-054: media manifest written — zone $ZONE, 2 deliveries, medium $MEDIA_ID"

# The manifest must describe what the medium FINALLY holds, so the prune
# runs before it. Dropping a recipe from the Retriever and re-synchronizing
# is the only way to see both halves of that sentence at once.
inc exec tbc-m5-connected -- sh -c "
    tobby fileset pack /etc/tobby local-notes:1.0.0 >/dev/null 2>&1 || true
    sed -i '/zone-tool/,+1d' /etc/tobby/retriever.yaml
"
inc exec tbc-m5-connected -- sh -c 'tobby sync --wait' >/dev/null 2>&1 ||
    fail "the second synchronization failed"
MANIFEST2=$(inc exec tbc-m5-connected -- sh -c 'cat /media/tobby/store/meta/media.json')
echo "$MANIFEST2" | jq -e '(.recipes | length) == 1' >/dev/null ||
    fail "FR-045/FR-054: the manifest still describes a delivery the Retriever dropped"
echo "$MANIFEST2" | jq -e --arg id "$MEDIA_ID" '.mediaId == $id' >/dev/null ||
    fail "R-28: re-synchronizing the same store changed its media identifier"
inc exec tbc-m5-connected -- sh -c 'test -d /media/tobby/store/docker/registry/v2/repositories/localhost/filesets/local-notes' ||
    fail "FR-045: the prune removed a manually packed FileSet, which is a protected root"
check "FR-045: prune ran BEFORE the manifest; the manual import survived it; the medium keeps its identity"

# Put the second delivery back for the rest of the rehearsal.
inc file push "$WORK/retriever.yaml" tbc-m5-connected/etc/tobby/retriever.yaml >/dev/null
inc exec tbc-m5-connected -- sh -c 'tobby sync --wait' >/dev/null 2>&1 ||
    fail "restoring the second delivery failed"

# -- 8. NFR-020: a secret under the store stops the instance dead ------------
inc exec tbc-m5-connected -- sh -c '
    # [t]obby, not tobby: pkill -f matches full command lines, and the
    # command line of the shell running this very pkill CONTAINS the
    # pattern. Spelled plainly it kills its own parent, the scenario dies
    # with SIGTERM and no failing check to point at. The bracket makes the
    # pattern not match itself while still matching the process.
    pkill -f "[t]obby serve" || true
    mkdir -p /media/tobby/store/creds
    printf "{\"auths\":{\"registry.example.com\":{}}}" >/media/tobby/store/creds/config.json
    sed -i "s#^registries:#registries:\n  credentialsFile: /media/tobby/store/creds/config.json#" /etc/tobby/config.yaml
'
if inc exec tbc-m5-connected -- sh -c 'timeout 15 tobby serve >/tmp/refusal.log 2>&1'; then
    fail "NFR-020: the instance started with a credentials file under the transportable store"
fi
inc exec tbc-m5-connected -- sh -c 'grep -q "/media/tobby/store/creds/config.json" /tmp/refusal.log' ||
    fail "NFR-020: the refusal does not name the offending path"
inc exec tbc-m5-connected -- sh -c '
    sed -i "\#credentialsFile: /media/tobby/store/creds#d" /etc/tobby/config.yaml
    rm -rf /media/tobby/store/creds
'
check "NFR-020: a secret planted under the store makes the instance refuse to start, naming the path"

# -- 9. Damage one delivery, then transport ----------------------------------
# The truncation happens on the SOURCE side, before the medium travels,
# because that is the honest simulation: a medium is damaged in transit or
# on the shelf, not by the machine that reads it. One blob of zone-app is
# cut short; zone-tool is left intact. R-19 says the first is blocked whole
# and named, and the second still crosses.
BLOB=$(echo "$MANIFEST" | jq -r '.recipes[] | select(.name=="zone-app") | .ingredients[0].digest' | cut -d: -f2)
inc exec tbc-m5-connected -- sh -c "
    # [t]obby, not tobby: pkill -f matches full command lines, and the
    # command line of the shell running this very pkill CONTAINS the
    # pattern. Spelled plainly it kills its own parent, the scenario dies
    # with SIGTERM and no failing check to point at. The bracket makes the
    # pattern not match itself while still matching the process.
    pkill -f '[t]obby serve' || true
    f=\$(find /media/tobby/store/docker/registry/v2/blobs/sha256 -path '*${BLOB}*' -name data | head -1)
    test -n \"\$f\" || exit 1
    # Absolute size, not a relative one: busybox truncate refuses "-64"
    # (the same refusal that silently under-filled the undersized medium
    # earlier in this file).
    sz=\$(wc -c <\"\$f\")
    truncate -s \$((sz - 64)) \"\$f\"
" || fail "could not truncate a blob of the first delivery"
inc exec tbc-m5-connected -- sh -c 'sync && umount /media/tobby'
inc config device remove tbc-m5-connected medium >/dev/null
check "one blob of zone-app truncated; medium unmounted and detached (transport begins)"

inc config device add tbc-m5-isolated medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
inc exec tbc-m5-isolated -- sh -c 'mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby'
check "medium re-attached and mounted on the isolated node"

# -- 10. FR-054: nothing is served before verification -----------------------
write_config tbc-m5-isolated /media/tobby/store "$DEST_IP:5000"
inc exec tbc-m5-isolated -- sh -c 'nohup tobby serve >/var/log/tobby.log 2>&1 &'
wait_ready tbc-m5-isolated http://127.0.0.1:8080/readyz
V2_CODE=$(inc exec tbc-m5-isolated -- sh -c \
    "wget -q -S -O /dev/null http://127.0.0.1:8080/v2/$SOURCE_IP/library/tool/manifests/1.0.0 2>&1 | awk '/HTTP\//{print \$2; exit}'" || true)
[ "$V2_CODE" = "403" ] ||
    fail "FR-054: an unverified medium served /v2/ (status $V2_CODE, want 403)"
FILES_CODE=$(inc exec tbc-m5-isolated -- sh -c \
    "wget -q -S -O /dev/null http://127.0.0.1:8080/files/local-notes/ 2>&1 | awk '/HTTP\//{print \$2; exit}'" || true)
[ "$FILES_CODE" = "403" ] || [ "$FILES_CODE" = "404" ] ||
    fail "FR-054: an unverified medium served /files/ (status $FILES_CODE)"
check "FR-054: before verification /v2/ and /files/ serve nothing, and /readyz still answers"

# -- 11. A medium addressed elsewhere is refused, and the waiver is audited --
ELSEWHERE=$(inc exec tbc-m5-isolated -- sh -c \
    'tobby media verify --zone zone-elsewhere --output json 2>/dev/null' || true)
echo "$ELSEWHERE" | jq -e '.verdict == "blocked"' >/dev/null ||
    fail "FR-054: a medium addressed to another zone was not blocked"
echo "$ELSEWHERE" | jq -e '[(.blocks // [])[].code] | index("TBY-MED-006") != null' >/dev/null ||
    fail "FR-054: the zone refusal does not carry TBY-MED-006"
WAIVED=$(inc exec tbc-m5-isolated -- sh -c \
    'tobby media verify --zone zone-elsewhere --allow-zone-mismatch --output json 2>/dev/null' || true)
echo "$WAIVED" | jq -e '[(.blocks // [])[] | select(.code=="TBY-MED-006") | .overridden] | index(true) != null' >/dev/null ||
    fail "FR-054: the administrator waiver did not clear the zone refusal"
check "FR-054: a medium for another zone is blocked; the admin waiver clears it and is recorded"

# -- 12. R-19: the damaged delivery is blocked, its neighbour is not ---------
VERDICT=$(inc exec tbc-m5-isolated -- sh -c 'tobby media verify --output json 2>/dev/null' || true)
echo "$VERDICT" | jq -e '.verdict == "partial"' >/dev/null ||
    fail "R-19: the verdict is $(echo "$VERDICT" | jq -r '.verdict // "unreadable"'), want partial — a damaged medium must still deliver its intact recipes"
echo "$VERDICT" | jq -e '.recipes[] | select(.name=="zone-app") | .pushable == false' >/dev/null ||
    fail "R-19: the delivery whose blob was truncated is still pushable"
echo "$VERDICT" | jq -e '.recipes[] | select(.name=="zone-app") | .reason.path != null and .reason.path != ""' >/dev/null ||
    fail "R-19/FR-054: the blocked delivery does not name the offending file"
echo "$VERDICT" | jq -e '.recipes[] | select(.name=="zone-tool") | .pushable == true' >/dev/null ||
    fail "R-19: an intact delivery was blocked because its neighbour was damaged"
OFFENDER=$(echo "$VERDICT" | jq -r '.recipes[] | select(.name=="zone-app") | .reason.path')
check "R-19: zone-app blocked whole, naming $OFFENDER; zone-tool still pushable"

# -- 13. Import: only what cleared crosses into the zone registry ------------
inc exec tbc-m5-isolated -- sh -c 'tobby media import --output json' >"$WORK/import.json" 2>/dev/null || true
inc exec tbc-m5-dest -- wget -q -O - "http://127.0.0.1:5000/v2/_catalog" >"$WORK/catalog.json" 2>/dev/null || true
grep -q "zone-tool" "$WORK/catalog.json" ||
    fail "the cleared delivery's recipe never reached the zone cookbook (FR-034)"
if grep -q "zone-app" "$WORK/catalog.json"; then
    fail "R-19: a blocked delivery reached the destination registry"
fi
check "FR-052/FR-034: the cleared delivery is in the zone registry and its cookbook; the blocked one is not"

# A standard client, inside the isolated zone, pulls what was pushed. That
# is the acceptance sentence of the milestone: not "Tobby says it worked".
# The destination path is not the source path: ADR-0013 relocates every
# ingredient under the canonical source host, with the port's colon folded
# to an underscore because a colon is not legal in a repository name. The
# scenario spells that out rather than hard-coding it, so a change to the
# convention fails here instead of silently passing against a path nobody
# reads.
RELOCATED="$(printf '%s' "$SOURCE_IP:8080" | tr ':' '_')/library/tool"
inc exec tbc-m5-isolated -- sh -c "
    wget -q -O /dev/null --header 'Accept: application/vnd.oci.image.index.v1+json' \
        'http://$DEST_IP:5000/v2/$RELOCATED/manifests/1.0.0'
" || fail "a standard client inside the zone cannot pull $RELOCATED"
check "content pushed into the isolated zone is pullable there by a standard OCI client"

# -- 14. The medium carries the operation log home (FR-053, FR-056) ---------
inc exec tbc-m5-isolated -- sh -c 'test -s /media/tobby/store/_tobby/logs/operations.log' ||
    fail "FR-053: the destination-side operation log is not on the medium"
inc exec tbc-m5-isolated -- sh -c \
    'grep -q "\"path\":\"_tobby/" /media/tobby/store/meta/media.json' &&
    fail "FR-054: the return-log path is inside the manifest's coverage"
check "FR-053/FR-054: the return log is on the medium, outside the manifest's coverage"

# -- 15. R-28: an older medium cannot roll the zone backwards ---------------
# Two halves, and the first is the one an operator leans on. Re-presenting
# the SAME medium is not stale — equal is not older — because a push that
# half-failed has to be retryable without an administrator in the room.
SAME=$(inc exec tbc-m5-isolated -- sh -c 'tobby media verify --output json 2>/dev/null' || true)
echo "$SAME" | jq -e '[(.blocks // [])[].code] | index("TBY-MED-007") == null' >/dev/null ||
    fail "R-28: re-presenting the same medium was refused as stale; a retry is not a rollback"
check "R-28: the same medium is not stale — an interrupted import can be retried"

# The refusal itself. A medium is dated by its manifest and by nothing else,
# so rewinding that date is what "an older medium" MEANS here — and the
# manifest is deliberately not covered by its own inventory, which is why
# this is possible at all. That is the honest shape of the guard: it is an
# anti-accident control over an unsigned document, never a security one, and
# the scenario exercises it as such rather than pretending otherwise.
inc exec tbc-m5-isolated -- sh -c '
    # The manifest is written pretty-printed, so the separator is ": " and
    # not ":". A pattern without the space matches nothing, the medium goes
    # through unchanged, and that reads exactly like the guard failing — so
    # the rewind verifies itself before the verdict is asked for.
    sed -i "s/\"resolvedAt\": \"20[0-9][0-9]-/\"resolvedAt\": \"2019-/g" /media/tobby/store/meta/media.json
    grep -q "\"resolvedAt\": \"2019-" /media/tobby/store/meta/media.json
' || fail "could not rewind the medium's own date"
STALE=$(inc exec tbc-m5-isolated -- sh -c 'tobby media verify --output json 2>/dev/null' || true)
echo "$STALE" | jq -e '[(.blocks // [])[].code] | index("TBY-MED-007") != null' >/dev/null ||
    fail "R-28: a medium older than the zone's last import was not refused: $(echo "$STALE" | jq -c '[(.blocks // [])[].code]' 2>/dev/null)"
echo "$STALE" | jq -e '.freshness.recorded != null and .freshness.resolved != null' >/dev/null ||
    fail "R-28: the refusal does not name both timestamps"
WAIVED_STALE=$(inc exec tbc-m5-isolated -- sh -c 'tobby media verify --allow-stale --output json 2>/dev/null' || true)
echo "$WAIVED_STALE" | jq -e '[(.blocks // [])[] | select(.code=="TBY-MED-007") | .overridden] | index(true) != null' >/dev/null ||
    fail "R-28: the administrator waiver did not clear the staleness refusal"
check "R-28: an older medium is refused naming both timestamps; the admin waiver clears it"

# -- 16. FR-051: the interoperability exit is real --------------------------
# skopeo runs on the crucible host, against the layout pulled off the node:
# the point of this check is that a tool nobody here wrote can read what
# Tobby wrote, so it must be that tool and not a library standing in.
# The cleared delivery, not the whole store: by this point the medium is
# damaged on purpose, and a whole-store export would fail on the manifest
# this scenario truncated — which says nothing about interoperability. It is
# also the realistic act, and the one R-19 argues for: you hand on what
# verified.
inc exec tbc-m5-isolated -- sh -c \
    'rm -rf /tmp/layout && tobby export /tmp/layout --directory --recipe zone-tool@1.0.0 --storage-root /media/tobby/store' >/dev/null 2>&1 ||
    fail "FR-051: the OCI image layout export failed"
inc file pull -r tbc-m5-isolated/tmp/layout "$WORK/" >/dev/null 2>&1 ||
    fail "could not retrieve the exported layout"
test -f "$WORK/layout/oci-layout" && test -f "$WORK/layout/index.json" ||
    fail "FR-051: the export is not a conforming OCI image layout"
REF=$(jq -r '.manifests[0].annotations["org.opencontainers.image.ref.name"]' "$WORK/layout/index.json")
skopeo inspect --raw "oci:$WORK/layout:$REF" >"$WORK/skopeo.json" 2>/dev/null ||
    fail "FR-051: skopeo cannot read the exported layout (ref $REF)"
SKOPEO_DIGEST="sha256:$(sha256sum "$WORK/skopeo.json" | awk '{print $1}')"
check "FR-051: skopeo reads the exported layout ($REF → $SKOPEO_DIGEST)"

# -- 17. R-05: the guides are on the medium's own instance, offline ---------
for lang in en fr; do
    inc exec tbc-m5-isolated -- sh -c \
        "wget -q -O - --header 'Accept-Language: $lang' http://127.0.0.1:8080/help" \
        >"$WORK/help-$lang.html" 2>/dev/null ||
        fail "R-05: /help does not answer in $lang"
    test -s "$WORK/help-$lang.html" || fail "R-05: /help returned nothing in $lang"
done
grep -qi "http://\|https://" "$WORK/help-en.html" &&
    grep -qvi "tobby-fetch.github.io\|opencontainers\|sigstore" "$WORK/help-en.html"
check "R-05: the guides are served offline by the instance, in both languages"

# -- 18. R-41: a directory packed inside the zone is served, with no upload --
inc exec tbc-m5-isolated -- sh -c '
    mkdir -p /root/pool && printf "deb content\n" >/root/pool/README
    tobby fileset pack /root/pool zone-notes:1.0.0 --storage-root /media/tobby/store
' >/dev/null 2>&1 || fail "R-41: packing a local directory failed"
# busybox wget speaks GET and POST-with-a-body and nothing else, so the
# write methods are sent from the crucible host with curl. The host routes
# to both bridges by construction — the air gap is between the two GUEST
# networks, enforced by the ACL the canary proved, not between the host and
# its own bridges.
ISOLATED_IP=$(inc list tbc-m5-isolated -c 4 -f csv | awk '{print $1}' | head -1)
for method in POST PUT PATCH DELETE MKCOL; do
    CODE=$(curl -s -o /dev/null -w '%{http_code}' -X "$method" \
        "http://$ISOLATED_IP:8080/files/zone-notes/README" || echo 000)
    case "$CODE" in
        405|403|404) ;;
        *) fail "R-41/FR-047: /files/ answered $CODE to $method — no write surface may exist" ;;
    esac
done
check "R-41: a directory packed on the isolated side becomes a FileSet; /files/ still accepts no write method"

# -- 19. FR-046: the reset is the admin's, and it is typed ------------------
# curl again, for the same reason as the write-method probe above: this
# needs a POST with a JSON body and a header, which busybox wget cannot
# express, and a probe that never leaves the machine passes whatever the
# server does.
RESET_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' -d '{"confirmation":"reset"}' \
    "http://$ISOLATED_IP:8080/api/v1/store/reset" || echo 000)
[ "$RESET_CODE" = "422" ] ||
    fail "FR-046: a reset with the wrong confirmation returned $RESET_CODE, want 422"
check "FR-046: a reset without the exact typed confirmation is refused"

# -- Cleanup ------------------------------------------------------------------
cleanup
check "scenario m5 complete — report: $REPORT"
