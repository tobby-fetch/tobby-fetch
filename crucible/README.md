# Crucible — realistic tier of the target-environment replication

The crucible is a disposable, versioned replica of the deployment reality —
a connected zone, an air-gapped zone, a removable medium that is a real
block device — managed by [Incus](https://linuxcontainers.org/incus/)
(ADR-0014). It complements the hermetic CI topology
([`test/topology/`](../test/topology/)): **CI gates merges, the crucible
gates milestone acceptance.**

## Requirements

- A Linux host. Milestone-1 scenarios run entirely in system containers, so
  any Linux host works — including cloud VMs without nested virtualization
  (validated on a disposable Hetzner cpx32). Scenarios of later milestones
  that need per-node kernels (filesystem-limit and power-loss media cases,
  RKE2 nodes) use the VM profiles and therefore need KVM (`/dev/kvm`);
  macOS and Windows drive the crucible remotely over the Incus API, never
  host it.
- Incus installed and initialized (`incus admin init`), with a ZFS or btrfs
  storage pool for instant snapshot-based scenario reset (`dir` works but
  resets are slow).
- Root on the crucible host (losetup for the media loop device — the same
  bar as Incus administration).
- Host tooling used by the scenarios, taken from the environment and never
  downloaded at run time (NFR-019): `go`, `jq`, `curl`, `openssl`, and
  **`helm`** — milestone 4 deploys its instance from the reference chart
  (`deploy/charts/tobby`), so a rendering tool is part of the harness, not
  a convenience. `cosign` is the one exception: m3 and m4 install it on
  demand with `go install` when it is absent, because the signatures they
  produce are the point of those scenarios.

## Layout

```
crucible/
├── setup.sh          # one-time: project, networks, profiles, volumes
├── teardown.sh       # destroys the whole crucible project
├── run.sh            # scenario runner: ./run.sh m1 [m2 …] | all
├── lib.sh            # shared helpers (checks, raw report)
└── scenarios/
    ├── m1/run.sh     # milestone-1 reduced scenario
    ├── m2/run.sh     # milestone-2 secure journey
    ├── m3/run.sh     # milestone-3 recipe engine
    └── m4/run.sh     # milestone-4 passthrough / promotion
```

Everything lives in the dedicated Incus project `tobby-crucible`; the host's
default project is never touched.

## Networks — the air gap is absence of path

| Network | Definition | Purpose |
|---|---|---|
| `tbc-net` | managed bridge, NAT enabled | connected zone (may reach upstream fixtures) |
| `tbc-airgap` | managed bridge, no uplink, NAT disabled, **ACL `tbc-airgap-noegress`** | air-gapped zone — only intra-zone traffic exists |

`ipv4.nat=false` alone is **not** a gap: the host kernel still routes
between its bridges. The ACL is what makes the gap structural — intra-zone
traffic allowed, everything else rejected in both directions. Every
scenario run still starts with the **egress canary**: an instance on
`tbc-airgap` attempts outbound traffic, and the scenario fails unless the
attempt fails. (Known residual channel: the bridge's dnsmasq still
forwards DNS resolution upstream; hardening tracked for a later
milestone.)

## The medium is a real block device

Scenarios create a loop-backed block device on the host and pass it
through as a `unix-block` device: Incus cannot attach custom block-type
storage volumes to containers, and the disposable acceptance VM may have
no nested KVM for Incus VMs. Guests format and mount it themselves through
seccomp mount interception
(`security.syscalls.intercept.mount.allowed=ext4`). The device is
attached to the connected node, filled, detached, and re-attached to the
air-gapped node — real attach/detach block-device semantics either way.

## Scenarios — tagged by milestone, replayable forever

Writing its crucible scenario(s) is part of every milestone's definition of
done (DEVELOPMENT-PLAN §5, item 8). Scenarios are independently invocable
and the suite of all completed milestones stays runnable at any time:

```sh
./crucible/run.sh m1          # just milestone 1
./crucible/run.sh all         # every completed milestone
```

Each run writes a raw check report (`crucible-report-<scenario>.txt`);
milestone acceptance publishes that report unedited.

### m1 — foundations (reduced scenario)

Registry push/pull across the topology, egress canary, media round-trip:

1. Connected node runs `tobby serve` (passthrough) with its store on the
   attached block volume; a multi-arch index is pushed to the embedded
   registry under a relocated name and pulled back digest-identical.
2. Canary proves the air-gapped bridge has no route out.
3. The volume is detached, re-attached to the air-gapped node; `tobby
   serve` (mirror) on the transported store serves the content inside the
   zone (FR-050: the store is self-contained and relocatable).

### m4 — passthrough between connected zones

Continuous promotion on real nodes, with the four properties this tier
exists for — real cosign signatures re-verified before every push, a real
TLS certificate issued by a private authority, a real authenticated
forward proxy, and a real network ACL that makes direct egress impossible
rather than merely unused:

1. The instance under test is **deployed from the reference chart**:
   `helm template deploy/charts/tobby` renders its configuration, and that
   rendering — not a hand-written file — is what the node reads. The
   rendered pod's hardening is asserted at the same time.
2. Secure by default: anonymous is refused on the API and the embedded
   registry; the documented FR-075 override, active on the fixture
   registry, opens access and shows the permanent danger banner.
3. One cycle resolves the signed recipe, fetches what is missing,
   re-verifies the signature over the exact bytes about to leave, and
   pushes them to the zone registry with the recipe itself (FR-013,
   FR-028, FR-033, FR-034). The **second cycle pushes nothing**.
4. The cadence is changed on the running instance, a scheduled cycle
   actually fires under the new interval, and the override outlives a
   restart (FR-013, audited per FR-094).
5. A destination outside the allow-list is refused **before any byte
   moves**, with the dedicated code on the audit log and on the metrics —
   and the refused recipe never appears at the destination (FR-030).
6. The same promotion with **direct egress blocked**: a second node whose
   interface ACL permits nothing but the proxy, reaching both registries
   entirely through an authenticated forward proxy, over a certificate
   issued by the scenario's own authority (FR-080, FR-081).

The forward proxy is built from this repository (`test/topology/proxy`)
rather than pulled: the crucible installs nothing at scenario time, and a
proxy of our own doubles as a witness — it logs every destination it was
asked to reach, so "the traffic went through the proxy" is observed
rather than assumed.
