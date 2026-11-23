#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Helpers shared by the milestone-7 "b-" scenarios (track B: R-30 diagnostic
# bundle, R-27 backup and rebuild, R-32 clock plausibility, R-11 supervision
# kit). Sourced, never run.
#
# The rule of m5 and m6 holds: a scenario that MANIPULATES something verifies
# its own manipulation before it asks the product for a verdict. A clock that
# was never set, a file mode that was never loosened, a secret that was never
# planted — each reads exactly like the guard under test failing.

# b7_tooling: host tooling, never downloaded at run time.
b7_tooling() {
    for _t in jq curl openssl tar; do
        command -v "$_t" >/dev/null 2>&1 || fail "$_t not found"
    done
    check "tooling available (jq, curl, openssl, tar)"
}

# b7_build <workdir> [<date-rfc3339>]: the binary under test for the crucible
# host's arch. With a date, it is stamped as the release build stamps it
# (ldflags, ADR-0011), which is what the FR-096 build anchor reads.
b7_build() {
    ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
    _pkg="github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
    _ld=""
    [ -n "${2:-}" ] && _ld="-X ${_pkg}.version=v7.0.0-crucible -X ${_pkg}.date=$2"
    # shellcheck disable=SC2086 # _ld is a list of ldflags words on purpose
    ( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
        go build -trimpath -ldflags "$_ld" -o "$1/tobby" ./cmd/tobby ) || fail "building tobby"
    check "built tobby for linux/$ARCH${2:+ stamped with the build date $2}"
}

# b7_launch <instance> <profile> [--vm]: a fresh guest carrying the binary.
b7_launch() {
    _name="$1"; _profile="$2"; shift 2
    inc delete --force "$_name" 2>/dev/null || true
    inc launch "${CRUCIBLE_IMAGE:-images:alpine/3.24}" "$_name" --profile "$_profile" "$@" >/dev/null ||
        fail "launching $_name"
    if [ "${1:-}" = "--vm" ]; then
        # A virtual machine needs its agent before anything can be run in it.
        _n=0
        until inc exec "$_name" -- true >/dev/null 2>&1; do
            _n=$((_n + 1))
            [ "$_n" -gt 120 ] && fail "$_name: the guest agent never came up"
            sleep 1
        done
    fi
    inc file push "$WORK/tobby" "$_name/usr/bin/tobby" >/dev/null || fail "pushing tobby to $_name"
    inc exec "$_name" -- chmod +x /usr/bin/tobby
}

# b7_user <instance> <name> <role> <password>: a local account, through the
# command line a person would use, with the state directory of the scenario.
b7_user() {
    inc exec "$1" -- sh -c "printf '%s\n' '$4' | tobby user add '$2' --role '$3' --password-stdin --state-root /var/lib/tobby-state" \
        >/dev/null 2>&1 || fail "creating the $3 account $2 on $1"
}

# b7_start <instance>: serve in the background and wait for readiness, from
# the crucible host (busybox wget in the guest cannot be told to trust a
# self-signed certificate, and a TLS-less probe would not test the listener).
b7_start() {
    inc exec "$1" -- sh -c 'nohup tobby serve --config /etc/tobby/config.yaml >/var/log/tobby.log 2>&1 &'
    _ip=$(instance_ip "$1")
    _n=0
    until curl -sk -o /dev/null -f "${B7_SCHEME:-http}://$_ip:8080/readyz"; do
        _n=$((_n + 1))
        [ "$_n" -gt 60 ] && fail "$1: the instance never became ready: $(inc exec "$1" -- tail -n 5 /var/log/tobby.log 2>&1)"
        sleep 1
    done
}

# [t]obby, not tobby: pkill -f matches the command line of the shell running
# this very pkill (m5, B-026).
b7_stop() { inc exec "$1" -- sh -c 'pkill -f "[t]obby serve" || true; sleep 2'; }

# b7_api <ip> <user:pass> <method> <path> [<body>]: from the crucible host.
b7_api() {
    if [ -n "${5:-}" ]; then
        curl -sk -u "$2" -X "$3" -H 'Content-Type: application/json' -d "$5" "${B7_SCHEME:-http}://$1:8080$4"
    else
        curl -sk -u "$2" -X "$3" "${B7_SCHEME:-http}://$1:8080$4"
    fi
}

# b7_status <ip> <user:pass> <method> <path>: only the HTTP status.
b7_status() {
    curl -sk -o /dev/null -w '%{http_code}' -u "$2" -X "$3" "${B7_SCHEME:-http}://$1:8080$4"
}

# b7_pull <instance> <path> <dest>: a file out of a guest.
b7_pull() { inc file pull "$1$2" "$3" >/dev/null || fail "pulling $2 from $1"; }

# b7_fingerprint <ip>: the SHA-256 of the certificate presented on :8080.
b7_fingerprint() {
    openssl s_client -connect "$1:8080" </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256 |
        cut -d= -f2 | tr -d ':' | tr 'A-F' 'a-f'
}
