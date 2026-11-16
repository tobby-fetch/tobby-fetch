#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright © 2026 infraBuilder SASU and contributors
#
# Milestone-7 crucible scenario, track B — clock plausibility in an isolated
# zone (FR-096, R-32), on a real VM whose clock the scenario sets:
#
#   1. An air-gapped VM (own kernel, own clock) is set BEFORE the build date
#      of the binary: the instance starts, and the startup log record, the
#      persistent banner, the metric and a clock.implausible audit event name
#      the gap (TBY-CLK-001). Nothing is corrected, no verdict changes: the
#      instance is ready, and the VM's clock still reads what was set.
#   2. A medium produced in the connected zone is opened on the destination VM
#      whose clock is set behind the medium's resolution time beyond the
#      tolerance: TBY-CLK-002 on the error output of `tobby media verify`,
#      on the banner of the running instance and in the audit log, naming the
#      medium.
#   3. Within the tolerance (clock 20 minutes behind), nothing is raised.
#   4. The scenario proves its own manipulation first: `date` on the guest
#      reads the value it set before Tobby is asked anything.
#
# The binary is stamped with the build date the way the release stamps it
# (ldflags, ADR-0011): a development build carries none, and with no anchor
# the check proves nothing.
#
# Topology:
#   tbc-m7b-source  (container, tbc-net)     source registry + cookbook
#   tbc-m7b-mirror  (container, tbc-net)     produces the medium
#   tbc-m7b-airgap  (VM, tbc-airgap)         destination, clock under test
#
# Run on its own (SCENARIO is set by crucible/run.sh when this is dispatched
# from m7/run.sh):  sh crucible/scenarios/m7/b-clock.sh
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"
SCENARIO="${SCENARIO:-m7}"
CRUCIBLE_REPORT="${CRUCIBLE_REPORT:-crucible-report-m7-b-clock.txt}"
. "$DIR/../../lib.sh"
. "$DIR/b-lib.sh"
. "$DIR/../m6/c-lib.sh"

WORK="$(mktemp -d)"
SOURCE=tbc-m7b-source
MIRROR=tbc-m7b-mirror
VM=tbc-m7b-airgap
cleanup() {
    inc delete --force "$SOURCE" "$MIRROR" "$VM" 2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

report_start
b7_tooling
c_tooling

BUILD_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_EPOCH=$(date -u -d "$BUILD_AT" +%s)
b7_build "$WORK" "$BUILD_AT"

# set_clock <epoch>: set the VM's clock and prove it was set. The value is
# formatted here, on the crucible host, so the guest only has to parse the
# portable "YYYY-MM-DD hh:mm:ss".
set_clock() {
    _want="$1"
    inc exec "$VM" -- date -u -s "$(date -u -d "@$_want" '+%Y-%m-%d %H:%M:%S')" >/dev/null ||
        fail "setting the guest clock"
    _got=$(inc exec "$VM" -- date -u +%s)
    _delta=$((_got - _want))
    if [ "$_delta" -lt 0 ] || [ "$_delta" -ge 30 ]; then
        fail "the guest clock reads $_got after being set to $_want: the manipulation did not happen"
    fi
}

# wait_up: the instance on the VM answers its liveness probe.
wait_up() {
    _n=0
    until curl -s -o /dev/null -f "http://$VM_IP:8080/healthz"; do
        _n=$((_n + 1))
        [ "$_n" -gt 60 ] && fail "the instance never answered on the VM"
        sleep 1
    done
}

stop_vm_instance() { inc exec "$VM" -- sh -c 'pkill -f "[t]obby serve" || true; sleep 2'; }

# -- 1. The VM, its clock before the build ------------------------------------------------
b7_launch "$VM" tbc-airgap-node --vm
VM_IP=$(instance_ip "$VM")
inc exec "$VM" -- mkdir -p /etc/tobby /var/lib/tobby /var/lib/tobby-state
cat >"$WORK/config.yaml" <<YAML
mode: mirror
zone: zone-c
storage:
  root: /var/lib/tobby
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
auth:
  disabled: true
YAML
inc file push "$WORK/config.yaml" "$VM/etc/tobby/config.yaml" >/dev/null

TEN_DAYS=$((10 * 86400))
set_clock $((BUILD_EPOCH - TEN_DAYS))
check "the guest clock is set ten days before the build date and reads it (date: $(inc exec "$VM" -- date -u '+%Y-%m-%d %H:%M'))"

inc exec "$VM" -- sh -c 'nohup tobby serve --config /etc/tobby/config.yaml >/var/log/tobby.log 2>&1 &'
wait_up
curl -s -o /dev/null -f "http://$VM_IP:8080/readyz" || fail "the instance is not ready: a clock in doubt must change no verdict"
check "the instance is ready: a clock in doubt changes no verdict"

inc exec "$VM" -- grep -q '"code":"TBY-CLK-001"' /var/log/tobby.log || fail "no TBY-CLK-001 startup record"
inc exec "$VM" -- sh -c "grep '\"code\":\"TBY-CLK-001\"' /var/log/tobby.log" | jq -e '.level == "WARN" and .anchor == "build"' >/dev/null ||
    fail "the TBY-CLK-001 record is not a build-anchor warning"
inc exec "$VM" -- sh -c "grep '\"action\":\"clock.implausible\"' /var/log/tobby.log" | jq -e '.target == "build" and (.detail | test("gap=2[34][0-9]h"))' >/dev/null ||
    fail "no clock.implausible audit event naming the build and a gap of about ten days"
curl -s "http://$VM_IP:8080/" | grep -q 'TBY-CLK-001' || fail "the UI shows no clock banner"
curl -s "http://$VM_IP:8080/metrics" | grep -q '^tobby_clock_implausible{anchor="build"} 1' || fail "tobby_clock_implausible{anchor=build} is not 1"
curl -s "http://$VM_IP:8080/api/v1/clock" | jq -e '.verdicts[0].implausible == true and .verdicts[0].code == "TBY-CLK-001"' >/dev/null ||
    fail "GET /api/v1/clock does not report the implausible build verdict"
AFTER=$(inc exec "$VM" -- date -u +%s)
[ "$AFTER" -lt $((BUILD_EPOCH - TEN_DAYS + 600)) ] || fail "the clock moved: something corrected it"
check "FR-096: a clock ten days before the build shows the log record, the banner, the metric, the audit event and the API verdict (TBY-CLK-001); nothing was corrected"

# Within the tolerance: nothing is raised.
stop_vm_instance
set_clock $((BUILD_EPOCH - 20 * 60))
inc exec "$VM" -- sh -c 'nohup tobby serve --config /etc/tobby/config.yaml >/var/log/tobby.log 2>&1 &'
wait_up
if inc exec "$VM" -- grep -q 'TBY-CLK' /var/log/tobby.log; then fail "a clock 20 minutes behind raised something"; fi
curl -s "http://$VM_IP:8080/metrics" | grep -q '^tobby_clock_implausible{anchor="build"} 0' || fail "the metric is not 0 within the tolerance"
if curl -s "http://$VM_IP:8080/" | grep -q 'TBY-CLK'; then fail "a banner within the tolerance"; fi
check "a clock 20 minutes behind the build (tolerance 1h) raises nothing: metric 0, no record, no banner"
stop_vm_instance

# -- 2. A medium, opened on a clock behind its production ----------------------------------------
c_launch "$SOURCE" tbc-connected-node
c_launch "$MIRROR" tbc-connected-node
SOURCE_IP=$(instance_ip "$SOURCE")
c_serve_source "$SOURCE"
c_keypair a
IMG=$(c_seed_image "$SOURCE_IP:8080/library/clock:1.0.0")
c_publish "$SOURCE_IP:8080" clock-app clock "$SOURCE_IP:8080/library/clock" "$IMG" a >/dev/null
cat >"$WORK/retriever.yaml" <<YAML
apiVersion: recipe.tobby.dev/v1alpha1
kind: Retriever
metadata:
  name: zone-c
spec:
  cookbook: $SOURCE_IP:8080/cookbook
  recipes:
    - name: clock-app
      version: "1.0.0"
YAML
inc file push "$WORK/retriever.yaml" "$MIRROR/etc/tobby/retriever.yaml" --create-dirs >/dev/null
inc file push "$WORK/a.pub" "$MIRROR/etc/tobby/a.pub" --create-dirs >/dev/null
c_config "$MIRROR" mirror /var/lib/tobby "$SOURCE_IP" '  roots:
    - name: a
      keyFile: /etc/tobby/a.pub'
c_start "$MIRROR"
inc exec "$MIRROR" -- sh -c 'tobby sync --wait' >/dev/null 2>&1 || fail "the mirror synchronization failed"
c_stop "$MIRROR"
inc exec "$MIRROR" -- sh -c 'cat /var/lib/tobby/meta/media.json' >"$WORK/media.json"
RESOLVED_AT=$(jq -r .resolvedAt "$WORK/media.json")
MEDIA_ID=$(jq -r .mediaId "$WORK/media.json")
[ -n "$MEDIA_ID" ] && [ "$MEDIA_ID" != null ] || fail "the medium carries no identifier"
RESOLVED_EPOCH=$(date -u -d "$RESOLVED_AT" +%s)
inc exec "$MIRROR" -- sh -c 'cd /var/lib/tobby && tar -cpf /tmp/medium.tar .' || fail "packing the medium"
b7_pull "$MIRROR" /tmp/medium.tar "$WORK/medium.tar"
inc file push "$WORK/medium.tar" "$VM/tmp/medium.tar" >/dev/null
inc exec "$VM" -- sh -c 'rm -rf /srv/medium && mkdir -p /srv/medium && cd /srv/medium && tar -xpf /tmp/medium.tar' ||
    fail "unpacking the medium on the VM"
cat >"$WORK/dest.yaml" <<YAML
mode: mirror
zone: zone-c
storage:
  root: /srv/medium
state:
  root: /var/lib/tobby-state
server:
  addr: :8080
auth:
  disabled: true
YAML
inc file push "$WORK/dest.yaml" "$VM/etc/tobby/dest.yaml" >/dev/null
check "a medium ($MEDIA_ID) was produced in the connected zone, resolved at $RESOLVED_AT, and carried to the VM"

# Three hours behind its production: beyond the one-hour tolerance.
set_clock $((RESOLVED_EPOCH - 3 * 3600))
inc exec "$VM" -- sh -c 'tobby media verify --config /etc/tobby/dest.yaml --output json >/tmp/verify.out 2>/tmp/verify.err; true'
inc exec "$VM" -- grep -q 'TBY-CLK-002' /tmp/verify.err || fail "no TBY-CLK-002 warning on the error output of tobby media verify"
inc exec "$VM" -- grep -q "$MEDIA_ID" /tmp/verify.err || fail "the warning does not name the medium"
if inc exec "$VM" -- grep -q 'CLK' /tmp/verify.out; then fail "the warning polluted the report on the standard output"; fi
inc exec "$VM" -- sh -c "grep '\"action\":\"clock.implausible\"' /tmp/verify.err" | jq -e --arg id "$MEDIA_ID" '.target == $id' >/dev/null ||
    fail "no clock.implausible audit event naming the medium"

# The same medium on the running instance: banner and API, through the
# verification the Media screen and POST /api/v1/media/verify start.
inc exec "$VM" -- sh -c 'nohup tobby serve --config /etc/tobby/dest.yaml >/var/log/tobby-dest.log 2>&1 &'
wait_up
curl -s -X POST "http://$VM_IP:8080/api/v1/media/verify" -H 'Content-Type: application/json' -d '{}' >/dev/null
curl -s "http://$VM_IP:8080/api/v1/clock" |
    jq -e --arg id "$MEDIA_ID" '[.verdicts[] | select(.anchor == "medium")][0] | .implausible == true and .medium == $id and .code == "TBY-CLK-002"' >/dev/null ||
    fail "GET /api/v1/clock does not report the implausible medium verdict"
curl -s "http://$VM_IP:8080/" | grep -q "TBY-CLK-002" || fail "the UI shows no banner for the medium"
check "FR-096: a medium opened on a clock three hours behind its production — TBY-CLK-002 on stderr naming it, the audit event, the API verdict and the banner"

# Within the tolerance: nothing.
stop_vm_instance
set_clock $((RESOLVED_EPOCH - 20 * 60))
inc exec "$VM" -- sh -c 'tobby media verify --config /etc/tobby/dest.yaml --output json >/tmp/verify.out 2>/tmp/verify.err; true'
if inc exec "$VM" -- grep -q 'CLK\|clock.implausible' /tmp/verify.err; then fail "a clock 20 minutes behind the medium raised something"; fi
check "a clock 20 minutes behind the medium's production raises nothing"
