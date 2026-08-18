// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/distribution/reference"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/policy"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Destination is the zone registry a passthrough instance promotes into
// (FR-013, FR-028, FR-034, FR-035).
//
// Like Publisher, and for the same reason, it is deliberately NOT built
// on Remotes. Source substitution answers "where do I read this from";
// applying it to a write would send content to an endpoint the operator
// never named. A promotion goes exactly where destination.registry says.
// Credentials (FR-004) and the per-host insecure opt-ins are shared with
// the reading side — they describe hosts, not intentions — the endpoint
// policy is not.
//
// The stakes are higher here than they were for `tobby recipe push`. That
// command runs once, in front of somebody who typed the reference and
// reads the result. This one runs on a timer, unattended, for as long as
// the instance lives: a push that goes to the wrong registry does not go
// there once, it goes there every interval until somebody notices. So
// there is no code path in this type that can produce an endpoint the
// configuration did not state, and the allowlist (FR-030) is consulted
// before the socket, exactly as it is on the reading side.
type Destination struct {
	host      string
	basePath  string
	cookbook  string
	insecure  bool
	keychain  authn.Keychain
	allowlist *policy.Allowlist
	egress    *netx.Egress

	// accepted caches the FR-035 pre-push acceptance verdict per
	// repository path: the probe is a network round trip, and a
	// destination's answer about a name shape does not change between two
	// ingredients of the same cycle.
	accepted sync.Map // string → error (nil error stored as acceptedOK)
}

// acceptedOK is the sentinel stored for a repository the destination
// accepts; sync.Map cannot distinguish a stored nil from a missing key.
var acceptedOK = new(struct{})

// NewDestination builds the promotion target from the destination
// section and the registry configuration the reading side already uses:
// credentials (FR-004 — promotion needs one with write scope on the
// destination host), per-host insecure opt-ins, the instance's single
// allowlist (FR-030, which covers destinations as well as sources), and
// the single outbound transport (FR-080, FR-081).
//
// It returns nil, nil when no destination is configured: an instance that
// promotes nothing is a valid instance — that is every mirror-mode
// instance, and any passthrough instance still being set up.
func NewDestination(dst config.Destination, regs config.Registries, allow *policy.Allowlist, eg *netx.Egress) (*Destination, error) {
	if !dst.Configured() {
		return nil, nil //nolint:nilnil // "no destination configured" is a state, not a failure
	}
	host, err := relocate.Host(dst.Registry)
	if err != nil {
		return nil, fmt.Errorf("destination.registry: %w", err)
	}
	kc, err := keychainFor(regs.CredentialsFile)
	if err != nil {
		return nil, err
	}
	d := &Destination{
		host:      host,
		basePath:  strings.Trim(dst.BasePath, "/"),
		cookbook:  strings.Trim(dst.Cookbook, "/"),
		keychain:  kc,
		allowlist: allow,
		egress:    netx.Or(eg),
	}
	if d.cookbook == "" {
		d.cookbook = config.DefaultCookbook
	}
	for _, h := range regs.Insecure {
		if canonical, herr := relocate.Host(h); herr == nil && canonical == host {
			d.insecure = true
		}
	}
	return d, nil
}

// Host reports the destination registry host, for the surfaces that show
// where this instance promotes to.
func (d *Destination) Host() string {
	if d == nil {
		return ""
	}
	return d.host
}

// Cookbook reports the destination cookbook path (FR-034).
func (d *Destination) Cookbook() string {
	if d == nil {
		return ""
	}
	return d.cookbook
}

// Path maps a relocated repository path onto its destination repository
// path, applying destination.basePath. The relocation itself (FR-035,
// RECIPE-SPEC §11.5) already happened: what arrives here is the canonical
// path the store holds, and the only thing left to decide is the prefix
// the next zone sees.
func (d *Destination) Path(relocated string) string {
	if d.basePath == "" {
		return relocated
	}
	return d.basePath + "/" + relocated
}

// Repository resolves one destination repository, refusing before any
// connection is opened.
//
// Two refusals happen here and nowhere later. FR-030: the allowlist
// covers destinations, and it is checked once the registry name is
// resolved and before the socket exists. FR-035: the relocated name must
// be one the destination can hold, and the length bound of the OCI name
// grammar is knowable without asking anyone — a name that cannot be
// written down cannot be pushed, and finding that out after half an image
// has been uploaded is the failure mode the requirement exists to
// prevent.
func (d *Destination) Repository(relocated string) (name.Repository, error) {
	path := d.Path(relocated)
	full := d.host + "/" + path
	if n := len(path); n > reference.RepositoryNameTotalLengthMax {
		return name.Repository{}, taxonomy.New(taxonomy.CodeDestinationLimit, taxonomy.Params{
			"reference": full,
			"limit": fmt.Sprintf("repository names are bounded at %d characters by the OCI name grammar; the relocated path is %d",
				reference.RepositoryNameTotalLengthMax, n),
		})
	}
	opts := []name.Option{}
	if d.insecure {
		opts = append(opts, name.Insecure)
	}
	repo, err := name.NewRepository(full, opts...)
	if err != nil {
		return name.Repository{}, taxonomy.New(taxonomy.CodeDestinationLimit, taxonomy.Params{
			"reference": full,
			"limit":     "the relocated path is not a valid OCI repository name: " + err.Error(),
		})
	}
	if err := d.allowlist.Check(repo.RegistryStr()); err != nil {
		return name.Repository{}, err
	}
	return repo, nil
}

// CookbookTag is where a recipe is re-published in the zone's own
// cookbook (FR-034): "<destination>/<cookbook>/<name>:<version>".
func (d *Destination) CookbookTag(recipeName, version string) (name.Tag, error) {
	repo, err := d.Repository(d.cookbookPath(recipeName))
	if err != nil {
		return name.Tag{}, err
	}
	return repo.Tag(version), nil
}

// cookbookPath is the cookbook repository of one recipe, relative to the
// destination base path. It deliberately does NOT go through relocate:
// the zone's cookbook is the zone's own namespace, not a relocation of
// the upstream one. A recipe promoted through three zones is published at
// the same "<cookbook>/<name>" in each of them, which is the whole point
// of FR-034 — each zone's cookbook reflects what that zone holds, stated
// in that zone's terms.
func (d *Destination) cookbookPath(recipeName string) string {
	return d.cookbook + "/" + recipeName
}

// options are the per-request remote options for every write and every
// read this type performs: credentials through the shared keychain, and
// the instance's single outbound transport (FR-080, FR-081). Every call
// goes through here, which is what makes the transport impossible to
// forget on the writing side too.
func (d *Destination) options(ctx context.Context) []remote.Option {
	return []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(d.keychain),
		remote.WithTransport(d.egress.RoundTripper()),
	}
}

// Accepts performs the FR-035 pre-push verification that the destination
// will hold this relocated name.
//
// The static bounds of Repository catch what the grammar forbids. This
// catches what a particular registry forbids, which no amount of local
// reasoning can predict: the relocation convention produces nested paths
// ("docker.io/bitnami/wordpress" under a base prefix is four components
// deep), and a number of registries in production refuse more than two,
// or refuse a path segment that looks like a hostname. The only authority
// on that is the destination itself.
//
// The probe is a tag listing: it writes nothing, it costs one request,
// and its failure modes are exactly the ones we need to tell apart. A
// registry that will not hold the name answers 400 (NAME_INVALID) or
// declines the route outright; one that will hold it answers 404 for a
// repository it has never seen, which is the expected answer on a first
// push and is therefore success here. Authentication and reachability
// failures pass through untouched: they are not naming verdicts, and
// dressing them up as one would send an operator to fix the wrong thing.
func (d *Destination) Accepts(ctx context.Context, repo name.Repository) error {
	key := repo.String()
	if cached, ok := d.accepted.Load(key); ok {
		if cached == acceptedOK {
			return nil
		}
		return cached.(error) //nolint:forcetypeassert // only errors and acceptedOK are ever stored
	}
	err := d.probe(ctx, repo)
	if err == nil {
		d.accepted.Store(key, acceptedOK)
		return nil
	}
	var te *taxonomy.Error
	if errors.As(err, &te) && te.Code() == taxonomy.CodeDestinationLimit {
		// Only the naming verdict is cached. A transport failure is about
		// this moment, not about this name, and caching it would keep an
		// instance refusing a perfectly good destination after the network
		// came back.
		d.accepted.Store(key, err)
	}
	return err
}

// probe runs one acceptance probe and translates the answer.
func (d *Destination) probe(ctx context.Context, repo name.Repository) error {
	_, err := remote.List(repo, d.options(ctx)...)
	if err == nil {
		return nil
	}
	var te *transport.Error
	if !errors.As(err, &te) {
		return err
	}
	switch te.StatusCode {
	case http.StatusNotFound:
		// The name is acceptable, the repository simply does not exist
		// yet. That is what a first push looks like.
		return nil
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return taxonomy.New(taxonomy.CodeDestinationLimit, taxonomy.Params{
			"reference": repo.String(),
			"limit": fmt.Sprintf("the destination refuses this name (HTTP %d): %s — the relocated path has %d components (RECIPE-SPEC §11.5); a registry limited to fewer needs destination.basePath shortened or a registry that accepts nested repositories",
				te.StatusCode, registryDiagnostic(te), strings.Count(repo.RepositoryStr(), "/")+1),
		})
	}
	return err
}

// registryDiagnostic renders what the registry actually said, so the
// refusal names the destination's limit in the destination's own words
// rather than in ours (FR-035).
func registryDiagnostic(te *transport.Error) string {
	parts := make([]string, 0, len(te.Errors))
	for _, e := range te.Errors {
		msg := strings.TrimSpace(e.Message)
		switch {
		case e.Code != "" && msg != "":
			parts = append(parts, string(e.Code)+": "+msg)
		case e.Code != "":
			parts = append(parts, string(e.Code))
		case msg != "":
			parts = append(parts, msg)
		}
	}
	if len(parts) == 0 {
		return "no diagnostic supplied"
	}
	return strings.Join(parts, "; ")
}
