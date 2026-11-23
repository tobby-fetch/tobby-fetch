#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Helpers shared by the milestone-7 scenarios of track C that stage an
# instance with authentication ON, a source registry, signed recipes and
# real OCI clients: c-provenance.sh (FR-098, provenance per repository) and
# d-hardening.sh (FR-098 hardening switches, and the 7.3 hardening seen from
# the wire). Sourced, never run.
#
# The m6 rule holds here too: a scenario that MANIPULATES something
# verifies its own manipulation before it asks the product for a verdict.
# A push that never happened reads exactly like a class that is wrong; a
# switch that was never set reads exactly like a refusal that is missing.
#
# Callers set ROOT (the repository) and WORK (a scratch directory) before
# sourcing this file.

# -- tooling ---------------------------------------------------------------------
# m7_tooling <tool…>: every named host tool must exist (NFR-019: the
# crucible never downloads its harness at run time); cosign is the m3-m6
# exception, installed on demand because the signatures are the point.
m7_tooling() {
    for _t in jq curl "$@"; do
        command -v "$_t" >/dev/null 2>&1 || fail "$_t not found on the crucible host"
    done
    COSIGN="${COSIGN:-cosign}"
    if ! command -v "$COSIGN" >/dev/null 2>&1; then
        GOBIN=/usr/local/bin go install github.com/sigstore/cosign/v2/cmd/cosign@latest >/dev/null 2>&1 ||
            fail "installing cosign"
        COSIGN=/usr/local/bin/cosign
    fi
    check "tooling available (jq, curl, cosign${*:+, $*})"
}

# m7_build: the binary under test, for the crucible host's arch, in $WORK.
m7_build() {
    ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
    ( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
        go build -trimpath -o "$WORK/tobby" ./cmd/tobby ) || fail "building tobby"
    check "built tobby for linux/$ARCH"
}

# m7_launch <instance> <profile> [<incus -c option>…]: a fresh container
# carrying the binary.
m7_launch() {
    _inst="$1"; _prof="$2"; shift 2
    inc delete --force "$_inst" 2>/dev/null || true
    _opts=""
    for _o in "$@"; do _opts="$_opts -c $_o"; done
    # shellcheck disable=SC2086 # a list of "-c key=value" words by construction
    inc launch "${CRUCIBLE_IMAGE:-images:alpine/3.24}" "$_inst" --profile "$_prof" $_opts >/dev/null ||
        fail "launching $_inst"
    inc file push "$WORK/tobby" "$_inst/usr/bin/tobby" >/dev/null
    inc exec "$_inst" -- chmod +x /usr/bin/tobby
}

# m7_apk <instance> <package…>: packages from the distribution mirror, on a
# connected node, retried while the guest's network settles.
m7_apk() {
    _inst="$1"; shift
    _n=0
    until inc exec "$_inst" -- apk add --no-cache "$@" >/dev/null 2>&1; do
        _n=$((_n + 1))
        [ "$_n" -ge 30 ] && fail "$_inst: apk add $*: no package mirror reachable"
        sleep 2
    done
}

# m7_seed <args…>: the topology seeder, a go-containerregistry client (a
# standard client, never a protocol mock). SEED_AUTH is passed through.
m7_seed() { ( cd "$ROOT" && go run ./test/topology/seed "$@" ); }

# m7_serve_source <instance>: the source registry and cookbook — a
# passthrough instance with authentication disabled (the documented FR-075
# override: a fixture registry, never a deployment).
m7_serve_source() {
    inc exec "$1" -- sh -c '
        nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
            TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
            TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
    '
    wait_ready "$1" http://127.0.0.1:8080/healthz
}

# -- signing ---------------------------------------------------------------------
# m7_keypair: $WORK/cosign.key and $WORK/cosign.pub, the zone's trust root.
m7_keypair() {
    ( cd "$WORK" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1 ) ||
        fail "generating the signing key pair"
}

# m7_publish <registry> <name> <yaml> [sign]: publishes one recipe artifact
# under <registry>/cookbook/<name>:1.0.0, signs it when a fourth argument
# is given, and prints its digest. A signed one is checked with cosign — a
# tool nobody here wrote — before anything depends on it.
m7_publish() {
    printf '%s\n' "$3" >"$WORK/recipe-$2.yaml"
    _d=$(m7_seed push-recipe "$1/cookbook/$2:1.0.0" "$WORK/recipe-$2.yaml") || fail "publishing recipe $2"
    if [ -n "${4:-}" ]; then
        COSIGN_PASSWORD='' "$COSIGN" sign --key "$WORK/cosign.key" --yes --allow-insecure-registry \
            --use-signing-config=false --new-bundle-format=false --tlog-upload=false \
            "$1/cookbook/$2@$_d" >/dev/null 2>&1 || fail "signing recipe $2"
        "$COSIGN" verify --key "$WORK/cosign.pub" --allow-insecure-registry --insecure-ignore-tlog=true \
            "$1/cookbook/$2@$_d" >/dev/null 2>&1 || fail "recipe $2 does not verify under the zone key: the fixture is wrong"
    fi
    printf '%s' "$_d"
}

# m7_image_recipe <name> <ref> <digest>: a one-image recipe document.
m7_image_recipe() {
    printf 'apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Recipe
metadata:
  name: %s
  version: 1.0.0
spec:
  ingredients:
    - name: app
      kind: ContainerImage
      ref: %s
      version: "1.0"
      digest: %s
' "$1" "$2" "$3"
}

# m7_retriever <file> <cookbook> <recipe…>: a Retriever pinning each recipe
# at 1.0.0, in the order given.
m7_retriever() {
    _f="$1"; _cb="$2"; shift 2
    {
        printf 'apiVersion: recipe.tobbyfetch.dev/v1alpha1\nkind: Retriever\nmetadata:\n  name: zone-m7\nspec:\n'
        printf '  cookbook: %s\n  recipes:\n' "$_cb"
        for _r in "$@"; do printf '    - name: %s\n      version: "1.0.0"\n' "$_r"; done
    } >"$_f"
}

# -- the instance under test -------------------------------------------------------
# m7_start <instance> <base url> [<curl option>…]: tobby serve with the
# default configuration file, appending to /var/log/tobby.log, waited for
# FROM THE HOST through the same URL every later check uses.
m7_start() {
    _inst="$1"; _base="$2"; shift 2
    inc exec "$_inst" -- sh -c 'nohup tobby serve >>/var/log/tobby.log 2>&1 &'
    _i=0
    until curl -fsS -o /dev/null "$@" "$_base/readyz" 2>/dev/null; do
        _i=$((_i + 1))
        [ "$_i" -gt 90 ] && fail "$_inst: $_base/readyz never answered: $(inc exec "$_inst" -- tail -5 /var/log/tobby.log)"
        sleep 1
    done
}

# [t]obby, not tobby: pkill -f matches full command lines, and the command
# line of the shell running this very pkill CONTAINS the pattern (m5, B-026).
m7_stop() {
    inc exec "$1" -- sh -c 'pkill -f "[t]obby serve" || true'
    _i=0
    while inc exec "$1" -- sh -c 'pgrep -f "[t]obby serve" >/dev/null'; do
        _i=$((_i + 1))
        [ "$_i" -gt 30 ] && fail "$1: tobby serve did not stop"
        sleep 1
    done
}

# m7_admin <instance> <user> <password>: the first local account (admin by
# construction), created against the configuration file already written.
m7_admin() {
    inc exec "$1" -- sh -c "printf '%s\n' '$3' | tobby user add $2 --password-stdin" >/dev/null 2>&1 ||
        fail "creating the local administrator on $1"
}

# m7_wait_task <api base> <task id> <out file> [<curl option>…]: polls the
# task through the API until it reaches a terminal state; the document is
# left in <out file> and its status printed.
m7_wait_task() {
    _base="$1"; _id="$2"; _out="$3"; shift 3
    _i=0
    while :; do
        curl -fsS "$@" "$_base/tasks/$_id" >"$_out" 2>/dev/null || true
        _st=$(jq -r '.task.status // empty' "$_out" 2>/dev/null)
        case "$_st" in
            done | failed | skipped) printf '%s' "$_st"; return 0 ;;
        esac
        _i=$((_i + 1))
        [ "$_i" -gt 600 ] && fail "task $_id never settled: $(cat "$_out")"
        sleep 1
    done
}

# m7_metric <metrics text> <series> -> value (exact series match).
m7_metric() { printf '%s\n' "$1" | awk -v n="$2" '$1 == n {print $2; exit}'; }

# m7_row <html file> <repository> -> the content-list row of that repository
# (the <tr> holding its link), on one line.
m7_row() {
    tr '\n' ' ' <"$1" | awk -v RS='<tr>' -v r="href=\"/content/$2\"" 'index($0, r) {print; exit}'
}

# m7_csrf <html file> -> the anti-forgery token embedded in a page of the
# session (every page carries it in the layout's forms).
m7_csrf() { grep -o 'name="csrf" value="[^"]*"' "$1" | head -1 | sed 's/.*value="\([^"]*\)"/\1/'; }

# -- the pinned scanner and its database (tobby-system channel) ---------------------
# m7_scanner_fixtures <registry>: the pinned Trivy release — from
# CRUCIBLE_TRIVY_ARCHIVE when set, otherwise fetched once — checked against
# the release checksum and the executable digest Tobby pins
# (internal/scan/trivy/pins.go), and the database the connected zone
# publishes, both pushed to <registry> exactly as m6 a-scanning does. Sets
# TRIVY_VERSION, TRIVY_DIG (the FileSet) and DB_DIG (the OCIArtifact).
m7_scanner_fixtures() {
    command -v oras >/dev/null 2>&1 || fail "oras not found: the database is fetched with it"
    TRIVY_VERSION="0.76.2"
    case "$ARCH" in
        amd64) _ta=64bit
               _tsha=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf
               _bsha=6a972c69606c7b3ea4c6bdcc6fc9332ea9ad38381624310c1e85cc77345909f3 ;;
        arm64) _ta=ARM64
               _tsha=ab9d51f86e6a5fb936f605faff6b593ac51bc378fff4d3e8b9d58358b9c367c3
               _bsha=7844c3851b28470b6d7f0975fa0b3262c5b2272c9a54a91f522801bb8926f51f ;;
        *) fail "unsupported crucible architecture $ARCH" ;;
    esac
    _arch="${CRUCIBLE_TRIVY_ARCHIVE:-$WORK/trivy.tgz}"
    if [ ! -f "$_arch" ]; then
        curl -fsSL -o "$_arch" \
            "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${_ta}.tar.gz" ||
            fail "fetching the pinned Trivy release"
    fi
    echo "${_tsha}  ${_arch}" | sha256sum -c - >/dev/null 2>&1 || fail "the Trivy archive does not match its release checksum"
    mkdir -p "$WORK/trivy-fileset"
    tar -xzf "$_arch" -C "$WORK/trivy-fileset" trivy || fail "extracting the Trivy executable"
    echo "${_bsha}  ${WORK}/trivy-fileset/trivy" | sha256sum -c - >/dev/null 2>&1 ||
        fail "the Trivy executable is not the one Tobby pins"
    ( cd "$WORK" && { oras pull mirror.gcr.io/aquasec/trivy-db:2 >/dev/null 2>&1 ||
        oras pull ghcr.io/aquasecurity/trivy-db:2 >/dev/null 2>&1; } ) ||
        fail "fetching the vulnerability database the connected zone publishes"
    test -s "$WORK/db.tar.gz" || fail "the database artifact carries no db.tar.gz layer"
    DB_DIG=$(m7_seed push-trivy-db "$1/ghcr.io/aquasecurity/trivy-db:2" "$WORK/db.tar.gz") || fail "publishing the database"
    TRIVY_DIG=$(m7_seed push-fileset "$1/ghcr.io/aquasecurity/trivy:$TRIVY_VERSION" "$WORK/trivy-fileset") ||
        fail "publishing the scanner FileSet"
    check "scanner fixtures: Trivy $TRIVY_VERSION checked twice (FileSet $TRIVY_DIG), database $DB_DIG"
}

# m7_system_recipe: the tobby-system recipe document carrying them.
m7_system_recipe() {
    printf 'apiVersion: recipe.tobbyfetch.dev/v1alpha1
kind: Recipe
metadata:
  name: tobby-system
  version: 1.0.0
spec:
  ingredients:
    - name: trivy
      kind: FileSet
      ref: ghcr.io/aquasecurity/trivy
      version: "%s"
      digest: %s
    - name: trivy-db
      kind: OCIArtifact
      ref: ghcr.io/aquasecurity/trivy-db
      version: "2"
      digest: %s
      artifactType: application/vnd.aquasec.trivy.config.v1+json
' "$TRIVY_VERSION" "$TRIVY_DIG" "$DB_DIG"
}
