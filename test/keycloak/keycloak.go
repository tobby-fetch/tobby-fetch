// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package keycloak is the identity-provider fixture of the OIDC and SAML
// tests (milestone 6): a real Keycloak, in a container, with a realm
// imported at start-up that carries one account per RBAC role.
//
// Nothing here mocks an IdP. A claim about OIDC or SAML verified against a
// fake is a claim about the fake; the code under test talks to the same
// server software a deployment would meet, over a real socket.
//
// A test starts the fixture with [Start], reads the issuer, the SAML
// metadata URL and the test accounts from the returned [Fixture], and plays
// the browser with [Fixture.LoginOIDC] and [Fixture.LoginSAML] — no browser
// is involved, the Keycloak login form is submitted over plain HTTP.
//
// The container runs on the docker CLI, like test/ocilogin: no module is
// added for it, and the only network use is the pull of the image pinned by
// digest in the IMAGE file. Without docker the tests skip with a message
// naming the missing piece; TOBBY_KEYCLOAK_REQUIRE_DOCKER=1 — set by CI on
// Linux — turns that skip into a failure, so a runner that lost docker can
// never look green by testing nothing.
//
// TOBBY_KEYCLOAK_URL (for example http://127.0.0.1:8080) attaches to a
// Keycloak that is already running and already carries the realm — a
// compose stack, a long-lived local container — instead of starting one.
// The redirect options are then the business of whoever imported the realm.
package keycloak

import (
	"bytes"
	"context"
	_ "embed" // the pinned image reference and the realm travel with the package
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//go:embed IMAGE
var imageFile string

//go:embed realm/tobby-realm.json
var realmJSON []byte

const (
	// RealmName is the realm every fixture URL lives under.
	RealmName = "tobby"

	// ClientID is the confidential OIDC client of the realm.
	ClientID = "tobby-oidc"
	// ClientSecret is its secret: a made-up test value that protects nothing.
	ClientSecret = "tobby-oidc-test-secret" //nolint:gosec // G101: fictitious fixture value, mirrored in realm/tobby-realm.json

	// DefaultSAMLEntityID is the SAML SP entity ID when [Options] leaves it
	// empty.
	DefaultSAMLEntityID = "https://tobby.test/saml/metadata"
	// DefaultSAMLACS is the SP's assertion consumer service URL when
	// [Options] leaves it empty.
	DefaultSAMLACS = "https://tobby.test/saml/acs"

	// DefaultOIDCRedirect is the redirect URI of the OIDC client when
	// [Options] leaves it empty. Nothing listens there: a login stops at the
	// redirect and hands the URL back, it never follows it.
	DefaultOIDCRedirect = "http://127.0.0.1:9/callback" //nolint:gosec // G101: a loopback URL, not a credential

	requireEnv = "TOBBY_KEYCLOAK_REQUIRE_DOCKER"
	attachEnv  = "TOBBY_KEYCLOAK_URL"

	containerPort = "8080"
)

// The RBAC groups of the realm: the IdP side of the group-to-role mapping
// (ADR-0009). The names are fixture data; Tobby maps them by configuration.
const (
	GroupViewer   = "tobby-viewer"
	GroupOperator = "tobby-operator"
	GroupAdmin    = "tobby-admin"
)

// User is a test account of the realm. Every value is fictitious.
type User struct {
	Username string
	Password string
	// Group is the realm group of the account, empty for the account that
	// belongs to none.
	Group string
	// Role is the Tobby role the group stands for ("viewer", "operator",
	// "admin"), empty when Group is.
	Role string
}

// Users is one account per role of the ladder, plus one in no group.
var (
	Viewer   = User{"viewer", "viewer-test-pw", GroupViewer, "viewer"}
	Operator = User{"operator", "operator-test-pw", GroupOperator, "operator"}
	Admin    = User{"admin", "admin-test-pw", GroupAdmin, "admin"}
	NoGroup  = User{"nogroup", "nogroup-test-pw", "", ""}
)

// Options parameterizes the realm at import time. Zero values select the
// Default* constants.
type Options struct {
	// OIDCRedirectURI is the single redirect URI the OIDC client accepts,
	// exact match (a test server's own callback URL, typically).
	OIDCRedirectURI string
	// SAMLEntityID is the entity ID of the SAML SP (the client ID).
	SAMLEntityID string
	// SAMLACSURL is the assertion consumer service URL of the SP.
	SAMLACSURL string
}

func (o Options) withDefaults() Options {
	if o.OIDCRedirectURI == "" {
		o.OIDCRedirectURI = DefaultOIDCRedirect
	}
	if o.SAMLEntityID == "" {
		o.SAMLEntityID = DefaultSAMLEntityID
	}
	if o.SAMLACSURL == "" {
		o.SAMLACSURL = DefaultSAMLACS
	}
	return o
}

// Fixture is a running Keycloak with the Tobby realm.
type Fixture struct {
	// BaseURL is the origin of the server, without a trailing slash.
	BaseURL string
	// Issuer is the OIDC issuer of the realm, as discovery reports it.
	Issuer string
	// SAMLMetadataURL is where the realm publishes its IdP descriptor.
	SAMLMetadataURL string
	// SAMLSSOURL is the IdP's single sign-on endpoint of the realm.
	SAMLSSOURL string
	// Options holds the realm parameters the fixture was started with.
	Options Options

	// alive reports why the server died, nil while it runs; nil in attach
	// mode, where the server is not ours to watch.
	alive func(context.Context) error
}

// Image returns the pinned image reference (tag and digest).
func Image() string { return strings.TrimSpace(imageFile) }

// RealmJSON returns the realm import file, placeholders unexpanded.
func RealmJSON() []byte { return bytes.Clone(realmJSON) }

// Start brings the fixture up and waits until the realm answers, or skips
// the test when it cannot (see the package comment). The container is
// removed when the test ends.
func Start(tb testing.TB, opts Options) *Fixture {
	tb.Helper()
	opts = opts.withDefaults()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	var base, name string
	if v := strings.TrimSpace(os.Getenv(attachEnv)); v != "" {
		base = strings.TrimRight(v, "/")
	} else {
		if _, err := exec.LookPath("docker"); err != nil {
			skip(tb, "docker is not installed: the Keycloak fixture cannot start")
		}
		out, err := docker(ctx, "info", "--format", "{{.OSType}}")
		if err != nil {
			skip(tb, "the docker daemon does not answer: %v\n%s", err, out)
		}
		// Keycloak ships Linux images only: a daemon in Windows-container
		// mode (the GitHub Windows runner) cannot pull it.
		if out != "linux" {
			skip(tb, "the docker daemon runs %q containers: the Keycloak image is Linux-only", out)
		}
		base, name = startContainer(ctx, tb, opts)
	}

	f := &Fixture{
		BaseURL:         base,
		Issuer:          base + "/realms/" + RealmName,
		SAMLMetadataURL: base + "/realms/" + RealmName + "/protocol/saml/descriptor",
		SAMLSSOURL:      base + "/realms/" + RealmName + "/protocol/saml",
		Options:         opts,
	}
	if name != "" {
		f.alive = func(ctx context.Context) error {
			out, err := docker(ctx, "inspect", "--format", "{{.State.Status}} (exit {{.State.ExitCode}})", name)
			if err == nil && !strings.HasPrefix(out, "running") {
				return fmt.Errorf("container %s: %s", name, out)
			}
			return nil
		}
	}
	if err := f.waitReady(ctx); err != nil {
		tb.Fatalf("keycloak fixture not ready: %v", err)
	}
	return f
}

func skip(tb testing.TB, format string, args ...any) {
	tb.Helper()
	if os.Getenv(requireEnv) == "1" {
		tb.Fatalf(requireEnv+"=1: "+format, args...)
	}
	tb.Skipf(format, args...)
}

// docker runs one docker CLI command and returns its combined output.
func docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: the docker CLI, argv composed by this fixture
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// startContainer creates the container, copies the realm into the import
// directory, starts it, and returns the origin of the published port and the container name.
//
// The realm is copied with `docker cp` rather than bind-mounted so the
// fixture works against a remote or VM-backed daemon too, where the host
// path of the file means nothing.
func startContainer(ctx context.Context, tb testing.TB, opts Options) (origin, name string) {
	tb.Helper()
	name = fmt.Sprintf("tobby-keycloak-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	out, err := docker(ctx, "create", "--name", name,
		"--publish", "127.0.0.1::"+containerPort,
		"--env", "TOBBY_OIDC_REDIRECT_URI="+opts.OIDCRedirectURI,
		"--env", "TOBBY_SAML_ENTITY_ID="+opts.SAMLEntityID,
		"--env", "TOBBY_SAML_ACS_URL="+opts.SAMLACSURL,
		Image(), "start-dev", "--import-realm")
	if err != nil {
		tb.Fatalf("docker create %s: %v\n%s", Image(), err, out)
	}
	tb.Cleanup(func() {
		rm, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if out, err := docker(rm, "rm", "--force", "--volumes", name); err != nil {
			tb.Errorf("removing %s: %v\n%s", name, err, out)
		}
	})

	dir, err := os.MkdirTemp("", "tobby-keycloak-realm-")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	// The container's import directory does not exist in the image, so the
	// whole directory is copied. Ownership becomes root's at the
	// destination: the files must be readable by the unprivileged user
	// Keycloak runs as.
	importDir := filepath.Join(dir, "import")
	if err := os.Mkdir(importDir, 0o755); err != nil { //nolint:gosec // G301: must be traversable by the keycloak user inside the container
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importDir, "tobby-realm.json"), realmJSON, 0o644); err != nil { //nolint:gosec // G306: same reason, and the file holds fictitious values only
		tb.Fatal(err)
	}
	if out, err := docker(ctx, "cp", importDir, name+":/opt/keycloak/data/"); err != nil {
		tb.Fatalf("docker cp realm: %v\n%s", err, out)
	}
	if out, err := docker(ctx, "start", name); err != nil {
		tb.Fatalf("docker start: %v\n%s", err, out)
	}
	hostPort, err := docker(ctx, "port", name, containerPort+"/tcp")
	if err != nil {
		tb.Fatalf("docker port: %v\n%s", err, hostPort)
	}
	// "127.0.0.1:49153" (one line per address family on some daemons).
	line, _, _ := strings.Cut(hostPort, "\n")
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "127.0.0.1:") {
		tb.Fatalf("unexpected published address %q", hostPort)
	}
	tb.Cleanup(func() {
		if tb.Failed() {
			lctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			logs, _ := docker(lctx, "logs", "--tail", "60", name)
			tb.Logf("keycloak logs (tail):\n%s", logs)
		}
	})
	return "http://" + line, name
}

// waitReady polls the realm's OIDC discovery document: it answers only
// once the server is up AND the import has created the realm, which is the
// one condition the tests depend on. It waits on that observable state,
// never on elapsed time.
func (f *Fixture) waitReady(ctx context.Context) error {
	client := &http.Client{Timeout: 5 * time.Second}
	target := f.Issuer + "/.well-known/openid-configuration"
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		last = err
		if f.alive != nil {
			if derr := f.alive(ctx); derr != nil {
				return fmt.Errorf("the server stopped before answering: %w", derr)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s never answered 200 (last: %w): %w", target, last, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// browser is an HTTP client that behaves like a browser for the one thing
// the login helpers need: it keeps cookies and never follows a redirect on
// its own, so the caller decides where the flow stops.
func browser() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// errNoLoginForm is returned when the page reached is not the Keycloak
// login form (a wrong redirect URI, a missing PKCE parameter, an error
// page): the message carries what Keycloak said.
var errNoLoginForm = errors.New("keycloak did not serve its login form")

// follow GETs rawURL and follows redirects that stay on this Keycloak,
// returning the final response (body unread) — the login form, or the
// redirect that leaves the server. Redirect targets are resolved against
// the URL that produced them.
func (f *Fixture) follow(ctx context.Context, c *http.Client, rawURL string) (*http.Response, *url.URL, error) {
	cur, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	for range 10 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cur.String(), http.NoBody)
		if err != nil {
			return nil, nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, nil, err
		}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
			return resp, cur, nil
		}
		_ = resp.Body.Close()
		next, err := cur.Parse(loc)
		if err != nil {
			return nil, nil, err
		}
		if !f.sameServer(next) {
			// Leaving Keycloak: hand the redirect back as a synthetic
			// response so the caller can read its Location.
			return &http.Response{StatusCode: resp.StatusCode, Header: http.Header{"Location": {next.String()}}, Body: http.NoBody}, cur, nil
		}
		cur = next
	}
	return nil, nil, errors.New("too many redirects inside keycloak")
}

func (f *Fixture) sameServer(u *url.URL) bool {
	b, err := url.Parse(f.BaseURL)
	return err == nil && u.Scheme == b.Scheme && u.Host == b.Host
}
