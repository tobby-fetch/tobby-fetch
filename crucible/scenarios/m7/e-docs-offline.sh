#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track E — the bilingual documentation, as
# the operator who needs it most meets it: inside the air-gapped zone.
#
#   1. An instance on tbc-airgap (no egress, canary first) serves /help in
#      English and in French: every page of the published corpus is there in
#      both languages, and a crawl of the embedded corpus finds no dangling
#      link, anchor or image.
#   2. Every taxonomy code rendered by the instance links to its entry, and
#      the entry exists in both languages.
#   3. The pages NFR-021 requires are present: installation, configuration,
#      both modes, security model with the entry-point table, monitoring,
#      backup and rebuild, troubleshooting, the emergency path.
#
# How each is proved:
#
#   0. Staging: the canary node on tbc-net answers the crucible host (the
#      positive control) and does NOT answer the instance under test, nor
#      does the internet; the node holds no documentation file beside the
#      binary (the corpus can only come from inside it); the corpus embedded
#      in the binary under test is the published one (helpsync -check).
#   1. The reader is a viewer account with a browser session (/help is
#      readable by every signed-in role, by nobody else). The crawl
#      (test/topology/helpcrawl, a plain HTTP client sharing no code with
#      internal/help) lists the published pages from the website sources
#      (website/src/content/docs, French under fr/docs) and checks, in each
#      language: the /help navigation lists exactly them; each page answers
#      200 in that language (<html lang>), under its published title, never
#      as the English fallback; every link, anchor and image on every served
#      page resolves on this instance; and every link the SOURCE page writes
#      is present on the served page — the renderer drops a link it cannot
#      resolve, and a dropped link leaves no dangling href to find.
#   2. The /help index carries exactly the codes of internal/taxonomy, each
#      linked from the code index, with what, cause and action, worded
#      differently in French (translated, not a fallback). A live error on
#      each surface — a 404 page of the interface, a problem document of the
#      API — renders a catalog code linked to /help#<code>. Every code any
#      served page names has its entry.
#   3. The required pages are published and served in both languages, the
#      configuration reference and the entry-point table (NFR-023,
#      security/input-validation) carrying their tables.
#
# Topology:
#   tbc-m7e-airgap (container, tbc-airgap)  instance under test, no egress
#   tbc-m7e-canary (container, tbc-net)     the egress canary's target
#   the crucible host                       the reader's HTTP client
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/e-docs-offline.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-e-docs-offline.txt}"
# shellcheck source=../../lib.sh
. "$DIR/../../lib.sh"
# shellcheck source=c-lib.sh
. "$DIR/c-lib.sh"

WORK="$(mktemp -d)"
NODE=tbc-m7e-airgap
CANARY=tbc-m7e-canary
ADMIN=root
ADMIN_PW="m7e-crucible-admin-pw"
READER=reader
READER_PW="m7e-crucible-reader-pw"
# The NFR-021 pages, by intention; ":table" where the page's point is one.
REQUIRED="try/install-and-start,passthrough/deploy,reference/configuration:table,passthrough/operate,air-gap/media-workflow,air-gap/import-destination,security/model,security/content-trust,security/threat-model,security/input-validation:table,passthrough/monitoring,reference/metrics-logs,passthrough/backup-rebuild,reference/troubleshooting,recipes/emergency-path"
cleanup() {
    inc delete --force "$NODE" "$CANARY" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
for t in curl go; do
    command -v "$t" >/dev/null 2>&1 || fail "$t not found on the crucible host"
done
check "tooling available (curl, go)"
m7_build
( cd "$ROOT" && go build -o "$WORK/helpcrawl" ./test/topology/helpcrawl ) || fail "building the documentation crawler"
check "built the documentation crawler (test/topology/helpcrawl)"

# -- 0. The zone, the canary, nothing beside the binary -----------------------------------
m7_launch "$CANARY" tbc-connected-node
m7_launch "$NODE" tbc-airgap-node
CANARY_IP=$(instance_ip "$CANARY")
NODE_IP=$(instance_ip "$NODE")
m7_serve_source "$CANARY"
# The positive control first: a canary nobody can reach proves nothing.
curl -fsS -o /dev/null "http://$CANARY_IP:8080/healthz" || fail "staging: the canary does not answer the crucible host"
if inc exec "$NODE" -- wget -q -T 5 -O /dev/null "http://$CANARY_IP:8080/healthz" >/dev/null 2>&1; then
    fail "the air-gapped node reached the connected zone: there is no air gap to rehearse"
fi
if inc exec "$NODE" -- wget -q -T 5 -O /dev/null http://example.com/ >/dev/null 2>&1; then
    fail "the air-gapped node reached the internet"
fi
check "egress canary failed as required: $CANARY ($CANARY_IP) answers the crucible host and not $NODE ($NODE_IP); no route to the internet"

# The documentation can only come from inside the binary: the node holds no
# copy of any page (the binary is the only file pushed to it).
LEFT=$(inc exec "$NODE" -- sh -c 'find / -xdev \( -name "emergency-path*" -o -name "backup-rebuild*" -o -name "status.yaml" \) 2>/dev/null' || true)
[ -z "$LEFT" ] || fail "staging: documentation files lie beside the binary on the node: $LEFT"
( cd "$ROOT" && go run ./tools/helpsync -check ) >"$WORK/helpsync.out" 2>&1 ||
    fail "staging: the corpus embedded in the binary under test has drifted from the published pages: $(tr '\n' ' ' <"$WORK/helpsync.out")"
check "the node holds no documentation file beside the binary; the corpus embedded in it is the published one ($(tr -d '\n' <"$WORK/helpsync.out"))"

inc exec "$NODE" -- mkdir -p /etc/tobby /var/lib/tobby /var/lib/tobby-state
cat >"$WORK/config.yaml" <<EOF
mode: mirror
storage:
  root: /var/lib/tobby
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
EOF
inc file push "$WORK/config.yaml" "$NODE/etc/tobby/config.yaml" >/dev/null
if inc exec "$NODE" -- grep -q 'disabled' /etc/tobby/config.yaml; then
    fail "staging: authentication is disabled on the instance under test"
fi
m7_admin "$NODE" "$ADMIN" "$ADMIN_PW"
inc exec "$NODE" -- sh -c "printf '%s\n' '$READER_PW' | tobby user add $READER --role viewer --password-stdin" >/dev/null 2>&1 ||
    fail "creating the viewer account"
BASE="http://$NODE_IP:8080"
m7_start "$NODE" "$BASE"
CODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/help")
case "$CODE" in 401 | 303) ;; *) fail "an anonymous GET /help answered $CODE: authentication is not on" ;; esac
check "instance under test: mirror on tbc-airgap, authentication on (anonymous /help answers $CODE), a viewer account for the reader"

# -- 1-3. The crawl --------------------------------------------------------------------------
# The crawler prints one "ok <fact>" line per check and one "FAIL <finding>"
# line per finding; every line goes to the raw report.
RC=0
HELPCRAWL_PASSWORD="$READER_PW" "$WORK/helpcrawl" -base "$BASE" -user "$READER" \
    -docs "$ROOT/website/src/content/docs" -catalog "$ROOT/internal/taxonomy/catalog.go" \
    -require "$REQUIRED" >"$WORK/crawl.out" 2>"$WORK/crawl.err" || RC=$?
while IFS= read -r line; do
    case "$line" in
        "ok   "*) check "${line#ok   }" ;;
        "FAIL "*) printf '%s\n' "$line" | tee -a "$REPORT" ;;
    esac
done <"$WORK/crawl.out"
[ "$RC" = "0" ] || fail "the documentation crawl failed (exit $RC, $(grep -c '^FAIL' "$WORK/crawl.out" || true) findings above): $(tr '\n' ' ' <"$WORK/crawl.err")"
grep -q '^ok   NFR-021: the [0-9]* required pages' "$WORK/crawl.out" || fail "the crawl did not check the required pages"

cleanup
trap - EXIT
check "scenario m7 e-docs-offline complete — report: $REPORT"
