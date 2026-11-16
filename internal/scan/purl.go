// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package scan

import (
	"fmt"
	"net/url"
	"strings"
)

// PackageURL is the part of a package URL (purl-spec) Tobby reads:
// pkg:type/namespace/name@version?qualifiers#subpath, each component
// percent-decoded.
type PackageURL struct {
	Type, Namespace, Name, Version, Subpath string
	Qualifiers                              url.Values
}

// ParsePackageURL reads a package URL. The type is lowercased (it is
// case-insensitive by the specification); the other components are kept
// as written, percent-decoded.
func ParsePackageURL(s string) (*PackageURL, error) {
	rest, ok := strings.CutPrefix(s, "pkg:")
	if !ok {
		return nil, fmt.Errorf("%q is not a package URL", s)
	}
	rest, subpath, _ := strings.Cut(rest, "#")
	rest, rawQuery, _ := strings.Cut(rest, "?")
	rest = strings.Trim(rest, "/")
	p := &PackageURL{Subpath: strings.Trim(subpath, "/")}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		v, err := url.PathUnescape(rest[at+1:])
		if err != nil {
			return nil, fmt.Errorf("%q: version: %w", s, err)
		}
		p.Version, rest = v, rest[:at]
	}
	typ, path, ok := strings.Cut(rest, "/")
	if !ok || typ == "" || path == "" {
		return nil, fmt.Errorf("%q: a package URL needs a type and a name", s)
	}
	p.Type = strings.ToLower(typ)
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		v, err := url.PathUnescape(seg)
		if err != nil || v == "" {
			return nil, fmt.Errorf("%q: malformed name", s)
		}
		segs[i] = v
	}
	p.Name = segs[len(segs)-1]
	p.Namespace = strings.Join(segs[:len(segs)-1], "/")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, fmt.Errorf("%q: qualifiers: %w", s, err)
	}
	p.Qualifiers = q
	return p, nil
}

// caseInsensitiveTypes are the purl types whose specification makes the
// namespace and name case-insensitive (lowercased in canonical form).
// For every other type they compare exactly.
var caseInsensitiveTypes = map[string]bool{
	"alpm": true, "apk": true, "bitbucket": true, "composer": true, "deb": true,
	"github": true, "hex": true, "npm": true, "pub": true, "pypi": true,
}

// identity is the namespace and name of p in the form two package URLs of
// the same type compare in.
func (p *PackageURL) identity() (string, string) {
	ns, name := p.Namespace, p.Name
	if caseInsensitiveTypes[p.Type] {
		ns, name = strings.ToLower(ns), strings.ToLower(name)
	}
	if p.Type == "pypi" {
		name = strings.ReplaceAll(name, "_", "-")
	}
	return ns, name
}

// Matches reports whether the package a finding reports — its package URL
// as the scanner gives it, and its installed version — is the package p
// names (B-070). It never widens p:
//
//   - the type, the namespace and the name must all be equal: a Go module
//     never matches a Debian package of the same last segment;
//   - a declared version must equal the finding's, exactly; under a
//     declared version, the epoch qualifier must agree too (absent on
//     both sides, or equal), so 3.0 never matches 1:3.0;
//   - every qualifier p declares must be present on the finding with the
//     same value (arch, distro...);
//   - a p with a subpath names a part of a package the scanner never
//     reports alone: it matches nothing.
//
// A finding without a package URL cannot be matched exactly, so it is not
// matched.
func (p *PackageURL) Matches(findingPURL, installedVersion string) bool {
	if p == nil || findingPURL == "" || p.Subpath != "" {
		return false
	}
	f, err := ParsePackageURL(findingPURL)
	if err != nil || f.Type != p.Type {
		return false
	}
	pns, pname := p.identity()
	fns, fname := f.identity()
	if pns != fns || pname != fname {
		return false
	}
	if p.Version != "" {
		v := f.Version
		if v == "" {
			v = installedVersion
		}
		if v != p.Version || p.Qualifiers.Get("epoch") != f.Qualifiers.Get("epoch") {
			return false
		}
	}
	for k, vs := range p.Qualifiers {
		for _, v := range vs {
			if f.Qualifiers.Get(k) != v {
				return false
			}
		}
	}
	return true
}
