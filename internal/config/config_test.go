// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package config

import (
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAlone(t *testing.T) {
	cfg := Default()
	if cfg.Server.Addr != ":8080" {
		t.Errorf("default server.addr = %q, want :8080", cfg.Server.Addr)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("default logging.level = %q, want info", cfg.Logging.Level)
	}
	if time.Duration(cfg.Shutdown.GracePeriod) != 30*time.Second {
		t.Errorf("default shutdown.gracePeriod = %v, want 30s", cfg.Shutdown.GracePeriod)
	}
	if cfg.Mode != "" {
		t.Errorf("default mode = %q, want empty (no default mode: FR-001)", cfg.Mode)
	}
	if time.Duration(cfg.Import.InspectTimeout) != 20*time.Second {
		t.Errorf("default import.inspectTimeout = %v, want 20s", cfg.Import.InspectTimeout)
	}
	if cfg.Tasks.KeepFinished != 500 {
		t.Errorf("default tasks.keepFinished = %d, want 500 (finished-task retention, 2026-08 audit)", cfg.Tasks.KeepFinished)
	}
}

// TestLayerPrecedence exercises the FR-003 acceptance criterion: for a
// setting defined at all three levels the flag wins; without the flag the
// environment wins; without either the file wins.
func TestLayerPrecedence(t *testing.T) {
	path := writeFile(t, "mode: passthrough\nlogging:\n  level: warn\n")

	t.Run("file over defaults", func(t *testing.T) {
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Logging.Level != "warn" {
			t.Errorf("level = %q, want warn (file layer)", cfg.Logging.Level)
		}
	})

	t.Run("environment over file", func(t *testing.T) {
		t.Setenv(EnvLoggingLevel, "error")
		cfg, err := Load(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Logging.Level != "error" {
			t.Errorf("level = %q, want error (environment layer)", cfg.Logging.Level)
		}
	})

	t.Run("flag over environment and file", func(t *testing.T) {
		t.Setenv(EnvLoggingLevel, "error")
		cfg, err := Load(path, true, func(c *Config) { c.Logging.Level = "debug" })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Logging.Level != "debug" {
			t.Errorf("level = %q, want debug (flag layer)", cfg.Logging.Level)
		}
	})
}

func TestLoadEveryEnvVariable(t *testing.T) {
	path := writeFile(t, "mode: passthrough\n")
	t.Setenv(EnvMode, "mirror")
	t.Setenv(EnvStorageRoot, "/srv/store")
	t.Setenv(EnvStateRoot, "/var/lib/tobby")
	t.Setenv(EnvServerAddr, ":9090")
	t.Setenv(EnvAuthDisabled, "true")
	t.Setenv(EnvAuthSessionTTL, "2h")
	// Trimmed and empty-skipped: an operator-written list is rarely tidy.
	t.Setenv(EnvRegistriesInsecure, " lab.example.com:5000 ,, other.lab ")
	t.Setenv(EnvUIThemeOverride, "/etc/tobby/theme.css")
	t.Setenv(EnvUIShowUpcoming, "1")
	t.Setenv(EnvLoggingLevel, "debug")
	t.Setenv(EnvShutdownGracePeriod, "45s")
	t.Setenv(EnvImportInspectTO, "7s")
	t.Setenv(EnvRetrieverSource, "https://cookbook.example.com/desired-state.yaml")
	t.Setenv(EnvStorageBasePrefix, "zone-b")
	t.Setenv(EnvSyncParallelism, "8")
	t.Setenv(EnvSyncRetries, "0")
	t.Setenv(EnvTasksKeepFinished, "1000")
	t.Setenv(EnvServerSecureCookies, "true")

	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeMirror {
		t.Errorf("mode = %q, want mirror", cfg.Mode)
	}
	if cfg.Storage.Root != "/srv/store" {
		t.Errorf("storage.root = %q, want /srv/store", cfg.Storage.Root)
	}
	if cfg.State.Root != "/var/lib/tobby" {
		t.Errorf("state.root = %q, want /var/lib/tobby", cfg.State.Root)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("server.addr = %q, want :9090", cfg.Server.Addr)
	}
	if !cfg.Server.SecureCookies {
		t.Error("server.secureCookies = false, want true (settable by environment for proxy-terminated TLS)")
	}
	if !cfg.Auth.Disabled {
		t.Error("auth.disabled = false, want true (FR-075 opt-out is settable by environment, never by flag)")
	}
	if time.Duration(cfg.Auth.SessionTTL) != 2*time.Hour {
		t.Errorf("auth.sessionTTL = %v, want 2h", cfg.Auth.SessionTTL)
	}
	if want := []string{"lab.example.com:5000", "other.lab"}; !reflect.DeepEqual(cfg.Registries.Insecure, want) {
		t.Errorf("registries.insecure = %q, want %q (trimmed, empties dropped)", cfg.Registries.Insecure, want)
	}
	if cfg.UI.ThemeOverride != "/etc/tobby/theme.css" {
		t.Errorf("ui.themeOverride = %q", cfg.UI.ThemeOverride)
	}
	if !cfg.UI.ShowUpcoming {
		t.Error(`ui.showUpcoming = false, want true ("1" is an accepted boolean)`)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want debug", cfg.Logging.Level)
	}
	if time.Duration(cfg.Shutdown.GracePeriod) != 45*time.Second {
		t.Errorf("gracePeriod = %v, want 45s", cfg.Shutdown.GracePeriod)
	}
	if time.Duration(cfg.Import.InspectTimeout) != 7*time.Second {
		t.Errorf("import.inspectTimeout = %v, want 7s", cfg.Import.InspectTimeout)
	}
	if cfg.Retriever.Source != "https://cookbook.example.com/desired-state.yaml" {
		t.Errorf("retriever.source = %q", cfg.Retriever.Source)
	}
	if cfg.Storage.BasePrefix != "zone-b" {
		t.Errorf("storage.basePrefix = %q, want zone-b (FR-035)", cfg.Storage.BasePrefix)
	}
	if cfg.Sync.Parallelism != 8 {
		t.Errorf("sync.parallelism = %d, want 8", cfg.Sync.Parallelism)
	}
	if cfg.Sync.Retries != 0 {
		t.Errorf("sync.retries = %d, want 0 (zero retries is a legitimate setting)", cfg.Sync.Retries)
	}
	if cfg.Tasks.KeepFinished != 1000 {
		t.Errorf("tasks.keepFinished = %d, want 1000", cfg.Tasks.KeepFinished)
	}
}

// TestInvalidEnvValues: a malformed environment value fails the load with a
// message naming the variable — never a silent fallback to the default,
// which would hide an operator typo behind a working instance.
func TestInvalidEnvValues(t *testing.T) {
	for _, tc := range []struct {
		name, env, value, wantErr string
	}{
		{"sync parallelism not an integer", EnvSyncParallelism, "three", `invalid integer "three"`},
		{"sync retries not an integer", EnvSyncRetries, "3.5", `invalid integer "3.5"`},
		{"auth disabled not a boolean", EnvAuthDisabled, "yes", `invalid boolean "yes"`},
		{"show upcoming not a boolean", EnvUIShowUpcoming, "maybe", `invalid boolean "maybe"`},
		{"session TTL not a duration", EnvAuthSessionTTL, "forever", `invalid duration "forever"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, "mode: mirror\n")
			t.Setenv(tc.env, tc.value)
			_, err := Load(path, true)
			if err == nil {
				t.Fatalf("%s=%q was accepted, want a refusal", tc.env, tc.value)
			}
			if !strings.Contains(err.Error(), tc.env) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want mention of %s and %q", err, tc.env, tc.wantErr)
			}
		})
	}
}

func TestModeValidation(t *testing.T) {
	// FR-001: any value other than the two modes fails startup with an
	// explicit error; the mode is required.
	for _, tc := range []struct {
		yaml    string
		wantErr string
	}{
		{"", "mode is required"},
		{"mode: sideways\n", `unknown mode "sideways"`},
	} {
		path := writeFile(t, tc.yaml)
		_, err := Load(path, true)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Load(%q) error = %v, want containing %q", tc.yaml, err, tc.wantErr)
		}
	}
}

func TestScopedValidation(t *testing.T) {
	// R-34/B-006: per-command validation. ScopeState tolerates a missing
	// mode — a state-directory command never uses one — but everything set
	// must stay coherent: an unknown mode or an invalid level still fails.
	if _, err := LoadFor(ScopeState, writeFile(t, ""), true); err != nil {
		t.Errorf("ScopeState without mode: %v, want success", err)
	}
	if _, err := LoadFor(ScopeState, writeFile(t, "mode: sideways\n"), true); err == nil ||
		!strings.Contains(err.Error(), `unknown mode "sideways"`) {
		t.Errorf("ScopeState with unknown mode: error = %v, want refusal", err)
	}
	if _, err := LoadFor(ScopeState, writeFile(t, "logging:\n  level: chatty\n"), true); err == nil {
		t.Error("ScopeState with invalid logging.level must still fail")
	}
}

func TestUnknownFileFieldRejected(t *testing.T) {
	path := writeFile(t, "mode: mirror\ntypo_field: 1\n")
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Errorf("unknown field: error = %v, want mention of typo_field", err)
	}
}

func TestExplicitMissingFileFails(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), true)
	if err == nil {
		t.Fatal("explicitly given missing file must fail")
	}
}

func TestDefaultMissingFileTolerated(t *testing.T) {
	t.Setenv(EnvMode, "passthrough")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false)
	if err != nil {
		t.Fatalf("default-location missing file must be tolerated, got %v", err)
	}
	if cfg.Mode != ModePassthrough {
		t.Errorf("mode = %q, want passthrough (from environment)", cfg.Mode)
	}
}

func TestInvalidEnvDuration(t *testing.T) {
	path := writeFile(t, "mode: mirror\n")
	t.Setenv(EnvShutdownGracePeriod, "not-a-duration")
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), EnvShutdownGracePeriod) {
		t.Errorf("error = %v, want mention of %s", err, EnvShutdownGracePeriod)
	}
}

func TestValidateGracePeriodPositive(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Shutdown.GracePeriod = Duration(-time.Second)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "gracePeriod") {
		t.Errorf("Validate() = %v, want gracePeriod error", err)
	}
}

func TestValidateInspectTimeoutPositive(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Import.InspectTimeout = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "import.inspectTimeout") {
		t.Errorf("Validate() = %v, want import.inspectTimeout error", err)
	}
}

func TestDumpRoundTrips(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeMirror
	cfg.Storage.Root = "/srv/store"

	out, err := cfg.Dump()
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	dec := yaml.NewDecoder(strings.NewReader(out))
	dec.KnownFields(true)
	if err := dec.Decode(&back); err != nil {
		t.Fatalf("dump does not parse back strictly: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, cfg) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", back, cfg)
	}
}

func TestDurationYAML(t *testing.T) {
	var s Shutdown
	if err := yaml.Unmarshal([]byte(`gracePeriod: 1m30s`), &s); err != nil {
		t.Fatal(err)
	}
	if time.Duration(s.GracePeriod) != 90*time.Second {
		t.Errorf("parsed %v, want 1m30s", s.GracePeriod)
	}
	if err := yaml.Unmarshal([]byte(`gracePeriod: ninety`), &s); err == nil {
		t.Error("invalid duration must fail")
	}
	if err := yaml.Unmarshal([]byte(`gracePeriod: [1, 2]`), &s); err == nil {
		t.Error("non-scalar duration must fail")
	}
}

// TestSecretNeverSerializes is the NFR-015 unit test: no serialization path
// may reveal a Secret's value.
func TestSecretNeverSerializes(t *testing.T) {
	const sensitive = "hunter2-swordfish"
	s := NewSecret(sensitive)

	yamlOut, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	jsonOut, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	structOut, err := yaml.Marshal(struct {
		Token Secret `yaml:"token"`
	}{s})
	if err != nil {
		t.Fatal(err)
	}

	renderings := map[string]string{
		"String":         s.String(),
		"fmt %v":         fmt.Sprintf("%v", s), //nolint:gocritic // redundantSprint: the fmt verb path is exactly what is under test
		"fmt %+v":        fmt.Sprintf("%+v", s),
		"fmt %#v":        fmt.Sprintf("%#v", s),
		"fmt %s":         fmt.Sprintf("%s", s), //nolint:gocritic,staticcheck // redundantSprint/S1025: the fmt verb path is exactly what is under test
		"yaml":           string(yamlOut),
		"json":           string(jsonOut),
		"yaml in struct": string(structOut),
	}
	for name, out := range renderings {
		if strings.Contains(out, sensitive) {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
	}

	if s.Reveal() != sensitive {
		t.Error("Reveal must return the value")
	}
	if NewSecret("").String() != "" {
		t.Error("empty secret renders empty, not REDACTED")
	}
}

// TestDisjointRoots is the R-16 guard: the state directory never lives
// inside the transportable store, nor the reverse.
func TestDisjointRoots(t *testing.T) {
	base := t.TempDir()
	for _, tc := range []struct {
		name, state, storage, wantErr string
	}{
		{"disjoint", filepath.Join(base, "state"), filepath.Join(base, "store"), ""},
		{"equal", base, base, "must differ"},
		{"state inside storage", filepath.Join(base, "sub"), base, "must not live inside storage.root"},
		{"storage inside state", base, filepath.Join(base, "sub"), "must not live inside state.root"},
		{"empty state", "", base, ""},
	} {
		err := disjointRoots(tc.state, tc.storage)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want containing %q", tc.name, err, tc.wantErr)
		}
	}
}

// TestDurationHelpers covers the scalar Duration round trip.
func TestDurationHelpers(t *testing.T) {
	d, err := ParseDuration("1m30s")
	if err != nil || time.Duration(d) != 90*time.Second {
		t.Errorf("ParseDuration = %v, %v", d, err)
	}
	if _, err := ParseDuration("bogus"); err == nil {
		t.Error("invalid duration must fail")
	}
	if s := Duration(20 * time.Second).String(); s != "20s" {
		t.Errorf("String() = %q, want 20s", s)
	}
}

// TestInvalidImportEnvDuration rejects a malformed inspect timeout.
func TestInvalidImportEnvDuration(t *testing.T) {
	path := writeFile(t, "mode: mirror\n")
	t.Setenv(EnvImportInspectTO, "soon")
	_, err := Load(path, true)
	if err == nil || !strings.Contains(err.Error(), EnvImportInspectTO) {
		t.Errorf("error = %v, want mention of %s", err, EnvImportInspectTO)
	}
}

// validating runs Validate over a Default() instance patched by tweak, and
// reports the error. It isolates one validation concern per case: the base
// configuration is always otherwise valid, so every reported error belongs
// to the setting under test.
func validating(tweak func(*Config)) error {
	cfg := Default()
	cfg.Mode = ModeMirror
	tweak(&cfg)
	return cfg.Validate()
}

// checkErr asserts that err carries every fragment of want (or is nil when
// want is empty). Substance, not mere non-nullity: the operator must read
// what to fix out of the message.
func checkErr(t *testing.T, err error, want []string) {
	t.Helper()
	if len(want) == 0 {
		if err != nil {
			t.Errorf("unexpected refusal: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("accepted, want a refusal mentioning %q", want)
	}
	for _, frag := range want {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("error = %v\n  misses %q", err, frag)
		}
	}
}

// TestValidateTrustRefusesMalformedDeclarations is the FR-033 startup guard:
// a malformed trust configuration must fail loudly with an actionable
// message. Verification is never allowed to relax by accident — a typo in a
// scope or a root is a refusal, not a silent downgrade.
func TestValidateTrustRefusesMalformedDeclarations(t *testing.T) {
	inlineRoot := TrustRoot{Name: "release", Key: "-----BEGIN PUBLIC KEY-----\nMCowBQ==\n-----END PUBLIC KEY-----"}
	scoped := func(s TrustScope) Trust { return Trust{Roots: []TrustRoot{inlineRoot}, Scopes: []TrustScope{s}} }

	for _, tc := range []struct {
		name  string
		trust Trust
		want  []string // every fragment must appear in the refusal
	}{
		{
			name: "complete declaration",
			trust: Trust{
				Roots: []TrustRoot{inlineRoot, {Name: "lab", KeyFile: "/etc/tobby/lab.pub"}, {Name: "vendor", KeyURL: "https://keys.example.com/vendor.pub"}},
				Scopes: []TrustScope{
					{Name: "lab-unsigned", Repositories: []string{"lab.example.com/cookbook/**"}, AllowUnsigned: true},
					{Name: "vendor-only", Repositories: []string{"vendor.example.com/*"}, Roots: []string{"vendor"}},
				},
			},
		},
		{
			name:  "root without a name",
			trust: Trust{Roots: []TrustRoot{{Key: "k"}}},
			want:  []string{"trust.roots[0]", "name is required"},
		},
		{
			name:  "duplicate root name",
			trust: Trust{Roots: []TrustRoot{inlineRoot, {Name: "release", KeyFile: "/etc/tobby/other.pub"}}},
			want:  []string{"trust.roots[1]", `duplicate name "release"`},
		},
		{
			name:  "root declaring no key at all",
			trust: Trust{Roots: []TrustRoot{{Name: "release"}}},
			want:  []string{"trust.roots[0] (release)", "exactly one of key, keyFile, keyURL"},
		},
		{
			name:  "root declaring two key sources",
			trust: Trust{Roots: []TrustRoot{{Name: "release", Key: "k", KeyFile: "/etc/tobby/release.pub"}}},
			want:  []string{"trust.roots[0] (release)", "exactly one of key, keyFile, keyURL"},
		},
		{
			name:  "keyURL over plain http",
			trust: Trust{Roots: []TrustRoot{{Name: "release", KeyURL: "http://keys.example.com/release.pub"}}},
			want:  []string{"trust.roots[0] (release)", "keyURL must be https://"},
		},
		{
			name:  "scope without a name",
			trust: scoped(TrustScope{Repositories: []string{"lab.example.com/*"}, AllowUnsigned: true}),
			want:  []string{"trust.scopes[0]", "name is required"},
		},
		{
			name: "duplicate scope name",
			trust: Trust{Roots: []TrustRoot{inlineRoot}, Scopes: []TrustScope{
				{Name: "lab", Repositories: []string{"lab.example.com/*"}, AllowUnsigned: true},
				{Name: "lab", Repositories: []string{"other.example.com/*"}, AllowUnsigned: true},
			}},
			want: []string{"trust.scopes[1]", `duplicate name "lab"`},
		},
		{
			name:  "scope without repositories",
			trust: scoped(TrustScope{Name: "everywhere", AllowUnsigned: true}),
			want:  []string{"trust.scopes[0] (everywhere)", "repositories patterns are required", "explicitly declared perimeter"},
		},
		{
			name:  "scope relaxing and restricting nothing",
			trust: scoped(TrustScope{Name: "inert", Repositories: []string{"lab.example.com/*"}}),
			want:  []string{"trust.scopes[0] (inert)", "must declare what it changes", "allowUnsigned"},
		},
		{
			name:  "scope pointing at an unknown root",
			trust: scoped(TrustScope{Name: "vendor-only", Repositories: []string{"lab.example.com/*"}, Roots: []string{"ghost"}}),
			want:  []string{"trust.scopes[0] (vendor-only)", `unknown trust root "ghost"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkErr(t, validating(func(c *Config) { c.Trust = tc.trust }), tc.want)
		})
	}
}

// TestValidateFilesRefusesHostileNames guards the FR-047 URL surface: the
// FileSet name becomes a /files/<name>/ path segment, so anything that is
// not a plain lowercase segment — traversal, separator, uppercase, exotic
// runes — is refused at configuration time, before it can reach a URL.
func TestValidateFilesRefusesHostileNames(t *testing.T) {
	const segment = "must be a lowercase URL segment"
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"debs", nil},
		{"site-config", nil},
		{"repo.d", nil},
		{"x_1", nil},
		{"", []string{`files.filesets[0]`, segment}},
		{".", []string{`name "."`, segment}},
		{"..", []string{`name ".."`, segment}},
		{"../../etc", []string{segment}},
		{"Debs", []string{`name "Debs"`, segment}},
		{"deb/s", []string{`name "deb/s"`, segment}},
		{"deb s", []string{segment}},
		{"débs", []string{segment}},
		{"debs%2f", []string{segment}},
	} {
		t.Run("name "+tc.name, func(t *testing.T) {
			err := validating(func(c *Config) {
				c.Files.FileSets = []FileSetServe{{Name: tc.name, Ref: "registry.example.com/filesets/site-config"}}
			})
			checkErr(t, err, tc.want)
		})
	}

	// A duplicate name would make two FileSets fight over one URL prefix.
	err := validating(func(c *Config) {
		c.Files.FileSets = []FileSetServe{
			{Name: "debs", Ref: "registry.example.com/filesets/a"},
			{Name: "debs", Ref: "registry.example.com/filesets/b"},
		}
	})
	checkErr(t, err, []string{"files.filesets[1]", `duplicate name "debs"`})

	// Without a ref there is nothing to serve.
	err = validating(func(c *Config) {
		c.Files.FileSets = []FileSetServe{{Name: "debs"}}
	})
	checkErr(t, err, []string{"files.filesets[0] (debs)", "ref is required"})
}

// TestValidateSyncBounds: the transfer bounds of NFR-008/FR-029 must stay
// meaningful — a non-positive parallelism would stall every sync, and a
// negative retry budget is nonsense. Zero retries, however, is a legitimate
// operator choice and must be accepted.
func TestValidateSyncBounds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		parallelism int
		retries     int
		want        []string
	}{
		{"defaults", 3, 3, nil},
		{"no retry budget is legitimate", 1, 0, nil},
		{"zero parallelism", 0, 3, []string{"sync.parallelism must be positive"}},
		{"negative parallelism", -1, 3, []string{"sync.parallelism must be positive"}},
		{"negative retries", 3, -1, []string{"sync.retries must not be negative"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validating(func(c *Config) {
				c.Sync = Sync{Parallelism: tc.parallelism, Retries: tc.retries}
			})
			checkErr(t, err, tc.want)
		})
	}
}

// TestValidateTasksRetention: the finished-task retention refuses a
// negative count and accepts 0 as "keep the whole history".
func TestValidateTasksRetention(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep int
		want []string
	}{
		{"default", 500, nil},
		{"unbounded history is legitimate", 0, nil},
		{"negative retention", -1, []string{"tasks.keepFinished must not be negative"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validating(func(c *Config) {
				c.Tasks = Tasks{KeepFinished: tc.keep}
			})
			checkErr(t, err, tc.want)
		})
	}
}

// TestSecretTextMarshalingAndZero completes the NFR-015 surface: the
// encoding.TextMarshaler path — the one slog's text handler and every
// TextMarshaler-aware encoder take — must redact like the others, and
// IsZero must answer without revealing anything.
func TestSecretTextMarshalingAndZero(t *testing.T) {
	const sensitive = "hunter2-swordfish"

	// The interface assertion is the contract: encoders reach the value
	// through it, so it must exist.
	var marshaler encoding.TextMarshaler = NewSecret(sensitive)
	text, err := marshaler.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(text) != Redacted {
		t.Errorf("MarshalText = %q, want %q", text, Redacted)
	}
	empty, err := NewSecret("").MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("empty secret MarshalText = %q, want empty (not %q)", empty, Redacted)
	}

	if !NewSecret("").IsZero() {
		t.Error("IsZero() = false for an unset secret")
	}
	if NewSecret(sensitive).IsZero() {
		t.Error("IsZero() = true for a set secret")
	}

	// End to end on the path that matters: a secret carried into a log
	// record must reach the output redacted.
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).
		Info("registry login", "credential", NewSecret(sensitive))
	if strings.Contains(logged.String(), sensitive) {
		t.Errorf("slog text output leaks the secret: %s", logged.String())
	}
	if !strings.Contains(logged.String(), Redacted) {
		t.Errorf("slog text output misses the redaction marker: %s", logged.String())
	}
}
