// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package config loads Tobby's layered configuration (FR-003).
//
// Precedence, lowest to highest: built-in defaults, then the YAML
// configuration file, then TOBBY_* environment variables, then command-line
// flags. The effective configuration — secrets redacted by construction
// (NFR-015) — is dumpable through `tobby config dump`.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
)

// Mode is the operating mode, selected by configuration at startup (FR-001).
type Mode string

const (
	// ModePassthrough runs Tobby as a long-lived service continuously
	// promoting content between two connected zones.
	ModePassthrough Mode = "passthrough"
	// ModeMirror runs Tobby against a self-contained transportable store
	// that physically crosses an air gap.
	ModeMirror Mode = "mirror"
)

// Config is the effective configuration of one Tobby instance.
type Config struct {
	// Mode selects the operating mode. Required; there is no default: an
	// instance must state what it is (FR-001).
	Mode Mode `yaml:"mode"`

	Storage     Storage     `yaml:"storage"`
	State       State       `yaml:"state"`
	Server      Server      `yaml:"server"`
	Auth        Auth        `yaml:"auth"`
	Network     Network     `yaml:"network"`
	Registries  Registries  `yaml:"registries"`
	UI          UI          `yaml:"ui"`
	Import      Import      `yaml:"import"`
	Transfer    Transfer    `yaml:"transfer"`
	Retriever   Retriever   `yaml:"retriever"`
	Destination Destination `yaml:"destination"`
	Sync        Sync        `yaml:"sync"`
	Preflight   Preflight   `yaml:"preflight"`
	Tasks       Tasks       `yaml:"tasks"`
	Trust       Trust       `yaml:"trust"`
	Files       Files       `yaml:"files"`
	Logging     Logging     `yaml:"logging"`
	Shutdown    Shutdown    `yaml:"shutdown"`
}

// Retriever locates the desired-state document (FR-010): an HTTP(S) URL,
// an OCI reference, or a local file path — reported as configured through
// the UI and the API.
type Retriever struct {
	Source string `yaml:"source"`
}

// Destination is the zone registry this instance promotes into: the
// "continuous promotion" half of passthrough (FR-013, FR-026, FR-028,
// FR-034, FR-035).
//
// It is deliberately its own section rather than an entry of Registries.
// Registries answers "where may this instance read from, and through
// which endpoint" — source substitution lives there (FR-036). A
// destination answers a different question, and the two must not share a
// mechanism: substitution rewrites the endpoint a reference is fetched
// from, and applying it to a write would publish to a registry the
// operator never named. The same posture the publishing path took
// (engine.Publisher): credentials and per-host insecure opt-ins are
// shared with the reading side, the endpoint policy is not.
//
// Nothing here is a credential. Pushing needs a write-scoped credential
// on the destination host, and it comes from registries.credentialsFile
// like every other one (FR-004) — one credential source, whichever
// direction the bytes travel.
type Destination struct {
	// Registry is the destination registry host ("registry.example.com"
	// or "registry.example.com:5000"). Empty means this instance
	// promotes nothing: it fetches into its own store and stops there,
	// which is what a mirror-mode instance does by construction.
	//
	// A bare host, never a URL and never a repository path: the scheme
	// is decided by registries.insecure like it is on the reading side,
	// and the path is computed by the relocation convention (FR-035),
	// not written by hand.
	Registry string `yaml:"registry,omitempty"`
	// BasePath is an optional repository path prefix under which every
	// relocated ingredient lands on the destination
	// ("<registry>/<basePath>/<canonical-source-host>/<repo>"). It is
	// NOT storage.basePrefix: that one shapes this instance's own store,
	// this one shapes what the next zone sees. They are usually equal
	// and deliberately separate — a zone that stores under one prefix
	// may well have to publish under another.
	BasePath string `yaml:"basePath,omitempty"`
	// Cookbook is the repository path of the zone's own cookbook, where
	// recipes are re-published with their signatures (FR-034):
	// "<registry>/<cookbook>/<name>:<version>". Default "cookbook".
	Cookbook string `yaml:"cookbook,omitempty"`
}

// Configured reports whether a destination is declared at all.
func (d *Destination) Configured() bool { return d.Registry != "" }

// Sync bounds the recipe engine's transfers (NFR-008, FR-029) and paces
// the reconciliation loop (FR-013).
type Sync struct {
	// Parallelism caps concurrent ingredient transfers. Default 3.
	Parallelism int `yaml:"parallelism"`
	// Retries bounds per-ingredient retry attempts on transient transfer
	// failures (bounded backoff, FR-029). Default 3.
	Retries int `yaml:"retries"`
	// Interval paces the periodic reconciliation of FR-013. Default 15m.
	// Zero disables the loop, which is reported at startup rather than
	// left to be discovered: an instance that promotes nothing on its own
	// looks exactly like one whose interval never elapsed.
	//
	// It applies in passthrough mode ONLY. FR-014 requires mirror-mode
	// synchronization to be triggered manually and forbids it running
	// unattended, so the scheduler is not merely idle there — it is never
	// built.
	//
	// This value is the configured floor, not necessarily the effective
	// one: FR-013 requires the interval to be changeable without
	// redeployment, so an operator override persisted in the state
	// directory wins over it (package schedule). The override is a
	// sensitive configuration change and is audited as one (FR-094).
	Interval Duration `yaml:"interval"`
}

// Preflight configures the checks that run before a synchronization or an
// export starts (FR-055).
type Preflight struct {
	// SafetyMarginPercent is the share of the target's free space the
	// projection must NOT consume. Default 10 (package preflight's
	// DefaultMarginPercent).
	//
	// It exists because a store is never the only writer on its volume:
	// the instance's own logs, the container runtime, and the operating
	// system keep writing while a multi-hour transfer runs, and a
	// projection that lands on the last free byte lands on a full disk.
	// Zero restores the default rather than removing the margin — an
	// absent key must not silently mean "fill the volume". Setting it to
	// 0 % on purpose is spelled `disabled: true`.
	SafetyMarginPercent int `yaml:"safetyMarginPercent"`
	// Disabled turns the FR-055 gate into a report: volumes and verdicts
	// are still computed and still displayed, and the synchronization
	// starts anyway.
	//
	// It is the FR-075 shape — a safety check is removed only by an
	// explicit, visible, logged opt-in, never by a value that happens to
	// be zero — and it exists for the operator whose volume the instance
	// cannot measure honestly (a network mount, an unusual driver) and
	// who would otherwise have no way forward.
	Disabled bool `yaml:"disabled"`
}

// Trust configures signature verification (FR-033, ADR-0007,
// RECIPE-SPEC §12.3). Verification is on by default for every recipe;
// relaxation exists only as explicitly declared scopes — never a global
// bypass — and every relaxed scope is visible in the reported
// configuration (FR-075 principle). List-valued: configuration file only.
type Trust struct {
	Roots  []TrustRoot  `yaml:"roots,omitempty"`
	Scopes []TrustScope `yaml:"scopes,omitempty"`
}

// TrustRoot is one trusted public key (cosign key-based, multi-key set for
// rotation by overlap). Exactly one of Key, KeyFile, KeyURL is set; a URL
// is fetched and cached at configuration time, never at verification time
// (RECIPE-SPEC §12.3 — air-gapped instances use inline or file forms).
type TrustRoot struct {
	Name    string `yaml:"name"`
	Key     string `yaml:"key,omitempty"`
	KeyFile string `yaml:"keyFile,omitempty"`
	KeyURL  string `yaml:"keyURL,omitempty"`
}

// TrustScope declares one relaxed verification perimeter (§12.3: "a named
// low-assurance source restricted to specific repositories"). Scopes are
// evaluated in declaration order; the first match applies; no match means
// the strict default (signature required, any configured root). A scope
// must relax or restrict something: AllowUnsigned, or a Roots restriction.
type TrustScope struct {
	Name string `yaml:"name"`
	// Repositories are glob patterns matched against the recipe's nominal
	// cookbook repository path in its CANONICAL form ("*" stays within one
	// path segment, "**" spans separators). Trust follows the nominal ref,
	// never the substituted endpoint (FR-036).
	//
	// Canonical means the form of ADR-0013: the host lowercased, the
	// Docker Hub aliases folded to "docker.io", and a port's ":" written
	// "_" — because ":" cannot appear in a repository path. A registry on
	// port 5000 is therefore matched by
	//
	//	lab.example.com_5000/cookbook/*
	//
	// and not by the "lab.example.com:5000/…" spelling of the reference
	// itself. The portless case reads the same either way, which is what
	// made this worth spelling out (B-014).
	Repositories []string `yaml:"repositories"`
	// AllowUnsigned admits unsigned recipes inside the scope — reported on
	// every surface (banner, logs, task report), never silent (FR-075).
	AllowUnsigned bool `yaml:"allowUnsigned,omitempty"`
	// Roots restricts verification to the named trust roots within the
	// scope (defense in depth for multi-tenant cookbooks).
	Roots []string `yaml:"roots,omitempty"`
}

// Files configures the FileSet HTTP serving surface (FR-047). Disabled by
// default: only FileSets listed here are served. List-valued:
// configuration file only.
type Files struct {
	FileSets []FileSetServe `yaml:"filesets,omitempty"`
}

// FileSetServe enables one verified FileSet under /files/<name>/.
type FileSetServe struct {
	// Name is the URL segment ("debs" serves under /files/debs/).
	Name string `yaml:"name"`
	// Ref is the nominal ingredient reference of the FileSet
	// ("registry.example.com/filesets/site-config": host + repository, no
	// tag or digest — the served content is whatever verified digest the
	// store holds for it).
	Ref string `yaml:"ref"`
	// Version pins the served tag; empty serves the highest semver tag
	// present locally.
	Version string `yaml:"version,omitempty"`
	// Platform selects the platform manifest of a multi-platform FileSet,
	// as "os/arch[/variant]" — the variant is optional and an omitted one
	// accepts any ("linux/arm64" finds the linux/arm64/v8 child, B-020).
	// Empty picks the single manifest and fails on an ambiguous index.
	Platform string `yaml:"platform,omitempty"`
	// Anonymous opts this FileSet into unauthenticated reads (bare-host
	// bootstrap) — reported like the FR-075 override, never silent.
	Anonymous bool `yaml:"anonymous,omitempty"`
}

// Tasks bounds the persistent task history (2026-08 audit). Without a
// bound every task lives forever — in memory, and as a .json plus a .log
// file inside the store — and a passthrough instance on a 10-minute cycle
// mints ~52 000 sync tasks a year, all reloaded at every start.
type Tasks struct {
	// KeepFinished is how many finished tasks (done or failed) the queue
	// retains, newest first; older ones are purged together with their
	// log files. Pending and running tasks are never purged — the FR-029
	// resume contract owns them. 0 keeps the whole history. Default 500.
	KeepFinished int `yaml:"keepFinished"`
}

// Transfer bounds how blobs cross the wire, whichever operation asks for
// them — a unit import (FR-023) and a recipe synchronization (FR-014) hit
// the same registries over the same link, so the knob belongs to neither.
type Transfer struct {
	// ResumeThreshold is the declared blob size from which a download
	// becomes resumable inside the blob itself (FR-029): the bytes are
	// spooled in the state directory with their offset, and an
	// interruption — a cut connection, a failed attempt, a killed
	// process — restarts at the offset through an HTTP Range request
	// instead of at zero. Default 64MiB.
	//
	// There is a threshold rather than "always" because resumability is
	// not free: a resumable blob transits the state directory before
	// reaching the store, so it costs its own size in temporary disk
	// space and one extra pass of local I/O. Below the threshold a blob
	// streams straight from the registry into the store as it always
	// has, and re-fetching it after a cut costs less than the bookkeeping
	// would.
	//
	// Zero disables the resumable path entirely and restores that
	// streaming behavior for every blob — the escape hatch for an
	// instance whose state directory cannot absorb the traffic. It is
	// reported at startup rather than left to be discovered: an instance
	// that never resumes looks exactly like one that never had to.
	ResumeThreshold Size `yaml:"resumeThreshold"`
}

// Import configures the unit-import screens and endpoints (FR-023).
type Import struct {
	// InspectTimeout bounds one remote inspection (UI-SPEC §5.6): a
	// deadline hit maps to the dedicated TBY-REG-004 code, distinct from
	// "unreachable". Default 20s.
	InspectTimeout Duration `yaml:"inspectTimeout"`
}

// Registries configures how source registries are reached.
type Registries struct {
	// Insecure lists source hosts reachable over plain HTTP ("host" or
	// "host:port"). Per-host and explicit — never a global switch
	// (FR-075) — it selects the scheme for the named host and changes
	// nothing for any other.
	//
	// It is NOT the answer to a registry behind a private PKI: that is
	// network.tls (FR-081), which keeps the peer authenticated. The two
	// coexist because they answer different questions — "this host
	// speaks plain HTTP" versus "this host's certificate chains to our
	// own authority" — and configuring the authority is what makes this
	// list unnecessary in the ordinary enterprise case.
	Insecure []string `yaml:"insecure,omitempty"`
	// Substitutions maps a nominal registry host to a substitute registry
	// base (FR-036, RECIPE-SPEC §11.5), applied at fetch time: a downstream
	// zone fetches "docker.io/…" from its upstream zone registry without
	// modifying the recipes. Substitution changes only the endpoint
	// contacted, never the relocated path (FR-035); credentials apply to
	// the effective host, trust scopes to the nominal ref. List-valued:
	// configuration file only.
	Substitutions map[string]string `yaml:"substitutions,omitempty"`
	// Allowlist bounds which registries this instance may contact at all
	// (FR-030), evaluated on the host ACTUALLY reached — after source
	// substitution, since that is the endpoint the bytes come from.
	// Entries are registry hosts, optionally with a port, and may carry
	// the same globs trust scopes use ("*" within a DNS label, "**"
	// across labels).
	//
	// The key's ABSENCE and an empty list are different statements, as
	// with a Kubernetes NetworkPolicy: absent means no restriction,
	// "allowlist: []" means nothing is allowed. There is no safe
	// non-empty default — an instance cannot guess its operator's
	// registries — so an undeclared policy is reported as undeclared
	// everywhere rather than silently passing for a satisfied one.
	// List-valued: configuration file only.
	Allowlist []string `yaml:"allowlist,omitempty"`
	// CredentialsFile points at a kubernetes.io/dockerconfigjson payload
	// (FR-004): registry credentials looked up by the effective host
	// actually contacted (RECIPE-SPEC §13.2). Secrets never live in the
	// configuration file itself, and the file must sit outside the
	// transportable store (R-16).
	CredentialsFile string `yaml:"credentialsFile,omitempty"`
}

// UI configures the web interface (ADR-0010, ADR-0015).
type UI struct {
	// ThemeOverride is an optional operator stylesheet served after the
	// embedded design tokens: rebranding without rebuild (FR-064). The
	// default tokens pass WCAG AA; overrides carry that responsibility.
	ThemeOverride string `yaml:"themeOverride"`
	// ShowUpcoming renders the navigation entries of future milestones as
	// inert, labeled placeholders (demo mode). Off by default: production
	// navigation only shows what works.
	ShowUpcoming bool `yaml:"showUpcoming"`
}

// Storage locates the self-contained store (FR-050).
type Storage struct {
	// Root is the store directory. Required for serving: everything Tobby
	// holds — artifacts, recipes, operation logs — lives under it.
	Root string `yaml:"root"`
	// BasePrefix is the optional relocation base prefix (FR-035), applied
	// identically to every ingredient of the instance. Default: none.
	BasePrefix string `yaml:"basePrefix,omitempty"`
}

// State locates the instance state directory: accounts and tokens today,
// trust roots, certificates and configuration tables as they land. It is
// the instance's identity and must stay strictly outside the transportable
// store: secrets never travel on the media (R-16), and the directory is the
// single backup target (R-27).
type State struct {
	// Root is the state directory. Required to serve unless authentication
	// is explicitly disabled.
	Root string `yaml:"root"`
}

// Auth configures authentication (ADR-0009; FR-072 to FR-075).
type Auth struct {
	// Disabled switches authentication off for every surface. Secure by
	// default: false. Disabling is a deliberate opt-in — settable only in
	// the configuration file or TOBBY_AUTH_DISABLED, never by flag — and
	// the UI shows a permanent banner while it is set (FR-075).
	Disabled bool `yaml:"disabled"`
	// SessionTTL bounds a UI session's lifetime. Sessions live in memory:
	// an instance restart signs everyone out. Default 12h.
	SessionTTL Duration `yaml:"sessionTTL"`
}

// Server configures the HTTP listener (UI, API, registry, probes, metrics).
type Server struct {
	// Addr is the listen address, host:port. Default ":8080".
	Addr string `yaml:"addr"`
	// TLS configures the certificate the listener presents (FR-082).
	TLS ServerTLS `yaml:"tls"`
	// SecureCookies marks every UI cookie Secure even though the listener
	// itself serves plain HTTP. In the documented deployment a reverse
	// proxy or an ingress terminates TLS in front of the instance
	// (deploy/), so the listener sees no TLS on any request and the
	// session cookie would otherwise be minted without the Secure
	// attribute (NFR-015). The topology is stated explicitly by the
	// operator rather than inferred from X-Forwarded-Proto: forwarding
	// headers are client-supplied, and Tobby already refuses to trust
	// them for audit origins (FR-094) — a spoofable header must not
	// decide a security attribute either. When the listener serves TLS
	// itself (server.tls), cookies are Secure regardless of this setting.
	SecureCookies bool `yaml:"secureCookies,omitempty"`
}

// ServerTLS configures the listener's own certificate (FR-082). One
// listener carries the UI, the API and the embedded registry (ADR-0015),
// so one certificate covers all three: there is nothing to configure per
// surface, and no surface can be left in the clear by accident.
type ServerTLS struct {
	// Enabled serves TLS. Supplying certFile and keyFile implies it —
	// an administrator who hands over a certificate has stated the
	// intent — so this flag is only needed to ask for the self-signed
	// fallback described below.
	Enabled bool `yaml:"enabled,omitempty"`
	// CertFile and KeyFile are the administrator-supplied PEM pair.
	// Both or neither. They are re-read whenever the files change on
	// disk, so replacing them replaces the served certificate without
	// restarting the instance — the "certificate replacement is
	// possible via configuration" half of FR-082.
	CertFile string `yaml:"certFile,omitempty"`
	KeyFile  string `yaml:"keyFile,omitempty"`
	// Hosts are the subject alternative names the self-signed fallback
	// is issued for, on top of the loopback names and the machine's own
	// hostname. Ignored when a certificate is supplied. List-valued:
	// configuration file only.
	Hosts []string `yaml:"hosts,omitempty"`
}

// Serves reports whether the listener must speak TLS: either explicitly
// enabled, or implied by a supplied certificate.
func (t *ServerTLS) Serves() bool {
	return t.Enabled || t.CertFile != "" || t.KeyFile != ""
}

// Network configures how this instance reaches the outside world: the
// forward proxy every outbound connection goes through (FR-080) and the
// certificate authorities it trusts on top of the public ones (FR-081).
//
// There is deliberately no "skip TLS verification" setting anywhere in
// this structure. FR-081 asks for private authorities to be TRUSTED, not
// for verification to be dropped, and the two are not interchangeable: a
// private CA still authenticates the peer, a disabled check authenticates
// nothing. The only relaxation Tobby offers remains registries.insecure —
// per host, explicitly named, and reported (FR-075) — which selects the
// plain-HTTP scheme for one host rather than weakening TLS for all of
// them. Configuring the private CA here is what makes that opt-in
// unnecessary in the ordinary enterprise case.
type Network struct {
	Proxy Proxy     `yaml:"proxy"`
	TLS   ClientTLS `yaml:"tls"`
}

// Proxy is the forward proxy every outbound request goes through
// (FR-080). It is instance-global on purpose: in a segmented enterprise
// zone direct egress is blocked, so a fetch path that does not use the
// proxy does not fail loudly — it hangs until its timeout. One setting,
// one transport, every path.
type Proxy struct {
	// URL is the forward proxy ("http://proxy.example.com:3128"). An
	// https:// proxy is accepted: the hop to the proxy is then itself
	// TLS, verified against the same trust store as everything else.
	// Credentials never belong in this string — they have their own
	// fields below, so that nothing which formats a URL can print them.
	URL string `yaml:"url,omitempty"`
	// HTTPSURL proxies https:// destinations when they take a different
	// route from plain-HTTP ones. Empty means URL serves both, the
	// common case: one CONNECT-capable proxy for everything.
	HTTPSURL string `yaml:"httpsURL,omitempty"`
	// NoProxy lists destinations reached directly — a host, a ".suffix",
	// a CIDR block, or "*" for everything. The peer zone's registry and
	// the instance's own loopback usually belong here. List-valued:
	// configuration file only (or the TOBBY_NETWORK_PROXY_NO_PROXY
	// comma-separated form).
	NoProxy []string `yaml:"noProxy,omitempty"`
	// Username authenticates to the proxy.
	Username string `yaml:"username,omitempty"`
	// Password is the proxy credential. Its type is what guarantees the
	// FR-080 acceptance criterion — proxy credentials never appear in
	// logs, error messages, or `tobby config dump` — by construction
	// rather than by review discipline (NFR-015). Settable in the
	// configuration file or through TOBBY_NETWORK_PROXY_PASSWORD, never
	// by flag: a flag value is readable in the process table.
	Password Secret `yaml:"password,omitempty"`
}

// Configured reports whether a proxy is set at all.
func (p *Proxy) Configured() bool { return p.URL != "" || p.HTTPSURL != "" }

// ClientTLS extends the outbound trust store (FR-081): the certificate
// authorities this instance trusts in addition to the public ones, used
// for registries, Helm repositories, the retriever, trust-root URLs, and
// the TLS hop to the proxy itself — one trust store, like one transport.
type ClientTLS struct {
	// CAFiles are paths to PEM bundles. List-valued: configuration file
	// only (or the TOBBY_NETWORK_TLS_CA_FILES comma-separated form).
	CAFiles []string `yaml:"caFiles,omitempty"`
	// CA is an inline PEM bundle, for deployments that inject
	// configuration but cannot mount a file.
	CA string `yaml:"ca,omitempty"`
	// ExclusiveTrust drops the host's public root store, leaving only
	// the authorities configured above. Default false: a private CA
	// normally adds to public trust rather than replacing it. This
	// setting only ever narrows what is trusted — it is the opposite
	// direction from the skip-verify switch FR-081 forbids, and it is
	// named so that the difference is not a matter of memory.
	ExclusiveTrust bool `yaml:"exclusiveTrust,omitempty"`
}

// Configured reports whether any additional authority is declared.
func (t *ClientTLS) Configured() bool { return len(t.CAFiles) > 0 || t.CA != "" }

// Logging configures the structured JSON logs (FR-090).
type Logging struct {
	// Level is one of debug, info, warn, error. Default "info".
	Level string `yaml:"level"`
}

// Shutdown configures the graceful-shutdown behavior (FR-093, ADR-0012).
type Shutdown struct {
	// GracePeriod is how long in-flight work gets to finish or checkpoint
	// after SIGTERM/SIGINT before the process exits. Default 30s.
	GracePeriod Duration `yaml:"gracePeriod"`
}

// Default returns the built-in defaults, the lowest configuration layer.
func Default() Config {
	return Config{
		Server:      Server{Addr: ":8080"},
		Auth:        Auth{SessionTTL: Duration(12 * time.Hour)},
		Import:      Import{InspectTimeout: Duration(20 * time.Second)},
		Transfer:    Transfer{ResumeThreshold: 64 * MiB},
		Destination: Destination{Cookbook: DefaultCookbook},
		Sync:        Sync{Parallelism: 3, Retries: 3, Interval: Duration(15 * time.Minute)},
		Preflight:   Preflight{SafetyMarginPercent: preflight.DefaultMarginPercent},
		Tasks:       Tasks{KeepFinished: 500},
		Logging:     Logging{Level: "info"},
		Shutdown:    Shutdown{GracePeriod: Duration(30 * time.Second)},
	}
}

// Scope names the configuration slice a command actually uses: validation
// is per-command (R-34) — a command must never demand a setting it ignores
// (B-006: `tobby user` only needs the state directory, not a mode).
type Scope int

const (
	// ScopeInstance validates the full instance configuration — what
	// serving requires, mode included (FR-001).
	ScopeInstance Scope = iota
	// ScopeState validates only what state-directory commands need:
	// everything set must be coherent, but no mode is required.
	ScopeState
	// ScopeStorage validates what store-facing commands need (`tobby
	// export`, `tobby import`, FR-051): the storage root and its
	// separation from the state directory. No mode is required — reading
	// a store out to an image layout says nothing about how this host
	// serves.
	ScopeStorage
	// ScopeRegistries validates what registry-facing commands need
	// (`tobby recipe push`, R-36): credentials and per-host insecure
	// opt-ins. Like ScopeState it requires no mode — publishing a recipe
	// is an authoring act, it says nothing about how this host serves.
	ScopeRegistries
)

// Load builds the effective configuration: defaults, overlaid with the YAML
// file at path (skipped when path is empty and optional), then environment
// variables, then flag overrides. Validation runs on the merged result, for
// the full instance scope.
func Load(path string, pathExplicit bool, overrides ...Override) (Config, error) {
	return LoadFor(ScopeInstance, path, pathExplicit, overrides...)
}

// LoadFor is Load with per-command validation (R-34): the merged result is
// validated only against what the command's scope actually uses.
func LoadFor(scope Scope, path string, pathExplicit bool, overrides ...Override) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // G304: reading the operator-supplied configuration file is the feature (FR-003)
		switch {
		case err == nil:
			dec := yaml.NewDecoder(bytes.NewReader(data))
			dec.KnownFields(true)
			if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
				// io.EOF means an empty file: nothing to overlay.
				return Config{}, fmt.Errorf("configuration file %s: %w", path, err)
			}
		case os.IsNotExist(err) && !pathExplicit:
			// The default file location is optional; an explicitly given
			// path must exist.
		default:
			return Config{}, fmt.Errorf("configuration file: %w", err)
		}
	}

	if err := applyEnv(&cfg, os.LookupEnv); err != nil {
		return Config{}, err
	}
	for _, o := range overrides {
		o(&cfg)
	}

	if err := cfg.validate(scope); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Override is one flag-level override, the highest configuration layer.
// The CLI translates every set flag into one Override.
type Override func(*Config)

// Validate checks the merged configuration for the full instance scope.
// Error messages state what to fix.
func (c *Config) Validate() error { return c.validate(ScopeInstance) }

// validate checks the merged configuration against one command scope: what
// is set must always be coherent; what is absent is only an error when the
// scope requires it (R-34).
func (c *Config) validate(scope Scope) error {
	var errs []error
	switch c.Mode {
	case ModePassthrough, ModeMirror:
	case "":
		if scope == ScopeInstance {
			errs = append(errs, errors.New(`mode is required: set "passthrough" or "mirror" (flag --mode, env TOBBY_MODE, or "mode:" in the configuration file)`))
		}
	default:
		errs = append(errs, fmt.Errorf(`unknown mode %q: valid modes are "passthrough" and "mirror"`, c.Mode))
	}
	if _, err := parseLevel(c.Logging.Level); err != nil {
		errs = append(errs, fmt.Errorf("logging.level: %w", err))
	}
	if c.Server.Addr == "" {
		errs = append(errs, errors.New("server.addr must not be empty"))
	}
	if time.Duration(c.Shutdown.GracePeriod) <= 0 {
		errs = append(errs, errors.New("shutdown.gracePeriod must be positive"))
	}
	if time.Duration(c.Auth.SessionTTL) <= 0 {
		errs = append(errs, errors.New("auth.sessionTTL must be positive"))
	}
	if time.Duration(c.Import.InspectTimeout) <= 0 {
		errs = append(errs, errors.New("import.inspectTimeout must be positive"))
	}
	if err := disjointRoots(c.State.Root, c.Storage.Root); err != nil {
		errs = append(errs, err)
	}
	if c.Transfer.ResumeThreshold < 0 {
		errs = append(errs, errors.New("transfer.resumeThreshold must not be negative: use 0 to disable the in-blob resume of FR-029"))
	}
	if c.Sync.Parallelism <= 0 {
		errs = append(errs, errors.New("sync.parallelism must be positive"))
	}
	if c.Sync.Retries < 0 {
		errs = append(errs, errors.New("sync.retries must not be negative"))
	}
	if time.Duration(c.Sync.Interval) < 0 {
		errs = append(errs, errors.New("sync.interval must not be negative: use 0 to disable the periodic reconciliation of FR-013"))
	}
	if c.Preflight.SafetyMarginPercent < 0 || c.Preflight.SafetyMarginPercent >= 100 {
		errs = append(errs, errors.New("preflight.safetyMarginPercent must be between 0 and 99: it is a share of the target's free space held back (FR-055); use \"preflight.disabled: true\" to remove the check itself"))
	}
	if c.Tasks.KeepFinished < 0 {
		errs = append(errs, errors.New("tasks.keepFinished must not be negative: use 0 to keep the whole task history"))
	}
	errs = append(errs, c.validateDestination()...)
	errs = append(errs, c.validateNetwork()...)
	errs = append(errs, c.validateServerTLS()...)
	errs = append(errs, c.validateTrust()...)
	errs = append(errs, c.validateFiles()...)
	return errors.Join(errs...)
}

// DefaultCookbook is the destination cookbook path used when none is
// configured (FR-034): the zone's recipes land at
// "<destination>/cookbook/<name>:<version>".
const DefaultCookbook = "cookbook"

// validateDestination checks the promotion target (FR-013, FR-035).
//
// A destination is a bare registry host and, at most, path prefixes. The
// checks below exist because every one of them is a way to end up pushing
// somewhere the operator did not mean: a URL would carry a scheme the
// per-host insecure opt-in is supposed to decide, a host with a path
// would silently prepend a repository segment the relocation convention
// never accounted for, and a tag would be a reference where a host was
// asked for. A promotion service pushes on a timer with nobody watching,
// so an ambiguous destination is not a nuisance to be discovered on the
// first run — it is a misdirection repeated forever.
func (c *Config) validateDestination() []error {
	var errs []error
	d := &c.Destination
	if d.Registry != "" {
		switch {
		case strings.Contains(d.Registry, "://"):
			errs = append(errs, fmt.Errorf("destination.registry: %q must be a bare registry host, not a URL — the scheme follows registries.insecure, like on the reading side", d.Registry))
		case strings.Contains(d.Registry, "/"):
			errs = append(errs, fmt.Errorf("destination.registry: %q must be a bare registry host — the repository path is computed by the relocation convention (FR-035), use destination.basePath for a prefix", d.Registry))
		case strings.Contains(d.Registry, "@"):
			errs = append(errs, fmt.Errorf("destination.registry: %q must be a bare registry host, not a reference", d.Registry))
		}
	}
	for _, f := range []struct {
		key, value string
	}{
		{"destination.basePath", d.BasePath},
		{"destination.cookbook", d.Cookbook},
	} {
		if err := validRepositoryPath(f.key, f.value); err != nil {
			errs = append(errs, err)
		}
	}
	if !d.Configured() {
		// Settings that only mean something with a destination are refused
		// rather than ignored: a cookbook path nobody pushes to reads, in a
		// configuration dump, exactly like one that works.
		if d.BasePath != "" {
			errs = append(errs, errors.New("destination.basePath is set without destination.registry: there is nothing to push to, so the prefix would be silently unused"))
		}
		if d.Cookbook != "" && d.Cookbook != DefaultCookbook {
			errs = append(errs, errors.New("destination.cookbook is set without destination.registry: recipes have nowhere to be propagated to (FR-034)"))
		}
	}
	return errs
}

// validRepositoryPath accepts an OCI repository path prefix: lowercase
// path components separated by single slashes, no traversal, no leading
// or trailing separator. The registry would refuse the malformed forms
// anyway — the point of checking here is that it refuses them at the
// first push of the first cycle, not at startup.
func validRepositoryPath(key, value string) error {
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") {
		return fmt.Errorf("%s: %q must not start, end, or double up on %q", key, value, "/")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("%s: %q must not contain a path traversal component", key, value)
		}
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			default:
				return fmt.Errorf("%s: %q is not a valid repository path — components are lowercase alphanumeric plus [._-] (OCI name grammar)", key, value)
			}
		}
	}
	return nil
}

// validateNetwork checks the outbound network configuration (FR-080,
// FR-081). A malformed proxy must fail startup rather than degrade to
// direct egress: in a zone where direct egress is blocked, the degraded
// mode is not "slower", it is "hangs until every timeout expires".
//
// Error messages quote the proxy URL as configured. That is safe by
// construction: credentials live in their own fields, never in the URL,
// so there is nothing in this string to redact.
func (c *Config) validateNetwork() []error {
	var errs []error
	for _, f := range []struct {
		key, value string
	}{
		{"network.proxy.url", c.Network.Proxy.URL},
		{"network.proxy.httpsURL", c.Network.Proxy.HTTPSURL},
	} {
		if f.value == "" {
			continue
		}
		u, err := url.Parse(f.value)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %q is not a URL", f.key, f.value))
			continue
		case u.Scheme != "http" && u.Scheme != "https":
			errs = append(errs, fmt.Errorf("%s: scheme %q is not a forward-proxy scheme (expected http:// or https://)", f.key, u.Scheme))
		case u.Host == "":
			errs = append(errs, fmt.Errorf("%s: %q has no host", f.key, f.value))
		}
		if u.User != nil {
			// Credentials in the URL would travel through every string
			// that ever formats it. The dedicated fields exist so that
			// cannot happen (NFR-015); accepting both would make the
			// guarantee conditional.
			errs = append(errs, fmt.Errorf("%s: credentials must not be embedded in the URL — use network.proxy.username and network.proxy.password, which never serialize", f.key))
		}
	}
	if !c.Network.Proxy.Configured() {
		if c.Network.Proxy.Username != "" || !c.Network.Proxy.Password.IsZero() {
			errs = append(errs, errors.New("network.proxy.username/password are set without network.proxy.url: credentials without a proxy would be silently unused"))
		}
		if len(c.Network.Proxy.NoProxy) > 0 {
			errs = append(errs, errors.New("network.proxy.noProxy is set without network.proxy.url: there is no proxy to exempt destinations from"))
		}
	}
	if c.Network.TLS.ExclusiveTrust && !c.Network.TLS.Configured() {
		errs = append(errs, errors.New("network.tls.exclusiveTrust drops the public root store but network.tls declares no authority: the instance would trust nothing"))
	}
	return errs
}

// validateServerTLS checks the listener certificate configuration
// (FR-082): a half-declared pair is a configuration error, never a
// silent fall back to the self-signed certificate — an operator who
// supplied a certificate must be told it was not used.
func (c *Config) validateServerTLS() []error {
	t := &c.Server.TLS
	switch {
	case t.CertFile != "" && t.KeyFile == "":
		return []error{errors.New("server.tls.certFile is set without server.tls.keyFile: a certificate needs its private key")}
	case t.KeyFile != "" && t.CertFile == "":
		return []error{errors.New("server.tls.keyFile is set without server.tls.certFile")}
	}
	if len(t.Hosts) > 0 && t.CertFile != "" {
		return []error{errors.New("server.tls.hosts names the subjects of the generated fallback certificate and has no effect when server.tls.certFile supplies one: remove it, or remove the certificate")}
	}
	return nil
}

// validateTrust checks the trust-root set and the declared scopes
// (FR-033): a malformed trust configuration must fail startup, never relax
// silently.
func (c *Config) validateTrust() []error {
	var errs []error
	rootNames := map[string]bool{}
	for i, r := range c.Trust.Roots {
		if r.Name == "" {
			errs = append(errs, fmt.Errorf("trust.roots[%d]: name is required", i))
		} else if rootNames[r.Name] {
			errs = append(errs, fmt.Errorf("trust.roots[%d]: duplicate name %q", i, r.Name))
		}
		rootNames[r.Name] = true
		set := 0
		for _, v := range []string{r.Key, r.KeyFile, r.KeyURL} {
			if v != "" {
				set++
			}
		}
		if set != 1 {
			errs = append(errs, fmt.Errorf("trust.roots[%d] (%s): exactly one of key, keyFile, keyURL is required", i, r.Name))
		}
		if r.KeyURL != "" && !strings.HasPrefix(r.KeyURL, "https://") {
			errs = append(errs, fmt.Errorf("trust.roots[%d] (%s): keyURL must be https:// (RECIPE-SPEC §12.3)", i, r.Name))
		}
	}
	scopeNames := map[string]bool{}
	for i, s := range c.Trust.Scopes {
		if s.Name == "" {
			errs = append(errs, fmt.Errorf("trust.scopes[%d]: name is required", i))
		} else if scopeNames[s.Name] {
			errs = append(errs, fmt.Errorf("trust.scopes[%d]: duplicate name %q", i, s.Name))
		}
		scopeNames[s.Name] = true
		if len(s.Repositories) == 0 {
			errs = append(errs, fmt.Errorf("trust.scopes[%d] (%s): repositories patterns are required — a scope is always an explicitly declared perimeter", i, s.Name))
		}
		if !s.AllowUnsigned && len(s.Roots) == 0 {
			errs = append(errs, fmt.Errorf("trust.scopes[%d] (%s): a scope must declare what it changes — allowUnsigned and/or a roots restriction", i, s.Name))
		}
		for _, want := range s.Roots {
			if !rootNames[want] {
				errs = append(errs, fmt.Errorf("trust.scopes[%d] (%s): unknown trust root %q", i, s.Name, want))
			}
		}
	}
	return errs
}

// validateFiles checks the FileSet serving declarations (FR-047).
func (c *Config) validateFiles() []error {
	var errs []error
	names := map[string]bool{}
	for i, f := range c.Files.FileSets {
		if !validFileSetName(f.Name) {
			errs = append(errs, fmt.Errorf("files.filesets[%d]: name %q must be a lowercase URL segment ([a-z0-9._-])", i, f.Name))
		} else if names[f.Name] {
			errs = append(errs, fmt.Errorf("files.filesets[%d]: duplicate name %q", i, f.Name))
		}
		names[f.Name] = true
		if f.Ref == "" {
			errs = append(errs, fmt.Errorf("files.filesets[%d] (%s): ref is required", i, f.Name))
		}
	}
	return errs
}

// validFileSetName accepts a safe URL path segment: lowercase alphanumeric
// plus [._-], no traversal, no separator.
func validFileSetName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// disjointRoots refuses a state directory inside the transportable store or
// the reverse: secrets and instance identity never travel on the media
// (R-16), and the store must stay relocatable without dragging state along.
func disjointRoots(state, storage string) error {
	if state == "" || storage == "" {
		return nil
	}
	s, err1 := filepath.Abs(state)
	g, err2 := filepath.Abs(storage)
	if err1 != nil || err2 != nil {
		return nil // path resolution problems surface later, on use
	}
	rel, err := filepath.Rel(g, s)
	if err == nil && rel == "." {
		return fmt.Errorf("state.root and storage.root must differ (%s)", s)
	}
	if err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("state.root must not live inside storage.root: secrets never travel on the transportable store (state.root=%s, storage.root=%s)", s, g)
	}
	rel, err = filepath.Rel(s, g)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("storage.root must not live inside state.root (state.root=%s, storage.root=%s)", s, g)
	}
	return nil
}

// Dump renders the effective configuration as YAML. Secret values are
// redacted by construction: the Secret type cannot serialize its content.
func (c *Config) Dump() (string, error) {
	out, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("rendering configuration: %w", err)
	}
	return string(out), nil
}

// parseLevel mirrors logging.ParseLevel's accepted values without importing
// the logging package (config stays dependency-free within the module).
func parseLevel(s string) (struct{}, error) {
	switch s {
	case "debug", "info", "warn", "warning", "error", "":
		return struct{}{}, nil
	}
	return struct{}{}, fmt.Errorf("unknown log level %q (expected debug, info, warn, or error)", s)
}
