#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Helpers of the crown scenario (crucible/scenarios/m7/crown.sh). Sourced,
# never run. Expects lib.sh to be sourced first, and $ROOT, $WORK, $COSIGN
# to be set by the caller.
#
# The rule m5 paid for applies to every helper below: a step that MANIPULATES
# or STAGES something checks its own work before the scenario asks the
# product for a verdict — a capture that never started, a package that was
# never downloaded, a digest that was never resolved all read exactly like
# the product failing.

# shellcheck disable=SC2034 # constants read by the sourcing scenario
CROWN_KUBECTL=/var/lib/rancher/rke2/bin/kubectl
CROWN_KUBECONFIG=/etc/rancher/rke2/rke2.yaml
CROWN_CRICTL=/var/lib/rancher/rke2/bin/crictl
CROWN_CRICTL_CONFIG=/var/lib/rancher/rke2/agent/etc/crictl.yaml

# The OCI and Docker manifest media types, so a HEAD returns the digest of
# what is actually stored (an index for a multi-platform image) rather than
# a registry's down-conversion.
CROWN_ACCEPT='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'

# -- documents ------------------------------------------------------------------
# crown_yj <json|yaml> <file|->: YAML/JSON conversion (crown/yamljson).
crown_yj() { "$WORK/yamljson" -to "$1" "$2"; }

# crown_relocate <host/repo>: the ADR-0013 destination path, spelled out here
# rather than asked of the product, so a change to the convention fails the
# scenario instead of silently agreeing with itself (m5's rule).
crown_relocate() {
    _h=${1%%/*}
    _r=${1#*/}
    _h=$(printf '%s' "$_h" | tr '[:upper:]' '[:lower:]')
    case "$_h" in index.docker.io | registry-1.docker.io) _h=docker.io ;; esac
    printf '%s/%s' "$(printf '%s' "$_h" | tr ':' '_')" "$_r"
}

# -- instances ------------------------------------------------------------------
# crown_container <instance> <profile>: an Alpine system container carrying
# the binary under test (the m5 node shape).
crown_container() {
    inc launch "$CTR_IMAGE" "$1" --profile "$2" \
        -c security.syscalls.intercept.mount=true \
        -c security.syscalls.intercept.mount.allowed=ext4 >/dev/null ||
        fail "launching container $1"
    inc file push "$WORK/tobby" "$1/usr/bin/tobby" >/dev/null
    inc exec "$1" -- chmod +x /usr/bin/tobby
}

# crown_vm <instance> <profile> <cpu> <memory> <root size>: a KVM guest of the
# bare-OS image — the crown node and its connected twin are VMs because
# RKE2 needs its own kernel (modules, SELinux, cgroups), which a system
# container cannot give it.
crown_vm() {
    inc launch "$NODE_IMAGE" "$1" --vm --profile "$2" \
        -c limits.cpu="$3" -c limits.memory="$4" \
        -d root,size="$5" >/dev/null || fail "launching VM $1"
    crown_wait_agent "$1"
}

# crown_wait_agent <instance> [timeout s]: a VM is usable once its
# incus-agent answers — which, after an SELinux relabel, takes two boots.
crown_wait_agent() {
    _n=0
    _max=${2:-300}
    until inc exec "$1" -- true >/dev/null 2>&1; do
        _n=$((_n + 5))
        [ "$_n" -gt "$_max" ] && fail "the incus agent of $1 never answered (${_max}s)"
        sleep 5
    done
}

# -- upstream resolution (cooking) -------------------------------------------------
# crown_resolve_digest <host/repo:tag>: the top-level manifest digest, by a
# HEAD request — Docker Hub does not count a HEAD against the pull rate
# limit, a GET it does (registry.rancher.com has no such limit). The bearer-token dance is the standard one, read
# from the registry's own WWW-Authenticate challenge.
crown_resolve_digest() {
    _ref=$1
    _host=${_ref%%/*}
    _rest=${_ref#*/}
    _repo=${_rest%:*}
    _tag=${_rest##*:}
    _api=$_host
    [ "$_host" = docker.io ] && _api=registry-1.docker.io
    _url="https://$_api/v2/$_repo/manifests/$_tag"
    _hdr="$WORK/resolve.hdr"
    curl -sS -I -o /dev/null -D "$_hdr" -H "Accept: $CROWN_ACCEPT" "$_url" || return 1
    if grep -qi '^www-authenticate: *bearer' "$_hdr"; then
        _chal=$(grep -i '^www-authenticate:' "$_hdr" | tr -d '\r')
        _realm=$(printf '%s' "$_chal" | sed -n 's/.*realm="\([^"]*\)".*/\1/p')
        _svc=$(printf '%s' "$_chal" | sed -n 's/.*service="\([^"]*\)".*/\1/p')
        set -- -sS -G "$_realm" --data-urlencode "service=$_svc" \
            --data-urlencode "scope=repository:$_repo:pull"
        # Docker Hub credentials, when supplied, go to Docker Hub only.
        if [ -n "${CROWN_HUB_BASIC:-}" ] && [ "$_host" = docker.io ]; then
            set -- "$@" -H "Authorization: Basic $CROWN_HUB_BASIC"
        fi
        _tok=$(curl "$@" | jq -r '.token // .access_token // empty')
        [ -n "$_tok" ] || return 1
        curl -sS -I -o /dev/null -D "$_hdr" -H "Accept: $CROWN_ACCEPT" \
            -H "Authorization: Bearer $_tok" "$_url" || return 1
    fi
    grep -qE '^HTTP/[0-9.]+ 200' "$_hdr" || return 1
    grep -i '^docker-content-digest:' "$_hdr" | awk '{print $2}' | tr -d '\r'
}

# crown_cook <draft.yaml> <digests.json> <cooked.yaml>: the cooked form of a
# draft — every ingredient's digest recorded, nothing else changed. The
# digest map is {"<ingredient name>": "sha256:…"}; an ingredient missing
# from it fails the cooking rather than publishing a draft (§8).
crown_cook() {
    crown_yj json "$1" >"$WORK/cook.json" || fail "cooking $1: not a document"
    jq --slurpfile d "$2" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
        .metadata.annotations = ((.metadata.annotations // {}) + {"recipe.tobby.dev/cooked-at": $at})
        | .spec.ingredients |= map(.digest = ($d[0][.name] // error("no digest for ingredient " + .name)))
    ' "$WORK/cook.json" >"$WORK/cooked.json" || fail "cooking $1: an ingredient has no resolved digest"
    crown_yj yaml "$WORK/cooked.json" >"$3"
}

# crown_publish <cooked.yaml> <name> <version>: publication through the
# product's own validating push (`tobby recipe push`: cooked profile,
# name/version against the reference, immutability), then a cosign
# signature by the crucible's test key. Prints the recipe digest.
crown_publish() {
    _d=$("$WORK/tobby" recipe push "$1" "$S/cookbook/$2:$3" --config "$WORK/publish.yaml" 2>"$WORK/publish.err") ||
        fail "publishing recipe $2:$3 was refused: $(tr '\n' ' ' <"$WORK/publish.err")"
    COSIGN_PASSWORD='' "$COSIGN" sign --key "$WORK/cosign.key" --yes --allow-insecure-registry \
        --use-signing-config=false --new-bundle-format=false --tlog-upload=false \
        "$S/cookbook/$2@$_d" >/dev/null 2>&1 || fail "signing recipe $2:$3"
    # Staging: the signature verifies under the key the zone will trust.
    "$COSIGN" verify --key "$WORK/cosign.pub" --allow-insecure-registry --insecure-ignore-tlog=true \
        "$S/cookbook/$2@$_d" >/dev/null 2>&1 || fail "staging: recipe $2 does not verify under the test key"
    printf '%s' "$_d"
}

# -- registries -----------------------------------------------------------------
# crown_manifest_digest <host:port> <repo> <tag|digest>: what a plain-HTTP
# registry holds under that name (empty when nothing), read from the host.
crown_manifest_digest() {
    curl -sS -I -H "Accept: $CROWN_ACCEPT" "http://$1/v2/$2/manifests/$3" 2>/dev/null |
        awk 'tolower($1)=="docker-content-digest:" {print $2}' | tr -d '\r'
}

# crown_api <ip:port> <method> <path> [<json body>]: the instance's REST
# API from the crucible host, which routes to both bridges by construction.
crown_api() {
    if [ -n "${4:-}" ]; then
        curl -sS -X "$2" -H 'Content-Type: application/json' -d "$4" "http://$1$3"
    else
        curl -sS -X "$2" "http://$1$3"
    fi
}

# crown_wait_task <ip:port> <task id> <out file> <timeout s>: polls a task to
# a terminal state; the document is left in <out file>.
crown_wait_task() {
    _n=0
    while :; do
        crown_api "$1" GET "/api/v1/tasks/$2" >"$3" 2>/dev/null || true
        case "$(jq -r '.task.status // empty' "$3" 2>/dev/null)" in
            done | failed) return 0 ;;
        esac
        _n=$((_n + 5))
        [ "$_n" -gt "$4" ] && fail "task $2 never finished (${4}s): $(head -c 400 "$3")"
        sleep 5
    done
}

# -- packet captures ---------------------------------------------------------------
# The two witnesses this scenario needs, neither of them the product:
#   - leak captures on every way OUT of the isolated zone (the connected
#     bridge, the host's uplink): any packet sourced in the zone there is a
#     leak, whatever the ACL claims;
#   - the node capture on the node's own host-side interface: every
#     request it sent — the plain-HTTP registry and /files/ requests that
#     say where each image and package came from, and every attempt to
#     reach outside, refused or not.

# crown_capture <label> <interface> <snaplen> <filter>
crown_capture() {
    _f="$WORK/capture-$1.pcap"
    tcpdump -i "$2" -nn -U -s "$3" -w "$_f" "$4" >"$WORK/capture-$1.log" 2>&1 &
    echo "$!" >>"$WORK/captures.pids"
    sleep 2
    # Staging: the witness is actually watching. A capture that died on a
    # bad interface name records nothing, and nothing reads like no leak.
    kill -0 "$!" 2>/dev/null || fail "staging: capture $1 on $2 did not start: $(cat "$WORK/capture-$1.log")"
}

crown_capture_stop_all() {
    [ -f "$WORK/captures.pids" ] || return 0
    while read -r _p; do kill "$_p" 2>/dev/null || true; done <"$WORK/captures.pids"
    sleep 2
    rm -f "$WORK/captures.pids"
}

# crown_capture_read <label> [<read filter>]: the capture as text, payload
# included (registry and /files/ requests are plain HTTP).
crown_capture_read() {
    tcpdump -nn -A -r "$WORK/capture-$1.pcap" ${2:+"$2"} 2>/dev/null
}

# crown_capture_count <label> [<read filter>]: packets in a capture.
crown_capture_count() {
    tcpdump -nn -r "$WORK/capture-$1.pcap" ${2:+"$2"} 2>/dev/null | wc -l | tr -d ' '
}

# -- the cluster ----------------------------------------------------------------
crown_kubectl() { inc exec "$NODE" -- env KUBECONFIG="$CROWN_KUBECONFIG" "$CROWN_KUBECTL" "$@"; }
crown_crictl() { inc exec "$NODE" -- env CRI_CONFIG_FILE="$CROWN_CRICTL_CONFIG" "$CROWN_CRICTL" "$@"; }

# ==============================================================================
# CONSUMPTION AIDS (feature 7.1, FR-065) — THE ONLY CALL POINT.
# ==============================================================================
# Feature 7.1 is implemented in parallel with this scenario. The command
# names, flags and output schemas below are HYPOTHESES to reconcile with it;
# everything else in the scenario consumes the two files these functions
# write and nothing else, so reconciling means editing these two functions.
#
# Hypothesis (CLI, in-process on the destination-side instance, like
# `tobby media`):
#
#   tobby consumption registries-yaml \
#       --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
#       --destination 10.180.20.x:5000
#     → stdout: ONE registries.yaml document for all the named recipes:
#
#       mirrors:
#         registry.rancher.com:               # the 16 RKE2 images
#           endpoint:
#             - "http://10.180.20.x:5000"     # http: the destination is
#           rewrite:                          # in registries.insecure
#             "^(.*)$": "registry.rancher.com/$1"
#         docker.io:                          # busybox, the nominal-path pod
#           endpoint:
#             - "http://10.180.20.x:5000"
#           rewrite:
#             "^(.*)$": "docker.io/$1"
#
#     one mirror per canonical source host of the recipes' ContainerImage
#     (and, optionally, FileSet) ingredients; the rewrite prefixes the
#     relocated path — "<base-prefix>/<canonical host>[_<port>]/$1" — so
#     that docker.io/library/busybox resolves at
#     <destination>/docker.io/library/busybox (ADR-0013). A `configs:`
#     section is only emitted for TLS material, never for credentials.
#
#   tobby consumption table \
#       --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
#       --destination 10.180.20.x:5000 --output json
#     → {"destination": "10.180.20.x:5000",
#        "entries": [{"recipe": "rke2", "recipeVersion": "1.36.5",
#                     "ingredient": "rke2-runtime", "kind": "ContainerImage",
#                     "source": "docker.io/rancher/rke2-runtime:v1.36.5-rke2r1",
#                     "destination": "10.180.20.x:5000/docker.io/rancher/rke2-runtime:v1.36.5-rke2r1",
#                     "digest": "sha256:…"}, …]}
#
# Debug mode — CRUCIBLE_M7_DEBUG_AIDS=handmade — writes both files from the
# scenario's own knowledge instead, so the rest of the crown can be put to
# the test before 7.1 lands. Such a run is marked DEBUG on every line that
# depends on it and ends with a FAIL: a hand-made snippet proves nothing
# about the generated one, which is the whole point of the criterion.

# crown_generate_registries_yaml <out file>
crown_generate_registries_yaml() {
    if [ "${CRUCIBLE_M7_DEBUG_AIDS:-}" = handmade ]; then
        crown_debug_registries_yaml "$1"
        return
    fi
    inc exec "$GATE" -- tobby consumption registries-yaml \
        --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
        --destination "$DEST" >"$1" 2>"$WORK/aid-registries.err" ||
        fail "FR-065: generating the registries.yaml snippet failed: $(tr '\n' ' ' <"$WORK/aid-registries.err")"
}

# crown_generate_mapping_table <out file>
crown_generate_mapping_table() {
    if [ "${CRUCIBLE_M7_DEBUG_AIDS:-}" = handmade ]; then
        crown_debug_mapping_table "$1"
        return
    fi
    inc exec "$GATE" -- tobby consumption table \
        --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
        --destination "$DEST" --output json >"$1" 2>"$WORK/aid-table.err" ||
        fail "FR-065: generating the mapping table failed: $(tr '\n' ' ' <"$WORK/aid-table.err")"
}

# -- debug-only stand-ins (never on the acceptance path) ------------------------
crown_debug_registries_yaml() {
    cat >"$1" <<EOF
# DEBUG: written by the crown scenario, NOT generated by Tobby.
mirrors:
  registry.rancher.com:
    endpoint:
      - "http://$DEST"
    rewrite:
      "^(.*)\$": "registry.rancher.com/\$1"
  docker.io:
    endpoint:
      - "http://$DEST"
    rewrite:
      "^(.*)\$": "docker.io/\$1"
EOF
}

crown_debug_mapping_table() {
    jq -n --arg dest "$DEST" --slurpfile r "$WORK/cooked-all.json" '
        {destination: $dest, entries: [
            $r[0][] as $rec | $rec.spec.ingredients[] |
            {recipe: $rec.metadata.name, recipeVersion: $rec.metadata.version,
             ingredient: .name, kind: .kind,
             source: (.ref + ":" + .version),
             destination: ($dest + "/" + (.ref | sub("^(index|registry-1)\\.docker\\.io/"; "docker.io/")) + ":" + .version),
             digest: .digest}]}' >"$1"
}
