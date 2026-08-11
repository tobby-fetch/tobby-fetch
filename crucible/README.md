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

## Layout

```
crucible/
├── setup.sh          # one-time: project, networks, profiles, volumes
├── teardown.sh       # destroys the whole crucible project
├── run.sh            # scenario runner: ./run.sh m1 [m2 …] | all
├── lib.sh            # shared helpers (checks, raw report)
└── scenarios/
    └── m1/run.sh     # milestone-1 reduced scenario
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
