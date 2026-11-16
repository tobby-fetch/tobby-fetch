// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func diagCode(err error) taxonomy.Code {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te.Code()
	}
	return ""
}

func member(t *testing.T, b *Bundle, name string) []byte {
	t.Helper()
	for _, f := range b.Files() {
		if f.Name == name {
			return f.Data
		}
	}
	t.Fatalf("no member %s", name)
	return nil
}

// TestWindowCutDoesNotLeakASecretPrefix: the log window cuts a record at
// its byte bound before any bundle knows the secrets; a known secret
// straddling the cut must not leave its first bytes in the archive.
// (Adversarial review of milestone 7, A2: 20 of 42 characters came out.)
func TestWindowCutDoesNotLeakASecretPrefix(t *testing.T) {
	secret := "S3cr3t-registry-password-0123456789abcdef"
	for _, kept := range []int{4, 8, 12, 20, 31, 32, 33, len(secret) - 1} {
		t.Run(fmt.Sprint(kept), func(t *testing.T) {
			s := NewSecrets()
			s.Add("configuration.Network.Proxy.Password", secret)
			w := NewWindow(10)
			head := `{"msg":"upstream said `
			pad := strings.Repeat("a", maxRecordBytes-len(head)-kept)
			_, _ = w.Write([]byte(head + pad + secret + `"}`))
			b, err := Build(Options{Secrets: s}, &Sources{Logs: func() ([]byte, error) { return w.Records(), nil }})
			if err != nil {
				t.Fatalf("build refused: %v", err)
			}
			logs := member(t, b, MemberLogs)
			if bytes.Contains(logs, []byte(secret[:kept])) {
				t.Fatalf("%d bytes of the secret survived the cut", kept)
			}
			if !bytes.Contains(logs, []byte(windowCut)) {
				t.Error("the cut is not marked")
			}
		})
	}
}

// TestMemberIsScrubbedBeforeItIsCut: a free-form member over the size
// bound is scrubbed whole, then cut; a secret across the bound is gone.
func TestMemberIsScrubbedBeforeItIsCut(t *testing.T) {
	defer func(m int) { maxMember = m }(maxMember)
	maxMember = 1 << 20
	secret := "another-Secret-value-crossing-the-bound"
	s := NewSecrets()
	s.Add("test", secret)
	data := append(append(bytes.Repeat([]byte("x"), maxMember-10), secret...), bytes.Repeat([]byte("y"), 100)...)
	b, err := Build(Options{Secrets: s}, &Sources{Logs: func() ([]byte, error) { return data, nil }})
	if err != nil {
		t.Fatal(err)
	}
	logs := member(t, b, MemberLogs)
	if bytes.Contains(logs, []byte(secret[:10])) || bytes.Contains(logs, []byte(secret[:8])) {
		t.Error("a prefix of the secret survived the member cut")
	}
	if !bytes.HasSuffix(logs, []byte(memberCut)) {
		t.Error("the member cut is not marked")
	}
}

// TestASecretPrefixAtACutRefusesAStructuredMember: the members Tobby writes
// are not scrubbed but searched; part of a known secret against a cut mark
// is a leak, and refuses the bundle (the filet of A2).
func TestASecretPrefixAtACutRefusesAStructuredMember(t *testing.T) {
	secret := "structured-Secret-Value-0001"
	s := NewSecrets()
	s.Add("test", secret)
	for _, kept := range []int{8, 16, 30} {
		k := min(kept, len(secret)-1)
		data := []byte(`{"x":"` + secret[:k] + windowCut)
		_, err := Build(Options{Secrets: s}, &Sources{State: func() ([]byte, error) { return data, nil }})
		if diagCode(err) != taxonomy.CodeDiagnosticsRedaction {
			t.Errorf("%d bytes at a cut: err = %v, want TBY-DIA-002", k, err)
		}
	}
	// Fewer than eight bytes at a cut prove nothing in a structured member.
	data := []byte(`{"x":"` + secret[:6] + windowCut)
	if _, err := Build(Options{Secrets: s}, &Sources{State: func() ([]byte, error) { return data, nil }}); err != nil {
		t.Errorf("six bytes at a cut refused the bundle: %v", err)
	}
}

// TestATailMemberDoesNotBeginWithTheEndOfASecret: the medium's log is a
// tail; what it begins with may be the end of a cut secret.
func TestATailMemberDoesNotBeginWithTheEndOfASecret(t *testing.T) {
	secret := "tail-Secret-Value-abcdefgh" //nolint:gosec // G101: a planted test value
	s := NewSecrets()
	s.Add("test", secret)
	data := []byte(secret[7:] + "\n{\"msg\":\"next\"}\n")
	b, err := Build(Options{Secrets: s}, &Sources{MediaLog: func() ([]byte, error) { return data, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(member(t, b, MemberMediaLog), []byte(secret[7:])) {
		t.Error("the end of a secret survived at the start of the tail")
	}
}

// TestMisalignedBase64IsFound: a secret base64-encoded inside a longer
// string — base64("user:"+password), an HTTP Basic credential without its
// word — is found whatever its alignment (review R1).
func TestMisalignedBase64IsFound(t *testing.T) {
	pw := "Proxy-Passw0rd-XYZ"
	s := NewSecrets()
	s.Add("configuration.Network.Proxy.Password", pw)
	for _, prefix := range []string{"", "u", "us", "user:", "robot-account:"} {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding} {
			blob := enc.EncodeToString([]byte(prefix + pw + "-and-more"))
			if s.Find([]byte(`{"integrity":"`+blob+`"}`)) == "" {
				t.Errorf("base64(%q+pw) not found: %s", prefix, blob)
			}
			if out := s.Scrub([]byte(blob), false); s.Find(out) != "" {
				t.Errorf("scrub left base64(%q+pw): %s", prefix, out)
			}
		}
	}
}

// TestTokenShapesAreScrubbed: bare JSON Web Tokens, jwt=, an Azure SAS
// sig=, a SAML response — credentials of a known shape (review R1).
func TestTokenShapesAreScrubbed(t *testing.T) {
	// Assembled at run time: a literal token would trip the repository's
	// secret scanner, which is the point of the shape.
	jwt := strings.Join([]string{"eyJhbGciOiJSUzI1NiIsImtpZCI6IjEifQ", "eyJzdWIiOiJhZG1pbiIsImV4cCI6MTk5OTk5OTk5OX0", "c2lnbmF0dXJlLXZhbHVlLWhlcmU"}, ".")
	s := NewSecrets()
	for _, line := range []string{
		`{"msg":"callback","token":"x","raw":"` + jwt + `"}`,
		`GET /cb?jwt=abcDEF123456789xyz&state=1`,
		`https://acct.blob.core.windows.net/c/b?sv=2024-01-01&sr=b&sig=Zm9vYmFyYmF6cXV4%2Bxyz%3D&se=2027`,
		`POST /saml/acs SAMLResponse=PHNhbWxwOlJlc3BvbnNlIHhtbG5zOnNhbWxwPSJ1cm46b2FzaXM6`,
	} {
		out := string(s.Scrub([]byte(line), true))
		for _, leak := range []string{jwt, "abcDEF123456789xyz", "Zm9vYmFyYmF6cXV4", "PHNhbWxwOlJlc3BvbnNl"} {
			if strings.Contains(out, leak) {
				t.Errorf("scrub kept %q in %s", leak, out)
			}
		}
	}
	if s.Find([]byte(`{"id_token_hint":"`+jwt+`"}`)) == "" {
		t.Error("a bare JWT in a structured member is not found")
	}
}

// TestOneAutomatonFindsWhatEveryFormFinds: the automaton over many forms
// finds and scrubs each of them, overlapping ones included, and leaves a
// clean member alone.
func TestOneAutomatonFindsWhatEveryFormFinds(t *testing.T) {
	s := NewSecrets()
	var values []string
	for i := range 2000 {
		v := fmt.Sprintf("value-%04d-%s", i, strings.Repeat("q", i%50))
		values = append(values, v)
		s.Add("file", v)
	}
	s.Add("short", "Pw42z") // boundary-matched
	clean := []byte(strings.Repeat("nothing secret here; value-x-qqq ", 1000))
	if r := s.Find(clean); r != "" {
		t.Fatalf("a clean member: %s", r)
	}
	if out := s.Scrub(clean, false); !bytes.Equal(out, clean) {
		t.Error("scrub changed a clean member")
	}
	for _, i := range []int{0, 1, 777, 1999} {
		v := values[i]
		data := []byte("a " + v + " b " + base64.StdEncoding.EncodeToString([]byte(v)) + " c")
		if s.Find(data) == "" {
			t.Errorf("%s not found", v)
		}
		if out := s.Scrub(data, false); s.Find(out) != "" || bytes.Contains(out, []byte(v)) {
			t.Errorf("%s survived the scrub: %s", v, out)
		}
	}
	if s.Find([]byte("xPw42zx")) != "" || s.Find([]byte("pw: Pw42z;")) == "" {
		t.Error("a short value is matched inside a word, or not at all between delimiters")
	}
	// Overlapping values are redacted as one span.
	o := NewSecrets()
	o.Add("a", "overlap-AAAA-BBBB")
	o.Add("b", "BBBB-CCCC-overlap")
	if out := string(o.Scrub([]byte("x overlap-AAAA-BBBB-CCCC-overlap y"), false)); out != "x "+Redacted+" y" {
		t.Errorf("overlapping values: %q", out)
	}
}

// TestOneBundleAtATime: a request that arrives while a bundle is being
// built is refused with TBY-DIA-003, audited and counted, not queued.
func TestOneBundleAtATime(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	svc := &Service{
		Secrets: func() (*Secrets, error) { return NewSecrets(), nil },
		Sources: &Sources{Version: func() ([]byte, error) {
			once.Do(func() { close(entered) })
			<-release
			return []byte("{}"), nil
		}},
		Logger: slog.New(slog.DiscardHandler),
	}
	var first error
	done := make(chan struct{})
	go func() { _, first = svc.Generate(context.Background(), "a", "local"); close(done) }()
	<-entered
	if _, err := svc.Generate(context.Background(), "b", "local"); diagCode(err) != taxonomy.CodeDiagnosticsBusy {
		t.Errorf("concurrent request: %v, want TBY-DIA-003", err)
	}
	close(release)
	<-done
	if first != nil {
		t.Fatal(first)
	}
	if _, err := svc.Generate(context.Background(), "c", "local"); err != nil {
		t.Errorf("the next request after the first finished: %v", err)
	}
	if e, ok := taxonomy.Lookup(taxonomy.CodeDiagnosticsBusy); !ok || e.HTTPStatus != 409 {
		t.Errorf("TBY-DIA-003 = %+v, want HTTP 409", e)
	}
}
