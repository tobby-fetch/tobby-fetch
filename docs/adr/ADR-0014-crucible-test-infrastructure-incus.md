# ADR-0014 — Crucible test infrastructure: Incus

## Status

Accepted — 2026-08-04

## Context

The project's quality model mandates, from the first milestone, a **crucible**:
a disposable, versioned replica of the target environment where every release
train proves the real scenarios end to end — a connected node pulling recipes
from public and private registries, physical-transport simulation, an
air-gapped node with a zone registry and an RKE2 cluster, applications
actually deployed at the end of the chain. The crucible complements, not
replaces, the hermetic CI tier (container topologies run on every PR): CI
gates merges, the crucible gates milestone acceptance.

What the realistic tier needs that containers cannot honestly provide:

- **Kernel-level fidelity** — removable-media filesystems (FAT32/exFAT
  limits, dirty ejection), power-loss and crash-resilience scenarios
  (NFR-010), RKE2 nodes with their own kernels and sysctls;
- **air-gap by construction** — an isolated network with *no route out*,
  provable by an egress canary that must fail;
- **a removable medium that is a real device** — attach, format, fill,
  detach, re-attach to the isolated side, as a block device;
- **fast scenario reset** — dozens of scenario runs per session make
  re-provisioning cost the dominant factor;
- **mixed density** — fidelity nodes (VMs) alongside cheap fixture nodes
  (registries, IdP, retriever HTTP host) that need no kernel of their own;
- **remote operation and IaaS independence** — the team develops on macOS
  (Apple Silicon); the definition must run identically on a local Linux box,
  a lab machine, or a disposable nested-virt cloud VM, tied to no
  hypervisor product or cloud API.

## Decision

**Incus manages the crucible's realistic tier.** Incus (Linux Containers'
community continuation of LXD; Apache-2.0, LTS releases) drives both **system
containers** and **KVM virtual machines** behind one API, CLI, and image
service.

1. **Canonical target: any Linux host with KVM.** The crucible definition —
   profiles, networks, storage pool, scenario scripts — is versioned in the
   repository. Three interchangeable execution profiles: local Linux machine,
   lab host, disposable nested-virtualization cloud VM (bootstrapped by IaC,
   destroyed after the run). SSH is needed only to install Incus; everything
   else goes through the Incus HTTPS API (client available on macOS and in CI).
2. **Instance placement by fidelity need.** VMs (`--vm`) for nodes where the
   kernel matters: RKE2 cluster nodes, the air-gapped workstation, power-loss
   targets. System containers for fixtures: seeded public/private registries,
   IdP, cookbook host. One tool, both densities.
3. **Air-gap as absence of path.** The isolated side lives on a managed bridge
   with NAT disabled and no uplink; a canary check in every run asserts that
   egress *fails*. Network ACLs model the DMZ path for passthrough scenarios.
4. **The medium is a block volume.** A storage-pool block volume is attached
   to the connected node, formatted from inside the guest (exFAT/vfat cases
   included), filled, detached, and re-attached to the air-gapped node —
   faithfully exercising the pre-flight checks (FR-055), media removal
   (NFR-010), and destination-side verification (FR-054).
5. **Scenario reset via snapshots.** The storage pool uses ZFS or btrfs;
   seeded states are snapshotted and restored in seconds between scenarios.
   Golden instances are cloned copy-on-write per scenario run.
6. **Scenarios grow with the milestones — and stay replayable forever.**
   Writing its crucible scenarios is part of every milestone's definition of
   done: the crucible ships at milestone 1 with the reduced scenario (registry
   push/pull across the topology, canary, media round-trip) and each milestone
   adds its own, up to the crown scenario: **bootstrapping an RKE2 cluster on
   a bare OS in the air-gapped zone entirely from Tobby** — OS packages from
   served FileSets (FR-047), RKE2 artifacts and images from the transported
   store, cluster pulling through the zone registry with the generated mirror
   configuration (FR-065). Scenarios are **tagged by milestone and
   independently invocable**: at any time, the suite of all completed
   milestones is runnable in whole or as any subset (e.g. milestones 1–3
   only), so every past capability is re-provable on demand and regressions
   are attributed to a milestone, not to "the suite". Milestone acceptance
   publishes the raw check report.
7. **Development hosts.** On Apple Silicon (M3+ with macOS 15+), the identical
   definition runs inside a Lima VM with nested virtualization — arm64
   end-to-end, suitable for harness development and most scenarios (Tobby and
   its fixtures are dual-arch). Acceptance runs execute on an amd64 Linux
   host, matching the target sites. The contractual Windows mirror e2e
   (NFR-018) runs on native Windows CI runners and is not a crucible concern.

## Consequences

### Positive

- Scenario reset in seconds instead of re-provisioning minutes: the crucible
  is usable as a daily development tool, not just a milestone ceremony.
- The air gap is structural (no route) plus verified (canary), not a firewall
  rule assumed correct.
- Real block-device media semantics unlock honest tests for FR-054/FR-055 and
  NFR-010 that containers cannot express.
- One definition from the developer's laptop to the acceptance host; the only
  variable is CPU architecture, which the dual-arch product absorbs.
- Governance aligned with the project's values: Apache-2.0, community-run,
  LTS supported into 2029, packaged in mainstream distributions.

### Negative

- The host must be Linux with KVM; macOS and Windows drive the crucible
  remotely, never host it. Nested-virt cloud VMs mitigate the "no Linux at
  hand" case.
- No ready-made Windows guest images (distrobuilder repack would be needed);
  acceptable because Windows coverage lives on native CI runners.
- A modest learning curve (profiles, projects, storage pools) for
  contributors accustomed to Vagrant or plain Docker; mitigated by the
  definition being small, versioned, and documented.
- arm64/amd64 duality demands discipline: amd64-only upstream images make
  some app-deployment scenarios acceptance-host-only.

## Alternatives considered

### Vagrant (+ libvirt)

The familiar ergonomic choice, with prebuilt boxes (including Windows) and a
multi-provider story. Rejected: the multi-provider abstraction is largely
notional in 2026 (libvirt is the one serious provider, and providers on Apple
Silicon are weak); no instant snapshot/restore workflow — scenario reset
means re-provisioning; every node pays VM cost, including fixtures that need
none; and the project moved to the BUSL license with development largely
stalled — a semi-frozen, source-available dependency is a conscious misfit
for a supply-chain-hygiene showcase, even in test tooling.

### Docker Compose only (extend the CI tier)

Already the merge-gate tier, and it stays. Rejected as the *acceptance* tier:
containers share the host kernel — no FAT32/exFAT media realism, no power-loss
semantics, no per-node kernels for RKE2 — and privileged nesting workarounds
erode exactly the fidelity the crucible exists to provide.

### Proxmox or a dedicated hypervisor platform

Full-featured, but it inverts the dependency: scenarios would target one
platform's API and its lifecycle, and "replayable by anyone" degrades into
"replayable by whoever runs our Proxmox". Incus provides the same primitives
as a package on any Linux host.

### Per-cloud IaC (Terraform against a provider's VM API)

Ties scenario definitions to a cloud provider's API and network model — the
opposite of the portability requirement. Retained only where it belongs:
bootstrapping the disposable nested-virt host on which the (provider-agnostic)
Incus definition then runs.
