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

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// mapError converts any engine failure into its taxonomy entry (R-03):
// SDK validation errors carry file/path/constraint (FR-011), signature
// failures carry the fingerprints tried (FR-033), registry failures their
// stable transport codes. subject names the file/reference for messages.
func (e *Engine) mapError(err error, subject string) *taxonomy.Error {
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
	var le *layoutError
	if errors.As(err, &le) {
		return taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": subject, "path": "artifact layout (RECIPE-SPEC §11.2)", "constraint": le.reason,
		}).WithCause(err)
	}

	// FR-033: signature verdicts.
	var noKey *sigverify.NoTrustedKeyError
	if errors.As(err, &noKey) {
		return taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
			"recipe": subject, "fingerprints": strings.Join(noKey.Tried, ", "),
		}).WithCause(err)
	}
	var badSig *sigverify.BadSignatureError
	if errors.As(err, &badSig) {
		return taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
			"recipe": subject, "fingerprints": badSig.Reason,
		}).WithCause(err)
	}
	if errors.Is(err, sigverify.ErrNoSignature) {
		return taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
			"recipe": subject, "fingerprints": "no signature artifact found",
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
