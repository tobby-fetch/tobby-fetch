#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Helpers shared by the milestone-6 "b-" scenarios (track B: R-14 session
# and token expiry, 6.3 OIDC and SAML, R-20 continuity when the identity
# provider is down). Sourced, never run.
#
# The identity provider is the same Keycloak as the CI fixture — the image
# pinned by digest in test/keycloak/IMAGE and the realm of
# test/keycloak/realm — run as an Incus OCI container, over HTTPS with a
# certificate from a throw-away CA this scenario generates, so the instance
# under test meets what a deployment meets: an https issuer, trusted
# through network.tls.caFiles.

# b_tooling: host tooling, never downloaded at run time.
b_tooling() {
    command -v openssl >/dev/null 2>&1 || fail "openssl not found"
    command -v jq >/dev/null 2>&1 || fail "jq not found"
    check "tooling available (openssl, jq)"
}

# b_build <workdir>: the binary under test, for the crucible host's arch.
b_build() {
    ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
    ( cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
        go build -trimpath -o "$1/tobby" ./cmd/tobby ) || fail "building tobby"
    check "built tobby for linux/$ARCH"
}

# b_pki <workdir> <host…>: a CA and one server certificate per host, named
# <host>.crt / <host>.key, plus an SP key pair (sp.crt / sp.key).
b_pki() {
    _w="$1"; shift
    openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=tobby-crucible-m6b-ca" \
        -keyout "$_w/ca.key" -out "$_w/ca.crt" >/dev/null 2>&1 || fail "generating the CA"
    for _h in "$@"; do
        # shellcheck disable=SC2015 # any step of the chain failing is the failure: fail is meant to run
        openssl req -newkey rsa:2048 -nodes -subj "/CN=$_h" -keyout "$_w/$_h.key" -out "$_w/$_h.csr" >/dev/null 2>&1 &&
        printf 'subjectAltName=DNS:%s\n' "$_h" >"$_w/$_h.ext" &&
        openssl x509 -req -in "$_w/$_h.csr" -CA "$_w/ca.crt" -CAkey "$_w/ca.key" -CAcreateserial \
            -days 2 -extfile "$_w/$_h.ext" -out "$_w/$_h.crt" >/dev/null 2>&1 ||
            fail "issuing the certificate of $_h"
    done
    openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=tobby-sp" \
        -keyout "$_w/sp.key" -out "$_w/sp.crt" >/dev/null 2>&1 || fail "generating the SP key pair"
    chmod 0644 "$_w"/*.key
    check "throw-away PKI generated (CA, $*, SP key pair)"
}

# b_keycloak <instance> <tobby-base-url>: the pinned Keycloak as an OCI
# container, realm imported, HTTPS on 8443 with the scenario's certificate.
b_keycloak() {
    _idp="$1"; _tobby="$2"
    _image=$(cut -d@ -f1 "$ROOT/test/keycloak/IMAGE" | sed 's|^quay.io/||')
    _digest=$(cut -d@ -f2 "$ROOT/test/keycloak/IMAGE")
    incus remote list -f csv | grep -q '^tbc-quay,' ||
        incus remote add tbc-quay https://quay.io --protocol=oci --public >/dev/null ||
        fail "adding the quay.io OCI remote"
    inc delete --force "$_idp" 2>/dev/null || true
    inc create "tbc-quay:${_image%%:*}@$_digest" "$_idp" --profile tbc-connected-node \
        -c environment.KC_HOSTNAME="https://$_idp.incus:8443" \
        -c environment.KC_HTTPS_PORT=8443 \
        -c environment.KC_HTTPS_CERTIFICATE_FILE=/opt/keycloak/conf/tls.crt \
        -c environment.KC_HTTPS_CERTIFICATE_KEY_FILE=/opt/keycloak/conf/tls.key \
        -c environment.TOBBY_OIDC_REDIRECT_URI="$_tobby/auth/oidc/callback" \
        -c environment.TOBBY_SAML_ENTITY_ID="$_tobby/auth/saml/metadata" \
        -c environment.TOBBY_SAML_ACS_URL="$_tobby/auth/saml/acs" \
        -c oci.entrypoint="/opt/keycloak/bin/kc.sh start-dev --import-realm" >/dev/null ||
        fail "creating the Keycloak container from $_image@$_digest"
    # shellcheck disable=SC2015 # any step of the chain failing is the failure: fail is meant to run
    inc file push "$WORK/$_idp.incus.crt" "$_idp/opt/keycloak/conf/tls.crt" >/dev/null &&
    inc file push "$WORK/$_idp.incus.key" "$_idp/opt/keycloak/conf/tls.key" >/dev/null &&
    inc file push --create-dirs "$ROOT/test/keycloak/realm/tobby-realm.json" \
        "$_idp/opt/keycloak/data/import/tobby-realm.json" >/dev/null ||
        fail "pushing the realm and the certificate into Keycloak"
    inc start "$_idp" || fail "starting Keycloak"
}

# b_wait_idp <node> <issuer>: until the discovery document answers from the
# node, through the scenario CA.
b_wait_idp() {
    _i=0
    until inc exec "$1" -- curl -fsS --cacert /etc/tobby/ca.crt -o /dev/null \
        "$2/.well-known/openid-configuration" >/dev/null 2>&1; do
        _i=$((_i + 1))
        [ "$_i" -gt 120 ] && fail "Keycloak never answered at $2"
        sleep 2
    done
}

# b_until <seconds> <description> <command…>: poll a condition.
b_until() {
    _n="$1"; _what="$2"; shift 2
    _i=0
    until "$@"; do
        _i=$((_i + 1))
        [ "$_i" -gt "$_n" ] && fail "$_what (waited ${_n}s)"
        sleep 1
    done
}

# b_browser_script: the browser this scenario plays, as a script on the
# node. It speaks to Tobby and Keycloak with curl, cookie jars per
# "browser", and stops at every redirect so the scenario sees each hop.
#
#   b-browser oidc <user> <pass> <jar> [next]   -> "<code> <location>" of the callback
#   b-browser saml <user> <pass> <jar> [next]   -> "<code> <location>" of the finish step
#   b-browser samlpost <user> <pass> <jar> <out> -> saves the SAMLResponse form, posts nothing
#   b-browser post <jar> <form-file>             -> replays a saved form, "<code> <location>"
#   b-browser get <jar> <path>                   -> "<code> <location>"
b_browser_script() {
    cat <<'SCRIPT'
#!/bin/sh
set -eu
T="${TOBBY_URL:?}"
C="curl -s --cacert /etc/tobby/ca.crt"
kc_login() { # <start-url> <user> <pass> <kcjar>: submit the Keycloak form, print the response
    page=$($C -c "$4" -b "$4" -L "$1")
    form=$(printf '%s' "$page" | tr '\n' ' ' | grep -o '<form[^>]*kc-form-login[^>]*>' | head -1)
    action=$(printf '%s' "$form" | sed -n 's/.* action="\([^"]*\)".*/\1/p' | sed 's/&amp;/\&/g')
    [ -n "$action" ] || { echo "no keycloak login form" >&2; exit 2; }
    $C -c "$4" -b "$4" -D "$4.h" --data-urlencode "username=$2" --data-urlencode "password=$3" "$action"
}
hop() { $C -c "$1" -b "$1" -o /dev/null -w '%{http_code} %{redirect_url}' "$2"; }
field() { tr '\n' ' ' | grep -o "name=\"$1\" value=\"[^\"]*\"" | head -1 | sed 's/.*value="\([^"]*\)"/\1/'; }
case "$1" in
oidc)
    loc=$($C -c "$4" -b "$4" -o /dev/null -w '%{redirect_url}' "$T/auth/oidc/login?next=${5:-%2F}")
    kc_login "$loc" "$2" "$3" "$4.kc" >/dev/null
    cb=$(sed -n 's/^[Ll]ocation: *//p' "$4.kc.h" | tr -d '\r')
    hop "$4" "$cb" ;;
saml|samlpost)
    loc=$($C -c "$4" -b "$4" -o /dev/null -w '%{redirect_url}' "$T/auth/saml/login?next=${5:-%2F}")
    body=$(kc_login "$loc" "$2" "$3" "$4.kc")
    resp=$(printf '%s' "$body" | field SAMLResponse)
    relay=$(printf '%s' "$body" | field RelayState)
    [ -n "$resp" ] || { echo "no SAMLResponse" >&2; exit 2; }
    if [ "$1" = samlpost ]; then printf 'SAMLResponse=%s\nRelayState=%s\n' "$resp" "$relay" >"$5"; exit 0; fi
    fin=$($C -c "$4" -b "$4" -o /dev/null -w '%{redirect_url}' --data-urlencode "SAMLResponse=$resp" \
        --data-urlencode "RelayState=$relay" "$T/auth/saml/acs")
    hop "$4" "$fin" ;;
post)
    resp=$(sed -n 's/^SAMLResponse=//p' "$3"); relay=$(sed -n 's/^RelayState=//p' "$3")
    $C -c "$2" -b "$2" -o "$2.body" -w '%{http_code} %{redirect_url}' --data-urlencode "SAMLResponse=$resp" \
        --data-urlencode "RelayState=$relay" "$T/auth/saml/acs" ;;
get)
    $C -c "$2" -b "$2" -o "$2.body" -w '%{http_code} %{redirect_url}' "$T$3" ;;
esac
SCRIPT
}
