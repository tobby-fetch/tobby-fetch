// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package vex

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/scan"
)

// MaxExceptions caps the exceptions one document may yield — products
// times identifiers. Beyond it the document is refused, never truncated.
const MaxExceptions = 100000

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Translation is what one document yields: its exceptions, and what it
// said that adds nothing, with the reason — for the startup log and the
// effective policy view.
type Translation struct {
	// Path is the local document, as scan.vex[].path names it.
	Path string `json:"path"`
	// DocumentID is the document's @id.
	DocumentID string `json:"document_id"`
	// Expires is the declared expiry of every exception it yields.
	Expires time.Time `json:"expires"`
	// Statements counts the statements of the document; Admitted those
	// that yielded at least one exception, Ignored the others.
	Statements int `json:"statements"`
	Admitted   int `json:"admitted"`
	Ignored    int `json:"ignored"`
	// Notes say why a statement — or one product of an admitted
	// statement — adds nothing.
	Notes []Note `json:"notes,omitempty"`
	// Exceptions are the exceptions, each scoped by one product.
	Exceptions []scan.Exception `json:"-"`
}

// Note is one statement, or one product of one, that adds nothing.
type Note struct {
	// Statement is the statement's index in the document.
	Statement int `json:"statement"`
	// Vulnerability is the statement's vulnerability name.
	Vulnerability string `json:"vulnerability"`
	// Product is the product @id, when the note is about one product.
	Product string `json:"product,omitempty"`
	// Reason is a closed value: "status" (a status other than
	// not_affected), "impact-statement-only", "unbounded-product" (a
	// product that cannot be mapped onto a digest or a repository).
	Reason string `json:"reason"`
	// Detail says more, in English, for the log.
	Detail string `json:"detail,omitempty"`
}

// The closed reasons of a Note.
const (
	NoteStatus              = "status"
	NoteImpactStatementOnly = "impact-statement-only"
	NoteUnboundedProduct    = "unbounded-product"
)

// Translate turns the admitted statements of d into exceptions: one per
// product and per identifier (the name and its aliases), at the instance
// level, bounded by expires, scoped by the product, and naming the
// document path (FR-031 amendment 2026-11-16).
//
// A product that cannot be bounded — not an image Tobby can name by
// digest or repository, a tag that moves, a subcomponent that is not a
// package URL — is SKIPPED, never widened into an exception for every
// item: skipping is the side that refuses more, and it is reported in the
// Notes (startup log, policy view). A statement none of whose products can
// be bounded is ignored as a whole.
func Translate(d *Document, path string, expires time.Time) (*Translation, error) {
	if expires.IsZero() {
		return nil, errors.New("no expiry declared: an exception is never unbounded")
	}
	t := &Translation{Path: path, DocumentID: d.ID, Expires: expires, Statements: len(d.Statements)}
	for i := range d.Statements {
		s := &d.Statements[i]
		name := s.Vulnerability.Name
		if !s.Admits() {
			t.Ignored++
			n := Note{Statement: i, Vulnerability: name, Reason: NoteStatus, Detail: "status " + string(s.Status) + " adds no exception"}
			if s.Status == StatusNotAffected {
				n.Reason, n.Detail = NoteImpactStatementOnly, "a not_affected statement with an impact statement and no justification adds no exception"
			}
			t.Notes = append(t.Notes, n)
			continue
		}
		ids := append([]string{name}, s.Vulnerability.Aliases...)
		justification := fmt.Sprintf("%s (OpenVEX %s)", s.Justification, d.ID)
		yielded := false
		for j := range s.Products {
			p := &s.Products[j]
			scope, err := ScopeOf(p)
			if err != nil {
				t.Notes = append(t.Notes, Note{Statement: i, Vulnerability: name, Product: productName(p), Reason: NoteUnboundedProduct, Detail: err.Error()})
				continue
			}
			yielded = true
			for _, id := range dedupe(ids) {
				if len(t.Exceptions) >= MaxExceptions {
					return nil, fmt.Errorf("the document yields more than %d exceptions", MaxExceptions)
				}
				sc := *scope
				sc.Packages = append([]string(nil), scope.Packages...)
				t.Exceptions = append(t.Exceptions, scan.Exception{
					ID: id, Justification: justification, Expires: expires,
					Level: scan.LevelInstance, Scope: &sc, VEX: path,
				})
			}
		}
		if yielded {
			t.Admitted++
		} else {
			t.Ignored++
		}
	}
	return t, nil
}

// productName is what a note names a product by.
func productName(p *Product) string {
	if p.ID != "" {
		return p.ID
	}
	if p.Identifiers != nil {
		return p.Identifiers.PURL
	}
	return ""
}

// ScopeOf maps a product onto an exception scope, or says why it cannot.
// The forms bounded safely:
//
//   - pkg:oci/<name>@sha256%3A<hex>, with an optional repository_url
//     qualifier: the digest, and the repository when it is named;
//   - pkg:oci/<name>?repository_url=<host/path>: the repository;
//   - sha256:<hex>: the digest;
//   - <host>/<path>@sha256:<hex>: the digest and the repository;
//   - <host>/<path>: the repository.
//
// The repository is canonicalized as ingredients are (ADR-0013), so it is
// compared with the same form. Subcomponents narrow the scope to the
// packages they name, by package URL matched exactly against each
// finding's (B-070); one that is not a package URL makes the product
// unbounded.
func ScopeOf(p *Product) (*scan.ExceptionScope, error) {
	id := p.ID
	if id == "" && p.Identifiers != nil {
		id = p.Identifiers.PURL
	}
	if id == "" {
		return nil, errors.New("the product has neither an @id nor a package URL: only images named by digest or repository can be bounded")
	}
	var scope *scan.ExceptionScope
	var err error
	switch {
	case strings.HasPrefix(id, "pkg:"):
		scope, err = ociPURLScope(id)
	case strings.HasPrefix(id, "sha256:"):
		if !digestPattern.MatchString(id) {
			return nil, fmt.Errorf("%q is not a sha256 digest", id)
		}
		scope = &scan.ExceptionScope{Digest: id}
	case strings.Contains(id, "://"):
		return nil, fmt.Errorf("%q is a URL, not an image reference", id)
	default:
		scope, err = referenceScope(id)
	}
	if err != nil {
		return nil, err
	}
	for i := range p.Subcomponents {
		c := &p.Subcomponents[i]
		sid := c.ID
		if !strings.HasPrefix(sid, "pkg:") && c.Identifiers != nil {
			sid = c.Identifiers.PURL
		}
		pu, err := packageURL(sid)
		if err != nil {
			return nil, fmt.Errorf("subcomponents[%d]: %w", i, err)
		}
		scope.Packages = append(scope.Packages, pu)
	}
	scope.Packages = dedupe(scope.Packages)
	sort.Strings(scope.Packages)
	return scope, nil
}

// ociPURLScope reads a package URL of type oci (purl-spec, type oci): its
// version is the manifest digest, its repository_url qualifier the
// repository.
func ociPURLScope(id string) (*scan.ExceptionScope, error) {
	pu, err := scan.ParsePackageURL(id)
	if err != nil {
		return nil, err
	}
	if pu.Type != "oci" {
		return nil, fmt.Errorf("%q is a package of type %s, not an image (pkg:oci)", id, pu.Type)
	}
	scope := &scan.ExceptionScope{}
	if pu.Version != "" {
		if !digestPattern.MatchString(pu.Version) {
			return nil, fmt.Errorf("%q: the version of a pkg:oci is the manifest digest, sha256:<64 hex>", id)
		}
		scope.Digest = pu.Version
	}
	if ru := pu.Qualifiers.Get("repository_url"); ru != "" {
		repo, err := canonicalRepository(ru)
		if err != nil {
			return nil, fmt.Errorf("%q: repository_url: %w", id, err)
		}
		if last := repo[strings.LastIndexByte(repo, '/')+1:]; !strings.EqualFold(last, pu.Name) {
			return nil, fmt.Errorf("%q: repository_url %q does not end with the package name %q", id, ru, pu.Name)
		}
		scope.Repository = repo
	}
	if scope.Digest == "" && scope.Repository == "" {
		return nil, fmt.Errorf("%q names neither a digest nor a repository_url: it would cover every image called %s", id, pu.Name)
	}
	return scope, nil
}

// referenceScope reads an OCI reference: host/path, optionally @digest.
// A tag is refused — it moves, so it bounds nothing.
func referenceScope(id string) (*scan.ExceptionScope, error) {
	ref, digest, pinned := strings.Cut(id, "@")
	scope := &scan.ExceptionScope{}
	if pinned {
		if !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("%q: the digest must be sha256:<64 hex>", id)
		}
		scope.Digest = digest
	}
	repo, err := canonicalRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", id, err)
	}
	scope.Repository = repo
	return scope, nil
}

// canonicalRepository canonicalizes a nominal repository (ADR-0013): an
// explicit registry host, no scheme, no tag, no digest.
func canonicalRepository(ref string) (string, error) {
	if i := strings.LastIndexByte(ref, '/'); i >= 0 && strings.Contains(ref[i:], ":") {
		return "", errors.New("a tag moves and bounds nothing: name the digest")
	}
	repo, err := relocate.Path(ref)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(repo, "*?[]") {
		return "", errors.New("a VEX product names one repository, never a pattern")
	}
	return repo, nil
}

// packageURL reads a subcomponent's package URL into the form an
// exception scope keeps (B-070): the package URL itself, matched against
// the finding's own package URL on type, namespace, name, and the
// declared version and qualifiers (scan.PackageURL.Matches). No bare name
// or alias is derived from it — that widened the exception to every
// package of the same name in any ecosystem, at any version. A
// subcomponent with a subpath names a part of a package the scanner never
// reports alone: it cannot be bounded, so its product is refused.
func packageURL(id string) (string, error) {
	if !strings.HasPrefix(id, "pkg:") {
		return "", fmt.Errorf("%q is not a package URL: a subcomponent narrows to a package, and one that cannot be read would leave the whole image covered", id)
	}
	pu, err := scan.ParsePackageURL(id)
	if err != nil {
		return "", err
	}
	if pu.Subpath != "" {
		return "", fmt.Errorf("%q names a subpath of a package: a finding is reported for a whole package, so it cannot be matched exactly", id)
	}
	return id, nil
}

// dedupe drops repeated strings, keeping the first occurrence.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
