// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"context"
	"errors"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
)

// samlProvider is the SAML 2.0 service provider (FR-071). Its behaviour
// lands with the SAML half of 6.3; until then configuration refuses
// auth.saml.enabled (config.pendingOptIns) and this is never built.
type samlProvider struct {
	name string
}

func newSAMLProvider(*config.SAML, *Service) (*samlProvider, error) {
	return nil, errors.New("federation: SAML is not implemented by this build yet (6.3)")
}

func (p *samlProvider) probes() bool                { return false }
func (p *samlProvider) probe(context.Context) error { return nil }

// MaxSAMLBody bounds the assertion consumer service's request body.
const MaxSAMLBody = 256 << 10

// SAMLBodyFailure refuses an unreadable or oversized ACS body.
func (s *Service) SAMLBodyFailure(err error) error { return err }

// BeginSAML is not implemented by this build yet.
func (s *Service) BeginSAML(context.Context, string) (redirect, binding string, err error) {
	return "", "", errors.New("federation: SAML is not enabled")
}

// ConsumeSAML is not implemented by this build yet.
func (s *Service) ConsumeSAML(context.Context, string, string) error {
	return errors.New("federation: SAML is not enabled")
}

// FinishSAML is not implemented by this build yet.
func (s *Service) FinishSAML(string, string) (*Result, error) {
	return nil, errors.New("federation: SAML is not enabled")
}

// SAMLMetadata is not implemented by this build yet.
func (s *Service) SAMLMetadata() ([]byte, error) {
	return nil, errors.New("federation: SAML is not enabled")
}
