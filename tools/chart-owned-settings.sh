#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# The settings the chart owns are refused, not overridden (B-072): a
# configuration that disagrees with the volumes the pod mounts must fail at
# `helm template` time. Each case below renders the chart with one value set
# and checks that it either renders or fails with the message that names
# what to use instead.
#
#   sh tools/chart-owned-settings.sh
#
# Needs helm. CI runs it where helm is installed. Stdout only: nothing is
# installed anywhere.
set -eu

chart="deploy/charts/tobby"
fail=0

# refused <expected fragment> <helm args...>: must fail, message names the fragment.
refused() {
    want="$1"
    shift
    if out="$(helm template tobby "$chart" "$@" 2>&1)"; then
        echo "FAIL: rendered, expected a refusal naming '$want': $*"
        fail=1
    elif ! printf '%s' "$out" | grep -qF -- "$want"; then
        echo "FAIL: refused without '$want': $*"
        printf '%s\n' "$out"
        fail=1
    else
        echo "ok: refused ($want): $*"
    fi
}

# renders <helm args...>: must render.
renders() {
    if out="$(helm template tobby "$chart" "$@" 2>&1)"; then
        echo "ok: renders: $*"
    else
        echo "FAIL: refused, expected to render: $*"
        printf '%s\n' "$out"
        fail=1
    fi
}

renders
refused "config.storage.root is set by the chart" --set config.storage.root=/x
refused "config.state.root is set by the chart" --set config.state.root=/x
refused "config.server.addr is set by the chart" --set config.server.addr=:1

# registries.credentialsFile is owned while the chart mounts the Secret...
renders --set registryCredentials.existingSecret=creds
refused "registryCredentials.existingSecret" \
    --set registryCredentials.existingSecret=creds --set config.registries.credentialsFile=/x/y
refused "registryCredentials.dockerconfigjson" \
    --set registryCredentials.enabled=true \
    --set-json 'registryCredentials.dockerconfigjson="{\"auths\":{}}"' \
    --set config.registries.credentialsFile=/x/y
# ... and the operator's own while it does not (a file from extraVolumes).
renders --set config.registries.credentialsFile=/x/y
renders --set registryCredentials.enabled=true --set config.registries.credentialsFile=/x/y

# --- Rendered-output cases (B-084, B-086, B-091) ---------------------------

# shows <expected regex> <helm args...>: output must match (extended regex,
# whole rendering). lacks: must not.
shows() {
    want="$1"
    shift
    if ! out="$(helm template tobby "$chart" "$@" 2>&1)"; then
        echo "FAIL: refused, expected to render: $*"; printf '%s\n' "$out"; fail=1
    elif printf '%s\n' "$out" | grep -Eq -- "$want"; then
        echo "ok: shows ($want): $*"
    else
        echo "FAIL: missing '$want': $*"; fail=1
    fi
}
lacks() {
    want="$1"
    shift
    if ! out="$(helm template tobby "$chart" "$@" 2>&1)"; then
        echo "FAIL: refused, expected to render: $*"; printf '%s\n' "$out"; fail=1
    elif printf '%s\n' "$out" | grep -Eq -- "$want"; then
        echo "FAIL: unexpected '$want': $*"; fail=1
    else
        echo "ok: lacks ($want): $*"
    fi
}
# count <regex> <n> <helm args...>: exactly n matching lines.
count() {
    want="$1"; n="$2"
    shift 2
    got="$(helm template tobby "$chart" "$@" 2>&1 | grep -Ec -- "$want" || true)"
    if [ "$got" = "$n" ]; then
        echo "ok: $n x '$want': $*"
    else
        echo "FAIL: $got x '$want', want $n: $*"; fail=1
    fi
}

sm="--set metrics.serviceMonitor.enabled=true"

# B-084: one listener, so a TLS pod needs HTTPS probes and an https scrape.
count 'scheme: HTTPS' 0
lacks 'scheme: https|tlsConfig' $sm
shows 'scheme: http$' $sm
count 'scheme: HTTPS' 3 --set config.server.tls.enabled=true
count 'scheme: HTTPS' 3 --set config.server.tls.certFile=/c/tls.crt --set config.server.tls.keyFile=/c/tls.key
# self-signed: https + skip-verify by default
shows 'scheme: https' $sm --set config.server.tls.enabled=true
shows 'insecureSkipVerify: true' $sm --set config.server.tls.enabled=true
# own certificate: https, never skip-verify unless asked
shows 'scheme: https' $sm --set config.server.tls.certFile=/c/tls.crt --set config.server.tls.keyFile=/c/tls.key
lacks 'insecureSkipVerify' $sm --set config.server.tls.certFile=/c/tls.crt --set config.server.tls.keyFile=/c/tls.key
# explicit tlsConfig replaces the default
shows 'serverName: tobby.example.com' $sm --set config.server.tls.enabled=true \
    --set metrics.serviceMonitor.tlsConfig.serverName=tobby.example.com
lacks 'insecureSkipVerify' $sm --set config.server.tls.enabled=true \
    --set metrics.serviceMonitor.tlsConfig.serverName=tobby.example.com
# explicitly disabled stays plain HTTP
count 'scheme: HTTPS' 0 --set config.server.tls.enabled=false

# B-086: storageClassName follows the Helm convention on both claims.
count 'storageClassName' 0
count 'storageClassName: ""$' 2 --set persistence.storage.storageClassName=- --set persistence.state.storageClassName=-
count 'storageClassName: ""$' 1 --set persistence.storage.storageClassName=-
count 'storageClassName: "fast"$' 2 --set persistence.storage.storageClassName=fast --set persistence.state.storageClassName=fast
count 'storageClassName' 0 --set persistence.storage.storageClassName= --set persistence.state.storageClassName=
count 'storageClassName: "-"' 0 --set persistence.storage.storageClassName=-

# B-091: syncMaxAgeSeconds=0 drops TobbySyncStale only.
shows 'alert: TobbySyncStale' --set alerting.prometheusRule.enabled=true
lacks 'alert: TobbySyncStale' --set alerting.prometheusRule.enabled=true --set alerting.thresholds.syncMaxAgeSeconds=0
shows 'alert: TobbySyncNeverSucceeded' --set alerting.prometheusRule.enabled=true --set alerting.thresholds.syncMaxAgeSeconds=0

exit "$fail"
