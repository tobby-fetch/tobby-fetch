// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Redacted replaces what the scrub removes.
const Redacted = "[REDACTED]"

// Thresholds of the search. A value shorter than minSecret is not
// searched at all — it would match inside ordinary text and prove
// nothing; one shorter than substringSecret is only matched when
// surrounded by characters that cannot belong to a word, which keeps a
// five-letter password from redacting every log line that contains it as
// a fragment of another word. The weakness is stated in the
// documentation: short secrets get a weaker search.
const (
	minSecret       = 4
	substringSecret = 8
	// maxSecretFile bounds what is read from a secret file: a credentials
	// file is kilobytes, and the search is linear in its size.
	maxSecretFile = 1 << 20
)

// Secrets is the set of secret values the instance knows (FR-095): the
// proxy password, client secrets, private keys, the clear form of any
// credential it read. It is built by the caller from the configuration,
// the environment and the secret files, and used twice: Scrub replaces the
// values in free-form members, Find proves no member still carries one.
//
// It never stores or reports a value in any output: Count and Describe
// say how many and from where, nothing more.
type Secrets struct {
	exact    [][]byte // substring-matched
	bounded  [][]byte // boundary-matched (short values)
	origins  []string
	patterns []pattern
}

// pattern is a shape that is a credential wherever it appears, and what it
// is replaced by.
type pattern struct {
	re   *regexp.Regexp
	repl string
}

// NewSecrets returns an empty set. The generic patterns — private key
// blocks, bearer and basic credentials, a password in a URL — are always
// part of it.
func NewSecrets() *Secrets { return &Secrets{patterns: genericPatterns} }

// Count is the number of distinct search strings, variants included.
func (s *Secrets) Count() int { return len(s.exact) + len(s.bounded) }

// Describe lists where the values came from (config key, file path,
// environment variable name) — never a value.
func (s *Secrets) Describe() []string { return append([]string(nil), s.origins...) }

// Add registers a secret value read from origin and every encoding it is
// likely to appear in: base64 (standard, raw, URL), hex, URL-escaped,
// JSON-escaped.
func (s *Secrets) Add(origin, value string) {
	value = strings.TrimSpace(value)
	if len(value) < minSecret {
		return
	}
	s.origins = append(s.origins, origin)
	forms := []string{value}
	raw := []byte(value)
	forms = append(forms,
		base64.StdEncoding.EncodeToString(raw), base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(raw),
		hex.EncodeToString(raw), url.QueryEscape(value), url.PathEscape(value))
	if j, err := json.Marshal(value); err == nil && len(j) > 2 {
		forms = append(forms, string(j[1:len(j)-1]))
	}
	seen := map[string]bool{}
	for _, f := range forms {
		if len(f) < minSecret || seen[f] {
			continue
		}
		seen[f] = true
		if len(f) < substringSecret {
			s.bounded = append(s.bounded, []byte(f))
		} else {
			s.exact = append(s.exact, []byte(f))
		}
	}
}

// AddValues walks v (a configuration, typically) and registers every
// value of a type that reveals itself — config.Secret has a Reveal method
// precisely so that this walk finds it wherever a future field puts it.
func (s *Secrets) AddValues(origin string, v any) {
	walk(reflect.ValueOf(v), origin, func(path, value string) { s.Add(path, value) })
}

type revealer interface{ Reveal() string }

var (
	secretFieldName = regexp.MustCompile(`(?i)password|passwd|passphrase|secret|token|apikey`)
	pathFieldName   = regexp.MustCompile(`(?i)(file|files|path|paths|dir|url|name|lifetime|ttl)$`)
)

func walk(v reflect.Value, path string, emit func(path, value string)) {
	if !v.IsValid() {
		return
	}
	if v.CanInterface() {
		if r, ok := v.Interface().(revealer); ok {
			emit(path, r.Reveal())
			return
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walk(v.Elem(), path, emit)
		}
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			// A secret that is a plain string rather than a config.Secret
			// is still one when its name says so: a future field typed
			// carelessly must not escape the search. Paths to secret files
			// ("…File", "…Path") name where a secret is, not what it is.
			if v.Field(i).Kind() == reflect.String && secretFieldName.MatchString(f.Name) && !pathFieldName.MatchString(f.Name) {
				emit(path+"."+f.Name, v.Field(i).String())
				continue
			}
			walk(v.Field(i), path+"."+f.Name, emit)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i), emit)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walk(v.MapIndex(k), fmt.Sprintf("%s[%v]", path, k), emit)
		}
	default:
	}
}

// AddFile reads a secret file and registers what it holds: the whole
// content, each line of it, the body of every PEM block, and — for a
// Docker credentials file — each credential in it, decoded. A file that
// does not exist holds nothing to leak and is skipped; any other read
// failure is returned, because an unreadable secret is one the search
// cannot know (the bundle then fails closed, TBY-DIA-001).
func (s *Secrets) AddFile(origin, path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: a configured secret file, read to learn what to redact
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("diag: reading %s (%s): %w", origin, path, err)
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(f, maxSecretFile)); err != nil {
		return fmt.Errorf("diag: reading %s (%s): %w", origin, path, err)
	}
	s.AddContent(origin, buf.Bytes())
	return nil
}

// AddContent registers the secrets in the raw content of a secret file.
func (s *Secrets) AddContent(origin string, content []byte) {
	text := strings.TrimSpace(string(content))
	if text == "" {
		return
	}
	s.Add(origin, text)
	for _, line := range strings.Split(text, "\n") {
		s.Add(origin, strings.TrimSpace(line))
	}
	rest := content
	for {
		block, r := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = r
		body := base64.StdEncoding.EncodeToString(block.Bytes)
		s.Add(origin, body)
		for i := 0; i+64 <= len(body); i += 64 {
			s.Add(origin, body[i:i+64])
		}
	}
	var docker struct {
		Auths map[string]struct {
			Username      string `json:"username"`
			Password      string `json:"password"`
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
			RegistryToken string `json:"registrytoken"`
		} `json:"auths"`
	}
	if json.Unmarshal(content, &docker) == nil {
		for _, a := range docker.Auths {
			s.Add(origin, a.Password)
			s.Add(origin, a.Auth)
			s.Add(origin, a.IdentityToken)
			s.Add(origin, a.RegistryToken)
			if dec, err := base64.StdEncoding.DecodeString(a.Auth); err == nil {
				if _, pw, ok := strings.Cut(string(dec), ":"); ok {
					s.Add(origin, pw)
				}
				s.Add(origin, string(dec))
			}
			if a.Username != "" && a.Password != "" {
				s.Add(origin, a.Username+":"+a.Password)
			}
		}
	}
}

// Find returns the first reason data still carries a secret, or "". The
// reason names the kind (known value, private key, credential), never the
// value.
func (s *Secrets) Find(data []byte) string {
	for _, v := range s.exact {
		if bytes.Contains(data, v) {
			return "a secret value the instance knows"
		}
	}
	for _, v := range s.bounded {
		if containsBounded(data, v) {
			return "a short secret value the instance knows"
		}
	}
	for _, p := range s.patterns {
		if p.re.Match(data) {
			return "a credential of a known shape"
		}
	}
	return ""
}

// Scrub replaces every known value and every generic pattern in data. With
// heuristics it also removes the values of credential-bearing headers and
// keys (Authorization, Cookie, password=…), for the free-form members —
// logs, task reports, metrics — where an unknown secret can have been
// printed by something that did not know it was one.
func (s *Secrets) Scrub(data []byte, heuristics bool) []byte {
	out := data
	for _, v := range s.exact {
		out = bytes.ReplaceAll(out, v, []byte(Redacted))
	}
	for _, v := range s.bounded {
		out = replaceBounded(out, v)
	}
	for _, p := range s.patterns {
		out = p.re.ReplaceAll(out, []byte(p.repl))
	}
	if heuristics {
		for _, h := range heuristicPatterns {
			out = h.ReplaceAll(out, []byte("${1}"+Redacted))
		}
	}
	return out
}

// genericPatterns are shapes that are credentials wherever they appear.
var genericPatterns = []pattern{
	// A private key, as PEM, raw or with its newlines JSON-escaped.
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), Redacted},
	// Bearer and basic credentials of an Authorization header.
	{regexp.MustCompile(`(?i)\b(?:bearer|basic)[ \t]+[A-Za-z0-9._~+/=-]{8,}`), Redacted},
	// A static API token of this instance (FR-072): the shape is known,
	// so a token echoed by something that did not know it was one is
	// removed too.
	{regexp.MustCompile(`\btby_[A-Za-z0-9_-]{12,}`), Redacted},
	// A password in a URL.
	{regexp.MustCompile(`(?i)://[^/\s:@"'\\]+:[^/\s@"'\\]+@`), "://" + Redacted + "@"},
}

// heuristicPatterns remove the VALUE of a header or key that carries a
// credential by name. Group 1 is kept (the name and separator).
var heuristicPatterns = []*regexp.Regexp{
	// "Authorization: …", "cookie=…", "set-cookie": "…" — headers.
	regexp.MustCompile(`(?i)("?(?:proxy-)?authorization"?[ \t]*[:=][ \t]*)(?:"(?:[^"\\]|\\.)*"|[^\s,;}]+(?:[ \t]+[^\s,;}]+)?)`),
	regexp.MustCompile(`(?i)("?(?:set-)?cookie"?[ \t]*[:=][ \t]*)(?:"(?:[^"\\]|\\.)*"|[^\r\n]+)`),
	regexp.MustCompile(`(?i)("?x-[a-z-]*(?:token|key|secret|auth)[a-z-]*"?[ \t]*[:=][ \t]*)(?:"(?:[^"\\]|\\.)*"|[^\s,;}]+)`),
	// password-ish keys, in JSON, logfmt or YAML.
	regexp.MustCompile(`(?i)("?[a-z0-9_.-]*(?:password|passwd|passphrase|secret|token|apikey|api_key|private_key|credential|signature)s?"?[ \t]*[:=][ \t]*)(?:"(?:[^"\\]|\\.)*"|[^\s,;}&]+)`),
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func containsBounded(data, v []byte) bool {
	for off := 0; ; {
		i := bytes.Index(data[off:], v)
		if i < 0 {
			return false
		}
		i += off
		end := i + len(v)
		if (i == 0 || !isWordByte(data[i-1])) && (end == len(data) || !isWordByte(data[end])) {
			return true
		}
		off = i + 1
	}
}

func replaceBounded(data, v []byte) []byte {
	var out []byte
	last := 0
	for off := 0; ; {
		i := bytes.Index(data[off:], v)
		if i < 0 {
			break
		}
		i += off
		end := i + len(v)
		if (i == 0 || !isWordByte(data[i-1])) && (end == len(data) || !isWordByte(data[end])) {
			out = append(out, data[last:i]...)
			out = append(out, Redacted...)
			last = end
		}
		off = i + 1
	}
	if last == 0 {
		return data
	}
	return append(out, data[last:]...)
}

// SortedOrigins is Describe, deduplicated and ordered, for reports.
func (s *Secrets) SortedOrigins() []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range s.origins {
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	sort.Strings(out)
	return out
}
