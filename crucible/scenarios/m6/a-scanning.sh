#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenario, track A (ADR-0014, DEVELOPMENT-PLAN §2.6):
# vulnerability scanning with a policy, the scanner and its database carried
# by Tobby's own channel across a real air gap — on real nodes, with the real
# pinned Trivy and a real database, and the properties only this tier shows:
# a medium that is a real block device, a gap that has no route, a scanner
# binary unpacked on a bare Linux host from the tobby-system channel and
# tampered with in place.
#
#   1. Fixtures (connected zone): the tobby-system recipe — the pinned Trivy
#      as a FileSet, the database as an OCIArtifact — plus a recipe whose
#      image is known-vulnerable (CVE-2019-14697, CVSS 9.8) and a clean
#      one, all signed by a key this scenario generates.
#   2. Degraded mode before anything crossed: no scanner, no database —
#      the metric, the UI banner and the log say so, never silently.
#   3. Mirror under a blocking policy (scratch store): tobby-system first
#      arms the scanner in the same run; the vulnerable recipe is withheld
#      whole (R-19 grain), named with TBY-SCN-005 and its CVE, absent from
#      the media manifest, audited; the clean one is written.
#   4. The channel-delivered binary is tampered with on disk: it is not
#      executed (TBY-SCN-002), the item is refused under block, and the
#      next refresh restores it from the verified delivery.
#   5. R-13: a database beyond scan.maxDbAge under the block action refuses
#      with TBY-SCN-004; the stale gauge says 1.
#   6. Mirror, advisory, on the real medium: the vulnerable image is
#      written with the finding and the database age in its report.
#   7. The medium is detached, carried, imported into the isolated zone's
#      registry (verification does not need the database).
#   8. A passthrough instance INSIDE the zone, whose only upstream is the
#      zone registry, synchronizes tobby-system first: its scanner comes
#      from the channel, its database is the one that crossed (same
#      digest); with the Retriever's threshold the vulnerable image is
#      refused and never pushed, the clean one is; without it (advisory)
#      the vulnerable image is pushed with the finding reported.
#
# Every staging step checks its own manipulation before the verdict it
# prepares is read: a tamper that did not change the file, a medium that
# did not mount, a scanner that never became available would otherwise
# make the next check pass or fail for the wrong reason.
#
# Topology:
#   tbc-m6a-source    (container, tbc-net)     production registry + cookbook
#   tbc-m6a-connected (container, tbc-net)     mirror instance, store ON the medium
#   tbc-m6a-dest      (container, tbc-airgap)  the isolated zone's registry
#   tbc-m6a-isolated  (container, tbc-airgap)  destination instance, on the medium
#   tbc-m6a-zone      (container, tbc-airgap)  passthrough instance inside the zone
#   medium            (loop-backed block dev)  attached, filled, detached, moved
#
# Host tooling on top of the crucible baseline: oras (to fetch the database
# the connected zone publishes), cosign (installed on demand like m3-m5), and
# the pinned Trivy release — taken from CRUCIBLE_TRIVY_ARCHIVE when set,
# otherwise fetched once and checked against the release checksum and the
# executable digest Tobby pins (internal/scan/trivy/pins.go): the scanner is
# the point of this scenario, like the signatures are the point of m3.
#
# Run alone:   sh crucible/scenarios/m6/a-scanning.sh
# Dispatched:  ./crucible/run.sh m6
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m6}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m6-a-scanning.txt}"
. "$DIR/../../lib.sh"

IMAGE="${CRUCIBLE_IMAGE:-images:alpine/3.24}"
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
MEDIUM_IMG="${CRUCIBLE_MEDIUM_IMG:-/tmp/tbc-m6a-medium.img}"
ZONE="zone-b"
WORK="$(mktemp -d)"
INSTANCES="tbc-m6a-source tbc-m6a-connected tbc-m6a-dest tbc-m6a-isolated tbc-m6a-zone"
COSIGN="${COSIGN:-cosign}"
TRIVY_VERSION="0.76.2"
KNOWN_CVE="CVE-2019-14697"

report_start

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

# -- 0. Tooling ---------------------------------------------------------------
command -v jq >/dev/null 2>&1 || fail "jq not found"
command -v oras >/dev/null 2>&1 || fail "oras not found: the connected zone's database is fetched with it"
if ! command -v "$COSIGN" >/dev/null 2>&1; then
    GOBIN=/usr/local/bin go install github.com/sigstore/cosign/v2/cmd/cosign@latest >/dev/null 2>&1 ||
        fail "installing cosign"
    COSIGN=/usr/local/bin/cosign
fi
case "$ARCH" in
    amd64) T_ARCH=64bit
           T_ARCHIVE_SHA=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf
           T_BIN_SHA=6a972c69606c7b3ea4c6bdcc6fc9332ea9ad38381624310c1e85cc77345909f3 ;;
    arm64) T_ARCH=ARM64
           T_ARCHIVE_SHA=ab9d51f86e6a5fb936f605faff6b593ac51bc378fff4d3e8b9d58358b9c367c3
           T_BIN_SHA=7844c3851b28470b6d7f0975fa0b3262c5b2272c9a54a91f522801bb8926f51f ;;
    *) fail "unsupported crucible architecture $ARCH" ;;
esac
T_ARCHIVE="${CRUCIBLE_TRIVY_ARCHIVE:-$WORK/trivy.tgz}"
if [ ! -f "$T_ARCHIVE" ]; then
    curl -fsSL -o "$T_ARCHIVE" \
        "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${T_ARCH}.tar.gz" ||
        fail "fetching the pinned Trivy release"
fi
echo "${T_ARCHIVE_SHA}  ${T_ARCHIVE}" | sha256sum -c - >/dev/null 2>&1 || fail "the Trivy archive does not match its release checksum"
mkdir -p "$WORK/trivy-fileset"
tar -xzf "$T_ARCHIVE" -C "$WORK/trivy-fileset" trivy || fail "extracting the Trivy executable"
echo "${T_BIN_SHA}  ${WORK}/trivy-fileset/trivy" | sha256sum -c - >/dev/null 2>&1 ||
    fail "the Trivy executable is not the one Tobby pins"
( cd "$WORK" && { oras pull mirror.gcr.io/aquasec/trivy-db:2 >/dev/null 2>&1 || oras pull ghcr.io/aquasecurity/trivy-db:2 >/dev/null 2>&1; } ) ||
    fail "fetching the vulnerability database the connected zone publishes"
test -s "$WORK/db.tar.gz" || fail "the database artifact carries no db.tar.gz layer"
check "tooling: jq, oras, cosign; Trivy $TRIVY_VERSION checked twice; database fetched ($(du -h "$WORK/db.tar.gz" | cut -f1))"

# -- 1. Binary under test, fresh instances, one medium -----------------------
( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -trimpath -o "$WORK/tobby" ./cmd/tobby ) ||
    fail "building tobby"
release_leftovers
for spec in "tbc-m6a-source tbc-connected-node" "tbc-m6a-connected tbc-connected-node" \
            "tbc-m6a-dest tbc-airgap-node" "tbc-m6a-isolated tbc-airgap-node" "tbc-m6a-zone tbc-airgap-node"; do
    # shellcheck disable=SC2086 # split on purpose: one "<instance> <profile>" pair per word list
    set -- $spec
    inc launch "$IMAGE" "$1" --profile "$2" \
        -c security.syscalls.intercept.mount=true \
        -c security.syscalls.intercept.mount.allowed=ext4 >/dev/null
done
truncate -s 8G "$MEDIUM_IMG"; MEDIUM_DEV=$(losetup -f --show "$MEDIUM_IMG")
inc config device add tbc-m6a-connected medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
for inst in $INSTANCES; do
    inc file push "$WORK/tobby" "$inst/usr/bin/tobby" >/dev/null
    inc exec "$inst" -- chmod +x /usr/bin/tobby
done
# shellcheck disable=SC2016 # expanded by the node's shell, not by this one
inc exec tbc-m6a-connected -- sh -c '
    n=0
    until apk add --no-cache e2fsprogs >/dev/null 2>&1; do
        n=$((n + 1)); [ "$n" -ge 30 ] && { echo "no package mirror reachable" >&2; exit 1; }; sleep 2
    done
    base=$(awk "{print \$2; exit}" /proc/self/uid_map)
    mkfs.ext4 -q -E root_owner="$base:$base" /dev/sdb && mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby
' || fail "formatting and mounting the medium on the connected node"
inc exec tbc-m6a-connected -- sh -c 'mountpoint -q /media/tobby' || fail "the medium is not mounted"
check "instances launched, medium $MEDIUM_DEV formatted and mounted on the connected node"

SOURCE_IP=$(instance_ip tbc-m6a-source)
if inc exec tbc-m6a-zone -- wget -q -T 5 -O /dev/null "http://$SOURCE_IP:8080/healthz" >/dev/null 2>&1; then
    fail "the zone node reached the connected zone: there is no air gap"
fi
if inc exec tbc-m6a-zone -- wget -q -T 5 -O /dev/null http://example.com/ >/dev/null 2>&1; then
    fail "the zone node reached the internet"
fi
check "egress canary failed as required (air gap proven by construction)"

# -- 2. Fixtures ---------------------------------------------------------------
inc exec tbc-m6a-source -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m6a-source http://127.0.0.1:8080/healthz
S="$SOURCE_IP:8080"
seed() { ( cd "$ROOT" && go run ./test/topology/seed "$@" ); }
DB_DIG=$(seed push-trivy-db "$S/ghcr.io/aquasecurity/trivy-db:2" "$WORK/db.tar.gz") || fail "publishing the database"
TRIVY_DIG=$(seed push-fileset "$S/ghcr.io/aquasecurity/trivy:$TRIVY_VERSION" "$WORK/trivy-fileset") || fail "publishing the scanner FileSet"
VULN_DIG=$(seed push-vulnerable "$S/docker.io/example/vulnerable:1.0") || fail "publishing the vulnerable image"
CLEAN_DIG=$(seed push "$S/docker.io/example/clean:1.0") || fail "publishing the clean image"
( cd "$WORK" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1 ) || fail "generating the signing key pair"

publish() { # <name> <yaml>
    printf '%s\n' "$2" >"$WORK/recipe-$1.yaml"
    dgst=$(seed push-recipe "$S/cookbook/$1:1.0.0" "$WORK/recipe-$1.yaml") || fail "publishing recipe $1"
    COSIGN_PASSWORD='' "$COSIGN" sign --key "$WORK/cosign.key" --yes --allow-insecure-registry \
        --use-signing-config=false --new-bundle-format=false --tlog-upload=false \
        "$S/cookbook/$1@$dgst" >/dev/null 2>&1 || fail "signing recipe $1"
}
publish tobby-system "apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: tobby-system
  version: 1.0.0
spec:
  ingredients:
    - name: trivy
      kind: FileSet
      ref: ghcr.io/aquasecurity/trivy
      version: \"$TRIVY_VERSION\"
      digest: $TRIVY_DIG
    - name: trivy-db
      kind: OCIArtifact
      ref: ghcr.io/aquasecurity/trivy-db
      version: \"2\"
      digest: $DB_DIG
      artifactType: application/vnd.aquasec.trivy.config.v1+json"
for pair in "vulnerable $VULN_DIG" "clean $CLEAN_DIG"; do
    # shellcheck disable=SC2086 # split on purpose: one "<name> <digest>" pair
    set -- $pair
    publish "$1" "apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: $1
  version: 1.0.0
spec:
  ingredients:
    - name: app
      kind: ContainerImage
      ref: docker.io/example/$1
      version: \"1.0\"
      digest: $2"
done
check "fixtures signed: tobby-system (trivy $TRIVY_DIG, db $DB_DIG), vulnerable $VULN_DIG, clean $CLEAN_DIG"

# retriever <file> <cookbook> [threshold]: tobby-system FIRST — the bootstrap.
retriever() {
    policy=""
    [ -n "${3:-}" ] && policy="  scanPolicy:
    threshold: $3"
    cat >"$1" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: $ZONE
spec:
  cookbook: $2
$policy
  recipes:
    - name: tobby-system
      version: "1.0.0"
    - name: vulnerable
      version: "1.0.0"
    - name: clean
      version: "1.0.0"
EOF
}
# config <instance> <mode> <store> <upstream> <scan block> [destination]
config() {
    dest=""
    [ -n "${6:-}" ] && dest="destination:
  registry: $6
  basePath: promoted"
    inc exec "$1" -- sh -c "mkdir -p /etc/tobby && cat >/etc/tobby/config.yaml <<'EOF'
mode: $2
zone: $ZONE
storage:
  root: $3
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
auth:
  disabled: true
registries:
  insecure:
    - $4
  substitutions:
    docker.io: $4
    ghcr.io: $4
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: zone
      keyFile: /etc/tobby/trust.pub
scan:
$5
$dest
EOF"
}
restart() { # <instance>
    # Two execs: one shell holding both "[t]obby serve" and "tobby serve"
    # would match its own pkill pattern and kill itself (B-026).
    inc exec "$1" -- sh -c 'pkill -f "[t]obby serve" || true; sleep 1'
    inc exec "$1" -- sh -c 'nohup tobby serve >>/var/log/tobby.log 2>&1 &'
    wait_ready "$1" http://127.0.0.1:8080/readyz
}
metric() { # <instance> <name> → value
    inc exec "$1" -- wget -q -O - http://127.0.0.1:8080/metrics | awk -v n="$2" '$1==n {print $2; exit}'
}
sync_json() { # <instance> → the task document
    inc exec "$1" -- sh -c 'tobby sync --wait --output json 2>/dev/null' || true
}
item() { # <task json> <item name> <jq filter>
    echo "$1" | jq -r --arg n "$2" ".task.items[] | select(.name == \$n) | $3"
}

retriever "$WORK/retriever.yaml" "$S/cookbook"
for inst in tbc-m6a-connected tbc-m6a-zone; do
    inc file push "$WORK/cosign.pub" "$inst/etc/tobby/trust.pub" --create-dirs >/dev/null
done
inc file push "$WORK/cosign.pub" tbc-m6a-isolated/etc/tobby/trust.pub --create-dirs >/dev/null
inc file push "$WORK/retriever.yaml" tbc-m6a-connected/etc/tobby/retriever.yaml --create-dirs >/dev/null

# -- 3. Degraded mode before anything crossed --------------------------------
config tbc-m6a-connected mirror /var/lib/scratch "$S" "  mode: block"
restart tbc-m6a-connected
[ "$(metric tbc-m6a-connected tobby_scan_scanner_available)" = "0" ] || fail "FR-031: the scanner reads available with nothing delivered"
[ "$(metric tbc-m6a-connected tobby_scan_db_present)" = "0" ] || fail "FR-032: a database reads present with nothing delivered"
inc exec tbc-m6a-connected -- wget -q -O - http://127.0.0.1:8080/ | grep -q "TBY-SCN-001" ||
    fail "FR-031: the UI shows no degraded-mode banner"
inc exec tbc-m6a-connected -- grep -q "vulnerability scanning degraded" /var/log/tobby.log ||
    fail "FR-031: the startup log does not state the degraded mode"
check "degraded mode visible on three channels before delivery: metric 0/0, banner TBY-SCN-001, log record"

# -- 4. Mirror, blocking, scratch store: tobby-system first arms the scanner --
T=$(sync_json tbc-m6a-connected)
echo "$T" | jq -e '.task.items | length > 0' >/dev/null || fail "the blocking synchronization produced no task"
[ "$(metric tbc-m6a-connected tobby_scan_scanner_available)" = "1" ] ||
    fail "6.2: the channel-delivered scanner is not available after tobby-system crossed (staging)"
[ "$(metric tbc-m6a-connected tobby_scan_db_present)" = "1" ] || fail "6.2: the delivered database is not present (staging)"
[ "$(item "$T" "vulnerable@1.0.0/recipe" '.error.code')" = "TBY-SCN-005" ] ||
    fail "FR-031: the vulnerable recipe was not withheld with TBY-SCN-005: $(item "$T" "vulnerable@1.0.0/recipe" '.')"
item "$T" "vulnerable@1.0.0/app" '.scan.evaluation.violations[]' | grep -qx "$KNOWN_CVE" ||
    fail "FR-031: the report does not name $KNOWN_CVE"
[ "$(item "$T" "clean@1.0.0/recipe" '.status')" = "done" ] || fail "R-19: the clean neighbour was not written"
MAN=$(inc exec tbc-m6a-connected -- cat /var/lib/scratch/meta/media.json)
echo "$MAN" | jq -e '[.recipes[].name] | index("vulnerable") == null and index("clean") != null and index("tobby-system") != null' >/dev/null ||
    fail "R-19: the media manifest does not reflect the refusal: $(echo "$MAN" | jq -c '[.recipes[].name]')"
inc exec tbc-m6a-connected -- grep -q '"action":"scan.block"' /var/log/tobby.log || fail "FR-094: no scan.block audit event"
check "mirror/block: vulnerable withheld whole (TBY-SCN-005, $KNOWN_CVE), not on the manifest, audited; clean written"

# -- 5. The unpacked binary is tampered with ---------------------------------
BIN=$(inc exec tbc-m6a-connected -- sh -c 'ls /var/lib/tobby-state/scanner/bin/*/trivy | head -1')
BEFORE=$(inc exec tbc-m6a-connected -- sha256sum "$BIN" | cut -d' ' -f1)
inc exec tbc-m6a-connected -- sh -c "printf X >>'$BIN'"
AFTER=$(inc exec tbc-m6a-connected -- sha256sum "$BIN" | cut -d' ' -f1)
[ "$BEFORE" != "$AFTER" ] || fail "staging: the tamper did not change the binary"
T=$(sync_json tbc-m6a-connected)
[ "$(item "$T" "clean@1.0.0/app" '.scan.evaluation.verdict')" = "not-scanned" ] ||
    fail "FR-031: a tampered binary still produced a scan"
item "$T" "clean@1.0.0/app" '.scan.evaluation.reasons[].code' | grep -qx "TBY-SCN-002" ||
    fail "FR-031: the refusal to execute is not reported as TBY-SCN-002"
[ "$(item "$T" "clean@1.0.0/recipe" '.status')" = "failed" ] || fail "FR-031: under block, an unscanned item crossed"
T=$(sync_json tbc-m6a-connected)
[ "$(item "$T" "clean@1.0.0/app" '.scan.evaluation.verdict')" = "pass" ] ||
    fail "the verified delivery did not restore the scanner after the tamper"
check "tampered binary never executed (TBY-SCN-002, item refused under block); restored from the delivery next cycle"

# -- 6. R-13: a stale database under the block action ------------------------
config tbc-m6a-connected mirror /var/lib/scratch "$S" "  mode: block
  maxDbAge: 1s
  maxDbAgeAction: block"
restart tbc-m6a-connected
T=$(sync_json tbc-m6a-connected)
[ "$(item "$T" "clean@1.0.0/recipe" '.error.code')" = "TBY-SCN-004" ] || fail "R-13: a stale database did not refuse with TBY-SCN-004"
[ "$(item "$T" "clean@1.0.0/app" '.scan.database.stale')" = "true" ] || fail "R-13: the report does not say stale"
[ "$(metric tbc-m6a-connected tobby_scan_db_stale)" = "1" ] || fail "R-13: the stale gauge is not 1"
check "R-13: stale database refuses (TBY-SCN-004), report and gauge say stale"

# -- 7. Mirror, advisory, on the medium --------------------------------------
config tbc-m6a-connected mirror /media/tobby/store "$S" "  mode: warn"
restart tbc-m6a-connected
T=$(sync_json tbc-m6a-connected)
[ "$(item "$T" "vulnerable@1.0.0/app" '.scan.evaluation.verdict')" = "warn" ] || fail "FR-031: advisory verdict missing"
[ "$(item "$T" "vulnerable@1.0.0/app" '.scan.database.age_seconds > 0')" = "true" ] || fail "R-13: no database age in the report"
inc exec tbc-m6a-connected -- cat /media/tobby/store/meta/media.json | jq -e '(.recipes | length) == 3' >/dev/null ||
    fail "the advisory medium does not carry the three deliveries"
inc exec tbc-m6a-connected -- sh -c 'pkill -f "[t]obby serve" || true; sleep 1; sync && umount /media/tobby'
inc config device remove tbc-m6a-connected medium >/dev/null
check "mirror/advisory: vulnerable written with $KNOWN_CVE and the database age; medium detached"

# -- 8. Across the gap ---------------------------------------------------------
DEST_IP=$(instance_ip tbc-m6a-dest)
inc exec tbc-m6a-dest -- sh -c '
    nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
        TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
        TOBBY_SERVER_ADDR=:5000 tobby serve >/var/log/tobby.log 2>&1 &
'
wait_ready tbc-m6a-dest http://127.0.0.1:5000/healthz
inc config device add tbc-m6a-isolated medium unix-block source="$MEDIUM_DEV" path=/dev/sdb >/dev/null
inc exec tbc-m6a-isolated -- sh -c 'mkdir -p /media/tobby && mount -t ext4 /dev/sdb /media/tobby && test -f /media/tobby/store/meta/media.json' ||
    fail "staging: the medium did not mount on the isolated node"
inc exec tbc-m6a-isolated -- sh -c "mkdir -p /etc/tobby && cat >/etc/tobby/config.yaml <<'EOF'
mode: mirror
zone: $ZONE
storage:
  root: /media/tobby/store
state:
  root: /var/lib/tobby-state
auth:
  disabled: true
registries:
  insecure:
    - $DEST_IP:5000
trust:
  roots:
    - name: zone
      keyFile: /etc/tobby/trust.pub
destination:
  registry: $DEST_IP:5000
  cookbook: cookbook
EOF"
inc exec tbc-m6a-isolated -- sh -c 'tobby media import --output json' >"$WORK/import.json" 2>/dev/null || true
CATALOG=$(inc exec tbc-m6a-dest -- wget -q -O - http://127.0.0.1:5000/v2/_catalog)
echo "$CATALOG" | grep -q "trivy-db" || fail "6.2: the database did not reach the zone registry"
echo "$CATALOG" | grep -q "cookbook/tobby-system" || fail "the tobby-system recipe did not reach the zone cookbook"
check "medium carried and imported: database, scanner and recipes in the zone registry"

# -- 9. Inside the zone: the scanner and its database came through the channel --
retriever "$WORK/zone-retriever.yaml" "$DEST_IP:5000/cookbook" 7.0
inc file push "$WORK/zone-retriever.yaml" tbc-m6a-zone/etc/tobby/retriever.yaml --create-dirs >/dev/null
config tbc-m6a-zone passthrough /var/lib/tobby "$DEST_IP:5000" "  mode: warn" "$DEST_IP:5000"
inc exec tbc-m6a-zone -- sh -c "printf 'sync:\n  interval: 0s\n' >>/etc/tobby/config.yaml"
restart tbc-m6a-zone
T=$(sync_json tbc-m6a-zone)
[ "$(metric tbc-m6a-zone tobby_scan_scanner_available)" = "1" ] || fail "6.2: the zone's scanner did not arrive by the channel"
[ "$(item "$T" "vulnerable@1.0.0/app" '.scan.database.digest')" = "$DB_DIG" ] ||
    fail "FR-032: the zone scanned with another database than the one that crossed"
[ "$(item "$T" "push/vulnerable@1.0.0/app" '.error.code')" = "TBY-SCN-005" ] ||
    fail "FR-031: the Retriever threshold did not refuse the vulnerable image in the zone"
[ "$(item "$T" "vulnerable@1.0.0/app" '.scan.evaluation.policy.mode_level')" = "retriever" ] ||
    fail "the report does not say the Retriever made the policy blocking"
[ "$(item "$T" "push/clean@1.0.0/recipe" '.status')" = "done" ] || fail "the clean recipe was not promoted in the zone"
inc exec tbc-m6a-dest -- wget -q -O /dev/null "http://127.0.0.1:5000/v2/promoted/docker.io/example/vulnerable/manifests/1.0" 2>/dev/null &&
    fail "FR-031: the vulnerable image reached the zone's promoted repository"
check "zone/blocking (Retriever 7.0): database $DB_DIG used, $KNOWN_CVE refused and absent; clean promoted"

retriever "$WORK/zone-retriever.yaml" "$DEST_IP:5000/cookbook"
inc file push "$WORK/zone-retriever.yaml" tbc-m6a-zone/etc/tobby/retriever.yaml >/dev/null
T=$(sync_json tbc-m6a-zone)
[ "$(item "$T" "vulnerable@1.0.0/app" '.scan.evaluation.verdict')" = "warn" ] || fail "FR-031: advisory verdict missing in the zone"
[ "$(item "$T" "push/vulnerable@1.0.0/recipe" '.status')" = "done" ] || fail "FR-031: the advisory policy did not push"
inc exec tbc-m6a-dest -- wget -q -O /dev/null "http://127.0.0.1:5000/v2/promoted/docker.io/example/vulnerable/manifests/1.0" ||
    fail "the advisory push did not land"
check "zone/advisory: vulnerable pushed with $KNOWN_CVE in its report"

cleanup
check "scenario m6/a-scanning complete — report: $CRUCIBLE_REPORT"
