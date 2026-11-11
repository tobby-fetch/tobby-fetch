#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-6 crucible scenario, track B — enterprise identity (6.3),
# continuity when the identity provider is down (R-20, FR-078), and bounded
# credentials (R-14), on real nodes: a real Keycloak over HTTPS from a
# private CA, a real Tobby over HTTPS, and an outage that is a stopped
# machine rather than a flag.
#
#   1. Nodes: the IdP (the CI fixture's pinned image and realm, as an Incus
#      OCI container) and the instance; a throw-away PKI for both.
#   2. The instance starts while the IdP is STOPPED (FR-078): it serves,
#      the sign-in page states TBY-AUTH-016 naming the provider beside the
#      local form, the local admin signs in — with its dedicated
#      auth.local_under_federation record — and a token authorizes the API.
#   3. The IdP starts: the page drops the outage on its own; OIDC and SAML
#      sign-ins succeed for each role, through the private CA; the account
#      in no mapped group is refused (TBY-AUTH-017).
#   4. Attacks that need the real thing: a forged OIDC state, a replayed
#      SAML response, a response collected by another browser.
#   5. The IdP is stopped mid-session: the federated session survives,
#      tokens keep working; restarted, federated sign-in works again with
#      no restart of the instance.
#   6. R-14: a token created with an expiry 20 s ahead authorizes, then is
#      refused with TBY-AUTH-013 and a token.expire record; a session left
#      idle past auth.session.idleTimeout lands on the sign-in page with
#      TBY-AUTH-005 and a session.expire record.
#
# Topology:
#   tbc-m6b-idp    (OCI container, tbc-net)  Keycloak, HTTPS :8443
#   tbc-m6b-tobby  (container, tbc-net)      the instance, HTTPS :8443, and
#                                            the "browser" (curl) under test
#
# Requires, on top of the crucible baseline: an Incus with OCI image
# support (6.x) and access to quay.io for the pinned image; openssl and
# jq on the host.
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m6}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m6-b-auth.txt}"
. "$DIR/../../lib.sh"
. "$DIR/b-lib.sh"

WORK="$(mktemp -d)"
IDP=tbc-m6b-idp
NODE=tbc-m6b-tobby
TOBBY="https://$NODE.incus:8443"
ISSUER="https://$IDP.incus:8443/realms/tobby"
ADMIN=root
ADMIN_PW="m6b-local-break-glass"
LOG=/var/log/tobby.log
cleanup() {
    inc delete --force "$IDP" "$NODE" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
b_tooling
b_build "$WORK"
b_pki "$WORK" "$IDP.incus" "$NODE.incus"

# -- 1. Nodes -------------------------------------------------------------------
b_keycloak "$IDP" "$TOBBY"
inc stop --force "$IDP" >/dev/null 2>&1 || true
inc delete --force "$NODE" 2>/dev/null || true
inc launch "${CRUCIBLE_IMAGE:-images:alpine/3.24}" "$NODE" --profile tbc-connected-node >/dev/null
# shellcheck disable=SC2016 # expanded by the node's shell, not by this one
inc exec "$NODE" -- sh -c '
    n=0
    until apk add --no-cache curl >/dev/null 2>&1; do
        n=$((n + 1)); [ "$n" -ge 30 ] && exit 1; sleep 2
    done' || fail "installing curl on the node"
inc file push "$WORK/tobby" "$NODE/usr/bin/tobby" >/dev/null
inc exec "$NODE" -- chmod +x /usr/bin/tobby
inc exec "$NODE" -- mkdir -p /etc/tobby
for f in ca.crt sp.crt sp.key; do inc file push "$WORK/$f" "$NODE/etc/tobby/$f" >/dev/null; done
inc file push "$WORK/$NODE.incus.crt" "$NODE/etc/tobby/tls.crt" >/dev/null
inc file push "$WORK/$NODE.incus.key" "$NODE/etc/tobby/tls.key" >/dev/null
printf '%s' "tobby-oidc-test-secret" >"$WORK/oidc-secret"
inc file push "$WORK/oidc-secret" "$NODE/etc/tobby/oidc-secret" >/dev/null
b_browser_script >"$WORK/b-browser"
inc file push "$WORK/b-browser" "$NODE/usr/local/bin/b-browser" >/dev/null
inc exec "$NODE" -- chmod +x /usr/local/bin/b-browser
check "nodes ready: Keycloak (stopped for now) and the instance node"

# The pinned IdP metadata: fetched ONCE by the operator, checked, and
# written into the configuration — never fetched by the instance. Keycloak
# must run for that one fetch, then goes down again for step 2.
inc start "$IDP"
b_wait_idp "$NODE" "$ISSUER"
inc exec "$NODE" -- curl -fsS --cacert /etc/tobby/ca.crt -o /etc/tobby/idp.xml \
    "$ISSUER/protocol/saml/descriptor" || fail "fetching the IdP descriptor to pin"
inc exec "$NODE" -- grep -q X509Certificate /etc/tobby/idp.xml ||
    fail "the pinned descriptor carries no signing certificate"
inc stop --force "$IDP"
check "IdP metadata pinned from the descriptor (signing certificate present); IdP stopped"

cat >"$WORK/config.yaml" <<YAML
mode: passthrough
storage:
  root: /var/lib/tobby
state:
  root: /var/lib/tobby-state
server:
  addr: :8443
  tls:
    certFile: /etc/tobby/tls.crt
    keyFile: /etc/tobby/tls.key
network:
  tls:
    caFiles: [/etc/tobby/ca.crt]
auth:
  session:
    idleTimeout: 1m
  oidc:
    enabled: true
    displayName: Crucible directory
    issuer: $ISSUER
    clientID: tobby-oidc
    clientSecretFile: /etc/tobby/oidc-secret
    redirectURL: $TOBBY/auth/oidc/callback
    scopes: [profile]
    roles: &roles
      viewer: [tobby-viewer]
      operator: [tobby-operator]
      admin: [tobby-admin]
  saml:
    enabled: true
    displayName: Crucible SAML
    entityID: $TOBBY/auth/saml/metadata
    acsURL: $TOBBY/auth/saml/acs
    idpMetadataFile: /etc/tobby/idp.xml
    certFile: /etc/tobby/sp.crt
    keyFile: /etc/tobby/sp.key
    roles: *roles
YAML
inc file push "$WORK/config.yaml" "$NODE/etc/tobby/config.yaml" >/dev/null
inc exec "$NODE" -- sh -c "printf '%s\n' '$ADMIN_PW' |
    tobby user add $ADMIN --state-root /var/lib/tobby-state --password-stdin" >/dev/null ||
    fail "creating the local administrator"

# -- 2. Start with the IdP down (FR-078) -------------------------------------
inc exec "$NODE" -- sh -c "nohup tobby serve --config /etc/tobby/config.yaml >>$LOG 2>&1 &"
b_until 60 "the instance did not start with its IdP down" \
    inc exec "$NODE" -- curl -fsS --cacert /etc/tobby/ca.crt -o /dev/null "$TOBBY/readyz"
check "FR-078: the instance starts and is ready while the identity provider is down"

page_has() { inc exec "$NODE" -- sh -c "curl -s --cacert /etc/tobby/ca.crt $TOBBY/login | grep -q '$1'"; }
page_lacks() { ! page_has "$1"; }
b_until 60 "the sign-in page never stated the outage" page_has TBY-AUTH-016
page_has 'Crucible directory' || fail "the outage block does not name the provider"
page_has 'id="password"' || fail "the local form is gone while the provider is down"
check "R-20: the sign-in page states TBY-AUTH-016 naming the provider, local form kept"

B="inc exec $NODE -- env TOBBY_URL=$TOBBY b-browser"
local_login() { # <jar>
    inc exec "$NODE" -- sh -c "curl -s --cacert /etc/tobby/ca.crt -c $1 -b $1 -o /dev/null -w '%{http_code}' \
        --data-urlencode username=$ADMIN --data-urlencode password=$ADMIN_PW --data-urlencode next=/ $TOBBY/login"
}
[ "$(local_login /tmp/j-local)" = 303 ] || fail "the local administrator cannot sign in while the IdP is down"
inc exec "$NODE" -- grep -q '"action":"auth.local_under_federation"' "$LOG" ||
    fail "no auth.local_under_federation record for the break-glass sign-in"
TOKEN=$(inc exec "$NODE" -- curl -fsS --cacert /etc/tobby/ca.crt -u "$ADMIN:$ADMIN_PW" \
    -H 'Content-Type: application/json' -d '{"name":"ci","role":"viewer"}' "$TOBBY/api/v1/tokens" | jq -r .secret)
[ -n "$TOKEN" ] && [ "$TOKEN" != null ] || fail "creating a token while the IdP is down"
api_code() { inc exec "$NODE" -- curl -s --cacert /etc/tobby/ca.crt -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer $1" "$TOBBY/api/v1/tasks"; }
[ "$(api_code "$TOKEN")" = 200 ] || fail "a token does not authorize the API while the IdP is down"
check "R-20: local admin signed in (auth.local_under_federation recorded), token authorizes the API"

# -- 3. The IdP comes up: every role, both protocols -------------------------
inc start "$IDP"
b_wait_idp "$NODE" "$ISSUER"
b_until 60 "the sign-in page still states the outage after the IdP came back" page_lacks data-idp-unavailable
check "the outage block cleared on its own, no restart"

role_of() { # <jar>
    for p in /admin/accounts:admin /import:operator /:viewer; do
        set -- "$1" "${p%%:*}" "${p##*:}"
        code=$($B get "$1" "$2" | cut -d' ' -f1)
        [ "$code" = 200 ] && { echo "$3"; return; }
    done
    echo none
}
for u in viewer operator admin; do
    for proto in oidc saml; do
        jar="/tmp/j-$proto-$u"
        res=$($B "$proto" "$u" "$u-test-pw" "$jar")
        [ "${res%% *}" = 303 ] || fail "$proto sign-in as $u: $res"
        got=$(role_of "$jar")
        [ "$got" = "$u" ] || fail "$proto sign-in as $u holds role $got"
    done
done
check "6.3: OIDC and SAML sign-in succeed for viewer, operator and admin, mapped by group"
for proto in oidc saml; do
    res=$($B "$proto" nogroup nogroup-test-pw "/tmp/j-$proto-nogroup")
    [ "${res%% *}" = 403 ] || fail "$proto: the account in no mapped group got $res, want 403"
done
check "6.3: the account in no mapped group is refused on both protocols (TBY-AUTH-017)"

# -- 4. Attacks ----------------------------------------------------------------
res=$($B get /tmp/j-forged "/auth/oidc/callback?code=x&state=forged")
[ "${res%% *}" = 401 ] || fail "a forged OIDC state answered $res, want 401"
inc exec "$NODE" -- grep -q TBY-AUTH-014 /tmp/j-forged.body || fail "the forged-state page misses TBY-AUTH-014"
$B samlpost admin admin-test-pw /tmp/j-replay /tmp/form-replay >/dev/null || fail "collecting a SAML response"
res=$($B post /tmp/j-replay /tmp/form-replay)
[ "${res%% *}" = 303 ] || fail "the first use of the SAML response answered $res"
res=$($B post /tmp/j-thief /tmp/form-replay)
[ "${res%% *}" = 401 ] || fail "a replayed SAML response answered $res, want 401"
$B samlpost admin admin-test-pw /tmp/j-victim /tmp/form-stolen >/dev/null || fail "collecting a SAML response"
fin=$($B post /tmp/j-thief2 /tmp/form-stolen | cut -d' ' -f2)
res=$($B get /tmp/j-thief2 "${fin#"$TOBBY"}")
[ "${res%% *}" = 401 ] || fail "a SAML response collected by another browser answered $res, want 401"
check "attacks refused: forged OIDC state (014), replayed SAML response, response finished by another browser (015)"

# -- 5. The IdP goes down mid-session ------------------------------------------
inc stop --force "$IDP"
[ "$(role_of /tmp/j-oidc-admin)" = admin ] || fail "the outage ended an existing federated session"
[ "$(api_code "$TOKEN")" = 200 ] || fail "a token stopped authorizing during the outage"
b_until 60 "the sign-in page never stated the mid-session outage" page_has TBY-AUTH-016
inc start "$IDP"
b_wait_idp "$NODE" "$ISSUER"
res=$($B oidc viewer viewer-test-pw /tmp/j-after)
[ "${res%% *}" = 303 ] && [ "$(role_of /tmp/j-after)" = viewer ] ||
    fail "federated sign-in does not work again after the outage: $res"
check "R-20: outage mid-session — session and token unaffected; federated sign-in back without a restart"

# -- 6. R-14: token and session expiry -------------------------------------------
EXP=$(date -u -d '+20 seconds' +%Y-%m-%dT%H:%M:%SZ)
SHORT=$(inc exec "$NODE" -- curl -fsS --cacert /etc/tobby/ca.crt -u "$ADMIN:$ADMIN_PW" \
    -H 'Content-Type: application/json' -d "{\"name\":\"short\",\"role\":\"viewer\",\"expires\":\"$EXP\"}" \
    "$TOBBY/api/v1/tokens" | jq -r .secret)
[ "$(api_code "$SHORT")" = 200 ] || fail "the expiring token does not authorize before its expiry"
sleep 25
code=$(inc exec "$NODE" -- curl -s --cacert /etc/tobby/ca.crt -o /tmp/expired.json -w '%{http_code}' \
    -H "Authorization: Bearer $SHORT" "$TOBBY/api/v1/tasks")
# shellcheck disable=SC2015 # any step of the chain failing is the failure: fail is meant to run
[ "$code" = 401 ] && inc exec "$NODE" -- grep -q TBY-AUTH-013 /tmp/expired.json ||
    fail "the expired token answered $code without TBY-AUTH-013"
inc exec "$NODE" -- grep -q '"action":"token.expire"' "$LOG" || fail "no token.expire record"
[ "$(api_code "$TOKEN")" = 200 ] || fail "the long-lived token stopped working"
check "R-14: a token authorizes until its expiry, then TBY-AUTH-013 with a token.expire record; long-lived token unchanged"

[ "$(local_login /tmp/j-idle)" = 303 ] || fail "signing in for the idle test"
sleep 70
res=$($B get /tmp/j-idle /tasks)
case "$res" in
    "303 "*"/login?next=%2Ftasks&expired=1") ;;
    *) fail "an idle session answered $res, want 303 to the sign-in page with the expiry marker" ;;
esac
inc exec "$NODE" -- grep '"action":"session.expire"' "$LOG" | grep -q '"target":"idle"' ||
    fail "no session.expire record with reason idle"
check "R-14: a session idle past auth.session.idleTimeout ends with TBY-AUTH-005 and a session.expire (idle) record"

cleanup
trap - EXIT
check "scenario m6 b-auth complete — report: $REPORT"
