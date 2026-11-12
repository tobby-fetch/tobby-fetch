// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package federation

import (
	"testing"
	"time"
)

// FuzzSAMLResponse fuzzes the parsing path of the assertion consumer
// service — everything a hostile POST reaches once its RelayState names a
// sign-in: the library's XML validation and signature checks, then
// Tobby's own Destination, clock and subject checks (ADR-0009 arbitration
// 3: the XML-DSig chain is where SAML implementations break).
//
// Two properties: nothing panics, and nothing the fuzzer derives from a
// signed response is accepted under another identity or role than the
// one signed — a mutation that changes who signs in must be refused.
//
//	go test -run '^$' -fuzz FuzzSAMLResponse ./internal/federation/
func FuzzSAMLResponse(f *testing.F) {
	idp := newTestIDP(f)
	svc := samlService(f, idp, 90*time.Second)
	p := svc.saml
	sp := p.current()
	const requestID = "_fuzz-request"
	seed := idp.response(f, &resp{inResponseTo: requestID, groups: []string{"staff"}})
	f.Add(seed)
	f.Add(idp.response(f, &resp{inResponseTo: requestID, groups: []string{"staff"}, unsigned: true}))
	f.Add(idp.response(f, &resp{groups: []string{"root"}}))
	f.Add([]byte(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"/>`))
	f.Add([]byte(`<!DOCTYPE x [<!ENTITY a "aaaa">]><x>&a;</x>`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxSAMLBody {
			return // the ACS refuses it before parsing
		}
		// A fresh replay cache per input: the property under test is the
		// verification, and the seed must be acceptable on every run.
		p.replay = &replayCache{now: time.Now}
		sp.ReplayCache = p.replay
		res, err := p.verify(sp, data, requestID, time.Now())
		if err != nil {
			return
		}
		if res.Name != "saml:alice" || res.Role != "viewer" {
			t.Fatalf("a derived response was accepted as %s/%s", res.Name, res.Role)
		}
	})
}
