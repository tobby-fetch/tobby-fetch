// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// FR-076 acceptance: docker, helm, and oras authenticate against the
// embedded registry unmodified — the same accounts and tokens as the web
// UI and the REST API — and the role ladder is honored on the wire (pull
// needs viewer, push needs operator).
//
// The tests drive the real binaries against a real Tobby registry over a
// real socket. Nothing here mocks the OCI protocol: an FR-076 claim
// verified against a fake is a claim about the fake. When a client binary
// is absent the corresponding subtest skips with an explicit message
// naming it — a skip that says which tool was missing is information; a
// silent one would be worse than no test at all.
package ocilogin_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// Accounts of the fixture instance, one per role of the ladder under test.
const (
	viewerUser = "lectrice"
	viewerPass = "pw-viewer"
	opUser     = "operatrice"
	opPass     = "pw-operator"
)

// registry is a running Tobby registry surface: the embedded OCI store
// behind the real authentication middleware, on a real loopback port.
type registry struct {
	// Host is "127.0.0.1:port" — what a client is given as the registry
	// name.
	Host string
	// CAFile is the PEM trust anchor of a TLS registry, empty for a plain
	// HTTP one. Only the Docker path needs it: oras and helm take
	// --plain-http, Docker does not.
	CAFile string
}

// newRegistryMux wires the /v2/ surface as internal/cli/serve.go does:
// the embedded store behind the real authentication middleware, with one
// account per role of the ladder under test.
func newRegistryMux(t *testing.T) *http.ServeMux {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		name, pass string
		role       auth.Role
	}{
		{viewerUser, viewerPass, auth.RoleViewer},
		{opUser, opPass, auth.RoleOperator},
	} {
		if err := accounts.AddAccount(a.name, a.role, a.pass, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	mux := http.NewServeMux()
	mux.Handle("/v2/", authn.Registry(st.APIHandler()))
	return mux
}

// startRegistry serves the registry over plain HTTP. The clients are told
// so explicitly (--plain-http), exactly as an operator would on a lab
// instance.
func startRegistry(t *testing.T) *registry {
	t.Helper()
	srv := httptest.NewServer(newRegistryMux(t))
	t.Cleanup(srv.Close)
	return &registry{Host: strings.TrimPrefix(srv.URL, "http://")}
}

// startTLSRegistry serves the registry over TLS and writes the server
// certificate out as a trust anchor. Docker has no --plain-http and no
// per-invocation CA flag: it verifies against the process trust store, so
// the only way to point it at a throwaway registry is a throwaway root.
func startTLSRegistry(t *testing.T) *registry {
	t.Helper()
	srv := httptest.NewTLSServer(newRegistryMux(t))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: srv.Certificate().Raw,
	})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return &registry{Host: strings.TrimPrefix(srv.URL, "https://"), CAFile: caFile}
}

// hostRoutableIP returns a non-loopback IPv4 address of this host.
//
// It exists for one client: the Docker daemon hardcodes 127.0.0.0/8 into
// its insecure-registry set, and that entry cannot be removed. A TLS
// registry on loopback is therefore reached WITHOUT certificate
// verification — the handshake succeeds whatever the trust store says, so
// a green test would prove nothing about the private root it claims to
// exercise. Serving on an address Docker considers secure is what makes
// the verification real.
func hostRoutableIP() (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4, nil
		}
	}
	return nil, errors.New("no non-loopback IPv4 address on this host")
}

// startRoutableTLSRegistry serves the fixture over TLS on a non-loopback
// address, behind a throwaway root whose leaf carries that address as an
// IP SAN. The root is written out for the client to trust explicitly:
// nothing here relaxes verification, which would defeat the point.
func startRoutableTLSRegistry(t *testing.T, ip net.IP) *registry {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tobby ocilogin throwaway root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: ip.String()},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{ip},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           newRegistryMux(t),
		ReadHeaderTimeout: 30 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}},
		},
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &registry{Host: ln.Addr().String(), CAFile: caFile}
}

// dockerCertsDir is where the docker daemon reads per-registry trust
// material. Not a guess: it is the only path it consults for a registry
// it considers secure.
const dockerCertsDir = "/etc/docker/certs.d"

// trustCAForDockerDaemon installs the throwaway root where the docker
// DAEMON looks for it, and reports whether it could.
//
// SSL_CERT_FILE is not enough and the reason matters: for a registry the
// daemon considers secure, it is the DAEMON that opens the TLS connection,
// not the CLI whose environment we control. Its trust material lives in
// /etc/docker/certs.d/<host>/ca.crt, per host, and nowhere else. Pointing
// the CLI at a root the daemon never reads is how this check spent a
// release reporting a skip that looked like a platform limit.
func trustCAForDockerDaemon(t *testing.T, reg *registry) bool {
	t.Helper()
	dir := filepath.Join(dockerCertsDir, reg.Host)
	install := func(args ...string) bool {
		cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: fixed argv, test-only privileged install
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%v: %v\n%s", args, err, out)
		}
		return err == nil
	}
	if !install("sudo", "-n", "mkdir", "-p", dir) {
		return false
	}
	if !install("sudo", "-n", "cp", reg.CAFile, filepath.Join(dir, "ca.crt")) {
		return false
	}
	t.Cleanup(func() { install("sudo", "-n", "rm", "-rf", dir) })
	return true
}

// dockerMustRun reports whether a docker subtest may skip.
//
// A skip is information only where the check genuinely cannot run. On the
// Linux operating scope it can, so TOBBY_OCILOGIN_REQUIRE_DOCKER turns
// every skip below into a failure — CI sets it. The whole reason this
// test was rewritten is that its TLS half quietly skipped everywhere and
// nobody noticed for a release.
func dockerMustRun() bool {
	return os.Getenv("TOBBY_OCILOGIN_REQUIRE_DOCKER") == "1"
}

// dockerSkip skips, or fails when the environment says the check was
// supposed to run here.
func dockerSkip(t *testing.T, format string, args ...any) {
	t.Helper()
	if dockerMustRun() {
		t.Fatalf("TOBBY_OCILOGIN_REQUIRE_DOCKER=1: "+format, args...)
	}
	t.Skipf(format, args...)
}

// run executes one client command in dir with an isolated environment and
// returns its combined output. It never touches the developer's own
// registry credentials: every client is pointed at a throwaway config.
func run(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("%s %s\n%s", name, strings.Join(args, " "), out)
	return string(out), err
}

// requireBinary skips the subtest with an explicit reason when a client is
// not installed. The message names the binary so a skipped run is still a
// report, not a silence.
func requireBinary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("FR-076 not verified for %s: the binary is not on PATH in this environment (%v). "+
			"Install it, or run this test where it exists — the check is real only against the real client.", name, err)
	}
	return path
}

// TestOrasLoginPushPull is the fullest of the three: oras is a pure
// client, so the whole FR-076 contract — login, push, pull — and the whole
// role ladder are observable through it, over the wire, against the
// embedded registry.
func TestOrasLoginPushPull(t *testing.T) {
	requireBinary(t, "oras")
	reg := startRegistry(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload.txt"), []byte("tobby fr-076\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opCfg := filepath.Join(dir, "oras-operator.json")
	viewerCfg := filepath.Join(dir, "oras-viewer.json")
	anonCfg := filepath.Join(dir, "oras-anonymous.json")

	// 1. Login with the operator account — the same account that signs in
	//    to the web UI. No registry-side configuration was needed.
	if out, err := run(t, dir, nil, "oras", "login", "--plain-http",
		"--registry-config", opCfg, "-u", opUser, "-p", opPass, reg.Host); err != nil {
		t.Fatalf("oras login as operator failed: %v\n%s", err, out)
	}
	// 2. A wrong password is refused: the middleware, not the client, says no.
	if out, err := run(t, dir, nil, "oras", "login", "--plain-http",
		"--registry-config", filepath.Join(dir, "oras-bad.json"),
		"-u", opUser, "-p", "not-the-password", reg.Host); err == nil {
		t.Errorf("oras login accepted a wrong password:\n%s", out)
	}
	// 3. Login with the viewer account.
	if out, err := run(t, dir, nil, "oras", "login", "--plain-http",
		"--registry-config", viewerCfg, "-u", viewerUser, "-p", viewerPass, reg.Host); err != nil {
		t.Fatalf("oras login as viewer failed: %v\n%s", err, out)
	}

	ref := reg.Host + "/fr076/artifact:v1"

	// 4. Push needs operator (ADR-0009). The viewer is refused on the wire.
	out, err := run(t, dir, nil, "oras", "push", "--plain-http",
		"--registry-config", viewerCfg, ref, "payload.txt:text/plain")
	if err == nil {
		t.Errorf("a viewer pushed to the embedded registry:\n%s", out)
	} else if !strings.Contains(out, "403") && !strings.Contains(strings.ToLower(out), "forbidden") {
		t.Errorf("the viewer's push was refused for the wrong reason (want 403):\n%s", out)
	}

	// 5. The operator pushes for real.
	if out, err := run(t, dir, nil, "oras", "push", "--plain-http",
		"--registry-config", opCfg, ref, "payload.txt:text/plain"); err != nil {
		t.Fatalf("operator push failed: %v\n%s", err, out)
	}

	// 6. Pull needs viewer, and the viewer gets the bytes back.
	pullDir := filepath.Join(dir, "pulled")
	if err := os.MkdirAll(pullDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, pullDir, nil, "oras", "pull", "--plain-http",
		"--registry-config", viewerCfg, ref); err != nil {
		t.Fatalf("viewer pull failed: %v\n%s", err, out)
	}
	//nolint:gosec // G304: the path is built from this test's own t.TempDir();
	// reading the bytes back is the point — a pull that lands the wrong content
	// would otherwise pass on the exit code alone.
	got, err := os.ReadFile(filepath.Join(pullDir, "payload.txt"))
	if err != nil {
		t.Fatalf("the pulled artifact is missing: %v", err)
	}
	if string(got) != "tobby fr-076\n" {
		t.Errorf("pulled content = %q", got)
	}

	// 7. Anonymous pull is refused: nothing on this surface is open (R-01).
	if out, err := run(t, pullDir, nil, "oras", "pull", "--plain-http",
		"--registry-config", anonCfg, ref); err == nil {
		t.Errorf("an anonymous client pulled from the embedded registry:\n%s", out)
	}
}

// TestOrasLoginOverTLS checks the registry over TLS with a client that
// takes an explicit CA. It stands on its own — a Tobby instance behind TLS
// is the normal deployment — and it also proves the throwaway root the
// Docker path exports is a usable trust anchor, so a Docker failure there
// can only be about Docker's own root resolution.
func TestOrasLoginOverTLS(t *testing.T) {
	requireBinary(t, "oras")
	reg := startTLSRegistry(t)
	dir := t.TempDir()

	if out, err := run(t, dir, nil, "oras", "login", "--ca-file", reg.CAFile,
		"--registry-config", filepath.Join(dir, "oras-tls.json"),
		"-u", opUser, "-p", opPass, reg.Host); err != nil {
		t.Fatalf("oras login over TLS failed: %v\n%s", err, out)
	}
	if out, err := run(t, dir, nil, "oras", "login", "--ca-file", reg.CAFile,
		"--registry-config", filepath.Join(dir, "oras-tls-bad.json"),
		"-u", opUser, "-p", "not-the-password", reg.Host); err == nil {
		t.Errorf("oras login over TLS accepted a wrong password:\n%s", out)
	}
}

// TestHelmRegistryLogin: `helm registry login` is the documented way to
// reach an OCI registry from Helm, and it must work against the embedded
// one without a proxy or a rewrite (FR-076).
func TestHelmRegistryLogin(t *testing.T) {
	requireBinary(t, "helm")
	reg := startRegistry(t)
	env := helmEnv(t, "login")

	out, err := run(t, t.TempDir(), env, "helm", "registry", "login", "--plain-http",
		"-u", opUser, "-p", opPass, reg.Host)
	if err != nil {
		t.Fatalf("helm registry login failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Login Succeeded") {
		t.Errorf("helm did not report a successful login:\n%s", out)
	}
	t.Cleanup(func() {
		_, _ = run(t, t.TempDir(), env, "helm", "registry", "logout", reg.Host)
	})
	if out, err := run(t, t.TempDir(), env, "helm", "registry", "login", "--plain-http",
		"-u", opUser, "-p", "not-the-password", reg.Host); err == nil {
		t.Errorf("helm registry login accepted a wrong password:\n%s", out)
	}
}

// TestHelmChartRoundTrip exercises the role ladder through Helm's own
// chart pipeline: a viewer's push is refused on the wire, an operator's
// succeeds, and a viewer pulls it back.
//
// The identities are used one at a time, with a logout between them. Helm
// delegates credential storage to the platform's Docker credential helper,
// which keys entries by registry host: two identities held at once against
// the same host would silently collapse into the last one written, and the
// test would then measure the helper, not the role ladder.
func TestHelmChartRoundTrip(t *testing.T) {
	requireBinary(t, "helm")
	reg := startRegistry(t)
	env := helmEnv(t, "roundtrip")
	dir := t.TempDir()

	chartDir := filepath.Join(dir, "fr076-chart")
	if err := os.MkdirAll(filepath.Join(chartDir, "templates"), 0o700); err != nil {
		t.Fatal(err)
	}
	chartYAML := "apiVersion: v2\nname: fr076-chart\nversion: 0.1.0\ntype: application\n"
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(chartYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, dir, nil, "helm", "package", "fr076-chart"); err != nil {
		t.Fatalf("helm package failed: %v\n%s", err, out)
	}

	login := func(user, pass string) {
		t.Helper()
		if out, err := run(t, dir, env, "helm", "registry", "login", "--plain-http",
			"-u", user, "-p", pass, reg.Host); err != nil {
			t.Fatalf("helm login as %s failed: %v\n%s", user, err, out)
		}
	}
	logout := func() {
		t.Helper()
		if out, err := run(t, dir, env, "helm", "registry", "logout", reg.Host); err != nil {
			t.Fatalf("helm logout failed: %v\n%s", err, out)
		}
	}
	target := "oci://" + reg.Host + "/fr076/charts"

	login(viewerUser, viewerPass)
	out, err := run(t, dir, env, "helm", "push", "--plain-http", "fr076-chart-0.1.0.tgz", target)
	if err == nil {
		t.Errorf("a viewer pushed a chart to the embedded registry:\n%s", out)
	} else if !strings.Contains(out, "403") {
		t.Errorf("the viewer's chart push was refused for the wrong reason (want 403):\n%s", out)
	}
	logout()

	login(opUser, opPass)
	if out, err := run(t, dir, env, "helm", "push", "--plain-http",
		"fr076-chart-0.1.0.tgz", target); err != nil {
		t.Fatalf("operator chart push failed: %v\n%s", err, out)
	}
	logout()

	login(viewerUser, viewerPass)
	t.Cleanup(logout)
	pullDir := filepath.Join(dir, "pulled")
	if err := os.MkdirAll(pullDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, pullDir, env, "helm", "pull", "--plain-http",
		target+"/fr076-chart", "--version", "0.1.0"); err != nil {
		t.Fatalf("viewer chart pull failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(pullDir, "fr076-chart-0.1.0.tgz")); err != nil {
		t.Errorf("the pulled chart is missing: %v", err)
	}
}

// helmEnv points helm at a throwaway credential file, pre-created empty so
// helm prefers it over the platform keychain where it can. The
// developer's own registry credentials are never read or written.
func helmEnv(t *testing.T, name string) []string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "helm-"+name+".json")
	if err := os.WriteFile(cfg, []byte(`{"auths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"HELM_REGISTRY_CONFIG=" + cfg}
}

// TestDockerLogin closes the FR-076 triple. Docker is the awkward one and
// the comment has to say why, because the outcome depends on the host.
//
// Docker offers neither a --plain-http flag nor a per-invocation CA flag:
// it decides on its own whether a registry may be reached over HTTP, and
// otherwise verifies TLS against the process trust store. So the test
// tries both doors. Plain HTTP first, for daemons that still treat
// loopback as insecure. Then TLS against a throwaway root exported through
// SSL_CERT_FILE, which Go's certificate loader honors on Linux — where
// this check has to hold, since that is the operating scope (NFR-018) and
// what CI runs. macOS resolves roots through the platform verifier and
// ignores SSL_CERT_FILE, so on a developer's Mac the second door is closed
// too; the test then says exactly that instead of passing on a
// technicality.
// TestDockerLogin exercises the plain-HTTP door.
//
// Deliberately NOT a fallback chain. It used to try HTTP and reach for
// TLS only when Docker refused, which meant the TLS door was unreachable
// wherever the HTTP door worked — and it always works on loopback, where
// the daemon hardcodes 127.0.0.0/8 as insecure. The transport a deployed
// instance actually presents (FR-082 serves TLS) was therefore never
// exercised anywhere. Two doors, two tests, like oras.
func TestDockerLogin(t *testing.T) {
	requireDocker(t)

	dir := t.TempDir()
	reg := startRegistry(t)
	env := []string{"DOCKER_CONFIG=" + filepath.Join(dir, "docker")}
	out, err := run(t, dir, env, "docker", "login", reg.Host, "-u", opUser, "-p", opPass)
	if err != nil {
		switch {
		case unreachable(out):
			dockerSkip(t, "FR-076 not verified for docker over plain HTTP: this host's Docker cannot "+
				"reach a loopback listener on the host (VM-isolated daemon). Output:\n%s", out)
			return
		case requiresTLS(out):
			// A daemon that refuses HTTP even to loopback cannot exercise
			// this door at all. That is a real environment, not a defect —
			// and TestDockerLoginOverTLS covers the transport that ships.
			dockerSkip(t, "FR-076 not verified for docker over plain HTTP: this daemon refuses HTTP to %s. "+
				"Output:\n%s", reg.Host, out)
			return
		}
		t.Fatalf("docker login over plain HTTP failed: %v\n%s", err, out)
	}
	assertDockerSession(t, dir, env, reg, out)
}

// TestDockerLoginOverTLS exercises the door a deployed instance presents
// (FR-082), against a private root the client must actually verify.
//
// It serves on a NON-LOOPBACK address on purpose. Docker cannot be told
// to treat 127.0.0.0/8 as secure, so a loopback TLS registry is reached
// with verification disabled: the handshake would succeed no matter what
// the trust store held, and a green result would say nothing about the
// private authority this test exists to check. On an address the daemon
// considers secure, the certificate has to chain to the root we exported
// — which is the assertion.
func TestDockerLoginOverTLS(t *testing.T) {
	requireDocker(t)

	ip, err := hostRoutableIP()
	if err != nil {
		dockerSkip(t, "FR-076 not verified for docker over TLS: %v. A loopback listener would be reached "+
			"with verification disabled, which would prove nothing.", err)
		return
	}

	dir := t.TempDir()
	reg := startRoutableTLSRegistry(t, ip)
	if !trustCAForDockerDaemon(t, reg) {
		dockerSkip(t, "FR-076 not verified for docker over TLS: this host's docker daemon trust store "+
			"(/etc/docker/certs.d) is not writable here, and the daemon — not the CLI — is what opens "+
			"the connection, so no client-side environment can stand in for it.")
		return
	}

	env := []string{"DOCKER_CONFIG=" + filepath.Join(dir, "docker")}
	out, err := run(t, dir, env, "docker", "login", reg.Host, "-u", opUser, "-p", opPass)
	if err != nil {
		if unreachable(out) {
			dockerSkip(t, "FR-076 not verified for docker over TLS: this host's docker cannot reach %s "+
				"(VM-isolated daemon). Output:\n%s", reg.Host, out)
			return
		}
		t.Fatalf("docker login over TLS failed: %v\n%s", err, out)
	}
	assertDockerSession(t, dir, env, reg, out)
}

// TestDockerRejectsAnUntrustedRegistry is the negative half of the door
// above: without the private root installed for the daemon, the same
// login must fail. Without it, a green TLS check could equally mean the
// daemon skipped verification — which is precisely what it does on
// loopback, and precisely what this pair exists to rule out.
func TestDockerRejectsAnUntrustedRegistry(t *testing.T) {
	requireDocker(t)

	ip, err := hostRoutableIP()
	if err != nil {
		dockerSkip(t, "FR-076 negative check not run: %v", err)
		return
	}
	dir := t.TempDir()
	reg := startRoutableTLSRegistry(t, ip)
	out, err := run(t, dir, []string{"DOCKER_CONFIG=" + filepath.Join(dir, "docker")},
		"docker", "login", reg.Host, "-u", opUser, "-p", opPass)
	switch {
	case err == nil:
		t.Errorf("docker logged in to a registry whose authority it does not trust:\n%s", out)
	case unreachable(out):
		dockerSkip(t, "FR-076 negative check not run: this host's docker cannot reach %s. Output:\n%s",
			reg.Host, out)
	case !untrustedRoot(out):
		t.Errorf("docker refused for the wrong reason; want an untrusted-authority refusal:\n%s", out)
	}
}

// requireDocker is requireBinary with the skip-to-failure switch applied.
func requireDocker(t *testing.T) {
	t.Helper()
	// The Windows runner carries a docker.exe on PATH, so the lookup below
	// would pass and the checks would then fall over further in, on the
	// wrong reason. They need a Linux daemon and a way to install a trust
	// anchor for it, and the runner offers neither (NFR-018).
	if runtime.GOOS == "windows" {
		t.Skip("FR-076 is NOT verified for docker on this run: the real-client checks need a Linux Docker " +
			"daemon and a way to install a private root for it, and neither exists on the Windows runner")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		dockerSkip(t, "FR-076 not verified for docker: the binary is not on PATH in this environment (%v).", err)
	}
}

// assertDockerSession checks what both doors must produce: a session, and
// a verdict that comes from Tobby rather than from the client — the same
// registry refuses a wrong password.
func assertDockerSession(t *testing.T, dir string, env []string, reg *registry, out string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = run(t, dir, env, "docker", "logout", reg.Host)
	})
	if !strings.Contains(out, "Login Succeeded") {
		t.Errorf("docker did not report a successful login:\n%s", out)
	}
	if out, err := run(t, dir, env, "docker", "login", reg.Host,
		"-u", opUser, "-p", "not-the-password"); err == nil {
		t.Errorf("docker login accepted a wrong password:\n%s", out)
	}
}

// requiresTLS recognizes Docker refusing to speak HTTP to the registry.
func requiresTLS(out string) bool {
	return strings.Contains(strings.ToLower(out), "server gave http response to https client")
}

// untrustedRoot recognizes Docker rejecting our throwaway certificate
// authority — the platform-verifier case.
func untrustedRoot(out string) bool {
	low := strings.ToLower(out)
	return strings.Contains(low, "certificate signed by unknown authority") ||
		strings.Contains(low, "failed to verify certificate")
}

// unreachable recognizes a transport failure between Docker and the test
// listener, as opposed to an authentication verdict.
func unreachable(out string) bool {
	low := strings.ToLower(out)
	for _, marker := range []string{
		"connection refused",
		"no route to host",
		"i/o timeout",
		"cannot connect to the docker daemon",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}
