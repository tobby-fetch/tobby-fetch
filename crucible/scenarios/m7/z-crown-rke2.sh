#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crown crucible scenario (ADR-0014, DEVELOPMENT-PLAN §2.7): an
# RKE2 cluster is bootstrapped fully air-gapped from a bare OS with
# everything served by Tobby — OS packages from served FileSets, RKE2
# artifacts and images from the transported store, the cluster pulling
# through the zone registry via the GENERATED registries.yaml mirror
# configuration. It exercises every milestone at once: recipes, signatures,
# the mirror flow over a real block device, destination-side verification,
# relocation, FileSet serving, and the consumption aids of feature 7.1.
#
#   1. Upstream (connected zone): the RKE2 v1.36.5+rke2r1 release artifacts,
#      checked against the release checksums and a pinned installer; the
#      image list of the release compared, entry for entry, with the draft
#      recipe. A twin of the bare host — same Incus image, connected —
#      tells which rpm transaction RKE2's host prerequisites need (SELinux
#      policy, container-selinux, rke2-selinux, iptables), and that
#      transaction becomes an rpm repository packed as a FileSet.
#   2. Three recipes cooked (digests resolved upstream, FileSets packed),
#      published through `tobby recipe push` and signed by the crucible's
#      test key: rke2 (16 images from registry.rancher.com + the artifacts
#      FileSet), rocky9-rke2-host (the rpm repository), crown-app (the
#      nominal-path test image, docker.io/library/busybox).
#   3. The mirror instance writes them onto a real loop-backed block
#      device, which is detached and re-attached in the isolated zone.
#   4. The destination-side instance verifies the medium (FR-054), which
#      opens /files/, and imports it into the zone registry (a Tobby in
#      passthrough mode). Every ingredient is checked at its relocated path
#      with its pinned digest.
#   5. Feature 7.1 generates the registries.yaml snippet and the mapping
#      table. Both are checked for shape; the table against what was
#      actually pushed, digest for digest.
#   6. A bare Rocky Linux 9 VM that has never had a route out installs its
#      packages with dnf from the served FileSet only, turns SELinux to
#      enforcing, fetches RKE2 from /files/, installs it by the tarball
#      method WITHOUT any image tarball, and starts with the generated
#      registries.yaml as its only image source (system-default-registry:
#      registry.rancher.com names the system images, rke2-runtime included;
#      registries.yaml decides where they are fetched).
#   7. Node Ready; every kube-system pod Running or Completed; a pod
#      referencing docker.io/library/busybox (and its short form) runs; a
#      pod referencing a nominal tag never pushed fails, at the zone.
#   8. Proof, from witnesses that are not the product: a capture on the
#      node's own interface shows every manifest the cluster runs being
#      requested from the zone registry (plain HTTP, readable), the
#      containerd image store holds each image under its pinned digest, and
#      leak captures on every way out of the zone (connected bridge, host
#      uplink) saw ZERO packet sourced in the zone. Attempts to reach out
#      — refused by the gap — are listed in the report.
#
# Topology:
#   tbc-m7-source    (container, tbc-net)     fixture registry: cookbook + FileSets
#   tbc-m7-connected (container, tbc-net)     mirror instance, store ON the medium
#   tbc-m7-builder   (VM, tbc-net)            twin of the bare host: rpm transaction
#   tbc-m7-zone      (container, tbc-airgap)  zone registry (Tobby passthrough :5000)
#   tbc-m7-gate      (container, tbc-airgap)  destination instance on the medium, /files/
#   tbc-m7-node      (VM, tbc-airgap)         the bare OS that becomes the RKE2 node
#   medium           (loop-backed block dev)  attached, filled, detached, moved
#
# Requires, on top of the crucible baseline: KVM (/dev/kvm — two VMs),
# tcpdump on the host (the witnesses), jq, curl, cosign (installed on demand
# like m3-m6). No registry credentials: the RKE2 images come from
# registry.rancher.com (no pull rate limit), and the one Docker Hub image
# (busybox) costs two manifest GETs, well under the anonymous limit.
# CRUCIBLE_DOCKER_CONFIG (a dockerconfigjson with a "docker.io" entry) is
# used when present, for Docker Hub only.
#
# Knobs (all optional):
#   CRUCIBLE_RKE2_ARTIFACTS  directory already holding rke2.linux-amd64.tar.gz,
#                            sha256sum-amd64.txt, rke2-images.linux-amd64.txt
#                            and install.sh (otherwise fetched once, checked)
#   CRUCIBLE_M7_NODE_IMAGE   bare-OS image (default images:rockylinux/9/cloud)
#   CRUCIBLE_M7_SELINUX      enforcing (default) | off — "off" is for mise au
#                            point only and is reported as such
#   CRUCIBLE_M7_DEBUG_AIDS   handmade: registries.yaml and the table written by
#                            the scenario instead of generated by
#                            `tobby consumption` (mise au point only). Every
#                            dependent line says DEBUG and the run ends with a
#                            FAIL: it can never accept the milestone.
#   CRUCIBLE_M7_KEEP=1       leave every instance up at the end (debugging)
#
# Run alone:   sh crucible/scenarios/m7/z-crown-rke2.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-z-crown-rke2.txt}"
# shellcheck source=../../lib.sh
. "$DIR/../../lib.sh"
# shellcheck source=crown/crown-lib.sh
. "$DIR/crown/crown-lib.sh"

DATA="$DIR/crown"
# shellcheck disable=SC2034 # read by crown_container (crown-lib.sh)
CTR_IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.24}"
NODE_IMAGE="${CRUCIBLE_M7_NODE_IMAGE:-images:rockylinux/9/cloud}"
SELINUX_MODE="${CRUCIBLE_M7_SELINUX:-enforcing}"
MEDIUM_IMG="${CRUCIBLE_MEDIUM_IMG:-/tmp/tbc-m7-medium.img}"
MEDIUM_SIZE=6G
ZONE="zone-crown"
# The relocation base of the instance that generates the consumption aids
# (destination.basePath on the gate): none. crown-lib.sh builds its
# expectations with it.
# shellcheck disable=SC2034 # read by crown_check_* (crown-lib.sh)
CROWN_BASE=""
ZONE_NET="10.180.20.0/24"
WORK="$(mktemp -d)"
COSIGN="${COSIGN:-cosign}"
SOURCE=tbc-m7-source CONNECTED=tbc-m7-connected BUILDER=tbc-m7-builder
ZONEREG=tbc-m7-zone GATE=tbc-m7-gate NODE=tbc-m7-node
INSTANCES="$SOURCE $CONNECTED $BUILDER $ZONEREG $GATE $NODE"

# The arbitrated version (DEVELOPMENT-PLAN §2.7) and what pins it. The
# installer is not covered by the release checksums, so its digest is
# pinned here; a different one is a different installer.
RKE2_VERSION="v1.36.5+rke2r1"
RKE2_TAG="v1.36.5-rke2r1"
RKE2_INSTALL_SH_SHA256="42983c86d1da64a92061d83afb57630cedd69241989f1b0673f3db6c3d92ee6b"
RKE2_RELEASE="https://github.com/rancher/rke2/releases/download/v1.36.5%2Brke2r1"
RKE2_INSTALLER="https://raw.githubusercontent.com/rancher/rke2/v1.36.5%2Brke2r1/install.sh"
RANCHER_RPM="https://rpm.rancher.io/rke2/stable/common/centos/9/noarch"
# What the bare host is asked to install. dnf decides the rest.
PKGS="tar selinux-policy-targeted container-selinux rke2-selinux iptables-nft"
APP_IMAGE="docker.io/library/busybox:1.37.0"
ABSENT_TAG="0.0.0-crown-absent"

T0=$(date +%s)
elapsed() { echo "$(( ($(date +%s) - $1) / 60 ))m$(( ($(date +%s) - $1) % 60 ))s"; }
# note <text>: an evidence line in the raw report, kept apart from checks.
note() { printf '#    %s\n' "$*" | tee -a "$REPORT"; }
DBG=""
[ "${CRUCIBLE_M7_DEBUG_AIDS:-}" = handmade ] && DBG="DEBUG(handmade aids) "

report_start

release_leftovers() {
    for inst in $INSTANCES; do inc delete --force "$inst" 2>/dev/null || true; done
    if [ -f "$MEDIUM_IMG" ]; then
        losetup -j "$MEDIUM_IMG" 2>/dev/null | cut -d: -f1 |
            while read -r dev; do losetup -d "$dev" 2>/dev/null || true; done
        rm -f "$MEDIUM_IMG"
    fi
}
# Captures are stopped on every exit, success or not: a tcpdump left
# behind outlives the scenario on a node billed by the hour.
trap 'crown_capture_stop_all' EXIT

# -- 0. Tooling, taken from the environment (NFR-019) -------------------------
[ "$(uname -m)" = x86_64 ] || fail "the crown scenario targets linux/amd64 (RKE2 artifacts and recipes are amd64)"
[ -e /dev/kvm ] || fail "no /dev/kvm: the crown runs two VMs (RKE2 needs its own kernel)"
for t in jq curl tcpdump sha256sum losetup; do
    command -v "$t" >/dev/null 2>&1 || fail "$t not found on the crucible host"
done
if ! command -v "$COSIGN" >/dev/null 2>&1; then
    GOBIN=/usr/local/bin go install github.com/sigstore/cosign/v2/cmd/cosign@latest >/dev/null 2>&1 ||
        fail "installing cosign"
    COSIGN=/usr/local/bin/cosign
fi
CROWN_HUB_BASIC=""
if [ -n "${CRUCIBLE_DOCKER_CONFIG:-}" ]; then
    [ -f "$CRUCIBLE_DOCKER_CONFIG" ] || fail "CRUCIBLE_DOCKER_CONFIG=$CRUCIBLE_DOCKER_CONFIG does not exist"
    CROWN_HUB_BASIC=$(jq -r '.auths["docker.io"].auth // .auths["https://index.docker.io/v1/"].auth // empty' "$CRUCIBLE_DOCKER_CONFIG")
    [ -n "$CROWN_HUB_BASIC" ] || fail "CRUCIBLE_DOCKER_CONFIG has no docker.io auth entry"
    check "tooling available (jq, curl, tcpdump, cosign, KVM); Docker Hub credentials supplied"
else
    check "tooling available (jq, curl, tcpdump, cosign, KVM); registries reached anonymously"
fi
[ "$SELINUX_MODE" = enforcing ] || check "WARNING SELinux mode '$SELINUX_MODE' requested: mise au point only, not the target posture"

# -- 1. The binaries under test ------------------------------------------------
( cd "$ROOT" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "$WORK/tobby" ./cmd/tobby ) ||
    fail "building tobby"
( cd "$ROOT" && go build -o "$WORK/yamljson" ./crucible/scenarios/m7/crown/yamljson ) ||
    fail "building the scenario's YAML helper"
( cd "$ROOT" && go build -o "$WORK/seed" ./test/topology/seed ) || fail "building the seed client"
check "built tobby for linux/amd64 (and the harness helpers)"

# -- 2. Upstream RKE2 release, checked before anything is built on it ---------
ART="$WORK/rke2-artifacts"
mkdir -p "$ART"
for f in rke2.linux-amd64.tar.gz sha256sum-amd64.txt rke2-images.linux-amd64.txt install.sh; do
    if [ -n "${CRUCIBLE_RKE2_ARTIFACTS:-}" ]; then
        cp "$CRUCIBLE_RKE2_ARTIFACTS/$f" "$ART/$f" || fail "CRUCIBLE_RKE2_ARTIFACTS lacks $f"
    elif [ "$f" = install.sh ]; then
        curl -fsSL -o "$ART/$f" "$RKE2_INSTALLER" || fail "fetching the RKE2 installer"
    else
        curl -fsSL -o "$ART/$f" "$RKE2_RELEASE/$f" || fail "fetching $f of RKE2 $RKE2_VERSION"
    fi
done
( cd "$ART" && grep -E '  (rke2\.linux-amd64\.tar\.gz|rke2-images\.linux-amd64\.txt)$' sha256sum-amd64.txt |
    sha256sum -c --status - ) || fail "the RKE2 tarball or image list does not match the release checksums"
[ "$(grep -cE '  (rke2\.linux-amd64\.tar\.gz|rke2-images\.linux-amd64\.txt)$' "$ART/sha256sum-amd64.txt")" = 2 ] ||
    fail "the release checksums do not cover both the tarball and the image list"
[ "$(sha256sum <"$ART/install.sh" | awk '{print $1}')" = "$RKE2_INSTALL_SH_SHA256" ] ||
    fail "the RKE2 installer is not the pinned one"
# The draft recipe's images are the release's images, entry for entry —
# sourced from registry.rancher.com instead of docker.io (same repositories,
# same tags): a list that drifted would cook a recipe the cluster cannot run
# on.
crown_yj json "$DATA/recipes/rke2.yaml" |
    jq -r '.spec.ingredients[] | select(.kind == "ContainerImage") | .ref + ":" + .version' | sort >"$WORK/draft-images.txt"
sed '/^$/d; s#^docker\.io/rancher/#registry.rancher.com/rancher/#' "$ART/rke2-images.linux-amd64.txt" | sort >"$WORK/release-images.txt"
diff "$WORK/draft-images.txt" "$WORK/release-images.txt" >"$WORK/images.diff" ||
    fail "the draft rke2 recipe and the $RKE2_VERSION image list differ: $(tr '\n' ' ' <"$WORK/images.diff")"
N_RKE2_IMAGES=$(wc -l <"$WORK/release-images.txt" | tr -d ' ')
check "RKE2 $RKE2_VERSION: tarball and image list match the release checksums, installer pinned, $N_RKE2_IMAGES images = draft recipe (registry.rancher.com)"

# -- 3. The gap's witnesses first, then the instances ---------------------------
release_leftovers
UPLINK=$(ip route show default | awk '{print $5; exit}')
[ -n "$UPLINK" ] || fail "staging: the host has no default route to watch"
# Every way out of the isolated zone: the connected bridge and the host
# uplink. The isolated bridge is not NATed, so a zone packet that leaked
# would still carry its zone source address there.
crown_capture leak-net tbc-net 128 "src net $ZONE_NET"
crown_capture leak-uplink "$UPLINK" 128 "src net $ZONE_NET"
check "leak witnesses watching tbc-net and $UPLINK for any packet sourced in $ZONE_NET"

crown_container "$SOURCE" tbc-connected-node
crown_container "$CONNECTED" tbc-connected-node
crown_container "$ZONEREG" tbc-airgap-node
crown_container "$GATE" tbc-airgap-node
truncate -s "$MEDIUM_SIZE" "$MEDIUM_IMG"
MEDIUM_DEV=$(losetup -f --show "$MEDIUM_IMG")
inc config device add "$CONNECTED" medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
# shellcheck disable=SC2016 # expanded by the node's shell
inc exec "$CONNECTED" -- sh -c '
    n=0
    until apk add --no-cache e2fsprogs >/dev/null 2>&1; do
        n=$((n + 1)); [ "$n" -ge 30 ] && { echo "no package mirror reachable" >&2; exit 1; }; sleep 2
    done
    base=$(awk "{print \$2; exit}" /proc/self/uid_map)
    mkfs.ext4 -q -E root_owner="$base:$base" /dev/sdb && mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby
' || fail "formatting and mounting the medium"
inc exec "$CONNECTED" -- mountpoint -q /media/tobby || fail "staging: the medium is not mounted"
SOURCE_IP=$(instance_ip "$SOURCE")
S="$SOURCE_IP:8080"
DEST_IP=$(instance_ip "$ZONEREG")
DEST="$DEST_IP:5000"
GATE_IP=$(instance_ip "$GATE")
if inc exec "$GATE" -- wget -q -T 5 -O /dev/null "http://$S/healthz" >/dev/null 2>&1; then
    fail "the isolated zone reached the connected zone: there is no air gap"
fi
check "containers launched, $MEDIUM_SIZE medium mounted on the connected node, egress canary failed as required"

# -- 4. The bare host's twin decides the rpm transaction ------------------------
# The same image as the crown node, but connected. dnf is asked for the
# transaction that installing $PKGS performs on THIS image, downloading
# only: what it fetches is exactly what the bare host lacks.
crown_vm "$BUILDER" tbc-connected-node 2 2GiB 10GiB
cat >"$WORK/builder.sh" <<EOF
set -eu
cat >/etc/yum.repos.d/rancher-rke2-common.repo <<'REPO'
[rancher-rke2-common-stable]
name=Rancher RKE2 Common (stable)
baseurl=$RANCHER_RPM
enabled=1
gpgcheck=1
gpgkey=https://rpm.rancher.io/public.key
REPO
mkdir -p /srv/crown-repo
n=0
until dnf -q -y install --downloadonly --destdir /srv/crown-repo $PKGS >/srv/dnf-download.log 2>&1; do
    n=\$((n + 1)); [ "\$n" -ge 20 ] && { cat /srv/dnf-download.log >&2; exit 1; }; sleep 5
done
rpm -q $PKGS >/srv/preinstalled.txt 2>&1 || true
dnf -q -y install createrepo_c >/dev/null
curl -fsSL -o /srv/crown-repo/RPM-GPG-KEY-rancher https://rpm.rancher.io/public.key
createrepo_c -q /srv/crown-repo
rpm -qp --qf '%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\n' /srv/crown-repo/*.rpm | sort >/srv/crown-repo.list
rpm -q rocky-release
EOF
inc file push "$WORK/builder.sh" "$BUILDER/root/builder.sh" >/dev/null
BUILDER_RELEASE=$(inc exec "$BUILDER" -- sh /root/builder.sh) ||
    fail "the twin could not compute the rpm transaction for: $PKGS"
inc file pull -r "$BUILDER/srv/crown-repo" "$WORK/" >/dev/null || fail "retrieving the rpm repository"
inc file pull "$BUILDER/srv/crown-repo.list" "$WORK/crown-repo.list" >/dev/null
inc file pull "$BUILDER/srv/preinstalled.txt" "$WORK/preinstalled.txt" >/dev/null
# Staging: every package asked for is either in the repository or already
# part of the image (in which case the bare host has it too).
for p in $PKGS; do
    if ! grep -q "^$p-[0-9]" "$WORK/crown-repo.list" && ! grep -q "^$p-[0-9]" "$WORK/preinstalled.txt"; then
        fail "staging: $p is neither in the rpm repository nor part of the image"
    fi
done
test -f "$WORK/crown-repo/repodata/repomd.xml" || fail "staging: the rpm repository has no metadata"
inc delete --force "$BUILDER" >/dev/null
N_RPMS=$(wc -l <"$WORK/crown-repo.list" | tr -d ' ')
check "rpm transaction of the bare host ($BUILDER_RELEASE): $N_RPMS packages for '$PKGS'"
while read -r rpm; do note "rpm $rpm"; done <"$WORK/crown-repo.list"

# -- 5. The fixture registry: cookbook and FileSets ------------------------------
inc exec "$SOURCE" -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready "$SOURCE" http://127.0.0.1:8080/healthz
# The FileSets are published under a nominal host of their own; the
# mirror instance reaches them through a substitution (FR-036), so their
# relocated path in the zone is artifacts.crucible.test/…, not an IP.
FS_ART=$("$WORK/seed" push-fileset "$S/artifacts.crucible.test/rke2/artifacts:$RKE2_TAG" "$ART") ||
    fail "packing the RKE2 artifacts FileSet"
FS_RPM=$("$WORK/seed" push-fileset "$S/artifacts.crucible.test/os/rocky9-rke2:1.36.5" "$WORK/crown-repo") ||
    fail "packing the rpm repository FileSet"
check "FileSets packed: rke2-artifacts $FS_ART, rocky9-rke2-repo $FS_RPM"

# -- 6. Cook, publish, sign -------------------------------------------------------
( cd "$WORK" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1 ) || fail "generating the test key pair"
printf 'registries:\n  insecure:\n    - %s\n' "$S" >"$WORK/publish.yaml"
mkdir -p "$WORK/cooked"
# Image digests resolved upstream by HEAD (no pull counted); FileSet
# digests are the ones the packing just printed.
{
    printf '{"rke2-artifacts": "%s"' "$FS_ART"
    while read -r img; do
        name=${img%:*}; name=${name##*/}
        d=$(crown_resolve_digest "$img") || fail "resolving $img upstream"
        [ -n "$d" ] || fail "resolving $img upstream: no digest"
        printf ', "%s": "%s"' "$name" "$d"
    done <"$WORK/release-images.txt"
    printf '}\n'
} >"$WORK/digests-rke2.json"
APP_DIGEST=$(crown_resolve_digest "$APP_IMAGE") && [ -n "$APP_DIGEST" ] || fail "resolving $APP_IMAGE upstream"
# Upstream signatures travel with their subject (RECIPE-SPEC §12.2). An
# ingredient's own signature is a consumer policy decision: by default the
# zone verifies it for the record and never blocks on it (FR-033 as amended
# for B-062) — the recipe signature pins every digest. As of
# v1.36.5+rke2r1, registry.rancher.com/rancher/mirrored-pause carries a
# legacy cosign ".sig" tag no crucible key verifies (found by the local
# proof of 7.1, where it blocked the whole rke2 recipe at import: B-062).
# Every such ingredient is listed here and held to the new default after
# the import: delivered, its signature carried, the verdict recorded as
# "unverifiable" by `media verify` and on the push item.
: >"$WORK/upstream-signed.tsv"
while read -r img; do
    ref=${img%:*}
    d=$(jq -r --arg n "${ref##*/}" '.[$n]' "$WORK/digests-rke2.json")
    if crown_resolve_digest "$ref:$(printf '%s' "$d" | tr ':' '-').sig" >/dev/null 2>&1; then
        printf '%s\t%s\t%s\n' "${ref##*/}" "$ref" "$d" >>"$WORK/upstream-signed.tsv"
        note "upstream cosign signature on $ref@$d — recorded, never blocking (B-062)"
    fi
done <"$WORK/release-images.txt"
printf '{"busybox": "%s"}\n' "$APP_DIGEST" >"$WORK/digests-crown-app.json"
printf '{"rocky9-rke2-repo": "%s"}\n' "$FS_RPM" >"$WORK/digests-rocky9-rke2-host.json"
for r in rke2:1.36.5 rocky9-rke2-host:1.36.5 crown-app:1.0.0; do
    name=${r%:*}; ver=${r#*:}
    crown_cook "$DATA/recipes/$name.yaml" "$WORK/digests-$name.json" "$WORK/cooked/$name.yaml"
    dgst=$(crown_publish "$WORK/cooked/$name.yaml" "$name" "$ver")
    note "recipe $name:$ver $dgst"
done
crown_yj json "$WORK/cooked/rke2.yaml" >"$WORK/cooked-rke2.json"
crown_yj json "$WORK/cooked/crown-app.yaml" >"$WORK/cooked-crown-app.json"
crown_yj json "$WORK/cooked/rocky9-rke2-host.yaml" >"$WORK/cooked-rocky9-rke2-host.json"
jq -s '.' "$WORK/cooked-rke2.json" "$WORK/cooked-crown-app.json" >"$WORK/cooked-all.json"
jq -s '.' "$WORK/cooked-rke2.json" "$WORK/cooked-crown-app.json" "$WORK/cooked-rocky9-rke2-host.json" >"$WORK/cooked-every.json"
N_ING=$(jq '[.[].spec.ingredients[]] | length' "$WORK/cooked-every.json")
check "three recipes cooked ($N_ING ingredients), published by tobby recipe push, signed and verified under the test key"

# -- 7. The mirror flow onto the medium -----------------------------------------
cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: $ZONE
spec:
  cookbook: $S/cookbook
  recipes:
    - name: rke2
      version: "1.36.5"
    - name: rocky9-rke2-host
      version: "1.36.5"
    - name: crown-app
      version: "1.0.0"
EOF
inc file push "$WORK/retriever.yaml" "$CONNECTED/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/cosign.pub" "$CONNECTED/etc/tobby/trust.pub" >/dev/null
CREDS=""
if [ -n "${CRUCIBLE_DOCKER_CONFIG:-}" ]; then
    # Outside the store: a credentials file under it stops the instance
    # (NFR-020, m5) — and must, since the store is what travels.
    inc file push "$CRUCIBLE_DOCKER_CONFIG" "$CONNECTED/etc/tobby/hub-config.json" >/dev/null
    CREDS="  credentialsFile: /etc/tobby/hub-config.json"
fi
inc exec "$CONNECTED" -- sh -c "cat >/etc/tobby/config.yaml <<'EOF'
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
  insecure: [$S]
  substitutions:
    artifacts.crucible.test: $S
$CREDS
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: crucible
      keyFile: /etc/tobby/trust.pub
EOF"
inc exec "$CONNECTED" -- sh -c 'nohup tobby serve >/var/log/tobby.log 2>&1 &'
wait_ready "$CONNECTED" http://127.0.0.1:8080/readyz
T_SYNC=$(date +%s)
inc exec "$CONNECTED" -- sh -c 'tobby sync --wait --output json' >"$WORK/sync.json" 2>"$WORK/sync.err" || true
if ! jq -e '.task.status == "done" and ([.task.items[] | select(.status == "failed")] | length == 0)' "$WORK/sync.json" >/dev/null 2>&1; then
    if jq -e '.task' "$WORK/sync.json" >/dev/null 2>&1; then
        fail "the mirror synchronization failed: $(jq -c '{status: .task.status, error: .task.error,
            failed: [.task.items[] | select(.status == "failed") | {name, code: .error.code, detail: .error.detail}]}' "$WORK/sync.json")"
    fi
    fail "the mirror synchronization produced no task document: $(tr '\n' ' ' <"$WORK/sync.err")"
fi
inc exec "$CONNECTED" -- cat /media/tobby/store/meta/media.json >"$WORK/media.json"
jq -e '[.recipes[].name] | sort == ["crown-app", "rke2", "rocky9-rke2-host"]' "$WORK/media.json" >/dev/null ||
    fail "the medium does not carry the three deliveries: $(jq -c '[.recipes[].name]' "$WORK/media.json")"
STORE_SIZE=$(inc exec "$CONNECTED" -- du -sh /media/tobby/store | awk '{print $1}')
inc exec "$CONNECTED" -- sh -c 'pkill -f "[t]obby serve" || true; sleep 2'
inc exec "$CONNECTED" -- sh -c 'sync && umount /media/tobby'
inc config device remove "$CONNECTED" medium >/dev/null
check "mirror flow: three deliveries on the medium ($STORE_SIZE, $(elapsed "$T_SYNC")); medium unmounted and detached"

# -- 8. The isolated zone: verify, serve, import ---------------------------------
inc exec "$ZONEREG" -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:5000 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready "$ZONEREG" http://127.0.0.1:5000/healthz
inc config device add "$GATE" medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
inc exec "$GATE" -- sh -c 'mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby && test -f /media/tobby/store/meta/media.json' ||
    fail "staging: the medium did not mount on the destination-side node"
inc file push "$WORK/cosign.pub" "$GATE/etc/tobby/trust.pub" --create-dirs >/dev/null
# anonymous: the bare host has no credentials of any kind — the FR-047
# bare-host opt-in, reported like the FR-075 override.
inc exec "$GATE" -- sh -c "cat >/etc/tobby/config.yaml <<'EOF'
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
  insecure: [$DEST]
trust:
  roots:
    - name: crucible
      keyFile: /etc/tobby/trust.pub
destination:
  registry: $DEST
  cookbook: cookbook
files:
  filesets:
    - name: rke2-artifacts
      ref: artifacts.crucible.test/rke2/artifacts
      version: $RKE2_TAG
      anonymous: true
    - name: rocky9-rke2
      ref: artifacts.crucible.test/os/rocky9-rke2
      version: 1.36.5
      anonymous: true
EOF"
# This configuration is also what `tobby consumption` reads (step 9): the
# zone registry is in registries.insecure, so the generated endpoint is
# http://$DEST; there is no destination.basePath, so the rewrites land at
# <host>/…; and storage.basePrefix is unset here exactly as on the
# connected instance that wrote the medium — the store refuses to describe
# recorded paths under another prefix (TBY-CFG-001).
inc exec "$GATE" -- sh -c 'nohup tobby serve >/var/log/tobby.log 2>&1 &'
wait_ready "$GATE" http://127.0.0.1:8080/readyz
G="$GATE_IP:8080"
# FR-054: nothing served before the verification.
PRE=$(curl -s -o /dev/null -w '%{http_code}' "http://$G/files/rke2-artifacts/sha256sum-amd64.txt" || echo 000)
case "$PRE" in 403 | 404) ;; *) fail "FR-054: /files/ answered $PRE before the medium was verified" ;; esac
crown_api "$G" POST /api/v1/media/verify >"$WORK/verify.json" || fail "the verification request failed"
jq -e '.verdict == "pushable"' "$WORK/verify.json" >/dev/null ||
    fail "the medium is not pushable whole: $(jq -c '{verdict, blocks, recipes: [.recipes[]? | {name, pushable, reason}]}' "$WORK/verify.json")"
SERVED=$(curl -fsS "http://$G/files/rke2-artifacts/sha256sum-amd64.txt" | sha256sum | awk '{print $1}') || true
[ "$SERVED" = "$(sha256sum <"$ART/sha256sum-amd64.txt" | awk '{print $1}')" ] ||
    fail "FR-047: /files/rke2-artifacts does not serve the checksums that were packed"
curl -fsS -o /dev/null "http://$G/files/rocky9-rke2/repodata/repomd.xml" ||
    fail "FR-047: /files/rocky9-rke2 does not serve the repository metadata"
check "FR-054/FR-047: the medium verified pushable whole; /files/ closed before, serving the packed bytes after"

T_IMPORT=$(date +%s)
crown_api "$G" POST /api/v1/media/import >"$WORK/import.json" || fail "the import request failed"
TASK=$(jq -r '.task.id // empty' "$WORK/import.json")
[ -n "$TASK" ] || fail "the import created no task: $(head -c 400 "$WORK/import.json")"
crown_wait_task "$G" "$TASK" "$WORK/import-task.json" 1800
jq -e '.task.status == "done"' "$WORK/import-task.json" >/dev/null ||
    fail "the import failed: $(jq -c '.task' "$WORK/import-task.json" | head -c 800)"
# Every ingredient at its relocated path, with its pinned digest — the
# content the mapping table will be held to.
jq -r '.[].spec.ingredients[] | [.ref, .version, .digest] | @tsv' "$WORK/cooked-every.json" >"$WORK/ingredients.tsv"
while IFS="$(printf '\t')" read -r ref ver dgst; do
    got=$(crown_manifest_digest "$DEST" "$(crown_relocate "$ref")" "$ver")
    [ "$got" = "$dgst" ] || fail "$ref:$ver is not in the zone registry at $(crown_relocate "$ref") with $dgst (got '${got:-nothing}')"
done <"$WORK/ingredients.tsv"
for r in rke2:1.36.5 rocky9-rke2-host:1.36.5 crown-app:1.0.0; do
    curl -fsS "http://$DEST/v2/cookbook/${r%:*}/tags/list" | jq -e --arg v "${r#*:}" '.tags | index($v) != null' >/dev/null ||
        fail "recipe $r is not in the zone cookbook"
done
check "import: all $N_ING ingredients at their relocated paths with their pinned digests, the three recipes in the zone cookbook ($(elapsed "$T_IMPORT"))"
# B-062: an ingredient signed upstream with a key the zone does not know is
# delivered with its signature, and both `media verify` and the import
# record what that signature is — they judge on one rule.
N_UPSTREAM=0
while IFS="$(printf '\t')" read -r name ref dgst; do
    [ -n "$name" ] || continue
    N_UPSTREAM=$((N_UPSTREAM + 1))
    jq -e --arg n "$name" '.recipes[] | select(.name == "rke2") | .ingredients[] | select(.name == $n)
        | .signature.state == "unverifiable" and (.required | not)' "$WORK/verify.json" >/dev/null ||
        fail "B-062: media verify does not record the upstream signature of $name as unverifiable: $(jq -c --arg n "$name" '[.recipes[] | select(.name == "rke2") | .ingredients[]? | select(.name == $n)]' "$WORK/verify.json")"
    jq -e --arg i "push/rke2@1.36.5/$name" '.task.items[] | select(.name == $i)
        | .status != "failed" and .signature.state == "unverifiable"' "$WORK/import-task.json" >/dev/null ||
        fail "B-062: the import item of $name does not carry its unverifiable signature verdict: $(jq -c --arg i "push/rke2@1.36.5/$name" '[.task.items[] | select(.name == $i)]' "$WORK/import-task.json")"
    sig=$(crown_manifest_digest "$DEST" "$(crown_relocate "$ref")" "$(printf '%s' "$dgst" | tr ':' '-').sig")
    [ -n "$sig" ] || fail "B-062: the upstream signature of $ref@$dgst did not travel to the zone registry"
done <"$WORK/upstream-signed.tsv"
check "B-062: $N_UPSTREAM upstream-signed ingredient(s) delivered with their signature, recorded unverifiable by media verify and the import, blocking nothing"

# -- 9. Consumption aids (feature 7.1) -------------------------------------------
crown_generate_registries_yaml "$WORK/registries.yaml"
crown_generate_mapping_table "$WORK/table.json"
# Both are checked against the recipes and ADR-0013, computed here, never
# against themselves; the table also against what the zone registry answers.
crown_check_registries_yaml "$WORK/registries.yaml" "$WORK/cooked-all.json" "$DEST"
check "${DBG}FR-065: registries.yaml generated — $CROWN_MIRRORS mirrored to http://$DEST, rewrites to the relocated paths, no other host, no credentials"
while IFS= read -r line; do note "registries.yaml | $line"; done <"$WORK/registries.yaml"
crown_check_mapping_table "$WORK/table.json" "$WORK/cooked-all.json" "$DEST"
check "${DBG}FR-065: the mapping table matches the pushed content — $CROWN_TABLE_ROWS entries, each answered by the zone registry with its digest"

# -- 10. The bare OS: never connected, everything from the zone ----------------
T_NODE=$(date +%s)
crown_vm "$NODE" tbc-airgap-node 4 8GiB 40GiB
NODE_IP=$(instance_ip "$NODE")
for target in "http://$S/healthz" "http://1.1.1.1/" "https://registry-1.docker.io/v2/"; do
    if inc exec "$NODE" -- curl -s -m 5 -o /dev/null "$target" >/dev/null 2>&1; then
        fail "the bare node reached $target: there is no air gap"
    fi
done
check "bare node $NODE ($NODE_IP, $NODE_IMAGE) up on the isolated bridge; it reaches neither the connected zone nor the internet"
node_tap() {
    _t=$(inc config get "$NODE" volatile.eth0.host_name)
    [ -n "$_t" ] || fail "staging: the node's host-side interface is unknown"
    printf '%s' "$_t"
}
crown_capture node-prep "$(node_tap)" 1024 "src host $NODE_IP"

# No resolver that answers: the bridge's dnsmasq forwards upstream (the
# residual channel the crucible README names), and the target zone has no
# such thing. The node points at an in-zone address with nothing on :53,
# so a lookup fails fast instead of leaking a query.
cat >"$WORK/node-prepare.sh" <<EOF
set -eu
mkdir -p /etc/NetworkManager/conf.d
printf '[main]\ndns=none\n' >/etc/NetworkManager/conf.d/90-crown-dns.conf
systemctl reload NetworkManager
printf 'nameserver $GATE_IP\n' >/etc/resolv.conf
grep -q " \$(hostname)\\\$" /etc/hosts || printf '%s %s\n' "$NODE_IP" "\$(hostname)" >>/etc/hosts
if getent hosts registry-1.docker.io >/dev/null 2>&1; then echo "a name still resolves" >&2; exit 1; fi
systemctl disable --now dnf-makecache.timer >/dev/null 2>&1 || true
# The image's own repositories point at the internet: set aside, as an
# operator of an isolated zone would. Tobby's FileSet is the only source.
mkdir -p /etc/yum.repos.d.offline
mv /etc/yum.repos.d/*.repo /etc/yum.repos.d.offline/ 2>/dev/null || true
cat >/etc/yum.repos.d/tobby-crown.repo <<'REPO'
[tobby-crown]
name=RKE2 host prerequisites served by Tobby (FR-047)
baseurl=http://$G/files/rocky9-rke2/
enabled=1
gpgcheck=1
repo_gpgcheck=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-Rocky-9 http://$G/files/rocky9-rke2/RPM-GPG-KEY-rancher
REPO
test "\$(ls /etc/yum.repos.d/*.repo)" = /etc/yum.repos.d/tobby-crown.repo
dnf -q -y install $PKGS
rpm -q $PKGS
rpm -q rocky-release
EOF
inc file push "$WORK/node-prepare.sh" "$NODE/root/node-prepare.sh" >/dev/null
NODE_PKGS=$(inc exec "$NODE" -- sh /root/node-prepare.sh 2>"$WORK/node-prepare.err") ||
    fail "the bare node could not install its prerequisites from Tobby: $(tail -c 800 "$WORK/node-prepare.err")"
NODE_RELEASE=$(printf '%s\n' "$NODE_PKGS" | tail -1)
[ "$NODE_RELEASE" = "$BUILDER_RELEASE" ] ||
    fail "staging: the bare node ($NODE_RELEASE) is not the image its twin computed the transaction on ($BUILDER_RELEASE)"
check "FR-047: dnf installed '$PKGS' from the Tobby-served FileSet alone (no other repository, no resolver)"
printf '%s\n' "$NODE_PKGS" | sed '$d' | while read -r p; do note "installed $p"; done

if [ "$SELINUX_MODE" = enforcing ]; then
    if [ "$(inc exec "$NODE" -- getenforce 2>/dev/null)" != Enforcing ]; then
        # shellcheck disable=SC2016 # expanded by the node's shell
        inc exec "$NODE" -- sh -c '
            sed -i "s/^SELINUX=.*/SELINUX=enforcing/" /etc/selinux/config
            grep -q selinux=0 /proc/cmdline && grubby --update-kernel=ALL --remove-args=selinux
            touch /.autorelabel
        ' || fail "staging: could not switch the node to SELinux enforcing"
        crown_capture_stop_all
        # The witnesses restart with the node: its interface is re-created.
        crown_capture leak-net2 tbc-net 128 "src net $ZONE_NET"
        crown_capture leak-uplink2 "$UPLINK" 128 "src net $ZONE_NET"
        inc restart "$NODE" >/dev/null || fail "restarting the node for the SELinux relabel"
        crown_wait_agent "$NODE" 900
        # The relabel reboots once more on its own: wait for the steady state.
        _n=0
        until [ "$(inc exec "$NODE" -- getenforce 2>/dev/null)" = Enforcing ] && ! inc exec "$NODE" -- test -e /.autorelabel; do
            _n=$((_n + 10)); [ "$_n" -gt 900 ] && fail "the node never came back Enforcing ($(inc exec "$NODE" -- getenforce 2>&1))"
            sleep 10
        done
        inc exec "$NODE" -- sh -c "printf 'nameserver $GATE_IP\n' >/etc/resolv.conf"
        [ "$(instance_ip "$NODE")" = "$NODE_IP" ] ||
            fail "staging: the node changed address across the relabel reboot; the witnesses' filters are stale"
    fi
    inc exec "$NODE" -- sh -c 'semodule -l | grep -qx rke2' || fail "the rke2 SELinux policy module is not loaded"
    check "SELinux Enforcing on the node, rke2 policy module loaded"
fi
crown_capture node "$(node_tap)" 1024 "src host $NODE_IP"

# RKE2 itself, from /files/, checked against what was packed.
cat >"$WORK/node-rke2.sh" <<EOF
set -eu
A=/root/rke2-artifacts
mkdir -p \$A /etc/rancher/rke2 /etc/NetworkManager/conf.d
for f in rke2.linux-amd64.tar.gz sha256sum-amd64.txt install.sh; do
    curl -fsS -o \$A/\$f http://$G/files/rke2-artifacts/\$f
done
sha256sum \$A/rke2.linux-amd64.tar.gz \$A/sha256sum-amd64.txt \$A/install.sh | awk '{print \$1}'
EOF
inc file push "$WORK/node-rke2.sh" "$NODE/root/node-rke2.sh" >/dev/null
GOT=$(inc exec "$NODE" -- sh /root/node-rke2.sh | tr '\n' ' ') || fail "fetching RKE2 from /files/ on the node"
WANT=$(cd "$ART" && sha256sum rke2.linux-amd64.tar.gz sha256sum-amd64.txt install.sh | awk '{print $1}' | tr '\n' ' ')
[ "$GOT" = "$WANT" ] || fail "staging: the RKE2 artifacts on the node are not the ones packed"
inc file push "$WORK/registries.yaml" "$NODE/etc/rancher/rke2/registries.yaml" >/dev/null
inc file push "$DATA/node/config.yaml" "$NODE/etc/rancher/rke2/config.yaml" >/dev/null
inc file push "$DATA/node/rke2-canal.conf" "$NODE/etc/NetworkManager/conf.d/rke2-canal.conf" >/dev/null
[ "$SELINUX_MODE" = enforcing ] || inc exec "$NODE" -- sed -i 's/^selinux: true/selinux: false/' /etc/rancher/rke2/config.yaml
# shellcheck disable=SC2016 # expanded by the node's shell
inc exec "$NODE" -- sh -c '
    set -e
    systemctl reload NetworkManager
    ls /root/rke2-artifacts | grep -q "^rke2-images" && { echo "an image tarball is staged" >&2; exit 1; }
    INSTALL_RKE2_ARTIFACT_PATH=/root/rke2-artifacts INSTALL_RKE2_METHOD=tar sh /root/rke2-artifacts/install.sh
    test -z "$(ls -A /var/lib/rancher/rke2/agent/images 2>/dev/null)"
    systemctl enable rke2-server.service >/dev/null 2>&1
    systemctl start --no-block rke2-server.service
' >"$WORK/install.log" 2>&1 || fail "installing RKE2 by the tarball method: $(tail -c 800 "$WORK/install.log")"
check "RKE2 $RKE2_VERSION installed from /files/ (tarball method), NO image tarball on the node; rke2-server started with the generated registries.yaml"

# -- 11. The cluster -------------------------------------------------------------
_n=0
until crown_kubectl get nodes -o json 2>/dev/null |
    jq -e '.items | length == 1 and (.[0].status.conditions[] | select(.type == "Ready") | .status == "True")' >/dev/null 2>&1; do
    _n=$((_n + 10))
    if [ "$_n" -gt 1500 ]; then
        inc exec "$NODE" -- journalctl -u rke2-server --no-pager -n 60 >"$WORK/rke2.journal" 2>&1 || true
        fail "the node never became Ready (25 min): $(tail -c 1500 "$WORK/rke2.journal" | tr '\n' ' ')"
    fi
    sleep 10
done
T_READY=$(elapsed "$T_NODE")
check "node Ready ($T_READY from a bare OS)"

_n=0
until crown_kubectl get pods -A -o json >"$WORK/pods.json" 2>/dev/null &&
    jq -e '.items | length > 0 and all(.status.phase == "Succeeded" or
        (.status.phase == "Running" and all(.status.containerStatuses[]?; .ready)))' "$WORK/pods.json" >/dev/null 2>&1; do
    _n=$((_n + 10))
    [ "$_n" -gt 1200 ] && fail "system pods not all Running/Completed (20 min): $(jq -c '[.items[] |
        select(.status.phase != "Succeeded" and (.status.phase != "Running" or any(.status.containerStatuses[]?; .ready | not))) |
        {pod: (.metadata.namespace + "/" + .metadata.name), phase: .status.phase,
         waiting: [.status.containerStatuses[]?.state.waiting.reason // empty]}]' "$WORK/pods.json" 2>/dev/null)"
    sleep 10
done
N_PODS=$(jq '.items | length' "$WORK/pods.json")
check "all $N_PODS system pods Running (ready) or Completed"
jq -r '.items[] | .metadata.namespace + "/" + .metadata.name + " " + .status.phase' "$WORK/pods.json" |
    while read -r p; do note "pod $p"; done

for h in registry.rancher.com docker.io; do
    inc exec "$NODE" -- grep -q "http://$DEST" "/var/lib/rancher/rke2/agent/etc/containerd/certs.d/$h/hosts.toml" ||
        fail "containerd's $h host configuration does not carry the generated endpoint"
done
check "${DBG}containerd configured from the generated snippet (certs.d/registry.rancher.com and certs.d/docker.io → http://$DEST)"

# The FR-065 acceptance pod, then the negative control.
inc file push "$DATA/node/crown-app-pod.yaml" "$NODE/root/crown-app-pod.yaml" >/dev/null
inc file push "$DATA/node/crown-absent-pod.yaml" "$NODE/root/crown-absent-pod.yaml" >/dev/null
crown_kubectl apply -f /root/crown-app-pod.yaml >/dev/null || fail "creating the test pod"
crown_kubectl apply -f /root/crown-absent-pod.yaml >/dev/null || fail "creating the negative-control pod"
crown_kubectl wait --for=condition=Ready pod/crown-app --timeout=300s >/dev/null 2>&1 ||
    fail "FR-065: the pod referencing $APP_IMAGE never ran: $(crown_kubectl get pod crown-app -o json | jq -c '[.status.containerStatuses[]?.state]')"
check "${DBG}FR-065: the pod referencing $APP_IMAGE (and busybox:1.37.0) runs"
_n=0
until crown_kubectl get pod crown-absent -o json 2>/dev/null |
    jq -e '[.status.containerStatuses[]?.state.waiting.reason] | any(. == "ErrImagePull" or . == "ImagePullBackOff")' >/dev/null 2>&1; do
    _n=$((_n + 5)); [ "$_n" -gt 300 ] && fail "the negative control neither ran nor failed to pull: $(crown_kubectl get pod crown-absent -o json | jq -c '.status')"
    sleep 5
done
check "negative control: docker.io/library/busybox:$ABSENT_TAG, never pushed, fails to pull"

# -- 12. Proof from the witnesses -------------------------------------------------
crown_kubectl get pods -A -o json >"$WORK/pods.json"
# Every image the cluster runs, normalized as the kubelet does.
jq -r '[.items[].spec | (.containers + (.initContainers // []))[] | .image] | unique[]' "$WORK/pods.json" |
    sed 's/@sha256:.*//' | while read -r img; do
        first=${img%%/*}
        case "$img" in
            */*) case "$first" in *.* | *:* | localhost) printf '%s\n' "$img" ;; *) printf 'docker.io/%s\n' "$img" ;; esac ;;
            *) printf 'docker.io/library/%s\n' "$img" ;;
        esac
    done | grep -v ":$ABSENT_TAG\$" | sort -u >"$WORK/running-images.txt"
jq -r '.[].spec.ingredients[] | select(.kind == "ContainerImage") | .ref + ":" + .version + " " + .digest' \
    "$WORK/cooked-all.json" >"$WORK/recipe-images.txt"
crown_capture_stop_all
crown_capture_read node | grep -oE '(GET|HEAD) /v2/[^ ]+' | sort -u >"$WORK/zone-requests.txt"
for c in node-prep node; do
    crown_capture_read "$c" "dst host $GATE_IP and tcp dst port 8080" | grep -oE 'GET /files/[^ ]+' || true
done | sort -u >"$WORK/files-requests.txt"
[ -s "$WORK/zone-requests.txt" ] || fail "staging: the node capture holds no registry request at all"

# rke2-runtime is pulled by RKE2 itself, before containerd exists.
printf 'registry.rancher.com/rancher/rke2-runtime:%s\n' "$RKE2_TAG" >>"$WORK/running-images.txt"
N_RUN=0
while read -r img; do
    ref=${img%:*}
    dgst=$(awk -v i="$img" '$1 == i {print $2}' "$WORK/recipe-images.txt")
    [ -n "$dgst" ] || fail "the cluster runs $img, which no recipe carries"
    grep -q " /v2/$(crown_relocate "$ref")/manifests/" "$WORK/zone-requests.txt" ||
        fail "no request for $img reached the zone registry (relocated $(crown_relocate "$ref"))"
    case "$ref" in
        */rke2-runtime) ;;
        *) crown_crictl inspecti -o json "$img" 2>/dev/null | jq -e --arg d "$ref@$dgst" '.status.repoDigests | index($d) != null' >/dev/null ||
               fail "containerd does not hold $img under its pinned digest $dgst" ;;
    esac
    N_RUN=$((N_RUN + 1))
done <"$WORK/running-images.txt"
check "every image the cluster runs ($N_RUN, rke2-runtime included) is in a recipe, was requested from the zone registry at its relocated path (/v2/registry.rancher.com/rancher/…, /v2/docker.io/library/…), and is held under its pinned digest"
grep -q " /v2/docker.io/library/busybox/manifests/$ABSENT_TAG" "$WORK/zone-requests.txt" ||
    fail "the negative control's pull never reached the zone registry: the mirror is not the route"
check "the negative control's pull was asked of the zone registry — the generated mirror is THE route"
for f in rke2-artifacts/rke2.linux-amd64.tar.gz rke2-artifacts/sha256sum-amd64.txt rke2-artifacts/install.sh \
         rocky9-rke2/repodata/repomd.xml rocky9-rke2/RPM-GPG-KEY-rancher; do
    grep -q "^GET /files/$f\$" "$WORK/files-requests.txt" ||
        fail "no request for /files/$f was seen from the node"
done
N_RPM_GOT=$(grep -c '^GET /files/rocky9-rke2/.*\.rpm$' "$WORK/files-requests.txt" || true)
check "the node's packages ($N_RPM_GOT rpms) and RKE2 artifacts were requested from Tobby's /files/"

LEAKS=0
for c in leak-net leak-uplink leak-net2 leak-uplink2; do
    [ -f "$WORK/capture-$c.pcap" ] || continue
    LEAKS=$((LEAKS + $(crown_capture_count "$c")))
done
[ "$LEAKS" -eq 0 ] || fail "$LEAKS packet(s) sourced in the isolated zone left it (tbc-net or $UPLINK)"
for target in "http://$S/healthz" "http://1.1.1.1/"; do
    if inc exec "$NODE" -- curl -s -m 5 -o /dev/null "$target" >/dev/null 2>&1; then
        fail "after bootstrap, the node reached $target"
    fi
done
check "no traffic left the isolated zone: 0 packets sourced in $ZONE_NET on tbc-net or $UPLINK for the whole run; canary still fails"
OUTSIDE="ip and not dst net $ZONE_NET and not dst host 255.255.255.255 and not ip multicast"
ATTEMPTS=$(( $(crown_capture_count node-prep "$OUTSIDE") + $(crown_capture_count node "$OUTSIDE") ))
note "outbound attempts by the node, all refused by the gap: $ATTEMPTS packet(s)"
for c in node-prep node; do
    tcpdump -nn -r "$WORK/capture-$c.pcap" "$OUTSIDE" 2>/dev/null | awk '{print $5}' |
        sed -E 's/:$//; s/\.[0-9]+$//' | sort | uniq -c | sort -rn | head -10 |
        while read -r cnt dst; do note "attempt $c → $dst ($cnt)"; done
done

# -- 13. Wrap-up ----------------------------------------------------------------------
note "durations: total $(elapsed "$T0"), bare OS to Ready $T_READY"
if [ -n "$DBG" ]; then
    fail "DEBUG run (CRUCIBLE_M7_DEBUG_AIDS=handmade): the registries.yaml and table were not generated by Tobby — acceptance withheld"
fi
if [ "${CRUCIBLE_M7_KEEP:-}" != 1 ]; then
    release_leftovers
    rm -rf "$WORK"
fi
check "scenario m7/crown complete — report: $CRUCIBLE_REPORT"
