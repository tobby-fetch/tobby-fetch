// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// mapError converts any engine failure into its taxonomy entry (R-03):
// SDK validation errors carry file/path/constraint (FR-011), signature
// failures carry the fingerprints tried (FR-033), registry failures their
// stable transport codes. subject names the file/reference for messages.
func (e *Engine) mapError(err error, subject string) *taxonomy.Error {
	// FR-055: a write that hit the filesystem's file-size ceiling reads
	// as the same problem the pre-flight refuses in advance, and it can
	// only be named here — the package-level mapping below has no store
	// to ask which filesystem the bytes were headed for.
	if te := fileTooLargeError(err, e.store.Root(), 0); te != nil {
		return te
	}
	return mapEngineError(err, subject)
}

// mapEngineError is the package-level form of the mapping, for the paths
// that run without an Engine: the Publisher (R-36/R-40) is deliberately
// not built on the reading side, but an unreachable or refusing registry
// must produce the same TBY-REG code with the same what/cause/action
// block there as everywhere else (R-03) — `tobby recipe push` used to
// print a raw "dial tcp: connection refused" for the exact failure the
// UI taxonomizes.
func mapEngineError(err error, subject string) *taxonomy.Error {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te
	}

	// FR-011: SDK validation — file, path, violated constraint.
	var fieldErrs spec.ErrorList
	var fieldErr *spec.FieldError
	switch {
	case errors.As(err, &fieldErrs) && len(fieldErrs) > 0:
		fieldErr = fieldErrs[0]
	case errors.As(err, &fieldErr):
	}
	if fieldErr != nil {
		return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": subject, "path": fieldErr.Path, "constraint": fieldErr.Message,
		}).WithCause(err)
	}

	// §11.2 layout violations read as validation failures too: the
	// artifact, not the network, is wrong.
	var le *cookbook.LayoutError
	if errors.As(err, &le) {
		return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": subject, "path": layoutPath, "constraint": le.Error(),
		}).WithCause(err)
	}

	// FR-033: signature verdicts. The trust scope that decided the verdict
	// is named when one matched (scopeOf): a key valid for another scope
	// is refused here, and the operator reads WHICH scope refused it.
	scope := scopeOf(err)
	var noKey *sigverify.NoTrustedKeyError
	if errors.As(err, &noKey) {
		params := taxonomy.Params{
			"recipe": subject, "fingerprints": strings.Join(noKey.Tried, ", "), "scope": scope,
		}
		// B-057: the reserved root the signature does verify against.
		var se *scopedError
		if errors.As(err, &se) && se.reserved != "" {
			params["reserved"], params["reservedto"] = se.reserved, se.reservedTo
		}
		return taxonomy.New(taxonomy.CodeSignature, params).WithCause(err)
	}
	var badSig *sigverify.BadSignatureError
	if errors.As(err, &badSig) {
		return taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
			"recipe": subject, "fingerprints": badSig.Reason, "scope": scope,
		}).WithCause(err)
	}
	if errors.Is(err, sigverify.ErrNoSignature) {
		return taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
			"recipe": subject, "fingerprints": "no signature artifact found", "scope": scope,
		}).WithCause(err)
	}

	// FR-021: version resolution.
	if strings.HasPrefix(err.Error(), "version ") {
		return taxonomy.New(taxonomy.CodeVersionResolve, taxonomy.Params{
			"reference": subject, "constraint": "", "detail": err.Error(),
		}).WithCause(err)
	}

	// Registry transport failures.
	if errors.Is(err, context.DeadlineExceeded) {
		return taxonomy.New(taxonomy.CodeInspectTimeout,
			taxonomy.Params{"host": subject, "timeout": "sync"}).WithCause(err)
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return taxonomy.New(taxonomy.CodeRegistryAuth,
				taxonomy.Params{"host": subject}).WithCause(err)
		case http.StatusNotFound:
			return taxonomy.New(taxonomy.CodeRefNotFound,
				taxonomy.Params{"reference": subject}).WithCause(err)
		}
	}
	if isNotFound(err) {
		return taxonomy.New(taxonomy.CodeRefNotFound,
			taxonomy.Params{"reference": subject}).WithCause(err)
	}
	var nerr net.Error
	if errors.As(err, &nerr) || errors.Is(err, syscall.ECONNREFUSED) ||
		strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such host") {
		return taxonomy.New(taxonomy.CodeRegistryUnreachable,
			taxonomy.Params{"host": subject}).WithCause(err)
	}

	return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
}

// scopedError carries the declared trust scope that decided a signature
// verdict (FR-033, RECIPE-SPEC §12.3) through the error chain to the one
// place that builds the taxonomy entry. The verdict itself stays the
// sigverify error underneath — errors.Is and errors.As see through it —
// and only the mapping reads the scope, so no call site between the
// verification and the report has to know it exists.
//
// It also carries, after a refusal, the excluded trust root the signature
// does verify against (B-057), so the refusal names the root and the
// scope it is reserved to instead of leaving a valid key unexplained.
type scopedError struct {
	scope string
	// reserved and reservedTo are TBY-SIG-001's "reserved" and
	// "reservedto" parameters (media.SignatureElsewhere); empty when no
	// excluded root verifies the signature.
	reserved, reservedTo string
	err                  error
}

func (e *scopedError) Error() string { return e.err.Error() }
func (e *scopedError) Unwrap() error { return e.err }

// withScope names the scope that decided err. An empty scope is the
// strict default, which has no name to give, and a nil error stays nil.
func withScope(scope string, err error) error {
	if err == nil || scope == "" {
		return err
	}
	return &scopedError{scope: scope, err: err}
}

// refused wraps a signature failure decided under d, naming the scope
// and — for a well-formed signature no key of d verifies — the excluded
// root that does verify it (B-057). src/repo/digest locate the subject
// exactly as the failed verification read it.
func refused(ctx context.Context, src sigverify.Manifests, repo, manifestDigest string, d Decision, err error) error {
	if err == nil {
		return nil
	}
	var noKey *sigverify.NoTrustedKeyError
	if !errors.As(err, &noKey) {
		return withScope(d.Scope, err)
	}
	reserved, reservedTo := media.SignatureElsewhere(ctx, src, repo, manifestDigest, d)
	if reserved == "" {
		return withScope(d.Scope, err)
	}
	return &scopedError{scope: d.Scope, reserved: reserved, reservedTo: reservedTo, err: err}
}

// noKeyRefusal is TBY-SIG-001 for a decision with no key to try and no
// allowUnsigned relaxation: either no trust root is configured, or every
// configured root is reserved to a scope (B-057) — then the message says
// so, and names the reserved root that verifies the signature, if any.
//
// Inside an allowUnsigned scope it is reached only by a subject that
// carries a signature (B-068), and the detail says so.
func noKeyRefusal(ctx context.Context, src sigverify.Manifests, repo, manifestDigest, subject string, d Decision) error {
	detail := media.NoKeyDetail(d)
	if d.AllowUnsigned {
		detail = media.SignedNoKeyDetail(d)
	}
	params := taxonomy.Params{"recipe": subject, "fingerprints": detail, "scope": d.Scope}
	if reserved, reservedTo := media.SignatureElsewhere(ctx, src, repo, manifestDigest, d); reserved != "" {
		params["reserved"], params["reservedto"] = reserved, reservedTo
	}
	return taxonomy.New(taxonomy.CodeSignature, params)
}

// scopeOf returns the scope an error was decided under, or "" for the
// strict default.
func scopeOf(err error) string {
	var se *scopedError
	if errors.As(err, &se) {
		return se.scope
	}
	return ""
}
