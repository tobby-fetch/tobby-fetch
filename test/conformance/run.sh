#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# OCI Distribution conformance (release criterion, DEVELOPMENT-PLAN §3):
# the upstream opencontainers/distribution-spec conformance suite, pinned
# in ./PIN, played against a REAL `tobby serve` over a real socket — the
# same /v2/ surface, authentication middleware and storage a client meets.
#
#   1. builds the pinned suite (go test -c), refusing any checkout whose
#      commit is not the pinned one;
#   2. starts an instance (fresh state, generated administrator account,
#      credentials never printed) and waits for /readyz;
#   3. runs the four workflows — pull, push, content discovery (tag listing
#      and the Referrers API), content management — with nothing excluded.
#
# Requires: go, git, jq, curl. Environment:
#   TOBBY_BIN         binary under test (default: built from this checkout)
#   CONFORMANCE_PORT  loopback port of the instance (default 15299)
#   CONFORMANCE_OUT   directory for reports and logs (default: a temp dir)
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=/dev/null
. "$HERE/PIN"

PORT="${CONFORMANCE_PORT:-15299}"
WORK="$(mktemp -d)"
OUT="${CONFORMANCE_OUT:-$WORK/out}"
mkdir -p "$OUT"
SERVER_PID=""

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
step() { printf '%s\n' "$*"; }

# -- 1. The pinned suite ------------------------------------------------------
SUITE="$WORK/distribution-spec"
mkdir -p "$SUITE"
(
    cd "$SUITE"
    git init -q .
    git remote add origin https://github.com/opencontainers/distribution-spec.git
    git fetch -q --depth 1 origin "$DISTRIBUTION_SPEC_COMMIT"
    git checkout -q FETCH_HEAD
) || fail "fetching distribution-spec at $DISTRIBUTION_SPEC_COMMIT"
HEAD_SHA="$(git -C "$SUITE" rev-parse HEAD)"
[ "$HEAD_SHA" = "$DISTRIBUTION_SPEC_COMMIT" ] ||
    fail "distribution-spec checkout is $HEAD_SHA, pinned $DISTRIBUTION_SPEC_COMMIT"
(cd "$SUITE/conformance" && GOWORK=off go test -c -o "$WORK/conformance.test") ||
    fail "building the conformance suite"
step "ok   suite: distribution-spec $DISTRIBUTION_SPEC_TAG ($HEAD_SHA)"

# -- 2. The instance under test -----------------------------------------------
if [ -z "${TOBBY_BIN:-}" ]; then
    (cd "$ROOT" && go build -trimpath -o "$WORK/tobby" ./cmd/tobby) || fail "building tobby"
    TOBBY_BIN="$WORK/tobby"
fi
mkdir -p "$WORK/state" "$WORK/store"
# A throw-away credential for a throw-away instance: generated here, handed
# to the CLI on stdin and to the suite through the environment only.
PASS="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
printf '%s\n' "$PASS" | "$TOBBY_BIN" user add conformance --state-root "$WORK/state" \
    --mode mirror --password-stdin >/dev/null 2>&1 || fail "creating the account"
"$TOBBY_BIN" serve --mode mirror --storage-root "$WORK/store" --state-root "$WORK/state" \
    --server-addr "127.0.0.1:$PORT" >"$OUT/tobby.log" 2>&1 &
SERVER_PID=$!
i=0
until curl -fsS "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -gt 100 ] && fail "the instance never became ready (see $OUT/tobby.log)"
    kill -0 "$SERVER_PID" 2>/dev/null || fail "the instance exited (see $OUT/tobby.log)"
    sleep 0.2
done
step "ok   instance ready on 127.0.0.1:$PORT"

# -- 3. The whole suite must pass ---------------------------------------------
# No spec is skipped on purpose: pull, push, content discovery (tag listing
# and the Referrers API) and content management, all of them.
export OCI_ROOT_URL="http://127.0.0.1:$PORT"
export OCI_NAMESPACE="conformance/main"
export OCI_CROSSMOUNT_NAMESPACE="conformance/other"
export OCI_USERNAME="conformance"
export OCI_PASSWORD="$PASS"
export OCI_TEST_PULL=1 OCI_TEST_PUSH=1 OCI_TEST_CONTENT_DISCOVERY=1 OCI_TEST_CONTENT_MANAGEMENT=1
# Automatic cross-mount is optional in the spec; Tobby (distribution) does
# not do it, and saying so turns the 202 expectation on rather than
# leaving those specs to skip themselves.
export OCI_AUTOMATIC_CROSSMOUNT=false
export OCI_REPORT_DIR="$OUT/report"
mkdir -p "$OCI_REPORT_DIR"

set +e
"$WORK/conformance.test" -ginkgo.no-color -ginkgo.json-report="$OUT/main.json" >"$OUT/main.log" 2>&1
MAIN_RC=$?
set -e

summary="$(jq -r '[.[0].SpecReports[] | select(.LeafNodeType == "It")] |
    "\(map(select(.State == "passed")) | length) passed, \(map(select(.State == "failed")) | length) failed, \(map(select(.State == "skipped")) | length) skipped"' "$OUT/main.json")"
step "run: $summary"
if [ "$MAIN_RC" -ne 0 ]; then
    jq -r '.[0].SpecReports[] | select(.State == "failed") |
        "FAIL " + ((.ContainerHierarchyTexts + [.LeafNodeText]) | join(" "))' "$OUT/main.json" >&2
    fail "conformance failures (full log: $OUT/main.log)"
fi
# The spec's own conditional skips are listed, not hidden.
jq -r '.[0].SpecReports[] | select(.LeafNodeType == "It" and .State == "skipped") |
    "skip " + ((.ContainerHierarchyTexts + [.LeafNodeText]) | join(" "))' "$OUT/main.json"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        printf '### OCI distribution-spec %s conformance\n\n' "$DISTRIBUTION_SPEC_TAG"
        printf 'commit %s: %s.\n' "$DISTRIBUTION_SPEC_COMMIT" "$summary"
    } >>"$GITHUB_STEP_SUMMARY"
fi
step "conformance: $summary"
