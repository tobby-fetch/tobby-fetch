// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package taxonomy

import "net/http"

// Stable codes. Grouped by domain: AUTH (authentication and authorization),
// CFG (configuration), VAL (recipe/retriever validation, FR-011), REG
// (source registry access), POL (policy refusals, FR-030), SIG (signature
// and digest verification, FR-033), DST (destination limits, FR-035), STO
// (local store), TSK (tasks), SRV (instance itself).
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

	// CodeConfigInvalid is a rejected configuration (FR-003 validation).
	CodeConfigInvalid Code = "TBY-CFG-001"

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

	// CodeChartDependency is a Helm chart whose declared dependency is not
	// embedded in the package (FR-024): unusable offline, the import fails
	// naming the missing dependency.
	CodeChartDependency Code = "TBY-CHT-001"

	// CodeStoreRead is a failed read of the local store.
	CodeStoreRead Code = "TBY-STO-001"
	// CodeStoreWrite is a failed write to the local store.
	CodeStoreWrite Code = "TBY-STO-002"

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

	CodeConfigInvalid: {Code: CodeConfigInvalid, Class: ClassOperational, Params: []string{"detail"}},

	CodeProxyInvalid: {Code: CodeProxyInvalid, Class: ClassOperational, Params: []string{"setting", "proxy"}},
	CodeTrustStore:   {Code: CodeTrustStore, Class: ClassOperational, Params: []string{"source"}},
	CodeServerTLS:    {Code: CodeServerTLS, Class: ClassOperational, Params: []string{"source"}},

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

	CodeVersionResolve: {Code: CodeVersionResolve, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "constraint", "detail"}},

	CodeSignature:      {Code: CodeSignature, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"recipe", "fingerprints"}},
	CodeDigestMismatch: {Code: CodeDigestMismatch, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "expected", "actual"}},
	CodeArtifactType:   {Code: CodeArtifactType, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "expected", "actual"}},

	CodeDestinationLimit: {Code: CodeDestinationLimit, Class: ClassOperational, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"reference", "limit"}},

	CodeChartDependency: {Code: CodeChartDependency, Class: ClassVerification, HTTPStatus: http.StatusUnprocessableEntity, Params: []string{"chart", "dependency"}},

	CodeStoreRead:  {Code: CodeStoreRead, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError, Params: []string{"detail"}},
	CodeStoreWrite: {Code: CodeStoreWrite, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError, Params: []string{"detail"}},

	CodeTaskNotFound: {Code: CodeTaskNotFound, Class: ClassOperational, HTTPStatus: http.StatusNotFound, Params: []string{"id"}},

	CodeInternal:    {Code: CodeInternal, Class: ClassOperational, HTTPStatus: http.StatusInternalServerError},
	CodeNotFound:    {Code: CodeNotFound, Class: ClassOperational, HTTPStatus: http.StatusNotFound},
	CodeUnreachable: {Code: CodeUnreachable, Class: ClassOperational},
}
