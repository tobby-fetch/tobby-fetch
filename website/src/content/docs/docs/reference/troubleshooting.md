---
title: Troubleshooting
description: Start from what you observe — the instance will not start, a sign-in fails, a recipe is refused, a medium is blocked, a scan degrades — and reach the TBY code, its probable cause and the corrective action.
sidebar:
  order: 6
---

This page is organized by **symptom**: what you see, not what Tobby calls
it. Each row points to the `TBY-*` code that explains it, with the
corrective action in one line. The [error reference](../errors/) is the
complete catalogue (what happened, probable cause, whether it can be fixed
offline, what it blocks); the same text is available on the instance itself
at `/help#TBY-SIG-001`, so an operator in an isolated zone does not need
this site.

## First, the four things worth doing every time

1. **Read the code.** Every user-visible error, in the interface, the CLI
   and the API (as an RFC 9457 problem document), carries a stable
   `TBY-<area>-<nnn>`. The code never changes meaning.
2. **Read the exit code** of the command. `3` is a policy refusal
   (allowlist, role, secure-by-default startup refusal, immutable tag),
   `4` a verification failure (signature, digest, checksum) that no
   override reopens, `1` an operational failure, `2` a usage error, `5`
   "changes planned" on `tobby sync --dry-run`. See
   [exit codes](../cli/#exit-codes).
3. **Follow the run identifier.** A synchronization, an import or a media
   verification carries one identifier through the logs, the task and its
   report. Filter the JSON logs on it.
4. **Check what the instance thinks its configuration is:**
   `tobby config dump` prints the effective configuration (secrets
   redacted), after flags, `TOBBY_*` variables and the file are combined.

## The instance does not start

| You see | Code | Do |
| --- | --- | --- |
| "no local account is configured" | [`TBY-AUTH-001`](../errors/#tby-auth-001) | `tobby user add --role admin <name>` on the host, then start again. Tobby never starts open. |
| "the configuration is invalid" | [`TBY-CFG-001`](../errors/#tby-cfg-001) | Fix the setting named in the message; check with `tobby config dump`; restart. |
| "a secret file is configured inside the transportable store" | [`TBY-CFG-002`](../errors/#tby-cfg-002) | Move `state.root`, `registries.credentialsFile` or `server.tls.keyFile` outside `storage.root` (symbolic links are resolved). |
| "the outbound proxy configuration is unusable" | [`TBY-NET-001`](../errors/#tby-net-001) | Use `http://host:port`; put credentials in `network.proxy.username` and `network.proxy.password`, not in the URL. |
| "a configured certificate authority could not be loaded" | [`TBY-NET-002`](../errors/#tby-net-002) | The file must exist, be readable and hold a PEM `CERTIFICATE`. There is no setting that disables TLS verification. |
| "the listener certificate could not be used" | [`TBY-NET-003`](../errors/#tby-net-003) | `server.tls.certFile` and `keyFile` must be a readable PEM pair that match; remove both for a generated self-signed certificate. |

## I cannot sign in, or an action is refused

| You see | Code | Do |
| --- | --- | --- |
| Wrong credentials | [`TBY-AUTH-002`](../errors/#tby-auth-002) | Check the name and password. The message never says which one is wrong. |
| "not allowed for your role" | [`TBY-AUTH-003`](../errors/#tby-auth-003) | Sign in with an account holding the role named, or ask an administrator. |
| A form is rejected, "could not be submitted safely" | [`TBY-AUTH-004`](../errors/#tby-auth-004) | The page was left open too long. The form reloaded with a fresh token: submit again. |
| "your session has expired" | [`TBY-AUTH-005`](../errors/#tby-auth-005) | Idle timeout (`auth.session.idleTimeout`), session lifetime (`auth.sessionTTL`) or an instance restart. Sign in again. |
| HTTP 429, "too many failed authentication attempts" | [`TBY-AUTH-012`](../errors/#tby-auth-012) | Wait, then check the credential. A shared egress address may hide another misconfigured client. |
| An API token is refused as expired | [`TBY-AUTH-013`](../errors/#tby-auth-013) | Ask an administrator for a new token and replace it in the client. |
| Single sign-in through OIDC fails | [`TBY-AUTH-014`](../errors/#tby-auth-014) | Start the sign-in again from the sign-in page. The administrator finds the failed check in the log under the same correlation identifier (state, nonce, signature, issuer, audience, callback URL). |
| Single sign-in through SAML fails | [`TBY-AUTH-015`](../errors/#tby-auth-015) | Start again from the sign-in page: Tobby accepts only sign-ins it started. Check the clocks and the audience. |
| The identity provider is unreachable | [`TBY-AUTH-016`](../errors/#tby-auth-016) | Use local sign-in, which stays available and is audited. Check the network path (proxy, private CA). |
| Signed in at the provider, but no role | [`TBY-AUTH-017`](../errors/#tby-auth-017) | Map one of your groups in `roles.viewer`, `roles.operator` or `roles.admin` under the provider's section. |
| "too many sign-ins started" | [`TBY-AUTH-018`](../errors/#tby-auth-018) | Wait a few seconds and sign in once. |
| An account or token name is refused | [`TBY-AUTH-019`](../errors/#tby-auth-019) | `:` is reserved for federated identities; pick another name. |
| "the last administrator" cannot be removed or demoted | [`TBY-AUTH-011`](../errors/#tby-auth-011) | Create a second admin first. |
| The password change fails | [`TBY-AUTH-006`](../errors/#tby-auth-006), [`TBY-AUTH-007`](../errors/#tby-auth-007) | Retype the current password; the new one must be non-empty, different, and confirmed. An admin can reset it with `tobby user passwd <name>`. |

## A recipe is refused

| You see | Code | Do |
| --- | --- | --- |
| "the recipe signature could not be verified" | [`TBY-SIG-001`](../errors/#tby-sig-001) | The message lists the fingerprints tried and, if any, the scope. Check that the signing key's public half is a configured trust root, that the matching scope lists it in `roots`, and that the recipe falls under the scope you expect. An unsigned or wrongly signed recipe has to be re-signed upstream. Never overridable. |
| "a pinned digest does not match the fetched content" | [`TBY-SIG-002`](../errors/#tby-sig-002) | Do not force it. Check the source and the recipe; a legitimate change needs a re-signed recipe pinning the new digest. |
| "the artifact's type does not match" | [`TBY-SIG-003`](../errors/#tby-sig-003) | Check the source repository; if the type change is legitimate, update and re-sign, otherwise treat the source as compromised. |
| "the recipe or retriever file is invalid" | [`TBY-VAL-001`](../errors/#tby-val-001) | The message names the file and the field path. Check against `recipe lint`. |
| "this recipe version is already published, with different content" | [`TBY-POL-004`](../errors/#tby-pol-004) | Publish under a new `metadata.version`. A published version is immutable. |
| A cookbook entry cannot be resolved | [`TBY-REG-006`](../errors/#tby-reg-006) | No published version satisfies the constraint. Tobby never falls back to a neighbour. Check the tags actually published. |

## A transfer is refused before it starts

| You see | Code | Do |
| --- | --- | --- |
| "the registry is not on the allowlist" | [`TBY-POL-001`](../errors/#tby-pol-001) | If legitimate, add the host to `registries.allowlist` (an audited change). The allowlist applies to the host actually contacted, after substitution. |
| "the destination registry cannot store this reference" | [`TBY-DST-001`](../errors/#tby-dst-001) | Shorten the relocated path with `destination.basePath` or `storage.basePrefix`. Nothing was pushed. |
| "not enough free space" | [`TBY-STO-004`](../errors/#tby-sto-004) | Free the stated number of bytes, use a larger volume, or remove unreferenced content. |
| "the target filesystem cannot hold a file this large" | [`TBY-STO-005`](../errors/#tby-sto-005) | Reformat with exFAT, NTFS, ext4 or XFS, or split the transfer. |
| "the Helm chart is missing an embedded dependency" | [`TBY-CHT-001`](../errors/#tby-cht-001) | Repackage with `helm dependency build`, then `helm package`, and publish again. |
| Individual removal refused | [`TBY-POL-002`](../errors/#tby-pol-002), [`TBY-POL-003`](../errors/#tby-pol-003) | Only unit-imported content is individually removable. Remove the managing recipe instead; seeded content is managed with the tool that pushed it. |

## A source registry does not answer

| You see | Code | Do |
| --- | --- | --- |
| The reference cannot be parsed | [`TBY-REG-001`](../errors/#tby-reg-001) | Use `registry/repository:tag` or `registry/repository@sha256:…`, with a registry host. |
| The registry cannot be reached | [`TBY-REG-002`](../errors/#tby-reg-002) | Check connectivity from the instance host and the proxy settings. The task is retried with bounded backoff. |
| Authentication refused | [`TBY-REG-003`](../errors/#tby-reg-003) | Add credentials for that host to `registries.credentialsFile`. |
| The inspection timed out | [`TBY-REG-004`](../errors/#tby-reg-004) | Check the registry and the path to it; raise `import.inspectTimeout` only if the link is known to be slow. |
| "does not exist" | [`TBY-REG-005`](../errors/#tby-reg-005) | Check the repository name and tag. |
| A partial download is unusable | [`TBY-REG-007`](../errors/#tby-reg-007) | Retry. If it recurs on the same source, set `transfer.resumeThreshold: 0` and check any caching proxy. |
| A platform is missing from the index | [`TBY-REG-008`](../errors/#tby-reg-008) | Confront `platforms` with what the source publishes; Tobby never drops a requested platform silently. |
| Certificate errors on an internal registry | [`TBY-NET-002`](../errors/#tby-net-002) | Trust the private authority with `network.tls.caFiles` or `network.tls.ca` (see the [configuration](../configuration/)); there is no way to skip verification. |
| Writing the partial download fails | [`TBY-STO-003`](../errors/#tby-sto-003) | Check the state directory's free space and permissions. |

## Vulnerability scanning is degraded, or refuses something

Scanning never turns itself off silently: a degraded state is a banner, a
report and a metric.

| You see | Code | Do |
| --- | --- | --- |
| "the scanner is unavailable" | [`TBY-SCN-001`](../errors/#tby-scn-001) | Container image: bundled. Linux or Windows host: synchronize the `tobby-system` recipe. macOS: install `trivy` on the `PATH`. Under `scan.mode: block`, nothing unscanned is pushed meanwhile. |
| "the scanner binary does not match its pinned digest" | [`TBY-SCN-002`](../errors/#tby-scn-002) | The file was replaced or damaged after it was pinned and was not executed. Restore it by synchronizing `tobby-system` again. |
| "no vulnerability database is available" | [`TBY-SCN-003`](../errors/#tby-scn-003) | Synchronize the `tobby-system` recipe, through the medium in an isolated zone. Media verification does not need the database. |
| "the database is too old" | [`TBY-SCN-004`](../errors/#tby-scn-004) | Bring a fresh database. With `scan.maxDbAgeAction: block`, items scanned with a stale database are not pushed. |
| "the database claims to be built in the future" | [`TBY-SCN-008`](../errors/#tby-scn-008) | The host clock is behind, or the database metadata is wrong. It is treated as stale. |
| "violates the vulnerability policy and was not pushed" | [`TBY-SCN-005`](../errors/#tby-scn-005) | Fix upstream and update the recipe, or list the CVE as an exception, with a justification, in `spec.scanPolicy` or `scan.exceptions`. |
| The same, but pushed | [`TBY-SCN-006`](../errors/#tby-scn-006) | Advisory mode: the push went ahead and the violation is on record. Switch to `block` to refuse. |
| "could not be scanned" | [`TBY-SCN-007`](../errors/#tby-scn-007) | The scanner stopped with an error; the detail is in the message and the log. |
| A recipe threshold was ignored | [`TBY-SCN-020`](../errors/#tby-scn-020) | A recipe may lower the threshold, never raise it. |
| An exception no longer applies | [`TBY-SCN-021`](../errors/#tby-scn-021) | It expired. Renew it deliberately or fix the finding. |
| Findings without a severity | [`TBY-SCN-022`](../errors/#tby-scn-022) | The advisory sources give no score; the policy cannot compare them with the threshold. |
| An ingredient was scanned as an image although it is not declared as one | [`TBY-SCN-023`](../errors/#tby-scn-023) | Expected: what is scanned is decided by content. |
| An exception "does not cover" an item | [`TBY-SCN-026`](../errors/#tby-scn-026) | Its scope (digest, repository, recipe) excludes the item. Widen the scope if the item was meant to be covered; `tobby scan exceptions` shows every scope. |
| The instance refuses to start on an OpenVEX document | [`TBY-SCN-027`](../errors/#tby-scn-027) | The file under `scan.vex` cannot be read or is not a valid OpenVEX v0.2.0 document; the detail names the field. Fix or remove it. |
| A Retriever exception "has no expiry" | [`TBY-SCN-028`](../errors/#tby-scn-028) | It applies, unbounded. Ask the Retriever's owner to add `expires` and a justification. |
| The instance refuses to start on `scan.exceptions[i]` | [`TBY-CFG-001`](../errors/#tby-cfg-001) | Since v0.7.0 every instance exception needs `expires` and `justification`; the message names the entry. |

## A removable medium is blocked

On the destination side, nothing is pushed or served until the medium has
been verified. Open the **Media** screen and run *Verify*; the report names
each blocked recipe and the file at fault.

| You see | Code | Do |
| --- | --- | --- |
| `/v2/` or `/files/` answer `403` | [`TBY-MED-030`](../errors/#tby-med-030) | Run *Verify* on the Media screen or `POST /api/v1/media/verify`. `tobby media verify` reaches a verdict but does not open the surfaces of a running instance. |
| A verification is already running | [`TBY-MED-031`](../errors/#tby-med-031) | Wait for it to finish. |
| Verify or import refused on an instance with no zone | [`TBY-CFG-001`](../errors/#tby-cfg-001) | Set `zone:` to the identity of the zone this instance serves ([import on the isolated side](../../air-gap/import-destination/)); nothing was enqueued. |
| Import or Push refused, verification works | [`TBY-CFG-001`](../errors/#tby-cfg-001) | Set `destination.registry`, the zone registry the import pushes into; verifying needs none. Nothing was enqueued or written to the medium. |
| The medium did not come out whole | [`TBY-MED-032`](../errors/#tby-med-032) | Read the report. Re-copy the medium and verify again; intact recipes can still be pushed. |
| "carries no media manifest", or it is unreadable | [`TBY-MED-001`](../errors/#tby-med-001), [`TBY-MED-002`](../errors/#tby-med-002) | The store was not written as a medium, or the file is truncated. Re-copy from the source. |
| Unsupported format version | [`TBY-MED-003`](../errors/#tby-med-003), [`TBY-MED-004`](../errors/#tby-med-004) | Medium and instance are on different major versions. Update the destination ([update Tobby](../../air-gap/update-tobby/)). |
| "addressed to another zone" | [`TBY-MED-006`](../errors/#tby-med-006) | Check you have the right medium. It can be waived deliberately (`admin` role in the web interface and the API, no role check on the host's command line); every waiver applied is audited, at Verify as at import. |
| "older than the last one imported" | [`TBY-MED-007`](../errors/#tby-med-007) | Check you plugged in the current medium. It can be waived the same way, for example to restore an older delivery on purpose. |
| A file is missing, the wrong size, or does not match its digest | [`TBY-MED-010`](../errors/#tby-med-010), [`TBY-MED-011`](../errors/#tby-med-011), [`TBY-MED-012`](../errors/#tby-med-012), [`TBY-MED-015`](../errors/#tby-med-015) | Re-copy the store from the source. The recipe reaching the file is blocked; no override exists. |
| A file or a manifest is not covered, or not readable | [`TBY-MED-013`](../errors/#tby-med-013), [`TBY-MED-014`](../errors/#tby-med-014), [`TBY-MED-005`](../errors/#tby-med-005) | The inventory does not match the content: re-copy from the source. |
| The medium's recipe graph lists an ingredient the signed recipe does not pin | [`TBY-MED-016`](../errors/#tby-med-016) | The bookkeeping was altered: do not import; re-produce the medium from the source. No override exists. |
| A file the recipes do not reach | [`TBY-MED-020`](../errors/#tby-med-020), [`TBY-MED-021`](../errors/#tby-med-021), [`TBY-MED-022`](../errors/#tby-med-022) | Reported, never pushed. Usually leftovers of an earlier delivery: [start from a clean medium](../../air-gap/manage-media/). |

## The store looks damaged

| You see | Code | Do |
| --- | --- | --- |
| A blob does not match its digest | [`TBY-INT-001`](../errors/#tby-int-001) | The verification changes nothing. Re-synchronize the recipes reaching it, or restore the store from a sound copy; check the disk. |
| A manifest is incomplete in the store | [`TBY-INT-002`](../errors/#tby-int-002) | Same. |
| Content reachable from no recipe | [`TBY-INT-003`](../errors/#tby-int-003) | Reported for information: run a prune, or leave it. |
| A verification is already running, or was cancelled | [`TBY-INT-004`](../errors/#tby-int-004), [`TBY-INT-006`](../errors/#tby-int-006) | Wait for the running one; a cancelled verification says nothing about the store, run it again. |
| The store cannot be read or written | [`TBY-STO-001`](../errors/#tby-sto-001), [`TBY-STO-002`](../errors/#tby-sto-002) | Mounted volume, permissions, free space. |
| A reset did not happen | [`TBY-STO-006`](../errors/#tby-sto-006) | The typed confirmation did not match. |

See [verify the store](../../passthrough/verify-store/) for what the
verification checks.

## Smaller tools and edges

| You see | Code | Do |
| --- | --- | --- |
| An OCI layout cannot be read, an archive entry is refused, the export destination exists | [`TBY-LAY-001`](../errors/#tby-lay-001), [`TBY-LAY-002`](../errors/#tby-lay-002), [`TBY-LAY-003`](../errors/#tby-lay-003) | Check the path, the archive (entries leading outside the root are refused), or choose a new destination. |
| A file set cannot be packed | [`TBY-FIL-001`](../errors/#tby-fil-001), [`TBY-FIL-002`](../errors/#tby-fil-002), [`TBY-FIL-003`](../errors/#tby-fil-003) | A symbolic link pointing outside the directory is refused; from the interface and API, only directories under `files.packRoots` can be packed. |
| "the task does not exist" | [`TBY-TSK-001`](../errors/#tby-tsk-001) | Open the task list and follow an existing task. |
| "an internal error occurred" | [`TBY-SRV-001`](../errors/#tby-srv-001) | Report it with the run identifier and the log lines around it. |
| "this resource does not exist" | [`TBY-SRV-002`](../errors/#tby-srv-002) | The address is wrong or the content was removed. |
| The page says the instance is unreachable | [`TBY-SRV-003`](../errors/#tby-srv-003) | A client-side condition; check the network link. |

## Reporting a problem you cannot solve

Give the maintainers the error code, the exit code, the version
(`tobby version`), the run identifier and the log lines that carry it, and
the output of `tobby config dump` with anything sensitive removed. Do not
attach secrets or the store. For a suspected vulnerability, follow the
[security policy](https://github.com/tobby-fetch/tobby-fetch/blob/main/SECURITY.md)
instead of opening a public issue.
