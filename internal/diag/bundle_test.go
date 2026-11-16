// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package diag

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const (
	needleProxy  = "pr0xy-P4ssw0rd-ZZ-1f3a"
	needleClient = "oidc-cl1ent-s3cret-QQ-77aa"
	needleDocker = "docker-hub-pa55-XX-9c1d"
	needleHeader = "tbt_9f8e7d6c5b4a3210fedcba98"
	needleEnv    = "env-TOKEN-value-VV-5555"
	needleCookie = "sess-cookie-value-CC-4242"
)

// plantedSet is the secrets as the instance knows them, the values the test
// planted (which only the test knows) and the PEM of the planted key.
type plantedSet struct {
	secrets *Secrets
	values  []string
	keyPEM  string
}

// plantedSecrets builds the set the way the instance does, from a real
// configuration, real files and a real environment — and returns the
// values planted, which the test (and nobody else) knows.
func plantedSecrets(t *testing.T) *plantedSet {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	clientFile := filepath.Join(dir, "oidc-secret")
	if err := os.WriteFile(clientFile, []byte(needleClient+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := base64.StdEncoding.EncodeToString([]byte("robot:" + needleDocker))
	creds := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(creds, []byte(`{"auths":{"registry.example":{"auth":"`+auth+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Network.Proxy.Username = "svc-proxy"
	cfg.Network.Proxy.Password = config.NewSecret(needleProxy)

	s := NewSecrets()
	s.AddValues("config", cfg)
	for origin, path := range map[string]string{"server.tls.keyFile": keyFile, "auth.oidc.clientSecretFile": clientFile, "registries.credentialsFile": creds} {
		if err := s.AddFile(origin, path); err != nil {
			t.Fatal(err)
		}
	}
	s.Add("env TOBBY_API_TOKEN", needleEnv)
	// Every form a planted value is expected to be recognized in.
	planted := []string{
		needleProxy, needleClient, needleDocker, needleEnv,
		base64.StdEncoding.EncodeToString([]byte(needleProxy)),
		auth,
		base64.StdEncoding.EncodeToString(der),
	}
	return &plantedSet{secrets: s, values: planted, keyPEM: string(keyPEM)}
}

func goodSources() *Sources {
	ok := func(s string) Source { return func() ([]byte, error) { return []byte(s), nil } }
	return &Sources{
		Version: ok(`{"version":"v1"}`), Config: ok("mode: mirror\n"), Metrics: ok("tobby_build_info 1\n"),
		Logs: ok(`{"msg":"started"}` + "\n"), Integrity: ok(`{"verdict":"clean"}`), State: ok(`{"items":[]}`),
		Tasks: func(int) ([]File, error) {
			return []File{{Name: "tasks/tsk_1.json", Data: []byte(`{"id":"tsk_1"}`)}}, nil
		},
	}
}

func unpack(t *testing.T, raw []byte) (members map[string][]byte) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

// TestBundleHoldsNoPlantedSecretAnywhere is the FR-095 acceptance: secrets
// planted in the configuration, files, environment, log records, task
// reports, metrics and headers — and the archive, unpacked member by
// member, contains none of them, raw or encoded.
func TestBundleHoldsNoPlantedSecretAnywhere(t *testing.T) {
	set := plantedSecrets(t)
	secrets, planted, keyPEM := set.secrets, set.values, set.keyPEM
	src := goodSources()
	src.Logs = func() ([]byte, error) {
		return []byte(strings.Join([]string{
			`{"msg":"proxy dial","detail":"using ` + needleProxy + ` for svc-proxy"}`,
			`{"msg":"request","headers":{"Authorization":"Bearer ` + needleHeader + `","Cookie":"tobby_session=` + needleCookie + `"}}`,
			`{"msg":"oidc","client_secret":"` + needleClient + `"}`,
			`{"msg":"b64","blob":"` + base64.StdEncoding.EncodeToString([]byte(needleProxy)) + `"}`,
			`{"msg":"key loaded","pem":` + mustJSON(keyPEM) + `}`,
			`{"msg":"pull","url":"https://robot:` + needleDocker + `@registry.example/v2/"}`,
			`{"msg":"basic","auth":"Basic ` + base64.StdEncoding.EncodeToString([]byte("robot:"+needleDocker)) + `"}`,
			`{"msg":"env","token":"` + needleEnv + `"}`,
		}, "\n") + "\n"), nil
	}
	src.Tasks = func(int) ([]File, error) {
		return []File{
			{Name: "tasks/tsk_1.json", Data: []byte(`{"id":"tsk_1","error":{"params":{"detail":"401 for ` + needleDocker + `"}}}`)},
			{Name: "tasks/tsk_2.json", Data: []byte(`{"id":"tsk_2","reference":"https://u:` + needleProxy + `@h/x"}`)},
		}, nil
	}
	src.Metrics = func() ([]byte, error) {
		return []byte("tobby_x{note=\"" + needleClient + "\"} 1\n"), nil
	}
	b, err := Build(Options{Secrets: secrets, Tasks: 5}, src)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.Archive()
	if err != nil {
		t.Fatal(err)
	}
	members := unpack(t, raw)
	if len(members) < 6 {
		t.Fatalf("only %d members: %v", len(members), keys(members))
	}
	forbidden := append([]string{needleHeader, needleCookie, "BEGIN EC PRIVATE KEY"}, planted...)
	for name, data := range members {
		for _, p := range forbidden {
			if bytes.Contains(data, []byte(p)) {
				t.Errorf("member %s contains the planted value %q…", name, p[:min(6, len(p))])
			}
		}
	}
	// The scrub keeps the records readable: what is not secret survives.
	if !bytes.Contains(members[MemberLogs], []byte("proxy dial")) || !bytes.Contains(members[MemberLogs], []byte(Redacted)) {
		t.Errorf("logs lost their structure or were not scrubbed:\n%s", members[MemberLogs])
	}
}

func mustJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBundleAbortsWhenAMemberStillHoldsASecret: the check fails closed.
// The configuration member is a structured document Tobby writes itself,
// so nothing scrubs it; one that carries a secret aborts the bundle with
// TBY-DIA-002 naming the member — and nothing about the secret.
func TestBundleAbortsWhenAMemberStillHoldsASecret(t *testing.T) {
	set := plantedSecrets(t)
	secrets, keyPEM := set.secrets, set.keyPEM
	for name, mutate := range map[string]func(*Sources){
		"config with a known value": func(s *Sources) {
			s.Config = func() ([]byte, error) { return []byte("proxy:\n  password: " + needleProxy + "\n"), nil }
		},
		"state with a private key": func(s *Sources) {
			s.State = func() ([]byte, error) { return []byte(keyPEM), nil }
		},
		"version with a url password": func(s *Sources) {
			s.Version = func() ([]byte, error) { return []byte(`{"u":"https://a:b1b1b1b1@host"}`), nil }
		},
		"integrity with a base64 form": func(s *Sources) {
			s.Integrity = func() ([]byte, error) {
				return []byte(base64.RawURLEncoding.EncodeToString([]byte(needleClient))), nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			src := goodSources()
			mutate(src)
			b, err := Build(Options{Secrets: secrets}, src)
			if b != nil {
				t.Fatal("a bundle carrying a secret was returned")
			}
			var te *taxonomy.Error
			if !errors.As(err, &te) || te.Code() != taxonomy.CodeDiagnosticsRedaction {
				t.Fatalf("err = %v, want TBY-DIA-002", err)
			}
			msg := taxonomy.Text("en", te)
			for _, leak := range []string{needleProxy, needleClient, "PRIVATE KEY"} {
				if strings.Contains(msg, leak) {
					t.Errorf("the refusal leaks %q", leak)
				}
			}
		})
	}
}

func TestManifestDigestsMatchMembersAndOnlyDocumentedOnes(t *testing.T) {
	secrets := plantedSecrets(t).secrets
	src := goodSources()
	src.MediaLog = func() ([]byte, error) { return nil, ErrOmitted("not a mirror instance") }
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	b, err := Build(Options{Secrets: secrets, Now: func() time.Time { return now }}, src)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := b.Archive()
	members := unpack(t, raw)
	var man Manifest
	if err := json.Unmarshal(members[MemberManifest], &man); err != nil {
		t.Fatal(err)
	}
	if err := man.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(man.Members)+1 != len(members) {
		t.Errorf("manifest lists %d members, archive holds %d files (+manifest)", len(man.Members), len(members))
	}
	for _, m := range man.Members {
		sum := sha256.Sum256(members[m.Name])
		if m.Digest != "sha256:"+hex.EncodeToString(sum[:]) || m.Size != int64(len(members[m.Name])) {
			t.Errorf("manifest entry %s does not match its member", m.Name)
		}
	}
	for name := range members {
		if name != MemberManifest && !Known(name) {
			t.Errorf("the archive holds %q, which is not a documented member", name)
		}
	}
	if len(man.Omitted) != 1 || man.Omitted[0].Name != MemberMediaLog || man.Omitted[0].Reason != "not a mirror instance" {
		t.Errorf("omissions = %+v", man.Omitted)
	}
	// Same inputs, same bytes.
	b2, _ := Build(Options{Secrets: secrets, Now: func() time.Time { return now }}, src)
	raw2, _ := b2.Archive()
	if !bytes.Equal(raw, raw2) {
		t.Error("the archive is not reproducible for the same inputs")
	}
}

func TestBuildFailureIsDIA001(t *testing.T) {
	secrets := plantedSecrets(t).secrets
	src := goodSources()
	src.Metrics = func() ([]byte, error) { return nil, errors.New("registry unavailable") }
	_, err := Build(Options{Secrets: secrets}, src)
	var te *taxonomy.Error
	if !errors.As(err, &te) || te.Code() != taxonomy.CodeDiagnosticsFailed {
		t.Fatalf("err = %v, want TBY-DIA-001", err)
	}
	if _, err := Build(Options{}, goodSources()); err == nil {
		t.Error("Build accepted no secrets to check against")
	}
}

func TestSecretsShortValuesAreBoundaryMatched(t *testing.T) {
	s := NewSecrets()
	s.Add("x", "hunter2")
	s.Add("x", "ab") // below the floor: not searched
	if got := s.Scrub([]byte("pw=hunter2 and hunter22 and xhunter2x"), false); string(got) != "pw="+Redacted+" and hunter22 and xhunter2x" {
		t.Errorf("Scrub = %q", got)
	}
	if s.Find([]byte("hunter2")) == "" || s.Find([]byte("unrelated hunter22")) != "" {
		t.Error("Find disagrees with Scrub on boundaries")
	}
	if s.Find([]byte("ab ab ab")) != "" {
		t.Error("a value under the floor is being searched")
	}
}

func TestAddFileMissingIsSkippedUnreadableFailsClosed(t *testing.T) {
	s := NewSecrets()
	if err := s.AddFile("k", filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("a missing secret file is not an error: %v", err)
	}
	if err := s.AddFile("k", ""); err != nil {
		t.Error(err)
	}
	dir := t.TempDir()
	if err := s.AddFile("k", dir); err == nil {
		t.Error("a path that cannot be read as a file was accepted silently")
	}
}

func TestWindowKeepsTheLastRecords(t *testing.T) {
	w := NewWindow(3)
	for _, l := range []string{"a", "b", "c", "d", "e"} {
		_, _ = w.Write([]byte(l + "\n"))
	}
	if got := string(w.Records()); got != "c\nd\ne\n" {
		t.Errorf("Records = %q", got)
	}
	if w.Len() != 3 {
		t.Errorf("Len = %d", w.Len())
	}
	w2 := NewWindow(5)
	_, _ = w2.Write([]byte("x\n"))
	if got := string(w2.Records()); got != "x\n" || w2.Len() != 1 {
		t.Errorf("partial window = %q (%d)", got, w2.Len())
	}
	big := bytes.Repeat([]byte("z"), maxRecordBytes*2)
	_, _ = w2.Write(big)
	if r := w2.Records(); len(r) > maxRecordBytes+4 {
		t.Errorf("an oversized record was not bounded: %d bytes", len(r))
	}
}

// TestStringFieldsNamedLikeSecretsAreFoundToo: a secret typed as a plain
// string is searched for when its name says what it is; the path of a secret
// file is not mistaken for the secret.
func TestStringFieldsNamedLikeSecretsAreFoundToo(t *testing.T) {
	type nested struct {
		ClientSecret     string
		ClientSecretFile string
		TokenLifetime    string
		Hosts            []string
	}
	s := NewSecrets()
	s.AddValues("cfg", nested{ClientSecret: "plain-string-secret-0001", ClientSecretFile: "/etc/tobby/secret", TokenLifetime: "720h"})
	if s.Find([]byte("x plain-string-secret-0001 y")) == "" {
		t.Error("a string field named ClientSecret is not searched")
	}
	if s.Find([]byte("/etc/tobby/secret and 720h")) != "" {
		t.Error("the path of a secret file, or a lifetime, was taken for a secret")
	}
}

// TestWindowIsBoundedInBytesToo: the record count alone would let a large
// bound hold a gigabyte.
func TestWindowIsBoundedInBytesToo(t *testing.T) {
	w := NewWindow(100000)
	rec := bytes.Repeat([]byte("r"), maxRecordBytes)
	for range 8000 { // 8000 × 16 KiB = 125 MiB offered
		_, _ = w.Write(rec)
	}
	if got := len(w.Records()); got > maxWindowBytes+8000 {
		t.Errorf("the window holds %d bytes, over its %d budget", got, maxWindowBytes)
	}
}

// TestADefaultConfigurationHoldsNothingToRedact: the walk over the real
// configuration type finds no secret in the defaults, so a name-based match
// cannot start aborting every bundle by mistaking a setting for a secret.
func TestADefaultConfigurationHoldsNothingToRedact(t *testing.T) {
	s := NewSecrets()
	cfg := config.Default()
	s.AddValues("configuration", &cfg)
	if s.Count() != 0 {
		t.Errorf("the defaults yield %d search strings from %v", s.Count(), s.Describe())
	}
	cfg.Network.Proxy.Password = config.NewSecret("another-proxy-password")
	s2 := NewSecrets()
	s2.AddValues("configuration", &cfg)
	if s2.Count() == 0 {
		t.Error("a configured proxy password is not found by the walk")
	}
}
