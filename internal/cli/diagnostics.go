// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/buildinfo"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/diag"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/medialog"
	"github.com/tobby-fetch/tobby-fetch/internal/metrics"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// `tobby diagnostics bundle` (FR-095, R-30): the one gesture that gathers
// what a support engineer needs into a single local, redacted archive.
//
// Two ways to take it, one archive layout. On the host, with nothing
// running, it is assembled from the configuration, the store and the state
// directory: the log window and the metrics live in the instance process
// and are omitted with their reason in the manifest. With --instance it is
// requested from a running instance, which adds them — the same request the
// administrator's screen makes, audited the same way.

func newDiagnosticsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diagnostics",
		Short: "Gather what support needs about this instance (R-30)",
	}
	cmd.AddCommand(newDiagnosticsBundleCmd())
	return cmd
}

// bundleReport is the machine form of `tobby diagnostics bundle`.
type bundleReport struct {
	// File is where the archive was written.
	File string `json:"file"`
	// Bytes is the archive size.
	Bytes int64 `json:"bytes"`
	// Source is "host" (assembled here) or "instance" (taken from a
	// running instance).
	Source  string          `json:"source"`
	Members []diag.Member   `json:"members"`
	Omitted []diag.Omission `json:"omitted,omitempty"`
}

func newDiagnosticsBundleCmd() *cobra.Command {
	flags := &commonFlags{}
	inst := &instanceFlags{}
	report := newReportFlag(outputText, outputJSON)
	var out string
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write one redacted diagnostic archive (version, configuration, logs, task reports, metrics, integrity, state inventory)",
		Long: `Write a single local archive an operator can hand to support (FR-095).

It holds the version and build, the effective configuration redacted as
'tobby config dump' redacts it, the recent log records, the reports of the
most recent tasks, a metrics snapshot, the last on-demand integrity report,
the state-directory inventory (names, sizes and modes — never contents) and a
MANIFEST.json listing every file with its digest.

Redaction fails closed: before anything is written, every file of the
archive is searched for the secret values this instance knows (proxy
password, client secrets, private keys, the clear form of any credential it
read). A match aborts the command with TBY-DIA-002 naming the file and
writes nothing. Tobby sends the archive nowhere (NFR-019): you choose whom
to give it to.

Without --instance (and with ` + EnvInstanceURL + ` unset) the archive is
assembled on this host from the configuration, the store and the state
directory; the log window and the metrics belong to a running process and
are listed as omitted. With --instance, or with ` + EnvInstanceURL + ` set, the
archive is requested from the running instance (role admin) and includes them.

Exit codes: 0 written, 3 refused by the redaction check
or a role, 1 failed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := report.validate(cmd); err != nil {
				return err
			}
			cfg, err := flags.load(cmd)
			if err != nil {
				return err
			}
			if instanceRequested(cmd) {
				return runBundleRemote(cmd, &cfg, inst, report, out)
			}
			return runBundleLocal(cmd, &cfg, report, out)
		},
	}
	flags.register(cmd)
	report.register(cmd)
	inst.register(cmd)
	cmd.Flags().StringVarP(&out, "output-file", "o", "",
		"archive to write (default tobby-diagnostics-<UTC time>.tar.gz in the current directory); an existing file is never overwritten")
	return cmd
}

// runBundleLocal assembles the bundle on the host.
func runBundleLocal(cmd *cobra.Command, cfg *config.Config, report *reportFlag, out string) error {
	level, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return err
	}
	logger := logging.New(cmd.ErrOrStderr(), level)
	svc := &diag.Service{
		Secrets: diagSecrets(cfg),
		Sources: hostSources(cfg),
		Tasks:   cfg.Diagnostics.Tasks,
		Logger:  logger,
	}
	b, err := svc.Generate(cmd.Context(), audit.ActorLocal, audit.OriginLocal)
	if err != nil {
		return err
	}
	if out == "" {
		out = diag.ArchiveName(b.Manifest.Generated)
	}
	n, err := writeNewFile(out, b.WriteTo)
	if err != nil {
		return taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
	return writeBundleReport(cmd, report, &bundleReport{
		File: out, Bytes: n, Source: "host", Members: b.Manifest.Members, Omitted: b.Manifest.Omitted,
	})
}

// runBundleRemote takes the bundle from a running instance.
func runBundleRemote(cmd *cobra.Command, cfg *config.Config, inst *instanceFlags, report *reportFlag, out string) error {
	client, err := newInstanceClient(cmd, cfg, inst)
	if err != nil {
		return err
	}
	defer client.close()
	if out == "" {
		out = diag.ArchiveName(time.Now())
	}
	// The answer is bounded — a bundle is at most every member at its own
	// bound — and checked before it is kept: an answer that is not a
	// diagnostic archive (a proxy's page, a truncated stream, another
	// service at that address) is removed rather than left looking like one.
	limit := diag.MaxArchive(cfg.Diagnostics.Tasks)
	n, err := writeNewFile(out, func(w io.Writer) (int64, error) {
		return client.download(cmd.Context(), http.MethodPost, "/api/v1/diagnostics/bundle", w, limit)
	})
	if err != nil {
		var te *taxonomy.Error
		var rp *remoteProblem
		if errors.As(err, &te) || errors.As(err, &rp) {
			return err
		}
		return taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
	man, err := verifyBundleArchive(out)
	if err != nil {
		_ = os.Remove(out)
		return taxonomy.New(taxonomy.CodeDiagnosticsFailed, taxonomy.Params{
			"detail": fmt.Sprintf("the answer of %s is not a complete diagnostic archive (%v); nothing was kept — check that --instance names a Tobby instance and retry", client.base, err),
		}).WithCause(err)
	}
	return writeBundleReport(cmd, report, &bundleReport{
		File: out, Bytes: n, Source: "instance", Members: man.Members, Omitted: man.Omitted,
	})
}

func writeBundleReport(cmd *cobra.Command, report *reportFlag, r *bundleReport) error {
	if report.json() {
		return writeJSON(cmd, r)
	}
	w := report.human(cmd)
	_, _ = fmt.Fprintf(w, "diagnostic bundle written to %s (%d bytes, %d files, from the %s)\n", r.File, r.Bytes, len(r.Members), r.Source)
	for _, o := range r.Omitted {
		_, _ = fmt.Fprintf(w, "  omitted  %s — %s\n", o.Name, o.Reason)
	}
	_, _ = fmt.Fprintln(w, "  redacted before writing; Tobby sends it nowhere — you choose whom to give it to (NFR-019)")
	return nil
}

// writeNewFile creates path exclusively (an existing file is never
// replaced), owner-only, and removes it again if writing fails — a partial
// archive is worse than none.
func writeNewFile(path string, write func(io.Writer) (int64, error)) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the destination the operator named
	if err != nil {
		return 0, err
	}
	n, werr := write(f)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return 0, errors.Join(werr, cerr)
	}
	return n, nil
}

// verifyBundleArchive reads an archive written from an instance's answer
// back whole: a gzip-compressed tar whose first file is MANIFEST.json, a
// valid manifest, and every member it lists present once with the size
// and digest it states — and nothing else.
func verifyBundleArchive(path string) (*diag.Manifest, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the file just written
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != diag.MemberManifest {
		return nil, fmt.Errorf("the archive does not begin with %s", diag.MemberManifest)
	}
	var m diag.Manifest
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", diag.MemberManifest, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	want := map[string]diag.Member{}
	for _, mem := range m.Members {
		want[mem.Name] = mem
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading the archive: %w", err)
		}
		mem, ok := want[h.Name]
		if !ok {
			return nil, fmt.Errorf("%s is not listed in the manifest", h.Name)
		}
		delete(want, h.Name)
		sum := sha256.New()
		n, err := io.Copy(sum, io.LimitReader(tr, mem.Size+1))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", h.Name, err)
		}
		if n != mem.Size || "sha256:"+hex.EncodeToString(sum.Sum(nil)) != mem.Digest {
			return nil, fmt.Errorf("%s does not match its manifest entry", h.Name)
		}
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(gz, 1<<20)); err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}
	for name := range want {
		return nil, fmt.Errorf("%s is listed in the manifest but missing", name)
	}
	return &m, nil
}

// download performs one API call and copies a successful body to w, at
// most limit bytes: a longer answer is an error, not a truncated copy.
func (c *instanceClient) download(ctx context.Context, method, path string, w io.Writer, limit int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, http.NoBody)
	if err != nil {
		return 0, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept-Language", cliLang())
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, taxonomy.New(taxonomy.CodeRegistryUnreachable, taxonomy.Params{"host": c.base}).WithCause(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return 0, c.problem(resp)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("the answer exceeds %d bytes, more than any diagnostic archive", limit)
	}
	return n, err
}

// ---- Sources -------------------------------------------------------------

// hostSources are the collectors of a bundle taken on the host.
func hostSources(cfg *config.Config) *diag.Sources {
	const noProcess = "no running instance was asked: this lives in the process; use --instance to take the bundle from a running one"
	return &diag.Sources{
		Version:   versionSource(cfg, "host"),
		Config:    configSource(cfg),
		Logs:      func() ([]byte, error) { return nil, diag.ErrOmitted(noProcess) },
		MediaLog:  mediaLogSource(cfg),
		Metrics:   func() ([]byte, error) { return nil, diag.ErrOmitted(noProcess) },
		Integrity: hostIntegritySource(cfg),
		State:     stateSource(cfg),
		Tasks:     hostTasks(cfg),
	}
}

func versionSource(cfg *config.Config, origin string) diag.Source {
	return func() ([]byte, error) {
		doc := map[string]string{
			"version": buildinfo.Version(), "commit": buildinfo.Commit(), "built": buildinfo.Date(),
			"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
			"mode": string(cfg.Mode), "zone": cfg.Zone, "source": origin,
		}
		return marshalDoc(doc)
	}
}

func configSource(cfg *config.Config) diag.Source {
	return func() ([]byte, error) {
		out, err := cfg.Dump()
		return []byte(out), err
	}
}

func stateSource(cfg *config.Config) diag.Source {
	return func() ([]byte, error) {
		if cfg.State.Root == "" {
			return nil, diag.ErrOmitted("this instance runs without a state directory")
		}
		inv, err := statedir.Inspect(cfg.State.Root)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, diag.ErrOmitted("the state directory does not exist yet: no instance has started on this configuration")
		}
		if err != nil {
			return nil, err
		}
		return marshalDoc(inv)
	}
}

func mediaLogSource(cfg *config.Config) diag.Source {
	return func() ([]byte, error) {
		if cfg.Mode != config.ModeMirror || cfg.Logging.Media.Disabled || cfg.Storage.Root == "" {
			return nil, diag.ErrOmitted("only a mirror instance keeps an operation log on the medium (FR-053)")
		}
		raw, err := medialog.Tail(cfg.Storage.Root, medialog.Options{Path: cfg.Logging.Media.File}, cfg.Diagnostics.LogRecords)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, diag.ErrOmitted("the medium holds no operation log yet")
		}
		return raw, err
	}
}

func marshalDoc(v any) ([]byte, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// taskFiles renders the n most recent tasks (newest first in list) as the
// tasks/ members.
func taskFiles(list []*tasks.Task, n int) ([]diag.File, error) {
	var out []diag.File
	for _, t := range list {
		if len(out) == n {
			break
		}
		raw, err := marshalDoc(t)
		if err != nil {
			return nil, err
		}
		out = append(out, diag.File{Name: diag.MemberTasks + safeName(t.ID) + ".json", Data: raw})
	}
	return out, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// safeName keeps an identifier usable as a file name inside the archive.
func safeName(id string) string { return strings.TrimLeft(unsafeName.ReplaceAllString(id, "_"), ".") }

func hostTasks(cfg *config.Config) func(int) ([]diag.File, error) {
	return func(n int) ([]diag.File, error) {
		if cfg.Storage.Root == "" {
			return nil, nil
		}
		list, err := tasks.Load(cfg.Storage.Root)
		if err != nil {
			return nil, err
		}
		return taskFiles(list, n)
	}
}

func hostIntegritySource(cfg *config.Config) diag.Source {
	return func() ([]byte, error) {
		list, err := tasks.Load(cfg.Storage.Root)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			if t.Type == tasks.TypeStoreVerify && t.Verification != nil {
				return marshalDoc(t.Verification)
			}
		}
		return nil, diag.ErrOmitted("no on-demand verification has been run on this store (FR-057)")
	}
}

// metricsSnapshot renders the registry as the OpenMetrics text a scrape
// would return.
func metricsSnapshot(reg *metrics.Registry) ([]byte, error) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("gathering the metrics: HTTP %d", rec.Code)
	}
	return rec.Body.Bytes(), nil
}

// ---- Secrets -------------------------------------------------------------

var secretEnvName = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASSPHRASE|SECRET|TOKEN|KEY|CREDENTIAL)`)
var pathEnvName = regexp.MustCompile(`(?i)(_FILES?|_PATH|_DIR|_ROOT)$`)

// diagSecrets returns the builder of the set of secret values this
// instance knows: every Secret of the configuration, the content of every
// secret file it references, the secret entries of the state directory,
// and the value of every environment variable that looks like a
// credential (the instance may have read it, and the CLI did: TOBBY_API_TOKEN).
func diagSecrets(cfg *config.Config) func() (*diag.Secrets, error) {
	return func() (*diag.Secrets, error) {
		s := diag.NewSecrets()
		s.AddValues("configuration", cfg)
		files := map[string]string{
			"registries.credentialsFile": cfg.Registries.CredentialsFile,
			"server.tls.keyFile":         cfg.Server.TLS.KeyFile,
			"auth.oidc.clientSecretFile": cfg.Auth.OIDC.ClientSecretFile,
			"auth.saml.keyFile":          cfg.Auth.SAML.KeyFile,
		}
		if cfg.State.Root != "" {
			// The generated and adopted keys of the state directory.
			_ = filepath.WalkDir(statedir.Path(cfg.State.Root, statedir.TLS), func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && statedir.SecretFileInDir(d.Name()) {
					files["state "+statedir.TLS+"/"+d.Name()] = p
				}
				return nil
			})
		}
		for origin, path := range files {
			if err := s.AddFile(origin, path); err != nil {
				return nil, err
			}
		}
		for _, kv := range os.Environ() {
			name, value, ok := strings.Cut(kv, "=")
			// Eight bytes at least: an environment variable that merely has
			// "KEY" in its name and a short value ("true", "1234") would
			// otherwise make every document that contains the word abort
			// the bundle.
			if ok && len(strings.TrimSpace(value)) >= 8 && secretEnvName.MatchString(name) && !pathEnvName.MatchString(name) {
				s.Add("environment "+name, value)
			}
		}
		return s, nil
	}
}

// instanceSources are the collectors of a bundle taken from the running
// instance; the pieces only the process holds are passed in.
type instanceSources struct {
	cfg       *config.Config
	window    *diag.Window
	reg       *metrics.Registry
	queue     *tasks.Queue
	integrity func() (*tasks.Task, bool)
}

func (i *instanceSources) sources() *diag.Sources {
	return &diag.Sources{
		Version:  versionSource(i.cfg, "instance"),
		Config:   configSource(i.cfg),
		Logs:     func() ([]byte, error) { return i.window.Records(), nil },
		MediaLog: mediaLogSource(i.cfg),
		Metrics:  func() ([]byte, error) { return metricsSnapshot(i.reg) },
		Integrity: func() ([]byte, error) {
			if i.integrity != nil {
				if t, ok := i.integrity(); ok && t.Verification != nil {
					return marshalDoc(t.Verification)
				}
			}
			return nil, diag.ErrOmitted("no on-demand verification has been run on this instance (FR-057)")
		},
		State: stateSource(i.cfg),
		Tasks: func(n int) ([]diag.File, error) { return taskFiles(i.queue.List("", "", ""), n) },
	}
}
