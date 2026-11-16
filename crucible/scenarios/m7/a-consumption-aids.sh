#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track A — the consumption aids (FR-065
# amendment 2026-11-16, public feature 7.1), checked against content that
# really crossed from a source registry into a zone registry:
#
#   1. A passthrough instance promotes three recipes into a zone registry
#      under destination.basePath "mirror": an image whose reference carries a
#      port (relocated under <host>_8080), an image of Docker Hub, and one
#      written with the alias index.docker.io (folded onto docker.io).
#      Docker Hub is stood in for by the source registry through the
#      documented source substitution (FR-036).
#   2. The mapping table, from the API (CSV, JSON, Markdown) and from the
#      command line, has the same rows; each row is checked against an
#      expectation computed here from the recipes — not against the table
#      itself — and the zone registry answers every destination reference
#      with the digest of its row.
#   3. The registries.yaml snippet has one mirrors entry per nominal host
#      (the alias folded onto docker.io), rewrite rules carrying the base
#      path, the CA path of consumption.caFile, and no credential. The API and
#      the command line produce the same bytes, twice. The rewrite rules are
#      then APPLIED here, the way containerd applies them to the repository
#      part of an image name, and the result is pulled from the zone registry:
#      the digest is the pinned one. Without the rewrite the nominal path
#      holds nothing.
#   4. An instance with neither destination.registry nor consumption.registry
#      refuses both aids with TBY-AID-001.
#
# What this scenario does not do: run containerd. A K3s/RKE2 node consuming the
# snippet end to end — registry access log as the witness, egress ACL on the
# node — is the crown's job (m7/crown.sh); here the rewrite semantics K3s
# documents (a map of regular expressions matched against the repository part
# of the image name) are applied by hand to the exact bytes Tobby generated.
#
# Topology:
#   tbc-m7a-source (container, tbc-net)  source registry + cookbook
#   tbc-m7a-node   (container, tbc-net)  passthrough instance under test
#   tbc-m7a-zone   (container, tbc-net)  zone registry
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/a-consumption-aids.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-a-consumption-aids.txt}"
. "$DIR/../../lib.sh"
. "$DIR/../m6/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m7a-source
NODE=tbc-m7a-node
ZONE=tbc-m7a-zone
STORE=/var/lib/tobby
BASE=mirror
CA=/etc/ssl/zone-ca.crt
cleanup() {
    inc delete --force "$SOURCE" "$NODE" "$ZONE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

ACCEPT='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'

# a_digest <host:port> <repo> <tag>: what a plain-HTTP registry answers under
# that name, read from the crucible host (empty when nothing).
a_digest() {
    curl -sS -I -H "Accept: $ACCEPT" "http://$1/v2/$2/manifests/$3" 2>/dev/null |
        awk 'tolower($1)=="docker-content-digest:" {print $2}' | tr -d '\r'
}

report_start
c_tooling
c_build "$WORK"

# -- 1. Three nodes, three recipes, one promotion ------------------------------------
c_launch "$SOURCE" tbc-connected-node
c_launch "$NODE" tbc-connected-node
c_launch "$ZONE" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
NODE_IP=$(instance_ip "$NODE")
ZONE_IP=$(instance_ip "$ZONE")
SRC="$SOURCE_IP:8080"
ZN="$ZONE_IP:8080"
c_serve_source "$SOURCE"
c_serve_source "$ZONE"

# The source holds the Docker Hub images under the canonical path the
# substitution reads (FR-036: base + full canonical path).
c_keypair a
D_PORT=$(c_seed_image "$SRC/library/portapp:1.0.0")
D_HUB=$(c_seed_image "$SRC/docker.io/library/hubapp:1.0.0")
D_ALIAS=$(c_seed_image "$SRC/docker.io/library/aliasapp:1.0.0")
c_publish "$SRC" port-app portapp "$SRC/library/portapp" "$D_PORT" a >/dev/null
c_publish "$SRC" hub-app hubapp docker.io/library/hubapp "$D_HUB" a >/dev/null
c_publish "$SRC" alias-app aliasapp index.docker.io/library/aliasapp "$D_ALIAS" a >/dev/null

cat >"$WORK/retriever.yaml" <<EOF
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-a
spec:
  cookbook: $SRC/cookbook
  recipes:
    - name: port-app
      version: "1.0.0"
    - name: hub-app
      version: "1.0.0"
    - name: alias-app
      version: "1.0.0"
EOF
inc file push "$WORK/retriever.yaml" "$NODE/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/a.pub" "$NODE/etc/tobby/a.pub" --create-dirs >/dev/null

# a_config <instance> <extra top-level yaml>
a_config() {
    inc exec "$1" -- sh -c "mkdir -p /etc/tobby && cat >/etc/tobby/config.yaml <<'EOF'
mode: passthrough
zone: zone-a
storage:
  root: $STORE
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
auth:
  disabled: true
retriever:
  source: /etc/tobby/retriever.yaml
trust:
  roots:
    - name: a
      keyFile: /etc/tobby/a.pub
$2
EOF"
}
a_config "$NODE" "registries:
  insecure:
    - $SRC
    - $ZN
  substitutions:
    docker.io: $SRC
destination:
  registry: $ZN
  basePath: $BASE
consumption:
  caFile: $CA"
c_start "$NODE"
inc exec "$NODE" -- sh -c 'tobby sync --wait --output json' >"$WORK/sync.json" 2>/dev/null || true
jq -e '.task.status == "done"' "$WORK/sync.json" >/dev/null ||
    fail "the promotion did not finish: $(jq -c '[.task.items[] | {name, status}]' "$WORK/sync.json" 2>/dev/null || cat "$WORK/sync.json")"
check "three recipes promoted from $SRC to the zone registry $ZN under basePath '$BASE'"

# The expectation, computed here from the recipes and ADR-0013 — never from
# the product: source reference as the recipe names it (alias folded), and the
# relocated path under the base prefix, a port written with an underscore.
SRC_US=$(printf '%s' "$SRC" | tr ':' '_')
cat >"$WORK/expected.tsv" <<EOF
port-app	portapp	$SRC/library/portapp:1.0.0	$ZN/$BASE/$SRC_US/library/portapp:1.0.0	$D_PORT
hub-app	hubapp	docker.io/library/hubapp:1.0.0	$ZN/$BASE/docker.io/library/hubapp:1.0.0	$D_HUB
alias-app	aliasapp	docker.io/library/aliasapp:1.0.0	$ZN/$BASE/docker.io/library/aliasapp:1.0.0	$D_ALIAS
EOF
while IFS="$(printf '\t')" read -r _ _ _ dst dgst; do
    rest=${dst#"$ZN"/}
    got=$(a_digest "$ZN" "${rest%:*}" "${rest##*:}")
    [ "$got" = "$dgst" ] || fail "the manipulation failed: the zone registry answers '${got:-nothing}' for $dst, want $dgst"
done <"$WORK/expected.tsv"
check "the zone registry holds all three images at the paths ADR-0013 predicts, with the source digests"

# -- 2. The mapping table ------------------------------------------------------------
c_api "$NODE_IP" GET "/api/v1/mapping?format=json" >"$WORK/table-api.json"
jq -e --arg d "$ZN" '.destination == $d and (.entries | length) == 3' "$WORK/table-api.json" >/dev/null ||
    fail "FR-065: the API table is not the expected document: $(head -c 400 "$WORK/table-api.json")"
while IFS="$(printf '\t')" read -r recipe ing src dst dgst; do
    jq -e --arg r "$recipe" --arg i "$ing" --arg s "$src" --arg d "$dst" --arg g "$dgst" \
        '[.entries[] | select(.recipe == $r and .ingredient == $i and .kind == "ContainerImage" and .source == $s and .destination == $d and .digest == $g)] | length == 1' \
        "$WORK/table-api.json" >/dev/null || fail "FR-065: the table has no exact entry $recipe: $src -> $dst ($dgst)"
done <"$WORK/expected.tsv"
check "FR-065: the JSON table has exactly the expected source, destination and digest for each ingredient"

c_api "$NODE_IP" GET "/api/v1/mapping?format=csv" >"$WORK/table.csv"
c_api "$NODE_IP" GET "/api/v1/mapping?format=markdown" >"$WORK/table.md"
[ "$(wc -l <"$WORK/table.csv")" -eq 4 ] || fail "the CSV table has $(wc -l <"$WORK/table.csv") lines, want a header and 3 rows"
[ "$(wc -l <"$WORK/table.md")" -eq 5 ] || fail "the Markdown table has $(wc -l <"$WORK/table.md") lines, want a header, a rule and 3 rows"
while IFS="$(printf '\t')" read -r _ _ _ dst dgst; do
    grep -qF "$dst" "$WORK/table.csv" && grep -qF "$dgst" "$WORK/table.csv" || fail "the CSV table lacks $dst"
    grep -qF "$dst" "$WORK/table.md" && grep -qF "$dgst" "$WORK/table.md" || fail "the Markdown table lacks $dst"
done <"$WORK/expected.tsv"
check "FR-065: CSV and Markdown carry the same three rows"

c_api "$NODE_IP" GET "/api/v1/recipes/hub-app/mapping?format=json" >"$WORK/table-one.json"
jq -e '(.entries | length) == 1 and .entries[0].recipe == "hub-app"' "$WORK/table-one.json" >/dev/null ||
    fail "the per-recipe table is not hub-app's alone"
c_api "$NODE_IP" GET "/api/v1/recipes/hub-app/mapping" | jq -e '.versions | length == 1' >/dev/null ||
    fail "the mapping route without ?format= changed its answer"
check "the per-recipe table is that recipe's rows; the route without ?format= answers as before"

inc exec "$NODE" -- sh -c 'tobby consumption table --config /etc/tobby/config.yaml --output json' >"$WORK/table-cli.json" 2>"$WORK/cli.err" ||
    fail "tobby consumption table failed: $(cat "$WORK/cli.err")"
[ "$(jq -S . "$WORK/table-cli.json")" = "$(jq -S . "$WORK/table-api.json")" ] ||
    fail "the command line and the API disagree on the table"
check "the command line prints the table the API serves (FR-061)"

# -- 3. The registries.yaml snippet --------------------------------------------------
c_api "$NODE_IP" GET /api/v1/registries.yaml >"$WORK/registries-api.yaml"
inc exec "$NODE" -- sh -c 'tobby consumption registries-yaml --config /etc/tobby/config.yaml' >"$WORK/registries-cli.yaml" 2>"$WORK/cli.err" ||
    fail "tobby consumption registries-yaml failed: $(cat "$WORK/cli.err")"
inc exec "$NODE" -- sh -c 'tobby consumption registries --config /etc/tobby/config.yaml' >"$WORK/registries-cli2.yaml" 2>/dev/null ||
    fail "the 'registries' alias failed"
cmp -s "$WORK/registries-api.yaml" "$WORK/registries-cli.yaml" || fail "the API and the command line produce different snippets"
cmp -s "$WORK/registries-cli.yaml" "$WORK/registries-cli2.yaml" || fail "two generations differ: the snippet is not deterministic"
check "FR-065: the API and the command line produce the same snippet, byte for byte, twice"

# One mirrors entry per nominal host, alias folded; no foreign host.
MIRRORS=$(awk '/^mirrors:/{m=1;next} /^[a-z#]/{m=0} m && /^  "[^"]+":$/ {gsub(/^  "|":$/,""); print}' "$WORK/registries-api.yaml" | sort | tr '\n' ' ')
WANT=$(printf '%s\n%s\n' docker.io "$SRC" | sort | tr '\n' ' ')
[ "$MIRRORS" = "$WANT" ] || fail "FR-065: mirrors are [$MIRRORS], want [$WANT]"
grep -q 'index.docker.io' "$WORK/registries-api.yaml" && fail "the Docker Hub alias was not folded onto docker.io"
for h in docker.io "$SRC"; do
    awk -v h="  \"$h\":" '$0==h{f=1;next} /^  "/{f=0} f' "$WORK/registries-api.yaml" >"$WORK/mirror.part"
    grep -qF "\"http://$ZN\"" "$WORK/mirror.part" || fail "FR-065: the $h mirror does not point at http://$ZN (the destination is in registries.insecure)"
done
grep -qF "\"^(.*)\$\": \"$BASE/docker.io/\$1\"" "$WORK/registries-api.yaml" || fail "the docker.io rewrite does not carry the base path"
grep -qF "\"^(.*)\$\": \"$BASE/$SRC_US/\$1\"" "$WORK/registries-api.yaml" || fail "the port host is not rewritten under $SRC_US"
grep -qF "ca_file: \"$CA\"" "$WORK/registries-api.yaml" || fail "consumption.caFile is not written into configs"
# No credential: every auth/username/password line is a comment.
if grep -E 'username|password|auth' "$WORK/registries-api.yaml" | grep -vE '^[[:space:]]*#' | grep -q .; then
    fail "FR-065: the snippet carries a credential key outside a comment"
fi
grep -qi 'admission' "$WORK/registries-api.yaml" || fail "the header does not state the limits of the aid"
check "FR-065: two mirrors (docker.io folded, the port host), endpoints $ZN over http, rewrites with the base path, the CA path, no credential, limits stated"

# The rewrite, applied the way containerd applies it: the rule's regular
# expression against the repository part of the image name (host and tag
# excluded), the replacement carrying $1. Then pull from the endpoint.
apply_rewrite() { # <mirror host> <repository path>
    rule=$(awk -v h="  \"$1\":" '$0==h{f=1;next} /^  "/{f=0} f && /rewrite:/{r=1;next} f && r && /"\^\(\.\*\)\$"/{print;exit}' "$WORK/registries-api.yaml")
    repl=$(printf '%s' "$rule" | sed -E 's/^[[:space:]]*"\^\(\.\*\)\$": "(.*)"$/\1/')
    printf '%s' "$2" | sed -E "s#^(.*)\$#$(printf '%s' "$repl" | sed 's/\$1/\\1/')#"
}
for item in "docker.io|library/hubapp|$D_HUB" "docker.io|library/aliasapp|$D_ALIAS" "$SRC|library/portapp|$D_PORT"; do
    host=${item%%|*}; rest=${item#*|}; repo=${rest%%|*}; want=${rest#*|}
    mapped=$(apply_rewrite "$host" "$repo")
    got=$(a_digest "$ZN" "$mapped" 1.0.0)
    [ "$got" = "$want" ] || fail "FR-065: $host/$repo rewrites to '$mapped', which the zone registry answers '${got:-nothing}' (want $want)"
done
for repo in library/hubapp library/portapp; do
    [ -z "$(a_digest "$ZN" "$repo" 1.0.0)" ] || fail "the manipulation failed: $repo is already at the root of the zone registry, so the rewrite proves nothing"
done
check "FR-065: applying the generated rewrite rules to each nominal repository reaches the pinned digest at the zone registry; the unrewritten path holds nothing"

# -- 4. No registry known ------------------------------------------------------------
a_config "$NODE" ""
if inc exec "$NODE" -- sh -c 'tobby consumption table --config /etc/tobby/config.yaml --output json' >"$WORK/noreg.out" 2>&1; then
    fail "FR-065: the table was generated although no registry is configured"
fi
grep -q 'TBY-AID-001' "$WORK/noreg.out" || fail "FR-065: the refusal is not TBY-AID-001: $(cat "$WORK/noreg.out")"
if inc exec "$NODE" -- sh -c 'tobby consumption registries-yaml --config /etc/tobby/config.yaml' >"$WORK/noreg2.out" 2>&1; then
    fail "FR-065: the snippet was generated although no registry is configured"
fi
grep -q 'TBY-AID-001' "$WORK/noreg2.out" || fail "FR-065: the snippet's refusal is not TBY-AID-001"
check "FR-065: with neither destination.registry nor consumption.registry both aids refuse with TBY-AID-001"

check "scenario m7 a-consumption-aids complete — report: $CRUCIBLE_REPORT"
