// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy

import "net/http"

// Stable codes. Grouped by domain: AUTH (authentication and authorization),
// CFG (configuration), VAL (recipe/retriever validation, FR-011), REG
// (source registry access), POL (policy refusals, FR-030), SIG (signature
// and digest verification, FR-033), DST (destination limits, FR-035), STO
// (local store), MED (removable-media transport, FR-054), TSK (tasks),
// SRV (instance itself).
//
// Codes marked "reserved" have no emitter yet: the class is fixed at
// milestone 2 (roadmap directive) so later milestones plug engines into an
// already-published contract. A code is never renumbered or reused.
const (
	// CodeNoAccount is the secure-by-default startup refusal of R-01: the
	// instance never starts with an open UI.
	CodeNoAccount Code = "TBY-AUTH-001"
	// CodeAuthFailed is a failed interactive sign-in. Deliberately
	// parameter-free: the message must not reveal whether the account
	// exists (NFR-015).
	CodeAuthFailed Code = "TBY-AUTH-002"
	// CodeRoleDenied is an action refused to the session's role.
	CodeRoleDenied Code = "TBY-AUTH-003"
	// CodeCSRF is a missing or expired anti-forgery token.
	CodeCSRF Code = "TBY-AUTH-004"
	// CodeSessionExpired is an expired or unknown UI session.
	CodeSessionExpired Code = "TBY-AUTH-005"
	// CodePasswordCurrent is a failed current-password check on a
	// self-service password change (R-34, FR-061). Deliberately
	// parameter-free, like CodeAuthFailed: the message never reveals
	// account details (NFR-015).
	CodePasswordCurrent Code = "TBY-AUTH-006" //nolint:gosec // G101: a stable error code, not a credential
	// CodePasswordInvalid is a rejected new password on a self-service
	// password change: empty, identical to the current one, or its
	// confirmation does not match.
	CodePasswordInvalid Code = "TBY-AUTH-007" //nolint:gosec // G101: a stable error code, not a credential
	// CodeAccountInvalid is a rejected account creation or update
	// (FR-073): empty name, unknown role, empty or mistyped password.
	// Deliberately parameter-free — the submitted name is echoed by the
	// form, not by the message.
	CodeAccountInvalid Code = "TBY-AUTH-008"
	// CodeAccountExists is an account creation whose name is already
	// taken (FR-073).
	CodeAccountExists Code = "TBY-AUTH-009"
	// CodeAccountUnknown is an account operation targeting a name this
	// instance does not know (FR-073). Distinct from the generic 404: the
	// corrective action is an account one.
	CodeAccountUnknown Code = "TBY-AUTH-010"
	// CodeLastAdmin refuses the removal or the demotion of the last
	// administrator (FR-073, FR-074): the instance would become
	// unmanageable, and FR-005 makes it refuse to start with no account at
	// all. A policy refusal, like every other secure-by-default barrier.
	CodeLastAdmin Code = "TBY-AUTH-011"
	// CodeAuthRateLimited throttles a network origin that keeps failing
	// authentication (v0.4.2 hardening): every failed password check
	// costs a deliberately expensive argon2id computation, so unbounded
	// failures are a denial-of-service lever against the instance
	// itself, not just a brute-force risk. Operational, not policy: the
	// client broke no rule — it has to wait. Deliberately parameter-free,
	// like CodeAuthFailed: the message must reveal nothing about
	// accounts or thresholds (NFR-015).
	CodeAuthRateLimited Code = "TBY-AUTH-012"

	// CodeConfigInvalid is a rejected configuration (FR-003 validation).
	CodeConfigInvalid Code = "TBY-CFG-001"
	// CodeSecretInStore is the NFR-020 startup refusal: a configured
	// secret path resolves inside the transportable store. A policy
	// refusal, like every other secure-by-default barrier — the
	// configuration is perfectly well-formed, it is the placement that is
	// forbidden, and no opt-out exists because the medium leaves the site.
	CodeSecretInStore Code = "TBY-CFG-002" //nolint:gosec // G101: a stable error code, not a credential

	// CodeProxyInvalid is an unusable outbound proxy configuration
	// (FR-080). Parameters name the setting and the credential-free
	// proxy URL — never the credentials, which have no path here.
	CodeProxyInvalid Code = "TBY-NET-001"
	// CodeTrustStore is a configured certificate authority that cannot
	// be loaded or that contributes nothing (FR-081).
	CodeTrustStore Code = "TBY-NET-002"
	// CodeServerTLS is an unusable listener certificate (FR-082):
	// missing file, mismatched key, unparseable PEM.
	CodeServerTLS Code = "TBY-NET-003"
	// CodeServerCertReplace refuses a certificate replacement submitted
	// from the administration surfaces (FR-082). Distinct from
	// CodeServerTLS on purpose: that one describes an instance that
	// cannot serve, this one an instance that is still serving —
	// nothing was written, and the previous certificate is untouched.
	CodeServerCertReplace Code = "TBY-NET-004"

	// CodeValidation is a rejected recipe/retriever file: file, path,
	// violated constraint (FR-011). Reserved: emitter lands at milestone 3.
	CodeValidation Code = "TBY-VAL-001"

	// CodeBadReference is an unparseable image/chart reference.
	CodeBadReference Code = "TBY-REG-001"
	// CodeRegistryUnreachable is a source registry that cannot be reached.
	CodeRegistryUnreachable Code = "TBY-REG-002"
	// CodeRegistryAuth is a source registry refusing authentication.
	CodeRegistryAuth Code = "TBY-REG-003"
	// CodeInspectTimeout is a remote inspection exceeding its deadline —
	// deliberately distinct from CodeRegistryUnreachable.
	CodeInspectTimeout Code = "TBY-REG-004"
	// CodeRefNotFound is a reference missing on the source registry.
	CodeRefNotFound Code = "TBY-REG-005"
	// CodeVersionResolve is a version expression no available tag
	// satisfies (FR-021): never a silent fallback.
	CodeVersionResolve Code = "TBY-REG-006"
	// CodeRangeUnusable is a source whose partial responses do not add up
	// while a large blob is being resumed (FR-029): a 206 that starts at
	// the wrong byte, a Content-Range contradicting the manifest, a
	// refused range, or content that changed between two attempts.
	// Operational, not a verification verdict: nothing has been proven
	// wrong about the content — the conversation about it broke.
	CodeRangeUnusable Code = "TBY-REG-007"
	// CodePlatformMissing is an ingredient asking for a platform the
	// source index does not carry (FR-022, RECIPE-SPEC §7.1). It used to
	// be a bare fmt.Errorf, so an operator whose recipe named one
	// platform too many read TBY-SRV-001 — "an internal error occurred",
	// whose corrective action is to search the logs for a correlation
	// identifier — for a mistake in their own document (found while
	// fixing B-020). Operational and not a policy refusal: nothing was
	// forbidden, the content asked for is simply not published.
	CodePlatformMissing Code = "TBY-REG-008"

	// CodeNotAllowlisted is the pre-transfer allowlist refusal (FR-030).
	// Reserved: emitter lands at milestone 4.
	CodeNotAllowlisted Code = "TBY-POL-001"
	// CodeRecipeManaged refuses individual removal of recipe-managed
	// content (FR-044 amendment): it goes away by removing the recipe.
	CodeRecipeManaged Code = "TBY-POL-002"
	// CodeSeedContent refuses UI/API removal of seeded content — pushed
	// through /v2/ by a standard client: the FR-044 amendment scopes
	// individual removal to unit-import provenance only.
	CodeSeedContent Code = "TBY-POL-003"
	// CodeTagImmutable refuses to republish a cooked recipe tag onto
	// different content (RECIPE-SPEC §8: a cooked recipe is immutable —
	// any change, even a single digest, requires a new metadata.version).
	CodeTagImmutable Code = "TBY-POL-004"

	// CodeSignature is a recipe signature that no configured trust root
	// validates (FR-033).
	CodeSignature Code = "TBY-SIG-001"
	// CodeDigestMismatch is fetched content not matching its pinned digest
	// (FR-033). Reserved: emitter lands at milestone 3.
	CodeDigestMismatch Code = "TBY-SIG-002"
	// CodeArtifactType is an OCIArtifact whose fetched artifactType does
	// not match the recipe's declaration (RECIPE-SPEC §7.3 — anti
	// tag-reuse and repository confusion).
	CodeArtifactType Code = "TBY-SIG-003"

	// CodeDestinationLimit is a pre-push refusal naming the destination's
	// limit (FR-035). Reserved: emitter lands at milestone 3.
	CodeDestinationLimit Code = "TBY-DST-001"

	// CodePackInput is a FileSet packing request Tobby cannot act on
	// (FR-048): a source that is not a readable directory, a directory
	// with nothing in it, an unusable FileSet name or version, or a tree
	// that changed while it was being packed. About the request, not
	// about the safety of its content.
	CodePackInput Code = "TBY-FIL-001"
	// CodePackUnsafe is a local file tree carrying an entry the FileSet
	// extraction rules refuse (RECIPE-SPEC §14.5, NFR-011): a symbolic
	// link escaping the root, a setuid bit, a device node, a name the
	// layer format reads as a whiteout, or a tree beyond the extraction
	// limits. Refused at packing time, where the operator can still fix
	// the tree, rather than admitted into a FileSet that fails to
	// extract later.
	CodePackUnsafe Code = "TBY-FIL-002"
	// CodePackNotAllowed is a packing request from the API or the UI
	// naming a directory outside the configured pack roots (FR-048,
	// FR-075). Nothing is wrong with the tree: these surfaces reach the
	// host filesystem only where the configuration says they may, and no
	// root configured means none.
	CodePackNotAllowed Code = "TBY-FIL-003"

	// CodeChartDependency is a Helm chart whose declared dependency is not
	// embedded in the package (FR-024): unusable offline, the import fails
	// naming the missing dependency.
	CodeChartDependency Code = "TBY-CHT-001"

	// CodeStoreRead is a failed read of the local store.
	CodeStoreRead Code = "TBY-STO-001"
	// CodeStoreWrite is a failed write to the local store.
	CodeStoreWrite Code = "TBY-STO-002"
	// CodeResumeSpool is a failed write to the partial-download area of
	// the state directory (FR-029). Deliberately distinct from
	// CodeStoreWrite: the two directories have different owners, different
	// sizing and different corrective actions, and sending an operator to
	// check the store when the state disk is full sends them to the wrong
	// machine — sometimes literally (R-16).
	CodeResumeSpool Code = "TBY-STO-003"
	// CodeInsufficientSpace is the FR-055 pre-flight refusal: the
	// projected write does not fit in the target's free space minus the
	// configured safety margin. It is raised BEFORE any transfer, and it
	// states the shortfall in bytes — an operator who has to guess how
	// much to free will free the wrong amount.
	CodeInsufficientSpace Code = "TBY-STO-004"
	// CodeFileTooLarge is the FR-055 file-size refusal: the target's
	// filesystem was positively identified and cannot hold the largest
	// file the operation would write — FAT32's 4 GiB ceiling, single-tar
	// export archives included. It also carries the mid-write failure of
	// the same condition, so the pre-flight refusal and the write that
	// slipped past it (a medium swapped between the check and the run)
	// read as one problem rather than two.
	CodeFileTooLarge Code = "TBY-STO-005"

	// Removable-media transport (FR-050, FR-054, ADR-0006). The medium is
	// a store that changed hands: everything it says about itself is a
	// claim until the destination has re-hashed it, so every code below
	// names the file the claim was about — the FR-054 acceptance is
	// "detected and blocks the push, NAMING the file".
	//
	// CodeMediaManifestMissing is a transported store carrying no media
	// manifest at all. Globally blocking with no override (FR-054
	// amendment R-19): without the inventory there is nothing to reason
	// about.
	CodeMediaManifestMissing Code = "TBY-MED-001"
	// CodeMediaManifestUnreadable is a media manifest that cannot be read
	// as one: truncated, unparseable, or internally inconsistent (a path
	// escaping the store, a duplicated inventory entry). Globally
	// blocking with no override.
	CodeMediaManifestUnreadable Code = "TBY-MED-002"
	// CodeMediaFormatUnsupported is a media manifest whose own format
	// version this build does not read.
	CodeMediaFormatUnsupported Code = "TBY-MED-003"
	// CodeMediaStoreFormat is a medium whose store layout version this
	// build does not read (R-26, the store's own compatibility policy,
	// restated for the medium so the refusal names both versions before
	// anything is opened).
	CodeMediaStoreFormat Code = "TBY-MED-004"
	// CodeMediaGraphAltered is a medium whose recipe graph
	// (meta/recipes.json) does not match its inventory entry. Globally
	// blocking with no override: the graph IS the reachability set, so an
	// altered one makes every per-recipe verdict meaningless.
	CodeMediaGraphAltered Code = "TBY-MED-005"
	// CodeMediaZoneMismatch is a medium addressed to another zone.
	// Globally blocking, admin-overridable and audited (FR-054, FR-094):
	// an anti-accident guard, not a security control.
	CodeMediaZoneMismatch Code = "TBY-MED-006"
	// CodeMediaStale is a medium older than the last import recorded for
	// its zone (R-28). Globally blocking, admin-overridable and audited;
	// like the zone guard it prevents an accident — re-importing last
	// month's medium — and not an attack, since the manifest is unsigned.
	CodeMediaStale Code = "TBY-MED-007"

	// CodeMediaFileMissing is a file a recipe reaches that the medium does
	// not carry. Blocks that recipe whole, no override (R-19).
	CodeMediaFileMissing Code = "TBY-MED-010"
	// CodeMediaFileSize is a covered file whose size differs from its
	// inventory entry.
	CodeMediaFileSize Code = "TBY-MED-011"
	// CodeMediaFileDigest is a covered file whose content does not hash to
	// its inventory entry — the truncated or corrupted blob of the FR-054
	// acceptance.
	CodeMediaFileDigest Code = "TBY-MED-012"
	// CodeMediaFileUninventoried is a file a recipe reaches that the
	// inventory does not list: the manifest cannot vouch for it, so it is
	// treated as an integrity failure of that recipe rather than as
	// extraneous content.
	CodeMediaFileUninventoried Code = "TBY-MED-013"
	// CodeMediaContentUnreadable is a reachable manifest or index on the
	// medium that cannot be parsed, so the walk cannot establish what the
	// recipe reaches.
	CodeMediaContentUnreadable Code = "TBY-MED-014"
	// CodeMediaContentAddress is a blob whose bytes do not hash to the
	// digest its own path claims — content-addressed storage disagreeing
	// with itself, independently of what the inventory says.
	CodeMediaContentAddress Code = "TBY-MED-015"

	// CodeMediaUncovered reports a file under manifest coverage that the
	// inventory does not list. Non-blocking: it is never pushed (FR-054).
	CodeMediaUncovered Code = "TBY-MED-020"
	// CodeMediaUnreachable reports an inventoried file no recipe reaches.
	// Non-blocking: it is never pushed (FR-054).
	CodeMediaUnreachable Code = "TBY-MED-021"
	// CodeMediaMetadataAltered reports a covered meta/ bookkeeping file —
	// other than the recipe graph, which blocks globally — that does not
	// match its inventory entry. Non-blocking: nothing is pushed from it.
	CodeMediaMetadataAltered Code = "TBY-MED-022"

	// The serving half of FR-054. "Destination-side verification SHALL
	// precede any push, any serving, and any local write": the push and
	// the local write are refused by the engine, and these two are what a
	// client asking the registry or the file surface of a destination
	// instance for content it has not verified gets back. They are
	// answers to a REQUEST, never task outcomes — which is why they carry
	// the surface that was refused and, above all, the way out.
	//
	// CodeMediaUnverified is a destination instance holding a transported
	// medium nothing has verified yet.
	CodeMediaUnverified Code = "TBY-MED-030"
	// CodeMediaVerificationRunning is a second verification asked for
	// while one is already walking the medium. Re-hashing a disk twice at
	// once halves both runs and answers nothing new.
	CodeMediaVerificationRunning Code = "TBY-MED-031"
	// CodeMediaNotCleared is a medium that HAS been verified and did not
	// come out whole. Serving stays closed: unlike the push decision,
	// which R-19 takes recipe by recipe, serving is a property of the
	// store as a whole — /v2/ and /files/ hand out blobs, and a blob a
	// blocked recipe reaches is exactly the byte range that failed.
	CodeMediaNotCleared Code = "TBY-MED-032"

	// CodeResetConfirmation is a store reset submitted without the exact
	// typed confirmation FR-046 requires. Distinct from a validation
	// error: nothing about the request is malformed — the operator was
	// asked to type a word and did not.
	CodeResetConfirmation Code = "TBY-STO-006"

	// CodeLayoutInvalid is a directory or archive that is not a usable
	// OCI image layout (FR-051): no marker file, an index that does not
	// parse, a blob that does not hash to the digest addressing it.
	CodeLayoutInvalid Code = "TBY-LAY-001"
	// CodeLayoutUnsafe is a layout archive carrying an entry that has no
	// place in one — an absolute path, a traversal, a link (NFR-011). A
	// verification failure, not an operational one: an archive shaped
	// like that is not a damaged transfer, it is a hostile one.
	CodeLayoutUnsafe Code = "TBY-LAY-002"
	// CodeLayoutTarget is an export destination that already exists and
	// was not explicitly allowed to be replaced.
	CodeLayoutTarget Code = "TBY-LAY-003"

	// CodeTaskNotFound is a task identifier unknown to this instance.
	CodeTaskNotFound Code = "TBY-TSK-001"

	// CodeInternal is the unexpected internal error; the correlation
	// identifier is the pointer into the logs (FR-090).
	CodeInternal Code = "TBY-SRV-001"
	// CodeNotFound is a UI page or API resource that does not exist.
	CodeNotFound Code = "TBY-SRV-002"
	// CodeUnreachable is the client-side "instance unreachable" condition
	// rendered by the UI shell on transport failure; catalogued so the
	// troubleshooting guide documents it. Never served by the instance.
	CodeUnreachable Code = "TBY-SRV-003"
)

var catalog = map[Code]Entry{
	CodeNoAccount:       {Code: CodeNoAccount, Class: ClassPolicy},
	CodeAuthFailed:      {Code: CodeAuthFailed, Class: ClassOperational, HTTPStatus: http.StatusUnauthorized},
	CodeRoleDenied:      {Code: CodeRoleDenied, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"role"}},
	CodeCSRF:            {Code: CodeCSRF, Class: ClassOperational, HTTPStatus: http.StatusForbidden},
	CodeSessionExpired:  {Code: CodeSessionExpired, Class: ClassOperational, HTTPStatus: http.StatusUnauthorized},
	CodePasswordCurrent: {Code: CodePasswordCurrent, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity},
	CodePasswordInvalid: {Code: CodePasswordInvalid, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity},
	CodeAccountInvalid:  {Code: CodeAccountInvalid, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity},
	CodeAccountExists:   {Code: CodeAccountExists, Class: ClassOperational, HTTPStatus: http.StatusConflict, Params: []string{"name"}},
	CodeAccountUnknown:  {Code: CodeAccountUnknown, Class: ClassOperational, HTTPStatus: http.StatusNotFound, Params: []string{"name"}},
	CodeLastAdmin:       {Code: CodeLastAdmin, Class: ClassPolicy, HTTPStatus: http.StatusConflict, Params: []string{"name"}},
	CodeAuthRateLimited: {Code: CodeAuthRateLimited, Class: ClassOperational, HTTPStatus: http.StatusTooManyRequests},

	CodeConfigInvalid: {Code: CodeConfigInvalid, Class: ClassOperational, Params: []string{"detail"}},
	// Never served over HTTP: the instance refuses to start, so there is
	// no listener to answer with it.
	CodeSecretInStore: {Code: CodeSecretInStore, Class: ClassPolicy, Params: []string{"paths", "root"}},

	CodeProxyInvalid: {Code: CodeProxyInvalid, Class: ClassOperational, Params: []string{"setting", "proxy"}},
	CodeTrustStore:   {Code: CodeTrustStore, Class: ClassOperational, Params: []string{"source"}},
	CodeServerTLS:    {Code: CodeServerTLS, Class: ClassOperational, Params: []string{"source"}},
	// 422: the submitted pair is the caller's input, and the instance is
	// still perfectly able to serve — the refusal is about the document,
	// not about the instance.
	CodeServerCertReplace: {Code: CodeServerCertReplace, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"detail"}},

	CodeValidation: {Code: CodeValidation, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"file", "path", "constraint"}},

	CodeBadReference:        {Code: CodeBadReference, Class: ClassOperational, HTTPStatus: http.StatusBadRequest, Params: []string{"reference"}},
	CodeRegistryUnreachable: {Code: CodeRegistryUnreachable, Class: ClassOperational, HTTPStatus: http.StatusBadGateway, Params: []string{"host"}},
	CodeRegistryAuth:        {Code: CodeRegistryAuth, Class: ClassOperational, HTTPStatus: http.StatusBadGateway, Params: []string{"host"}},
	CodeInspectTimeout:      {Code: CodeInspectTimeout, Class: ClassOperational, HTTPStatus: http.StatusGatewayTimeout, Params: []string{"host", "timeout"}},
	CodeRefNotFound:         {Code: CodeRefNotFound, Class: ClassOperational, HTTPStatus: http.StatusBadGateway, Params: []string{"reference"}},

	CodeNotAllowlisted: {Code: CodeNotAllowlisted, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"host"}},
	CodeRecipeManaged:  {Code: CodeRecipeManaged, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"repository", "recipes"}},
	CodeSeedContent:    {Code: CodeSeedContent, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"repository"}},
	CodeTagImmutable:   {Code: CodeTagImmutable, Class: ClassPolicy, HTTPStatus: http.StatusConflict, Params: []string{"reference", "published", "candidate"}},

	CodeRangeUnusable: {Code: CodeRangeUnusable, Class: ClassOperational, HTTPStatus: http.StatusBadGateway, Params: []string{"reference", "detail"}},

	CodeVersionResolve: {Code: CodeVersionResolve, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "constraint", "detail"}},

	// 422 like CodeVersionResolve, and for the same reason: the request is
	// well formed and permitted, the recipe simply names something the
	// source does not publish.
	CodePlatformMissing: {Code: CodePlatformMissing, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "platforms", "available"}},

	CodeSignature:      {Code: CodeSignature, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"recipe", "fingerprints"}},
	CodeDigestMismatch: {Code: CodeDigestMismatch, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "expected", "actual"}},
	CodeArtifactType:   {Code: CodeArtifactType, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "expected", "actual"}},

	CodeDestinationLimit: {Code: CodeDestinationLimit, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "limit"}},

	CodeChartDependency: {Code: CodeChartDependency, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"chart", "dependency"}},

	// 422 for both packing refusals: the instance is perfectly able to
	// serve — what it refuses is the tree it was pointed at.
	CodePackInput:      {Code: CodePackInput, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"detail"}},
	CodePackUnsafe:     {Code: CodePackUnsafe, Class: ClassPolicy, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"detail"}},
	CodePackNotAllowed: {Code: CodePackNotAllowed, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"path"}},

	CodeStoreRead:   {Code: CodeStoreRead, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError, Params: []string{"detail"}},
	CodeStoreWrite:  {Code: CodeStoreWrite, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError, Params: []string{"detail"}},
	CodeResumeSpool: {Code: CodeResumeSpool, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError, Params: []string{"path", "detail"}},
	// 507: the request is well-formed and permitted — the target simply
	// cannot hold the result. RFC 4918's Insufficient Storage is the one
	// status that says that, and it keeps the pre-flight refusal apart
	// from the 500 of a store that broke.
	CodeInsufficientSpace: {Code: CodeInsufficientSpace, Class: ClassOperational, HTTPStatus: http.StatusInsufficientStorage, Params: []string{"path", "needed", "available", "shortfall", "margin", "free"}},
	CodeFileTooLarge:      {Code: CodeFileTooLarge, Class: ClassOperational, HTTPStatus: http.StatusInsufficientStorage, Params: []string{"path", "filesystem", "limit", "size", "what"}},

	// Removable-media transport (FR-054). The two overridable blocks are
	// policy refusals — an operator plugged in the wrong or the older
	// medium, and an admin may say otherwise (FR-094); everything else is
	// a verification verdict, which no override reopens.
	CodeMediaManifestMissing:    {Code: CodeMediaManifestMissing, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path"}},
	CodeMediaManifestUnreadable: {Code: CodeMediaManifestUnreadable, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "detail"}},
	CodeMediaFormatUnsupported:  {Code: CodeMediaFormatUnsupported, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"found", "supported"}},
	CodeMediaStoreFormat:        {Code: CodeMediaStoreFormat, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"found", "supported"}},
	CodeMediaGraphAltered:       {Code: CodeMediaGraphAltered, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "expected", "actual"}},
	CodeMediaZoneMismatch:       {Code: CodeMediaZoneMismatch, Class: ClassPolicy, HTTPStatus: http.StatusConflict, Params: []string{"expected", "found"}},
	CodeMediaStale:              {Code: CodeMediaStale, Class: ClassPolicy, HTTPStatus: http.StatusConflict, Params: []string{"zone", "resolved", "recorded", "media"}},

	CodeMediaFileMissing:       {Code: CodeMediaFileMissing, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "recipe"}},
	CodeMediaFileSize:          {Code: CodeMediaFileSize, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "expected", "actual"}},
	CodeMediaFileDigest:        {Code: CodeMediaFileDigest, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "expected", "actual"}},
	CodeMediaFileUninventoried: {Code: CodeMediaFileUninventoried, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "recipe"}},
	CodeMediaContentUnreadable: {Code: CodeMediaContentUnreadable, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "detail"}},
	CodeMediaContentAddress:    {Code: CodeMediaContentAddress, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "expected", "actual"}},

	CodeMediaUncovered:       {Code: CodeMediaUncovered, Class: ClassOperational, Params: []string{"path"}},
	CodeMediaUnreachable:     {Code: CodeMediaUnreachable, Class: ClassOperational, Params: []string{"path"}},
	CodeMediaMetadataAltered: {Code: CodeMediaMetadataAltered, Class: ClassOperational, Params: []string{"path"}},

	// 403 on both closed-gate codes, and deliberately not 404 or a bare
	// 503: the content exists and the instance is alive — it is refusing,
	// and a refusal an operator can read is the difference between
	// clicking Verify and calling support. ClassPolicy, because that is
	// what a secure-by-default refusal is (FR-075); the verification
	// verdicts themselves keep ClassVerification above.
	CodeMediaUnverified: {Code: CodeMediaUnverified, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"surface", "media", "screen"}},
	// 409: the medium is busy being verified, and the answer is to wait
	// for the run already in flight.
	CodeMediaVerificationRunning: {Code: CodeMediaVerificationRunning, Class: ClassOperational, HTTPStatus: http.StatusConflict, Params: []string{"stage"}},
	CodeMediaNotCleared:          {Code: CodeMediaNotCleared, Class: ClassPolicy, HTTPStatus: http.StatusForbidden, Params: []string{"surface", "media", "verdict", "screen"}},

	// 422: the request is well formed, the confirmation is simply not the
	// word the requirement asks for.
	CodeResetConfirmation: {Code: CodeResetConfirmation, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"phrase"}},

	CodeLayoutInvalid: {Code: CodeLayoutInvalid, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "detail"}},
	CodeLayoutUnsafe:  {Code: CodeLayoutUnsafe, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"path", "entry", "reason"}},
	CodeLayoutTarget:  {Code: CodeLayoutTarget, Class: ClassOperational, HTTPStatus: http.StatusConflict, Params: []string{"path"}},

	CodeTaskNotFound: {Code: CodeTaskNotFound, Class: ClassOperational, HTTPStatus: http.StatusNotFound, Params: []string{"id"}},

	CodeInternal:    {Code: CodeInternal, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError},
	CodeNotFound:    {Code: CodeNotFound, Class: ClassOperational, HTTPStatus: http.StatusNotFound},
	CodeUnreachable: {Code: CodeUnreachable, Class: ClassOperational},
}
