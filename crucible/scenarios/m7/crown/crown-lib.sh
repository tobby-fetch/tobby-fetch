#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Helpers of the crown scenario (crucible/scenarios/m7/z-crown-rke2.sh). Sourced,
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
#
# Created, then started: the images: server's cloud variants carry
# requirements.cdrom_agent=true — Incus refuses to create such a VM until
# it has the agent:config disk (the incus-agent and its credentials come
# from that CD-ROM, not from the image).
crown_vm() {
    # The device comes in the instance config on stdin: -d only overrides
    # a device the profile already has.
    printf 'devices:\n  agent:\n    type: disk\n    source: agent:config\n' |
        inc init "$NODE_IMAGE" "$1" --vm --profile "$2" \
            -c limits.cpu="$3" -c limits.memory="$4" \
            -d root,size="$5" >/dev/null ||
        fail "launching VM $1"
    inc start "$1" >/dev/null || { incus info --show-log "$1" --project tobby-crucible >&2; free -g >&2; incus list --project tobby-crucible -c ns >&2; fail "starting VM $1"; }
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
        .metadata.annotations = ((.metadata.annotations // {}) + {"recipe.tobbyfetch.dev/cooked-at": $at})
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
# The aids are generated by the destination-side instance's own CLI, which
# reads the recipe graph of the store directory (no running instance
# needed): `tobby consumption registries-yaml` and `tobby consumption table
# --output json`, each with `--recipe name@version` (repeatable) and
# `--destination host[:port]`. What the scenario relies on, as the
# implementation settles it (internal/consume):
#
#   - endpoint: the --destination host, "http://" because the gate's
#     configuration lists it in registries.insecure ("https://" otherwise);
#   - one mirror per source host of the ContainerImage ingredients only —
#     here registry.rancher.com (RKE2) and docker.io (busybox); the FileSet
#     host artifacts.crucible.test gets none;
#   - rewrite "^(.*)$" → "[<base>/]<host>[_port]/$1", base being
#     destination.basePath (the gate configures a destination, without a
#     basePath) — so "<host>/$1" here;
#   - the store's recorded paths must carry the current storage.basePrefix
#     (TBY-CFG-001 otherwise): the connected instance wrote the medium with
#     none, and the gate reads it with none;
#   - `configs:` only with consumption.caFile (unset here), never an auth;
#   - table: {"destination", "entries": [{recipe, recipeVersion,
#     ingredient, kind, source, destination, digest}]}, FileSets included,
#     references by tag ("@digest" only for an untagged ingredient).
#
# Debug mode — CRUCIBLE_M7_DEBUG_AIDS=handmade — writes both files from the
# scenario's own knowledge instead, to put the rest of the crown to the test
# without the generator (mise au point only). Such a run is marked DEBUG on
# every line that depends on it and ends with a FAIL: a hand-made snippet
# proves nothing about the generated one, which is the whole point of the
# criterion.

# crown_generate_registries_yaml <out file>
crown_generate_registries_yaml() {
    if [ "${CRUCIBLE_M7_DEBUG_AIDS:-}" = handmade ]; then
        crown_debug_registries_yaml "$1"
        return
    fi
    inc exec "$GATE" -- tobby consumption registries-yaml --config /etc/tobby/config.yaml \
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
    inc exec "$GATE" -- tobby consumption table --config /etc/tobby/config.yaml \
        --recipe rke2@1.36.5 --recipe crown-app@1.0.0 \
        --destination "$DEST" --output json >"$1" 2>"$WORK/aid-table.err" ||
        fail "FR-065: generating the mapping table failed: $(tr '\n' ' ' <"$WORK/aid-table.err")"
}

# -- validation of the aids -------------------------------------------------------
# The expectation is computed from the cooked recipes and ADR-0013
# (crown_relocate), never read back from the product. CROWN_BASE is the
# relocation base the gate is configured with (none in this scenario).

# crown_check_registries_yaml <registries.yaml> <cooked recipes json> <dest>:
# sets CROWN_MIRRORS (the mirrored hosts, for the report line).
crown_check_registries_yaml() {
    crown_yj json "$1" >"$WORK/registries.json" ||
        fail "FR-065: the generated registries.yaml is not a YAML document"
    _want=$(jq -r '[.[].spec.ingredients[] | select(.kind == "ContainerImage") | .ref | split("/")[0]
        | ascii_downcase | sub("^(index|registry-1)\\.docker\\.io$"; "docker.io")] | unique | join(" ")' "$2")
    _got=$(jq -r '.mirrors | keys | join(" ")' "$WORK/registries.json")
    [ "$_got" = "$_want" ] ||
        fail "FR-065: the snippet mirrors [$_got], the recipes' image hosts are [$_want]"
    for _h in $_want; do
        _rw="${CROWN_BASE:+$CROWN_BASE/}$(printf '%s' "$_h" | tr ':' '_')/\$1"
        jq -e --arg h "$_h" --arg e "http://$3" '.mirrors[$h].endpoint == [$e]' "$WORK/registries.json" >/dev/null ||
            fail "FR-065: the $_h mirror does not point at http://$3 alone: $(jq -c --arg h "$_h" '.mirrors[$h].endpoint' "$WORK/registries.json")"
        jq -e --arg h "$_h" --arg r "$_rw" '.mirrors[$h].rewrite == {"^(.*)$": $r}' "$WORK/registries.json" >/dev/null ||
            fail "FR-065: the $_h rewrite is not ^(.*)\$ → $_rw (ADR-0013): $(jq -c --arg h "$_h" '.mirrors[$h].rewrite' "$WORK/registries.json")"
    done
    # No credential, ever, in a snippet meant to be copied around.
    jq -e '[.configs // {} | .[] | .auth // empty] | length == 0' "$WORK/registries.json" >/dev/null ||
        fail "FR-065: the snippet carries credentials"
    CROWN_MIRRORS=$(printf '%s' "$_want" | sed 's/ / and /g')
}

# crown_check_mapping_table <table.json> <cooked recipes json> <dest>: one
# entry per ingredient (FileSets included), source as the recipe writes it,
# destination at the relocated path, digest the pinned one — and the zone
# registry answering that digest there. Sets CROWN_TABLE_ROWS.
crown_check_mapping_table() {
    jq -e --arg d "$3" '.destination == $d' "$1" >/dev/null ||
        fail "FR-065: the table is for $(jq -r '.destination' "$1"), not $3"
    CROWN_TABLE_ROWS=$(jq '[.[].spec.ingredients[]] | length' "$2")
    jq -e --argjson n "$CROWN_TABLE_ROWS" '.entries | length == $n' "$1" >/dev/null ||
        fail "FR-065: the table has $(jq '.entries | length' "$1") entries for $CROWN_TABLE_ROWS ingredients"
    jq -r '.[] | .metadata as $m | .spec.ingredients[] | [$m.name, $m.version, .name, .kind, .ref, .version, .digest] | @tsv' "$2" >"$WORK/table-want.tsv"
    while IFS="$(printf '\t')" read -r _rn _rv _in _k _ref _ver _dg; do
        _dst="$3/${CROWN_BASE:+$CROWN_BASE/}$(crown_relocate "$_ref"):$_ver"
        jq -e --arg rn "$_rn" --arg rv "$_rv" --arg i "$_in" --arg k "$_k" --arg s "$_ref:$_ver" --arg d "$_dst" --arg g "$_dg" \
            '[.entries[] | select(.recipe == $rn and .recipeVersion == $rv and .ingredient == $i and .kind == $k
                and .source == $s and .destination == $d and .digest == $g)] | length == 1' "$1" >/dev/null ||
            fail "FR-065: the table has no exact entry $_rn@$_rv/$_in: $_ref:$_ver → $_dst ($_dg)"
    done <"$WORK/table-want.tsv"
    jq -r '.entries[] | [.destination, .digest] | @tsv' "$1" >"$WORK/table-got.tsv"
    while IFS="$(printf '\t')" read -r _dst _dg; do
        _rest=${_dst#"$3"/}
        _got=$(crown_manifest_digest "$3" "${_rest%:*}" "${_rest##*:}")
        [ "$_got" = "$_dg" ] || fail "FR-065: the table says $_dst is $_dg, the zone registry answers '${_got:-nothing}'"
    done <"$WORK/table-got.tsv"
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
