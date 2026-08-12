// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// TestEffective locks the FR-036 endpoint mapping: without a substitution
// the nominal reference is contacted as written; with one, the substitute
// base is prefixed to the CANONICAL relocated path — Docker Hub aliases
// folded, ports folded — exactly where an upstream instance holds it.
func TestEffective(t *testing.T) {
	cases := []struct {
		name string
		subs map[string]string
		ref  string
		want string
	}{
		{"no substitution: as written", nil,
			"docker.io/bitnami/wordpress", "docker.io/bitnami/wordpress"},
		{"no substitution keeps aliases as written", nil,
			"index.docker.io/library/nginx", "index.docker.io/library/nginx"},
		{"docker.io substituted", map[string]string{"docker.io": "127.0.0.1:5000"},
			"docker.io/bitnami/wordpress", "127.0.0.1:5000/docker.io/bitnami/wordpress"},
		{"alias folds into the substitution", map[string]string{"docker.io": "127.0.0.1:5000"},
			"index.docker.io/library/nginx", "127.0.0.1:5000/docker.io/library/nginx"},
		{"alias key canonicalizes too", map[string]string{"registry-1.docker.io": "mirror.local"},
			"docker.io/library/nginx", "mirror.local/docker.io/library/nginx"},
		{"ported host: '_' in the path, ':' in the key", map[string]string{"registry.example.com:5000": "mirror.local:8443"},
			"registry.example.com:5000/team/app", "mirror.local:8443/registry.example.com_5000/team/app"},
		{"unrelated host untouched", map[string]string{"docker.io": "127.0.0.1:5000"},
			"quay.io/team/app", "quay.io/team/app"},
		{"trailing slash on the base is trimmed", map[string]string{"docker.io": "127.0.0.1:5000/"},
			"docker.io/library/nginx", "127.0.0.1:5000/docker.io/library/nginx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRemotes(t, tc.subs)
			got, err := r.Effective(tc.ref)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Effective(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}

	t.Run("invalid nominal reference", func(t *testing.T) {
		if _, err := newRemotes(t, nil).Effective("no-host-reference"); err == nil {
			t.Error("Effective accepted a hostless reference")
		}
	})
	t.Run("invalid substitution key", func(t *testing.T) {
		if _, err := NewRemotes(map[string]string{"not a host": "x"}, nil, ""); err == nil {
			t.Error("NewRemotes accepted an invalid substitution key")
		}
	})
}

// TestRepositoryInsecure locks the per-host insecure opt-in: only listed
// EFFECTIVE hosts downgrade to HTTP.
func TestRepositoryInsecure(t *testing.T) {
	r, err := NewRemotes(nil, []string{"reg.example.com"}, "")
	if err != nil {
		t.Fatal(err)
	}
	repo, eff, err := r.Repository("reg.example.com/team/app")
	if err != nil {
		t.Fatal(err)
	}
	if eff != "reg.example.com/team/app" || repo.Scheme() != "http" {
		t.Errorf("insecure host: effective %q scheme %q, want http", eff, repo.Scheme())
	}
	repo, _, err = r.Repository("other.example.com/team/app")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Scheme() != "https" {
		t.Errorf("unlisted host scheme = %q, want https", repo.Scheme())
	}
}

// TestDockerConfigKeychain locks the FR-004/§13.2 credential lookup: the
// kubernetes.io/dockerconfigjson "auths" table, keyed by the effective
// host actually contacted, URL-form keys normalized, unknown hosts
// anonymous.
func TestDockerConfigKeychain(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("hub-user:hub-pass"))
	kc, err := newDockerConfigKeychain([]byte(`{
		"auths": {
			"https://index.docker.io/v1/": {"auth": "` + b64 + `"},
			"registry.example.com": {"username": "alice", "password": "s3same"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	hub := repoRegistry(t, "docker.io/library/nginx")
	assertAuth(t, kc, hub, "hub-user", "hub-pass")
	assertAuth(t, kc, repoRegistry(t, "registry.example.com/team/app"), "alice", "s3same")

	// Unknown host: anonymous, never a guessed credential.
	auth, err := kc.Resolve(repoRegistry(t, "quay.io/team/app"))
	if err != nil {
		t.Fatal(err)
	}
	if auth != authn.Anonymous {
		t.Errorf("unknown host resolved %v, want Anonymous", auth)
	}

	// The bare "docker.io" key serves Docker Hub's canonical endpoint.
	kc2, err := newDockerConfigKeychain([]byte(`{"auths":{"docker.io":{"username":"u2","password":"p2"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertAuth(t, kc2, hub, "u2", "p2")

	t.Run("invalid base64 auth refuses", func(t *testing.T) {
		if _, err := newDockerConfigKeychain([]byte(`{"auths":{"h.example.com":{"auth":"%%%"}}}`)); err == nil {
			t.Error("accepted an invalid base64 auth entry")
		}
	})
	t.Run("invalid json refuses", func(t *testing.T) {
		if _, err := newDockerConfigKeychain([]byte("not-json")); err == nil {
			t.Error("accepted a non-JSON credentials payload")
		}
	})
}

// TestNewRemotesCredentialsFile locks the FR-004 wiring: a configured
// dockerconfigjson file loads; a missing or unparseable one refuses at
// configuration time.
func TestNewRemotesCredentialsFile(t *testing.T) {
	path := writeTempFile(t, "creds.json", []byte(`{"auths":{"registry.example.com":{"username":"a","password":"b"}}}`))
	if _, err := NewRemotes(nil, nil, path); err != nil {
		t.Errorf("NewRemotes with a valid credentials file: %v", err)
	}
	if _, err := NewRemotes(nil, nil, path+".missing"); err == nil {
		t.Error("NewRemotes accepted a missing credentials file")
	}
	bad := writeTempFile(t, "bad.json", []byte("not-json"))
	if _, err := NewRemotes(nil, nil, bad); err == nil {
		t.Error("NewRemotes accepted an unparseable credentials file")
	}
}

func repoRegistry(t *testing.T, ref string) authn.Resource {
	t.Helper()
	repo, err := name.NewRepository(ref)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Registry
}

func assertAuth(t *testing.T, kc authn.Keychain, res authn.Resource, user, pass string) {
	t.Helper()
	a, err := kc.Resolve(res)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := a.Authorization()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != user || cfg.Password != pass {
		t.Errorf("resolved %s:%s for %s, want %s:%s", cfg.Username, cfg.Password, res.RegistryStr(), user, pass)
	}
}

// TestWithRetries locks the FR-029 retry discipline: bounded retries on
// operational failures only — policy and verification verdicts are
// deterministic and never retried.
func TestWithRetries(t *testing.T) {
	ctx := context.Background()
	opErr := taxonomy.New(taxonomy.CodeRegistryUnreachable, taxonomy.Params{"host": "h"})

	t.Run("operational errors retry up to the bound", func(t *testing.T) {
		calls := 0
		err := withRetries(ctx, 1, func() error { calls++; return opErr })
		if calls != 2 || err == nil {
			t.Errorf("calls = %d (err %v), want 2 attempts ending in the error", calls, err)
		}
	})

	t.Run("verification verdicts never retry", func(t *testing.T) {
		calls := 0
		verErr := taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{"recipe": "r", "fingerprints": "f"})
		if err := withRetries(ctx, 5, func() error { calls++; return verErr }); err == nil || calls != 1 {
			t.Errorf("calls = %d (err %v), want exactly 1 attempt", calls, err)
		}
	})

	t.Run("policy refusals never retry", func(t *testing.T) {
		calls := 0
		polErr := taxonomy.New(taxonomy.CodeNotAllowlisted, taxonomy.Params{"host": "h"})
		if err := withRetries(ctx, 5, func() error { calls++; return polErr }); err == nil || calls != 1 {
			t.Errorf("calls = %d (err %v), want exactly 1 attempt", calls, err)
		}
	})

	t.Run("success clears after a transient failure", func(t *testing.T) {
		calls := 0
		err := withRetries(ctx, 2, func() error {
			calls++
			if calls == 1 {
				return opErr
			}
			return nil
		})
		if err != nil || calls != 2 {
			t.Errorf("calls = %d, err = %v, want success on the second attempt", calls, err)
		}
	})

	t.Run("zero retries means one attempt", func(t *testing.T) {
		calls := 0
		if err := withRetries(ctx, 0, func() error { calls++; return opErr }); err == nil || calls != 1 {
			t.Errorf("calls = %d, want 1", calls)
		}
	})

	t.Run("cancellation stops the backoff", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		calls := 0
		start := time.Now()
		if err := withRetries(cctx, 3, func() error { calls++; return opErr }); err == nil {
			t.Error("want the last error after cancellation")
		}
		if calls != 1 || time.Since(start) > 2*time.Second {
			t.Errorf("calls = %d in %v, want a single attempt and no backoff wait", calls, time.Since(start))
		}
	})
}
