// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Driving a running instance from the command line (FR-014, FR-066).
//
// Some operations cannot be done beside an instance, only through it. A
// synchronization writes to the store, and the store is held open for
// writing by whoever is serving it — a second process opening the same
// directory is the one thing the format forbids. So the command-line
// trigger does what the "Synchronize" button does: it calls
// POST /api/v1/sync on the instance and reports what came back. It
// pilots; it does not work on its own, and its help says so, because an
// operator who believes otherwise will point it at a store on a host
// where nothing is running and wonder why nothing happens.
//
// Everything here speaks to /api/v1 and to nothing else: the API is the
// contract (FR-060), so the CLI is just another client of it and inherits
// its authentication, its roles and its taxonomy.

// EnvInstanceURL names the instance a command should drive. It exists
// because the listen address is not a URL: an instance configured with
// `server.addr: ":8080"` is reachable at a hostname only its operator
// knows.
const EnvInstanceURL = "TOBBY_INSTANCE_URL"

// EnvAPIToken carries the static API token (FR-072) the command
// authenticates with.
//
// It is an environment variable and deliberately NOT a flag, for the
// reason --proxy-url's password is not one either (FR-080, NFR-015): a
// flag value is visible in the process table and in shell history. A file
// is the other supported form, for hosts where the environment is itself
// the exposure.
const EnvAPIToken = "TOBBY_API_TOKEN" //nolint:gosec // G101: the NAME of a variable, not a credential

// instanceFlags are the flags of a command that drives a running
// instance.
type instanceFlags struct {
	url       string
	tokenFile string
	timeout   string
}

func (f *instanceFlags) register(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.StringVar(&f.url, "instance", "",
		"base URL of the instance to drive, e.g. https://tobby.example:8443 (default "+EnvInstanceURL+", then the configured listen address on localhost)")
	fs.StringVar(&f.tokenFile, "token-file", "",
		"file holding the static API token; "+EnvAPIToken+" is read when it is absent (the token is never a flag — flag values are visible in the process table)")
	fs.StringVar(&f.timeout, "request-timeout", "",
		`per-request timeout against the instance, e.g. "30s" (default 30s)`)
}

// defaultRequestTimeout bounds one call to the instance. It bounds a
// REQUEST, never the wait: --wait polls, and a synchronization that runs
// for six hours is a normal synchronization.
const defaultRequestTimeout = 30 * time.Second

// instanceClient is a thin /api/v1 client.
type instanceClient struct {
	base   string
	token  string
	http   *http.Client
	egress *netx.Egress
	// poll paces waitForTask. A field rather than the bare constant so a
	// test can watch several polls happen without spending several
	// seconds doing it.
	poll time.Duration
}

// newInstanceClient resolves the target instance and its credential.
func newInstanceClient(cmd *cobra.Command, cfg *config.Config, f *instanceFlags) (*instanceClient, error) {
	base, err := f.baseURL(cmd, cfg)
	if err != nil {
		return nil, err
	}
	token, err := f.token()
	if err != nil {
		return nil, err
	}
	timeout := defaultRequestTimeout
	if f.timeout != "" {
		d, perr := config.ParseDuration(f.timeout)
		if perr != nil {
			return nil, &usageError{err: fmt.Errorf("--request-timeout: %w", perr), hint: "see '" + cmd.CommandPath() + " --help'"}
		}
		if d <= 0 {
			return nil, &usageError{
				err:  fmt.Errorf("--request-timeout %q: a request needs a positive budget", f.timeout),
				hint: "see '" + cmd.CommandPath() + " --help'",
			}
		}
		timeout = time.Duration(d)
	}
	// The instance may serve TLS with the site's own authority, so the
	// call goes through the configured egress (FR-081) rather than a
	// bare client. The proxy selection honours noProxy, and httpproxy
	// excludes loopback by itself — an instance on this host is not
	// reached through the site proxy.
	egress, err := netx.New(&cfg.Network)
	if err != nil {
		return nil, err
	}
	return &instanceClient{
		base: base, token: token, http: egress.Client(timeout),
		egress: egress, poll: pollInterval,
	}, nil
}

// close releases the pooled connections.
func (c *instanceClient) close() { c.egress.CloseIdleConnections() }

// baseURL resolves which instance to drive: the flag, then the
// environment, then the configured listen address read as a localhost
// URL — the last one being right exactly when the command runs on the
// instance's own host, which is the common case for a cron entry.
func (f *instanceFlags) baseURL(cmd *cobra.Command, cfg *config.Config) (string, error) {
	raw := f.url
	if raw == "" {
		raw = os.Getenv(EnvInstanceURL)
	}
	if raw == "" {
		raw = localInstanceURL(cfg)
	}
	if raw == "" {
		return "", &usageError{
			err: errors.New("no instance to drive: give --instance, set " + EnvInstanceURL +
				", or configure server.addr so the local instance can be addressed"),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", &usageError{
			err:  fmt.Errorf("--instance %q is not a base URL (expected e.g. https://tobby.example:8443)", raw),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// localInstanceURL builds the loopback URL of the instance this host
// serves, from the configuration the instance itself would load. A
// listen address is a bind spec, not a location: an empty or wildcard
// host means "every interface", and the one an operator standing on the
// host can always reach is the loopback.
func localInstanceURL(cfg *config.Config) string {
	addr := cfg.Server.Addr
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	scheme := "http"
	if cfg.Server.TLS.CertFile != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// token reads the credential: the file when one is named, the
// environment otherwise, and nothing at all on an instance running under
// the FR-075 authentication override.
func (f *instanceFlags) token() (string, error) {
	if f.tokenFile == "" {
		return strings.TrimSpace(os.Getenv(EnvAPIToken)), nil
	}
	raw, err := os.ReadFile(f.tokenFile)
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("--token-file %s: the file is empty", f.tokenFile)
	}
	return token, nil
}

// maxProblemBody bounds what is read from an error response. A problem
// document is a few hundred bytes; anything larger is not one, and a CLI
// must not be turned into a memory sink by whatever answered on that
// port.
const maxProblemBody = 1 << 20

// do performs one API call and decodes a successful body into out.
func (c *instanceClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// The API answers RFC 9457 problem documents and negotiates their
	// language from Accept-Language. The CLI asks for the host's, which
	// is the same rule it applies to its own taxonomy messages (cliLang).
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", cliLang())
	resp, err := c.http.Do(req)
	if err != nil {
		return taxonomy.New(taxonomy.CodeRegistryUnreachable, taxonomy.Params{
			"host": c.base,
		}).WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return c.problem(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProblemBody))
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// problem turns an error response into the error the CLI reports. A body
// that is not a problem document — a reverse proxy's HTML, a wrong port —
// degrades to an operational failure naming the status, never to a silent
// success.
func (c *instanceClient) problem(resp *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProblemBody))
	if err != nil {
		return fmt.Errorf("%s: reading the error response: %w", resp.Status, err)
	}
	var doc taxonomy.Problem
	if json.Unmarshal(raw, &doc) == nil && doc.Code != "" {
		if _, known := taxonomy.Lookup(doc.Code); known {
			return &remoteProblem{doc: doc, code: taxonomy.New(doc.Code, nil)}
		}
	}
	return fmt.Errorf("the instance answered %s to %s", resp.Status, resp.Request.URL.Path)
}

// remoteProblem is a refusal an INSTANCE decided, carried to the exit path
// without being re-invented here.
//
// A problem document carries rendered sentences, not the parameters that
// produced them, so this build cannot re-render it — and must not try:
// the instance may run a different version, and its message may name a
// configuration key this binary has never heard of. So the document's own
// three parts are printed verbatim, in the language the instance was
// asked for (Accept-Language, from the host locale), while the wrapped
// parameter-free taxonomy error carries the code to errors.As and the
// class to the exit-code mapping. The instance decides; the CLI reports
// and exits accordingly (FR-066).
type remoteProblem struct {
	doc  taxonomy.Problem
	code *taxonomy.Error
}

func (r *remoteProblem) Error() string { return string(r.doc.Code) + ": " + r.doc.Title }

// Unwrap exposes the taxonomy error, so errors.As and exitCodeFor treat a
// remote refusal exactly like a local one.
func (r *remoteProblem) Unwrap() error { return r.code }

// Text renders the document the way taxonomy.Text renders a local error,
// so an operator reads one form of refusal and not two.
func (r *remoteProblem) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n", r.doc.Code, r.doc.Title)
	fmt.Fprintf(&b, "  cause:  %s\n", r.doc.ProbableCause)
	fmt.Fprintf(&b, "  action: %s\n", r.doc.Action)
	fmt.Fprintf(&b, "  see:    %s", r.doc.Type)
	if r.doc.CorrelationID != "" {
		fmt.Fprintf(&b, "    correlation: %s", r.doc.CorrelationID)
	}
	b.WriteString("\n")
	return b.String()
}

// taskEnvelope is what /api/v1 wraps a single task in.
type taskEnvelope struct {
	Task *tasks.Task `json:"task"`
}

// createSync triggers a synchronization and returns the created task.
func (c *instanceClient) createSync(ctx context.Context, body any) (*tasks.Task, error) {
	var task tasks.Task
	if err := c.do(ctx, http.MethodPost, "/api/v1/sync", body, &task); err != nil {
		return nil, err
	}
	if task.ID == "" {
		return nil, errors.New("the instance accepted the trigger but named no task")
	}
	return &task, nil
}

// getTask reads one task.
func (c *instanceClient) getTask(ctx context.Context, id string) (*tasks.Task, error) {
	var env taskEnvelope
	if err := c.do(ctx, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(id), nil, &env); err != nil {
		return nil, err
	}
	if env.Task == nil {
		return nil, fmt.Errorf("the instance returned no task for %s", id)
	}
	return env.Task, nil
}

// pollInterval paces the wait. Two seconds: short enough that a
// ten-second import does not look hung, long enough that waiting on an
// hour-long synchronization is not a thousand requests.
const pollInterval = 2 * time.Second

// waitForTask blocks until the task reaches a terminal state (R-08).
//
// It polls rather than streams because /api/v1/tasks/{id} is the endpoint
// the contract has (FR-061) and because a poll survives what a long-lived
// connection does not: an instance restarting mid-run, a proxy with an
// idle timeout, a laptop's suspend. A task that was running when the
// instance went down comes back resumed (FR-029), and the next poll finds
// it.
func waitForTask(ctx context.Context, c *instanceClient, id string, timeout time.Duration) (*tasks.Task, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	every := c.poll
	if every <= 0 {
		every = pollInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		task, err := c.getTask(ctx, id)
		if err != nil {
			return nil, err
		}
		// Terminal is "no longer active": the queue owns that predicate
		// (tasks.Task.Active), and re-deriving it here from a list of
		// status literals is how a fourth status would one day make this
		// loop spin forever.
		if !task.Active() {
			return task, nil
		}
		// The deadline bounds the WAIT and never the requests: it is
		// checked between polls rather than carried on the request
		// context, so giving up produces the sentence below and not a
		// "context deadline exceeded" that says nothing about the task.
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			// The task is still running on the instance: say so rather
			// than let "tobby stopped" read as "the synchronization
			// stopped". The identifier is the handle to come back with.
			return task, fmt.Errorf("stopped waiting for task %s after %s: it is still %s on %s",
				id, timeout, task.Status, c.base)
		}
		select {
		case <-ctx.Done():
			return task, fmt.Errorf("stopped waiting for task %s: %w — it is still %s on %s",
				id, ctx.Err(), task.Status, c.base)
		case <-ticker.C:
		}
	}
}
