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
  produce are the point of those scenarios. Milestone 6 adds an Incus
  with OCI image support (6.x) and access to `quay.io` for the pinned
  Keycloak image (`b-auth.sh`); `a-scanning.sh` also needs `oras`, and
  takes the pinned Trivy release from `CRUCIBLE_TRIVY_ARCHIVE` or fetches
  it once, checked against the release checksum and the digest Tobby pins.

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
    ├── m4/run.sh     # milestone-4 passthrough / promotion
    ├── m5/run.sh     # milestone-5 mirror & air-gap
    ├── m6/           # milestone-6 controls & enterprise identity
    │   ├── run.sh    # dispatcher: runs every a-*, b-*, c-* scenario
    │   ├── a-*.sh    # track A: scanning, database, self-update
    │   ├── b-*.sh    # track B: enterprise identity, auth hardening
    │   ├── c-*.sh    # track C: integrity, trust scopes, assurance
    │   └── *-lib.sh  # per-track sourced helpers (b-lib.sh, c-lib.sh)
    └── m7/           # milestone-7 finishing, documentation & acceptance
        ├── run.sh    # dispatcher: every a-* … e-*, then z-crown-rke2.sh
        ├── *.sh      # one scenario per external feature (see below)
        └── crown/    # the crown's helpers, draft recipes, node files, YAML helper
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

### m5 — mirror & air-gap

The complete rehearsal of a physical transfer, with the medium a real
detachable block device and the damage inflicted between the two halves
of the trip:

1. Pre-flight refuses an undersized medium **before any transfer**,
   stating the shortfall, with nothing written (FR-055); a real FAT32
   volume is positively identified with its 4 GiB per-file ceiling. The
   refusal on that ceiling is exercised by execution in the Go suite —
   filling 4 GiB on a crucible node costs more than it proves — so what
   the node adds here is that the filesystem is recognised at all.
2. The mirror synchronization is triggered by hand, and the absence of an
   automatic one is **asserted rather than assumed** (FR-014).
3. Prune to the Retriever runs **before** the manifest is written, so the
   inventory describes what the medium finally holds (FR-045, FR-054); a
   manually packed FileSet survives it, and the medium keeps its
   identifier across synchronizations (R-28).
4. A credentials file planted under the store makes the instance refuse to
   start, naming the path (NFR-020).
5. One blob of one delivery is truncated, then the device is detached and
   re-attached to the air-gapped node — real block-device semantics.
6. Before verification, `/v2/` and `/files/` serve **nothing**, and
   `/readyz` still answers 200 with the reason (FR-054's third clause).
7. A medium addressed to another zone is blocked; the administrator waiver
   clears it and is recorded (FR-054, FR-094).
8. **R-19 exercised as arbitrated**: the damaged delivery is blocked whole
   and named with its offending file, its intact neighbour is pushed. Two
   deliveries on one medium is why this scenario carries two recipes.
9. The cleared delivery lands in the zone registry and its cookbook, and a
   standard OCI client **inside the isolated zone** pulls it back.
10. The return log is on the medium, outside the manifest's coverage
    (FR-053, FR-054).
11. Re-presenting the same medium is refused, both timestamps named (R-28).
12. `skopeo` — the tool, not a library standing in for it — reads the
    exported OCI image layout (FR-051).
13. `/help` answers offline in both languages (R-05); a directory packed
    inside the zone is served under `/files/`, which still accepts no
    write method (R-41, FR-047); a reset without the exact typed
    confirmation is refused (FR-046).

### m6 — controls and enterprise identity

The milestone-6 scenarios are one file per track, named by track prefix
(`a-*`, `b-*`, `c-*`), dispatched by `scenarios/m6/run.sh`, which discovers
and runs every one of them in name order and fails if any fails; files
named `*-lib.sh` are sourced helpers, never run. Each is independently
invocable (`sh crucible/scenarios/m6/<name>.sh`) and writes its own raw
report (`crucible-report-m6-<name>.txt`).

**Track A** — vulnerability scanning (6.1, 6.2, R-13) and self-update
(R-25):

- `a-scanning.sh` — the pinned Trivy and its database carried by the
  `tobby-system` channel across a real air gap. The degraded mode is
  visible before anything crossed (metric, banner, log); under a blocking
  policy a known-vulnerable recipe is withheld whole (R-19 grain), named
  with `TBY-SCN-005` and its CVE, absent from the media manifest and
  audited, while the clean one is written; a scanner binary tampered with
  on disk is never executed (`TBY-SCN-002`); a database past
  `scan.maxDbAge` under the block action refuses (`TBY-SCN-004`). Inside
  the zone, a passthrough instance arms its scanner from the channel and
  refuses or reports the vulnerable image depending on the Retriever's
  threshold.
- `a-self-update.sh` — Tobby's own recipe (image, chart, binaries as a
  recipe-ready FileSet) crosses by the standard mirror flow: a release
  signed by the site key is pushed, one signed by the project key alone is
  refused at the destination; the binary that crossed is byte-identical
  and runs; nothing updates by itself (NFR-019).

**Track B** — enterprise identity (6.3), continuity (R-20) and bounded
credentials (R-14), against a real Keycloak over HTTPS from a private CA:

- `b-auth.sh` — the instance starts while the IdP is stopped and serves
  the local form with `TBY-AUTH-016` (the local sign-in audited as
  `auth.local_under_federation`); once the IdP is up, OIDC and SAML
  sign-ins work for each role and an unmapped account is refused
  (`TBY-AUTH-017`); a forged state, a replayed SAML response and a
  response collected by another browser are refused; an IdP stopped
  mid-session ends no session; an expiring token and an idle session are
  refused with `TBY-AUTH-013` and `TBY-AUTH-005` and their audit records.

**Track C** — integrity, trust compartments and assurance. Track C
writes three, and every one of them follows the rule m5 paid for: **a scenario that manipulates something verifies its own
manipulation before it asks the product for a verdict** — a blob that was
never truncated, a key that was never swapped and a date that matched no
pattern all read exactly like the guard under test failing.

- `c-integrity.sh` — R-31, on-demand store verification (FR-057). A mirror
  store (a transportable one) is verified clean from the API and the
  screen, and by `tobby store verify` on the stopped store, which leaves
  every byte alone. It is then damaged four ways — a truncated blob, a
  referenced blob removed, an orphan planted, a trust root dropped
  (cosign confirms the signature is still valid under the dropped key) —
  and the command exits 4 naming each defect with its object, byte-identical
  afterwards. The running instance reports the same defects through the
  API and the screen; the metric and the audit event record it.
- `c-trust-scopes.sh` — the trust-root compartments named in the error
  (plan §2.6). A recipe signed by a key that is a valid root, but not one
  the matching scope lists, is refused with `TBY-SIG-001` carrying the
  **scope** that refused it; the scope that lists the key admits it, and an
  unscoped recipe is unaffected. cosign confirms the key is valid for the
  zone and does not verify under the scope's own root.
- `c-assurance.sh` — R-22, the reduced-assurance marker (FR-037) across the
  whole trip: set where a scope admits an unsigned recipe, carried on the
  task report, the API, the UI, a log field and a metric, recorded per
  recipe in the media manifest, re-read on the far side of a real block
  device as a *claim* beside the destination's own verdict — a manifest
  that claims nothing hides nothing — and never written into the
  destination registry (same digests, no marker artifact: RECIPE-SPEC §7.1).

### m7 — finishing, documentation and the crown

Same layout as m6 — one file per external feature, named by track prefix,
dispatched by `scenarios/m7/run.sh` in name order — with one addition: the
**crown**, `z-crown-rke2.sh`, sorts last on purpose. Until its track
writes it, each file is a named skeleton that **fails**, stating in its
header what it will prove: m7 is not accepted while one is pending, and a
skeleton that passed would report a check nobody made. Replay the
completed milestones meanwhile with `./crucible/run.sh m1 m2 m3 m4 m5 m6`.

| Scenario | Track | Proves |
|---|---|---|
| `a-consumption-aids.sh` | A (7.1) | mapping table on every surface, `registries.yaml` snippet deterministic and credential-free, a containerd node pulling by nominal reference, `TBY-AID-001`, FileSet badge |
| `b-diagnostics.sh` | B (R-30) | one-gesture bundle, zero planted secret in any member, documented members only, admin only, no egress |
| `b-supervision.sh` | B (R-11) | signed OpenAPI and metrics reference, chart alert rules and dashboard, each alert fired by its condition |
| `b-clock.sh` | B (R-32) | clock before the build date and before a medium's production: warned and audited, never corrected |
| `b-rebuild.sh` | B (R-27) | state directory, configuration and store restored on a fresh node: same accounts, token, certificate, import record |
| `c-exceptions.sh` | C (R-15) | mandatory expiry, digest/repository/recipe scopes, visible re-blocking, OpenVEX input |
| `c-provenance.sh` | C (R-17) | provenance class per repository on every surface, unmanaged-content metric, both hardening switches |
| `c-emergency-path.sh` | C, E (R-21) | the published emergency procedure followed literally, admitted only under its declared scope |
| `d-hardening.sh` | D (7.3) | security headers per route class, CSRF over the wire, bounded inputs, OCI clients unaffected |
| `e-docs-offline.sh` | E (7.2) | the complete bilingual corpus served offline, no dangling target |
| `z-crown-rke2.sh` | integration | RKE2 v1.36.5 bootstrapped air-gapped from a bare OS: OS packages and RKE2 artifacts from served FileSets, images through the zone registry via the **generated** `registries.yaml` |

The crown needs an Incus **VM** with its own kernel and at least 4 vCPU and
8 GiB for the RKE2 server node, hence a KVM host (the Elastic Metal
acceptance host has it); the scenario launches it with its own limits.

#### The crown, step by step

The scenario that exercises every milestone at once (DEVELOPMENT-PLAN §2.7):
an RKE2 v1.36.5 cluster bootstrapped fully air-gapped from a bare Rocky
Linux 9 VM, with everything served by Tobby.

1. Three recipes are cooked from the drafts under `scenarios/m7/crown/recipes/`
   and signed: `rke2` (the release's 16 images, checked entry for entry
   against its `rke2-images.linux-amd64.txt`, plus the tarball, checksums
   and pinned installer as a FileSet), `rocky9-rke2-host` (an rpm repository
   FileSet: the exact dnf transaction a connected twin of the bare host
   computes for SELinux policy, container-selinux, rke2-selinux and
   iptables) and `crown-app` (busybox, referenced by nominal path).
2. They cross on a real block device; the destination-side instance
   verifies the medium (which opens `/files/`) and imports it into the zone
   registry, where every ingredient is checked at its relocated path with
   its pinned digest.
3. Feature 7.1 generates the `registries.yaml` snippet and the mapping
   table; the table is held to what was pushed, digest for digest.
4. The bare VM — never connected, no resolver — installs its packages with
   dnf from the served FileSet alone, switches to SELinux enforcing,
   installs RKE2 by the tarball method with **no image tarball**, and pulls
   every image through the generated mirror configuration.
5. Node Ready, every system pod Running or Completed, the nominal-path pod
   runs, a never-pushed nominal tag fails at the zone. Proof comes from
   witnesses that are not the product: a capture on the node's own
   interface (every manifest the cluster runs was requested from the zone
   registry at its relocated path), containerd's image store (each image
   under its pinned digest), and leak captures on every way out of the zone
   (zero packet sourced in it for the whole run).

The RKE2 images are sourced from `registry.rancher.com` (same tags as
`docker.io/rancher`, different index digests, no pull rate limit) and the
node runs with `system-default-registry: registry.rancher.com`; busybox is
the one Docker Hub image, and carries the nominal `docker.io/...` criterion.
The generated snippet therefore mirrors both hosts to the zone registry.

On top of the baseline it needs KVM (two VMs: 4 vCPU / 8 GiB / 40 GiB for
the node, 2 vCPU / 2 GiB for its twin) and `tcpdump` on the host. No
registry credentials are needed; `CRUCIBLE_DOCKER_CONFIG` is used for
Docker Hub when present. `CRUCIBLE_M7_DEBUG_AIDS=handmade` replaces the generated aids
with hand-written ones to rehearse the rest of the scenario; such a run
says DEBUG on every dependent line and always ends with a FAIL.
