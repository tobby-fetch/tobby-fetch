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
# The VM is driven through a mailbox on a host directory shared with it
# (virtiofs), not through `incus exec`: the incus-agent refuses every new
# connection once the guest clock is earlier than the NotBefore of the
# client certificate Incus generated when it created the VM ("Not
# authorized"), which is exactly the state this scenario puts the guest
# in. The share is a host-to-guest channel like the agent's: it opens no
# route out of the isolated zone.
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

# -- The VM's control channel ----------------------------------------------------------------
CTL="$WORK/ctl"
# vm <command> [<arg>...]: run a command on the VM, as `incus exec` would —
# stdout and stderr relayed, its exit status returned — through the
# mailbox the guest-side runner polls.
vm() {
    _cmd=""
    for _a in "$@"; do
        _cmd="$_cmd '$(printf '%s' "$_a" | sed "s/'/'\\\\''/g")'"
    done
    _id=$(date +%s%N)
    printf '%s\n' "$_cmd" >"$CTL/req/$_id.tmp" && mv "$CTL/req/$_id.tmp" "$CTL/req/$_id.sh"
    _n=0
    until [ -f "$CTL/done/$_id.rc" ]; do
        _n=$((_n + 1))
        [ "$_n" -gt 3000 ] && fail "the VM never answered the request: $*"
        sleep 0.2
    done
    cat "$CTL/done/$_id.out"
    cat "$CTL/done/$_id.err" >&2
    return "$(cat "$CTL/done/$_id.rc")"
}
# vm_push <host file> <guest path>: a file onto the VM, through the share.
vm_push() {
    _b="$(date +%s%N)-$(basename "$1")"
    cp "$1" "$CTL/xfer/$_b" || fail "staging $1 for the VM"
    { vm mkdir -p "$(dirname "$2")" && vm cp "/mnt/ctl/xfer/$_b" "$2"; } || fail "pushing $2 to the VM"
    rm -f "$CTL/xfer/$_b"
}
# vm_launch: the air-gapped VM, its share, its runner and the binary. The
# agent is used once, while the guest clock is still right, to start the
# runner.
vm_launch() {
    inc delete --force "$VM" 2>/dev/null || true
    mkdir -p "$CTL/req" "$CTL/done" "$CTL/xfer"
    cat >"$CTL/runner.sh" <<'RUNNER'
cd /mnt/ctl || exit 1
while :; do
    for f in req/*.sh; do
        [ -e "$f" ] || continue
        id=${f#req/}; id=${id%.sh}
        sh "$f" >"done/$id.out" 2>"done/$id.err" </dev/null
        echo "$?" >"done/$id.rc.tmp"
        rm -f "$f"
        mv "done/$id.rc.tmp" "done/$id.rc"
    done
    sleep 0.2
done
RUNNER
    # The Alpine VM image is not signed for UEFI Secure Boot (it carries
    # requirements.secureboot=false): Incus refuses it otherwise.
    inc init "${CRUCIBLE_IMAGE:-images:alpine/3.24}" "$VM" --vm --profile tbc-airgap-node \
        -c security.secureboot=false >/dev/null || fail "launching $VM"
    inc config device add "$VM" ctl disk source="$CTL" path=/mnt/ctl >/dev/null ||
        fail "sharing the control directory with $VM"
    inc start "$VM" >/dev/null || fail "starting $VM"
    _n=0
    until inc exec "$VM" -- test -f /mnt/ctl/runner.sh >/dev/null 2>&1; do
        _n=$((_n + 1))
        [ "$_n" -gt 120 ] && fail "$VM: the guest agent never came up with the share mounted"
        sleep 1
    done
    inc exec "$VM" -- sh -c 'setsid nohup sh /mnt/ctl/runner.sh </dev/null >/dev/null 2>&1 &' ||
        fail "starting the control runner on $VM"
    vm true || fail "the control channel of $VM does not answer"
    vm_push "$WORK/tobby" /usr/bin/tobby
    vm chmod +x /usr/bin/tobby
}

# set_clock <epoch>: set the VM's clock and prove it was set. The value is
# formatted here, on the crucible host, so the guest only has to parse the
# portable "YYYY-MM-DD hh:mm:ss".
set_clock() {
    _want="$1"
    vm date -u -s "$(date -u -d "@$_want" '+%Y-%m-%d %H:%M:%S')" >/dev/null ||
        fail "setting the guest clock"
    _got=$(vm date -u +%s)
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

stop_vm_instance() { vm sh -c 'pkill -f "[t]obby serve" || true; sleep 2'; }

# -- 1. The VM, its clock before the build ------------------------------------------------
vm_launch
VM_IP=$(instance_ip "$VM")
vm mkdir -p /etc/tobby /var/lib/tobby /var/lib/tobby-state
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
vm_push "$WORK/config.yaml" /etc/tobby/config.yaml

TEN_DAYS=$((10 * 86400))
set_clock $((BUILD_EPOCH - TEN_DAYS))
check "the guest clock is set ten days before the build date and reads it (date: $(vm date -u '+%Y-%m-%d %H:%M'))"

vm sh -c 'nohup tobby serve --config /etc/tobby/config.yaml >/var/log/tobby.log 2>&1 &'
wait_up
curl -s -o /dev/null -f "http://$VM_IP:8080/readyz" || fail "the instance is not ready: a clock in doubt must change no verdict"
check "the instance is ready: a clock in doubt changes no verdict"

vm grep -q '"code":"TBY-CLK-001"' /var/log/tobby.log || fail "no TBY-CLK-001 startup record"
vm sh -c "grep '\"code\":\"TBY-CLK-001\"' /var/log/tobby.log" | jq -e '.level == "WARN" and .anchor == "build"' >/dev/null ||
    fail "the TBY-CLK-001 record is not a build-anchor warning"
vm sh -c "grep '\"action\":\"clock.implausible\"' /var/log/tobby.log" | jq -e '.target == "build" and (.detail | test("gap=2[34][0-9]h"))' >/dev/null ||
    fail "no clock.implausible audit event naming the build and a gap of about ten days"
curl -s "http://$VM_IP:8080/" | grep -q 'TBY-CLK-001' || fail "the UI shows no clock banner"
curl -s "http://$VM_IP:8080/metrics" | grep -q '^tobby_clock_implausible{anchor="build"} 1' || fail "tobby_clock_implausible{anchor=build} is not 1"
curl -s "http://$VM_IP:8080/api/v1/clock" | jq -e '.verdicts[0].implausible == true and .verdicts[0].code == "TBY-CLK-001"' >/dev/null ||
    fail "GET /api/v1/clock does not report the implausible build verdict"
AFTER=$(vm date -u +%s)
[ "$AFTER" -lt $((BUILD_EPOCH - TEN_DAYS + 600)) ] || fail "the clock moved: something corrected it"
check "FR-096: a clock ten days before the build shows the log record, the banner, the metric, the audit event and the API verdict (TBY-CLK-001); nothing was corrected"

# Within the tolerance: nothing is raised.
stop_vm_instance
set_clock $((BUILD_EPOCH - 20 * 60))
vm sh -c 'nohup tobby serve --config /etc/tobby/config.yaml >/var/log/tobby.log 2>&1 &'
wait_up
if vm grep -q 'TBY-CLK' /var/log/tobby.log; then fail "a clock 20 minutes behind raised something"; fi
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
vm_push "$WORK/medium.tar" /tmp/medium.tar
vm sh -c 'rm -rf /srv/medium && mkdir -p /srv/medium && cd /srv/medium && tar -xpf /tmp/medium.tar' ||
    fail "unpacking the medium on the VM"
# The destination trusts the key the recipe is signed with: a medium whose
# verification is blocked (an unverifiable recipe) is no anchor to judge
# the clock against (FR-096), and would prove nothing here.
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
trust:
  roots:
    - name: a
      keyFile: /etc/tobby/a.pub
YAML
vm_push "$WORK/dest.yaml" /etc/tobby/dest.yaml
vm_push "$WORK/a.pub" /etc/tobby/a.pub
check "a medium ($MEDIA_ID) was produced in the connected zone, resolved at $RESOLVED_AT, and carried to the VM"

# Three hours behind its production: beyond the one-hour tolerance.
set_clock $((RESOLVED_EPOCH - 3 * 3600))
vm sh -c 'tobby media verify --config /etc/tobby/dest.yaml --output json >/tmp/verify.out 2>/tmp/verify.err; true'
VERDICT=$(vm cat /tmp/verify.out | jq -r '.verdict // empty')
[ "$VERDICT" != blocked ] && [ -n "$VERDICT" ] ||
    fail "the medium's verification is '$VERDICT' on the VM: $(vm tail -5 /tmp/verify.err)"
vm grep -q 'TBY-CLK-002' /tmp/verify.err ||
    fail "no TBY-CLK-002 warning on the error output of tobby media verify: $(vm tail -5 /tmp/verify.err; vm head -c 600 /tmp/verify.out)"
vm grep -q "$MEDIA_ID" /tmp/verify.err || fail "the warning does not name the medium"
if vm grep -q 'CLK' /tmp/verify.out; then fail "the warning polluted the report on the standard output"; fi
vm sh -c "grep '\"action\":\"clock.implausible\"' /tmp/verify.err" | jq -e --arg id "$MEDIA_ID" '.target == $id' >/dev/null ||
    fail "no clock.implausible audit event naming the medium"

# The same medium on the running instance: banner and API, through the
# verification the Media screen and POST /api/v1/media/verify start.
vm sh -c 'nohup tobby serve --config /etc/tobby/dest.yaml >/var/log/tobby-dest.log 2>&1 &'
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
vm sh -c 'tobby media verify --config /etc/tobby/dest.yaml --output json >/tmp/verify.out 2>/tmp/verify.err; true'
if vm grep -q 'CLK\|clock.implausible' /tmp/verify.err; then fail "a clock 20 minutes behind the medium raised something"; fi
check "a clock 20 minutes behind the medium's production raises nothing"
