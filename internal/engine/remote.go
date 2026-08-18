// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
)

// Remotes reaches source registries with source substitution applied
// (FR-036, RECIPE-SPEC §11.5): the nominal host of a reference decides
// WHERE the content lives logically; the substitution decides WHICH
// endpoint is contacted. Credentials follow the effective host
// (§13.2) through the keychain; trust scopes follow the nominal ref —
// "policy follows the wire, trust follows the recipe" (ADR-0013).
type Remotes struct {
	subs      map[string]string
	insecure  map[string]bool
	keychain  authn.Keychain
	allowlist *policy.Allowlist
	egress    *netx.Egress
}

// NewRemotes builds the substitution-aware remote access from the
// instance's registry configuration: substitutions (FR-036), per-host
// insecure opt-ins and credentials (FR-004, default keychain when unset).
//
// allow is the instance's single allowlist (FR-030), built once and
// shared with every other outbound path, so that one object decides — and
// one object is observed — wherever the bytes are headed. A nil allow is
// an undeclared policy: no restriction.
//
// eg is the instance's single outbound transport (FR-080, FR-081): the
// proxy and the trust store the bytes actually travel through. It is a
// constructor argument rather than something this type builds, for the
// same reason the allowlist is — there must be no second place an
// outbound connection can come from. A nil egress is the unconfigured
// direct one.
func NewRemotes(cfg config.Registries, allow *policy.Allowlist, eg *netx.Egress) (*Remotes, error) {
	r := &Remotes{subs: map[string]string{}, insecure: map[string]bool{}, egress: netx.Or(eg)}
	for nominal, base := range cfg.Substitutions {
		canonical, err := relocate.Host(nominal)
		if err != nil {
			return nil, fmt.Errorf("registries.substitutions: %w", err)
		}
		r.subs[canonical] = strings.Trim(base, "/")
	}
	for _, h := range cfg.Insecure {
		r.insecure[h] = true
	}
	r.allowlist = allow
	kc, err := keychainFor(cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	r.keychain = kc
	return r, nil
}

// Allowlist exposes the configured policy, for the surfaces that report
// what this instance is allowed to reach.
func (r *Remotes) Allowlist() *policy.Allowlist { return r.allowlist }

// keychainFor builds the credential keychain of a configured
// credentialsFile (FR-004), falling back to the default keychain
// (~/.docker/config.json) when none is configured. Shared by the reading
// side (Remotes) and the publishing side (Publisher): one credential
// source, whichever direction the bytes travel.
func keychainFor(credentialsFile string) (authn.Keychain, error) {
	if credentialsFile == "" {
		return authn.DefaultKeychain, nil
	}
	raw, err := os.ReadFile(credentialsFile) //nolint:gosec // G304: operator-configured credentials path (FR-004)
	if err != nil {
		return nil, fmt.Errorf("registries.credentialsFile: %w", err)
	}
	kc, err := newDockerConfigKeychain(raw)
	if err != nil {
		return nil, fmt.Errorf("registries.credentialsFile: %w", err)
	}
	return authn.NewMultiKeychain(kc, authn.DefaultKeychain), nil
}

// dockerConfigKeychain resolves credentials from a
// kubernetes.io/dockerconfigjson payload (FR-004, RECIPE-SPEC §13):
// the standard Docker config.json "auths" structure, looked up by the
// effective host actually contacted (§13.2).
type dockerConfigKeychain struct {
	auths map[string]authn.AuthConfig
}

// dockerConfigFile is the subset of Docker's config.json the lookup reads.
type dockerConfigFile struct {
	Auths map[string]struct {
		Auth     string `json:"auth,omitempty"`
		Username string `json:"username,omitempty"`
		Password string `json:"password,omitempty"`
		Identity string `json:"identitytoken,omitempty"`
	} `json:"auths"`
}

func newDockerConfigKeychain(raw []byte) (*dockerConfigKeychain, error) {
	var f dockerConfigFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parsing dockerconfigjson: %w", err)
	}
	kc := &dockerConfigKeychain{auths: map[string]authn.AuthConfig{}}
	for host, a := range f.Auths {
		cfg := authn.AuthConfig{Username: a.Username, Password: a.Password, IdentityToken: a.Identity}
		if a.Auth != "" {
			decoded, err := base64.StdEncoding.DecodeString(a.Auth)
			if err != nil {
				return nil, fmt.Errorf("dockerconfigjson auths[%s]: invalid base64 auth", host)
			}
			if user, pass, ok := strings.Cut(string(decoded), ":"); ok {
				cfg.Username, cfg.Password = user, pass
			}
		}
		// Keys may be bare hosts or URL forms (https://index.docker.io/v1/):
		// normalize to the host.
		key := host
		key = strings.TrimPrefix(key, "https://")
		key = strings.TrimPrefix(key, "http://")
		key, _, _ = strings.Cut(key, "/")
		kc.auths[key] = cfg
	}
	return kc, nil
}

// Resolve implements authn.Keychain: lookup by the contacted host.
func (k *dockerConfigKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	if cfg, ok := k.auths[target.RegistryStr()]; ok {
		return authn.FromConfig(cfg), nil
	}
	// Docker Hub's canonical config key.
	if target.RegistryStr() == "index.docker.io" {
		if cfg, ok := k.auths["docker.io"]; ok {
			return authn.FromConfig(cfg), nil
		}
	}
	return authn.Anonymous, nil
}

// Effective maps a nominal repository ("docker.io/bitnami/wordpress") to
// the repository actually contacted. Without a substitution the nominal
// reference is contacted as written; with one, the content is fetched
// from the substitute base under the canonical relocated path — exactly
// where an upstream Tobby holds it (§11.5 cascade).
func (r *Remotes) Effective(nominalRepo string) (string, error) {
	canonical, err := relocate.Path(nominalRepo)
	if err != nil {
		return "", err
	}
	host, _, _ := strings.Cut(canonical, "/")
	base, ok := r.subs[strings.ReplaceAll(host, "_", ":")]
	if !ok {
		return nominalRepo, nil
	}
	return base + "/" + canonical, nil
}

// Repository resolves the effective name.Repository of a nominal
// repository path, honoring per-host insecure opt-ins.
func (r *Remotes) Repository(nominalRepo string) (name.Repository, string, error) {
	eff, err := r.Effective(nominalRepo)
	if err != nil {
		return name.Repository{}, "", err
	}
	opts := []name.Option{}
	if host, _, _ := strings.Cut(eff, "/"); r.insecure[host] {
		opts = append(opts, name.Insecure)
	}
	repo, err := name.NewRepository(eff, opts...)
	if err != nil {
		return name.Repository{}, "", fmt.Errorf("effective repository %q: %w", eff, err)
	}
	// FR-030, checked here because every read goes through this function
	// and none of them has opened a connection yet: the registry name has
	// been resolved, the socket has not. Evaluated on RegistryStr() — the
	// host the client will actually dial, which under substitution is not
	// the one the recipe names.
	if err := r.allowlist.Check(repo.RegistryStr()); err != nil {
		return name.Repository{}, "", err
	}
	return repo, eff, nil
}

// options are the per-request remote options: authentication through the
// keychain, and the instance's shared transport — proxy and private
// authorities included (FR-080, FR-081). Every read this type performs
// goes through here, which is what makes the transport impossible to
// forget on the reading side.
func (r *Remotes) options(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(r.keychain),
		remote.WithTransport(r.egress.RoundTripper()),
	}
}

// Egress exposes the shared outbound transport to the paths that reach
// the network without going through go-containerregistry — the retriever
// over HTTP(S) (FR-010) — so they cannot end up on a different one.
func (r *Remotes) Egress() *netx.Egress { return r.egress }

// Get fetches the descriptor of nominalRepo at reference (tag or digest).
func (r *Remotes) Get(ctx context.Context, nominalRepo, reference string) (*remote.Descriptor, error) {
	repo, _, err := r.Repository(nominalRepo)
	if err != nil {
		return nil, err
	}
	return remote.Get(parseInRepo(repo, reference), r.options(ctx)...)
}

// ListTags lists the tags of a nominal repository on its effective
// endpoint — the FR-021 resolution input.
func (r *Remotes) ListTags(ctx context.Context, nominalRepo string) ([]string, error) {
	repo, _, err := r.Repository(nominalRepo)
	if err != nil {
		return nil, err
	}
	return remote.List(repo, r.options(ctx)...)
}

// parseInRepo builds a tag or digest reference inside repo.
func parseInRepo(repo name.Repository, reference string) name.Reference {
	if strings.HasPrefix(reference, "sha256:") || strings.HasPrefix(reference, "sha512:") {
		return repo.Digest(reference)
	}
	return repo.Tag(reference)
}

// remoteManifests adapts Remotes to sigverify.Manifests for one nominal
// repository: signatures are verified against the endpoint actually
// serving the content, before anything is acted upon (§12.3 point 1).
type remoteManifests struct {
	r       *Remotes
	nominal string
}

// Manifests returns the sigverify read surface over a nominal repository.
func (r *Remotes) Manifests(nominal string) sigverify.Manifests {
	return &remoteManifests{r: r, nominal: nominal}
}

func (m *remoteManifests) Manifest(ctx context.Context, _, reference string) (payload []byte, mediaType, dgst string, err error) {
	desc, err := m.r.Get(ctx, m.nominal, reference)
	if err != nil {
		if isNotFound(err) {
			return nil, "", "", sigverify.ErrNotFound
		}
		return nil, "", "", err
	}
	return desc.Manifest, string(desc.MediaType), desc.Digest.String(), nil
}

// Referrers implements sigverify.ReferrersLister: cosign 3.x publishes
// signatures as artifacts REFERRING to their subject, and a registry that
// serves the OCI 1.1 Referrers API (GHCR, Harbor, ECR…) has no reason to
// also create the fallback tag the verifier would otherwise look for.
// Without this, a signature published in that layout is invisible.
// go-containerregistry queries the API and falls back to the tag on its
// own, so both registry generations answer here.
func (m *remoteManifests) Referrers(ctx context.Context, _, subjectDigest string) ([]string, error) {
	repo, _, err := m.r.Repository(m.nominal)
	if err != nil {
		return nil, err
	}
	idx, err := remote.Referrers(repo.Digest(subjectDigest), m.r.options(ctx)...)
	if err != nil {
		if isNotFound(err) {
			return nil, nil // no referrers surface, no referrers: not an error
		}
		return nil, err
	}
	man, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(man.Manifests))
	for i := range man.Manifests {
		out = append(out, man.Manifests[i].Digest.String())
	}
	return out, nil
}

func (m *remoteManifests) Blob(ctx context.Context, _, dgst string) ([]byte, error) {
	rrepo, _, err := m.r.Repository(m.nominal)
	if err != nil {
		return nil, err
	}
	layer, err := remote.Layer(rrepo.Digest(dgst), m.r.options(ctx)...)
	if err != nil {
		return nil, err
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	return readBounded(rc, 1<<20)
}
