#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Helpers shared by the milestone-6 "c-" scenarios (track C: on-demand
# store integrity R-31, reduced-assurance marker R-22, trust-root
# compartments). Sourced, never run: the scenarios themselves are
# c-integrity.sh, c-trust-scopes.sh and c-assurance.sh.
#
# The rule they all follow, learned the expensive way in m5: a scenario that
# MANIPULATES something verifies its own manipulation before it asks the
# product for a verdict. A truncated blob that was never truncated, a key
# that was never swapped, a rewound date that matched no pattern — each reads
# exactly like the guard under test failing, and costs a node run to tell
# apart. So every mise en scène below ends with a check that the world is
# what the scenario says it is.

# -- tooling ------------------------------------------------------------------
c_tooling() {
    command -v jq >/dev/null 2>&1 || fail "jq not found"
    command -v curl >/dev/null 2>&1 || fail "curl not found"
    COSIGN="${COSIGN:-cosign}"
    if ! command -v "$COSIGN" >/dev/null 2>&1; then
        GOBIN=/usr/local/bin go install github.com/sigstore/cosign/v2/cmd/cosign@latest >/dev/null 2>&1 ||
            fail "installing cosign"
        COSIGN=/usr/local/bin/cosign
    fi
    check "tooling available (jq, curl, cosign)"
}

# c_build <workdir>: the binary under test, for the crucible host's arch.
c_build() {
    ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
    ( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
        go build -trimpath -o "$1/tobby" ./cmd/tobby ) || fail "building tobby"
    check "built tobby for linux/$ARCH"
}

# c_launch <instance> <profile>: a fresh container carrying the binary.
c_launch() {
    inc delete --force "$1" 2>/dev/null || true
    inc launch "${CRUCIBLE_IMAGE:-images:alpine/3.24}" "$1" --profile "$2" \
        -c security.syscalls.intercept.mount=true \
        -c security.syscalls.intercept.mount.allowed=ext4 >/dev/null
    inc file push "$WORK/tobby" "$1/usr/bin/tobby" >/dev/null
    inc exec "$1" -- chmod +x /usr/bin/tobby
}

# -- signing ------------------------------------------------------------------
# c_keypair <name>: $WORK/<name>.key and $WORK/<name>.pub.
c_keypair() {
    ( cd "$WORK" && mkdir -p "k-$1" && cd "k-$1" && COSIGN_PASSWORD= "$COSIGN" generate-key-pair >/dev/null 2>&1 ) ||
        fail "generating key pair $1"
    cp "$WORK/k-$1/cosign.key" "$WORK/$1.key"
    cp "$WORK/k-$1/cosign.pub" "$WORK/$1.pub"
}

# c_sign <keyname> <ref@digest>: a cosign attached signature (RECIPE-SPEC
# §12.2), the layout Tobby reads and re-verifies.
c_sign() {
    COSIGN_PASSWORD= "$COSIGN" sign --key "$WORK/$1.key" --yes --allow-insecure-registry \
        --use-signing-config=false --new-bundle-format=false --tlog-upload=false "$2" >/dev/null 2>&1
}

# c_verifies <keyname> <ref@digest>: whether cosign — a tool nobody here
# wrote — accepts the signature under that key. This is how a scenario
# proves a manipulation of the KEYS: "valid for B, not for A" is a fact
# about the signature, checked independently of the product.
c_verifies() {
    "$COSIGN" verify --key "$WORK/$1.pub" --allow-insecure-registry --insecure-ignore-tlog=true \
        "$2" >/dev/null 2>&1
}

# c_recipe <file> <name> <ingredient> <ref> <digest>
c_recipe() {
    cat >"$1" <<EOR
apiVersion: recipe.tobby.dev/v1alpha1
kind: Recipe
metadata:
  name: $2
  version: 1.0.0
spec:
  ingredients:
    - name: $3
      kind: ContainerImage
      ref: $4
      version: 1.0.0
      digest: $5
EOR
}

# c_publish <registry> <cookbook-name> <ingredient> <ref> <digest> [<keyname>]:
# publishes one recipe and signs it when a key is named. Prints the digest.
c_publish() {
    c_recipe "$WORK/recipe-$2.yaml" "$2" "$3" "$4" "$5"
    _d=$( cd "$ROOT" && go run ./test/topology/seed push-recipe \
        "$1/cookbook/$2:1.0.0" "$WORK/recipe-$2.yaml" ) || fail "publishing recipe $2"
    if [ -n "${6:-}" ]; then
        c_sign "$6" "$1/cookbook/$2@$_d" || fail "signing recipe $2 with $6"
    fi
    printf '%s' "$_d"
}

# -- the instance under test ---------------------------------------------------
# c_config <instance> <mode> <store root> <source ip> <trust yaml>: writes
# /etc/tobby/config.yaml. <trust yaml> is the body under "trust:" and is
# passed through verbatim — it is the thing these scenarios vary.
c_config() {
    inc exec "$1" -- sh -c "mkdir -p /etc/tobby && cat >/etc/tobby/config.yaml <<'EOF'
mode: $2
zone: zone-c
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
    - $4:8080
retriever:
  source: /etc/tobby/retriever.yaml
trust:
$5
EOF"
}

c_start() { inc exec "$1" -- sh -c 'nohup tobby serve >/var/log/tobby.log 2>&1 &'; wait_ready "$1" http://127.0.0.1:8080/readyz; }

# [t]obby, not tobby: pkill -f matches full command lines, and the command
# line of the shell running this very pkill CONTAINS the pattern (m5, B-026).
c_stop() { inc exec "$1" -- sh -c 'pkill -f "[t]obby serve" || true; sleep 2'; }

# c_fingerprint <instance> <dir>: every file's name and content, so "the
# store is byte-identical" is observed on a real filesystem, not assumed.
c_fingerprint() {
    inc exec "$1" -- sh -c "cd '$2' && find . -type f -exec sha256sum {} + 2>/dev/null | sort | sha256sum"
}

# c_blob_file <instance> <store root> <digest>: the data file of one blob.
c_blob_file() {
    _hex=$(printf '%s' "$3" | cut -d: -f2)
    inc exec "$1" -- sh -c "find '$2/docker/registry/v2/blobs/sha256' -path '*${_hex}*' -name data | head -1"
}

# c_api <instance-ip> <method> <path> [<body>]: from the crucible host, which
# routes to the bridge by construction — busybox wget speaks neither PATCH
# nor an empty POST, and a probe that never leaves the machine passes
# whatever the server does.
c_api() {
    if [ -n "${4:-}" ]; then
        curl -s -X "$2" -H 'Content-Type: application/json' -d "$4" "http://$1:8080$3"
    else
        curl -s -X "$2" "http://$1:8080$3"
    fi
}

# c_serve_source <instance>: the source registry, a passthrough instance
# with authentication disabled (the documented FR-075 override — a fixture
# registry, never a deployment).
c_serve_source() {
    inc exec "$1" -- sh -c '
        nohup env TOBBY_MODE=passthrough TOBBY_STORAGE_ROOT=/var/lib/tobby \
            TOBBY_STATE_ROOT=/var/lib/tobby-state TOBBY_AUTH_DISABLED=true \
            TOBBY_SERVER_ADDR=:8080 tobby serve >/var/log/tobby.log 2>&1 &
    '
    wait_ready "$1" http://127.0.0.1:8080/healthz
}

# c_seed_image <host:port/repo:tag>: a multi-platform image pushed by a
# standard client (never a protocol mock). Prints the index digest.
c_seed_image() {
    ( cd "$ROOT" && go run ./test/topology/seed push "$1" ) || fail "seeding image $1"
}

# c_wait_verification <instance-ip> <file>: waits, through the API a script
# would use, for the verification to finish, and leaves the document in
# <file>.
c_wait_verification() {
    _n=0
    while :; do
        c_api "$1" GET /api/v1/store/verification >"$2"
        if jq -e '.verification != null and (.verification.finished_at // "") != ""' "$2" >/dev/null 2>&1; then
            return 0
        fi
        _n=$((_n + 1))
        [ "$_n" -gt 120 ] && fail "the store verification never finished: $(cat "$2")"
        sleep 1
    done
}
