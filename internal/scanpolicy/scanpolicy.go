// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package scanpolicy is the effective content policy of one instance, as
// one document (FR-031 amendment 2026-11-16, R-15; FR-098, R-17): the
// scan mode and threshold, every exception in force or past — the
// instance's own, those born of its OpenVEX documents, and those of the
// last served Retriever — with its origin, scope, justification, expiry
// and state, the documents with what they admitted and ignored, and the
// hardening switches.
//
// One builder serves GET /api/v1/policy, the /admin/policy screen and
// `tobby scan exceptions`, so the three cannot answer differently
// (FR-061), and the tobby_scan_exceptions gauge counts from the same
// snapshot. The state is computed at the instant the document is asked
// for: an expiry that passes on a running instance shows at once,
// without a restart.
package scanpolicy

import (
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
	"github.com/tobby-fetch/tobby-fetch/internal/scan/vex"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The states of an exception.
const (
	StateActive    = "active"
	StateExpired   = "expired"
	StateUnbounded = "unbounded"
)

// Source is the policy of one instance: fixed at startup but for the
// Retriever's exceptions, which change with each served Retriever.
type Source struct {
	mode      scan.Mode
	threshold float64
	instance  []scan.Exception
	documents []*vex.Translation
	hardening hardening.Switches
	// retriever returns the last served Retriever's exceptions, and
	// whether one has been served; nil on a source that cannot know them
	// (the command line, offline).
	retriever func() ([]scan.Exception, bool)
	// blocking reports whether the policy in force blocks — the instance
	// mode, or a threshold the Retriever poses; nil reads the mode alone.
	blocking func() bool
	now      func() time.Time
}

// Options assembles a Source.
type Options struct {
	Scan      *config.Scan
	Documents []*vex.Translation
	Hardening hardening.Switches
	Retriever func() ([]scan.Exception, bool)
	Blocking  func() bool
	Now       func() time.Time
}

// New builds the Source of an instance.
func New(o *Options) *Source {
	s := &Source{
		mode:      scan.Mode(o.Scan.Mode),
		threshold: o.Scan.Threshold,
		instance:  InstanceExceptions(o.Scan),
		documents: o.Documents,
		hardening: o.Hardening,
		retriever: o.Retriever,
		blocking:  o.Blocking,
		now:       o.Now,
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// InstanceExceptions maps scan.exceptions[] onto policy exceptions,
// stamped with the instance level, their scope kept (R-15). An empty
// configured scope stays nil: the exception is instance-wide.
func InstanceExceptions(c *config.Scan) []scan.Exception {
	out := make([]scan.Exception, 0, len(c.Exceptions))
	for _, ex := range c.Exceptions {
		se := scan.Exception{ID: ex.ID, Justification: ex.Justification, Expires: ex.Expires, Level: scan.LevelInstance}
		if !ex.Scope.IsZero() {
			se.Scope = &scan.ExceptionScope{Digest: ex.Scope.Digest, Repository: ex.Scope.Repository, Recipe: ex.Scope.Recipe}
		}
		out = append(out, se)
	}
	return out
}

// LoadDocuments reads and translates every scan.vex document. A document
// that cannot be read or validated is the TBY-SCN-027 startup refusal,
// naming the file; the documents are never fetched (NFR-019).
func LoadDocuments(docs []config.VEXDocument) ([]*vex.Translation, error) {
	out := make([]*vex.Translation, 0, len(docs))
	for _, d := range docs {
		doc, err := vex.ReadFile(d.Path)
		if err == nil {
			var tr *vex.Translation
			if tr, err = vex.Translate(doc, d.Path, d.Expires); err == nil {
				out = append(out, tr)
				continue
			}
		}
		return nil, taxonomy.New(taxonomy.CodeScanVEXInvalid, taxonomy.Params{"path": d.Path, "detail": err.Error()}).WithCause(err)
	}
	return out, nil
}

// Exceptions returns every VEX-born exception of the documents, in
// declaration order — what the engine adds to the instance level.
func Exceptions(docs []*vex.Translation) []scan.Exception {
	var out []scan.Exception
	for _, d := range docs {
		out = append(out, d.Exceptions...)
	}
	return out
}

// View is the effective policy document: the body of GET /api/v1/policy
// and of `tobby scan exceptions --output json`.
type View struct {
	// EvaluatedAt is the instant the states were computed at.
	EvaluatedAt time.Time `json:"evaluated_at"`
	// Mode and Threshold are the instance level (scan.mode,
	// scan.threshold); Blocking whether the policy in force refuses —
	// the mode, or a threshold the served Retriever poses.
	Mode      string  `json:"mode"`
	Threshold float64 `json:"threshold"`
	Blocking  bool    `json:"blocking"`
	// RetrieverKnown is false while no Retriever has been served by this
	// process, and always on the command line: the Retriever's
	// exceptions are then not listed — which does not mean there are
	// none.
	RetrieverKnown bool `json:"retriever_known"`
	// Exceptions lists every exception: the instance's, the VEX-born,
	// the Retriever's.
	Exceptions []Exception `json:"exceptions"`
	// Documents are the scan.vex documents with what they yielded.
	Documents []Document `json:"vex_documents"`
	// Hardening is the pair of FR-098 switches.
	Hardening Hardening `json:"hardening"`
}

// Exception is one exception as the view states it.
type Exception struct {
	ID string `json:"id"`
	// Origin is "instance", "retriever" or "vex".
	Origin string `json:"origin"`
	// Document is the OpenVEX document path, for origin "vex".
	Document      string               `json:"document,omitempty"`
	Justification string               `json:"justification,omitempty"`
	Scope         *scan.ExceptionScope `json:"scope,omitempty"`
	// Expires is absent for an unbounded Retriever exception.
	Expires *time.Time `json:"expires,omitempty"`
	// State is "active", "expired" or "unbounded" (a Retriever exception
	// without expiry, applied: TBY-SCN-028).
	State string `json:"state"`
}

// Document is one scan.vex document as the view states it.
type Document struct {
	Path       string    `json:"path"`
	DocumentID string    `json:"document_id"`
	Expires    time.Time `json:"expires"`
	// State is "active" or "expired": the declared expiry of every
	// exception the document yields.
	State      string `json:"state"`
	Statements int    `json:"statements"`
	Admitted   int    `json:"admitted"`
	Ignored    int    `json:"ignored"`
	// Exceptions counts the exceptions it yields (products × identifiers).
	Exceptions int        `json:"exceptions"`
	Notes      []vex.Note `json:"notes,omitempty"`
}

// Hardening is the pair of FR-098 switches, both always stated.
type Hardening struct {
	DisableUnitImport   bool `json:"disableUnitImport"`
	DisableRegistryPush bool `json:"disableRegistryPush"`
}

// stateOf is the state of one exception at now.
func stateOf(ex *scan.Exception, now time.Time) string {
	switch {
	case ex.Unbounded():
		return StateUnbounded
	case ex.ExpiredAt(now):
		return StateExpired
	}
	return StateActive
}

func exceptionView(ex *scan.Exception, now time.Time) Exception {
	v := Exception{
		ID: ex.ID, Origin: ex.Source(), Document: ex.VEX,
		Justification: ex.Justification, Scope: ex.Scope, State: stateOf(ex, now),
	}
	if !ex.Expires.IsZero() {
		t := ex.Expires.UTC()
		v.Expires = &t
	}
	return v
}

// View builds the document at the source's clock.
func (s *Source) View() *View {
	now := s.now()
	v := &View{
		EvaluatedAt: now.UTC(),
		Mode:        string(s.mode),
		Threshold:   s.threshold,
		Blocking:    s.mode == scan.ModeBlock,
		Exceptions:  []Exception{},
		Documents:   []Document{},
		Hardening:   Hardening{DisableUnitImport: s.hardening.DisableUnitImport, DisableRegistryPush: s.hardening.DisableRegistryPush},
	}
	if s.blocking != nil {
		v.Blocking = s.blocking()
	}
	for i := range s.instance {
		v.Exceptions = append(v.Exceptions, exceptionView(&s.instance[i], now))
	}
	for _, d := range s.documents {
		dv := Document{
			Path: d.Path, DocumentID: d.DocumentID, Expires: d.Expires.UTC(), State: StateActive,
			Statements: d.Statements, Admitted: d.Admitted, Ignored: d.Ignored,
			Exceptions: len(d.Exceptions), Notes: d.Notes,
		}
		if now.After(d.Expires) {
			dv.State = StateExpired
		}
		v.Documents = append(v.Documents, dv)
		for i := range d.Exceptions {
			v.Exceptions = append(v.Exceptions, exceptionView(&d.Exceptions[i], now))
		}
	}
	if s.retriever != nil {
		exs, known := s.retriever()
		v.RetrieverKnown = known
		for i := range exs {
			v.Exceptions = append(v.Exceptions, exceptionView(&exs[i], now))
		}
	}
	return v
}

// Counts feeds tobby_scan_exceptions at the source's clock: an
// unbounded exception counts as active — it applies.
func (s *Source) Counts() metrics.ExceptionCounts {
	counts := metrics.ExceptionCounts{}
	for _, ex := range s.View().Exceptions {
		state := metrics.ExceptionActive
		if ex.State == StateExpired {
			state = metrics.ExceptionExpired
		}
		if counts[ex.Origin] == nil {
			counts[ex.Origin] = map[string]int{}
		}
		counts[ex.Origin][state]++
	}
	return counts
}
