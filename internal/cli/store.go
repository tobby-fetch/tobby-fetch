// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/runid"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// `tobby store` groups the commands that act on a store directory as such,
// whatever mode produced it.
func newStoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Inspect a store (FR-057)",
		Long: `Operate on a store directory.

The commands here read a store; none of them changes one.`,
	}
	cmd.AddCommand(newStoreVerifyCmd())
	return cmd
}

func newStoreVerifyCmd() *cobra.Command {
	var (
		common   commonFlags
		report   = newReportFlag(outputText, outputJSON)
		instance instanceFlags
		wait     waitFlag
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify the integrity of a whole store",
		Long: `Verify a whole store on demand (FR-057), in either mode — an instance's
store and a transportable one are verified the same way. Four checks:

  blobs       every blob is re-hashed against the digest it is stored under;
  manifests   every manifest reachable from a recorded recipe is complete —
              its config, its layers, its signature artifacts, its index
              children (a platform-filtered index stays complete);
  signatures  every recorded recipe signature verifies against the trust
              roots configured now (scopes applied); the reduced-assurance
              marker of a recipe admitted unsigned by a scope is re-reported;
  orphans     content that no recorded recipe and no protected root
              (unit imports, seeded content, the vulnerability database)
              reaches is listed.

The verification only reads. It repairs nothing, deletes nothing and
writes nothing into the store — which is byte-identical afterwards — and
the report is timestamped: start and end, run identifier, verdict, and
every defect as a taxonomy code naming the file or object concerned.
Orphaned content is listed as a note and never makes the verdict "defects".

Without --instance it runs in this process, against the directory, and
works on a host where no instance is running. When an instance IS serving
the store, this form refuses (it cannot see the instance's content lock, so
a removal in progress would read as a defect) and tells you to use
--instance: the verification then runs on that instance as a tracked task,
under its content lock, on a lane of its own — it never holds up a
synchronization — and --wait follows it to its end and exits on its outcome.
Trust roots come from the configuration (trust.roots, trust.scopes), never
from the store.

Exit codes: 0 the store is intact, 4 a defect was found, 1 cancelled or failed (a blob that does
not match its digest, an incomplete or unreadable manifest, a signature
that no longer verifies).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A mistyped --output is a usage error whatever else is
			// missing, as everywhere else.
			if err := report.validate(cmd); err != nil {
				return err
			}
			if cmd.Flags().Changed("instance") {
				return runStoreVerifyRemote(cmd, &common, &instance, &wait, report)
			}
			if cmd.Flags().Changed("wait") || cmd.Flags().Changed("wait-timeout") {
				return &usageError{
					err:  fmt.Errorf("--wait needs --instance: without one the verification runs in this process, which already blocks"),
					hint: "see '" + cmd.CommandPath() + " --help'",
				}
			}
			cfg, err := common.loadFor(cmd, config.ScopeStorage)
			if err != nil {
				return err
			}
			return runStoreVerify(cmd, &cfg, report)
		},
	}
	common.register(cmd)
	report.register(cmd)
	// --instance hands the work to a RUNNING instance, as `tobby sync`
	// does: the verification is then a tracked task on it, under its own
	// content lock, and the command owes --wait (R-08).
	instance.register(cmd)
	wait.register(cmd)
	return cmd
}

// runStoreVerify opens the store, verifies it, prints the report and turns
// the verdict into the exit class.
func runStoreVerify(cmd *cobra.Command, cfg *config.Config, report *reportFlag) error {
	root := cfg.Storage.Root
	if url := servedAt(cmd.Context(), cfg); url != "" {
		return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
			"detail": root + " is being served by a running instance (" + url + "): verifying it from a second process " +
				"cannot see that instance's content lock, so a removal in progress would read as a defect. " +
				"Run `tobby store verify --instance " + url + " --wait` instead, or stop the instance first",
		})
	}
	// Opening a store stamps a directory that lacks a format version or a
	// medium identity — a write, and this command promises none. A
	// directory that is not already a Tobby store is refused before
	// anything is opened.
	for _, f := range []string{"format.json", "media-id.json"} {
		if _, err := os.Stat(filepath.Join(root, "meta", f)); err != nil {
			return taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
				"detail": root + " is not a Tobby store (no meta/" + f + "): nothing was opened or written. " +
					"Point --storage-root at the store an instance serves or a mirror synchronization produced",
			}).WithCause(err)
		}
	}

	level, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return err
	}
	runID := runid.New()
	logger := logging.WithRunID(logging.New(cmd.ErrOrStderr(), level), runID)

	st, err := store.Open(cmd.Context(), root, logger)
	if err != nil {
		return err
	}
	defer st.Close() //nolint:errcheck // read side

	// THIS instance's trust roots, resolved now (RECIPE-SPEC §12.3) —
	// never read from the store, which carries none as far as this code
	// is concerned.
	egress, err := netx.New(&cfg.Network)
	if err != nil {
		return err
	}
	defer egress.CloseIdleConnections()
	trustCache := ""
	if cfg.State.Root != "" {
		trustCache = filepath.Join(cfg.State.Root, "trust-cache")
	}
	trust, err := engine.LoadTrust(cfg.Trust, trustCache, egress)
	if err != nil {
		return err
	}

	rep, err := check.Verify(cmd.Context(), st, check.Options{
		Trust: engine.MediaTrust{Policy: trust}, RunID: runID, Logger: logger,
	})
	if err != nil {
		return err
	}
	outcome := audit.OutcomeSuccess
	if rep.Verdict == integrity.VerdictDefects {
		outcome = audit.OutcomeFailure
	}
	audit.Log(cmd.Context(), logger, &audit.Event{
		Actor: audit.ActorLocal, Action: audit.ActionIntegrityVerify,
		Target: root, Outcome: outcome, Origin: audit.OriginLocal,
	})

	if err := writeIntegrityReport(cmd, report, rep); err != nil {
		return err
	}
	if p := rep.Principal(); p != nil {
		return p
	}
	return nil
}

// writeIntegrityReport renders the report: the machine document alone on
// stdout under --output json, the narration otherwise.
func writeIntegrityReport(cmd *cobra.Command, report *reportFlag, rep *integrity.Report) error {
	if report.json() {
		return writeJSON(cmd, rep)
	}
	out := report.human(cmd)
	mode := "instance store"
	if rep.Store.Transportable {
		mode = "transportable store"
	}
	_, _ = fmt.Fprintf(out, "%s %s, verified %s → %s (run %s)\n", mode, rep.Store.Root,
		rep.StartedAt.Format(time.RFC3339), rep.FinishedAt.Format(time.RFC3339), rep.RunID)
	_, _ = fmt.Fprintf(out, "verdict: %s — %d blobs (%d bytes), %d manifests, %d recipes checked\n",
		rep.Verdict, rep.Checked.Blobs, rep.Checked.Bytes, rep.Checked.Manifests, rep.Checked.Recipes)
	for i := range rep.Recipes {
		r := &rep.Recipes[i]
		complete := "complete"
		if !r.Complete {
			complete = "INCOMPLETE"
		}
		line := fmt.Sprintf("  recipe %s@%s  %s, signature %s", r.Name, r.Version, complete, r.Signature)
		if r.KeyFingerprint != "" {
			line += " (key " + r.KeyFingerprint + ")"
		}
		if r.Assurance.Reduced() {
			line += " [reduced assurance: scope " + r.Assurance.Scope + "]"
		}
		_, _ = fmt.Fprintln(out, line)
	}
	for i := range rep.Defects {
		d := &rep.Defects[i]
		_, _ = fmt.Fprintf(out, "  DEFECT  %s %s\n", d.Code, d.Path)
	}
	if rep.DefectsOmitted > 0 {
		_, _ = fmt.Fprintf(out, "  … and %d more defects (not listed)\n", rep.DefectsOmitted)
	}
	for i := range rep.Findings {
		f := &rep.Findings[i]
		_, _ = fmt.Fprintf(out, "  orphaned  %s %s (reachable from no recipe nor protected root; never deleted by this command)\n", f.Code, f.Path)
	}
	if rep.FindingsOmitted > 0 {
		_, _ = fmt.Fprintf(out, "  … and %d more orphaned items (not listed)\n", rep.FindingsOmitted)
	}
	return nil
}

// servingFile is the marker a running instance keeps in its STATE directory
// — never in the store, which is transportable and whose every byte a
// verification must leave alone.
const servingFile = "serving.json"

type servingMarker struct {
	StorageRoot string `json:"storage_root"`
	URL         string `json:"url"`
	PID         int    `json:"pid"`
}

// markServing records that this instance serves cfg's store, and returns
// the function that removes the record. addr reports the address the
// listener actually bound (the configured one may say port 0); the marker
// is written as soon as it is known. Without a state directory there is
// nowhere to keep it, and `store verify` cannot tell.
func markServing(cfg *config.Config, addr func() string) func() {
	if cfg.State.Root == "" {
		return func() {}
	}
	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil {
		return func() {}
	}
	path := filepath.Join(cfg.State.Root, servingFile)
	var mu sync.Mutex
	removed := false
	go func() {
		for range 250 {
			if a := addr(); a != "" {
				url := "http://" + a
				if cfg.Server.TLS.CertFile != "" {
					url = "https://" + a
				}
				raw, merr := json.Marshal(servingMarker{StorageRoot: root, URL: url, PID: os.Getpid()})
				mu.Lock()
				if merr == nil && !removed {
					_ = os.WriteFile(path, raw, 0o600)
				}
				mu.Unlock()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return func() {
		mu.Lock()
		removed = true
		mu.Unlock()
		_ = os.Remove(path)
	}
}

// servedAt returns the URL of a running instance serving cfg's store, or
// "". The marker alone is not believed — a crashed instance leaves one
// behind — the instance has to answer its liveness probe.
func servedAt(ctx context.Context, cfg *config.Config) string {
	if cfg.State.Root == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(cfg.State.Root, servingFile))
	if err != nil {
		return ""
	}
	var m servingMarker
	if json.Unmarshal(raw, &m) != nil || m.URL == "" {
		return ""
	}
	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil || filepath.Clean(m.StorageRoot) != filepath.Clean(root) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL+"/healthz", http.NoBody)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return m.URL
}

// storeVerifyTrigger is the machine form of `tobby store verify --instance`
// without --wait: the task the instance created, which is all there is to
// report yet (R-08). With --wait the report itself is printed instead.
type storeVerifyTrigger struct {
	Instance string      `json:"instance"`
	Waited   bool        `json:"waited"`
	Task     *tasks.Task `json:"task"`
}

// runStoreVerifyRemote asks a running instance to verify its store.
func runStoreVerifyRemote(cmd *cobra.Command, common *commonFlags, instance *instanceFlags, wait *waitFlag, report *reportFlag) error {
	timeout, err := wait.duration(cmd)
	if err != nil {
		return err
	}
	if !wait.wait && cmd.Flags().Changed("wait-timeout") {
		return &usageError{
			err:  fmt.Errorf("--wait-timeout without --wait: there is nothing to time out"),
			hint: "see '" + cmd.CommandPath() + " --help'",
		}
	}
	cfg, err := common.loadFor(cmd, config.ScopeNetwork)
	if err != nil {
		return err
	}
	client, err := newInstanceClient(cmd, &cfg, instance)
	if err != nil {
		return err
	}
	defer client.close()

	var env taskEnvelope
	if err := client.do(cmd.Context(), http.MethodPost, "/api/v1/store/verify", nil, &env); err != nil {
		return err
	}
	task := env.Task
	if task == nil || task.ID == "" {
		return errors.New("the instance accepted the request but named no task")
	}
	if !wait.wait {
		if report.json() {
			return writeJSON(cmd, &storeVerifyTrigger{Instance: client.base, Task: task})
		}
		_, _ = fmt.Fprintf(report.human(cmd), "verification task %s created on %s\n", task.ID, client.base)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "task %s created on %s, waiting for it to finish\n", task.ID, client.base)
	settled, waitErr := waitForTask(cmd.Context(), client, task.ID, timeout)
	if settled != nil {
		task = settled
	}
	if waitErr != nil {
		return waitErr
	}
	if task.Verification != nil && !task.Verification.Running() {
		if err := writeIntegrityReport(cmd, report, task.Verification); err != nil {
			return err
		}
	} else if report.json() {
		if err := writeJSON(cmd, &storeVerifyTrigger{Instance: client.base, Waited: true, Task: task}); err != nil {
			return err
		}
	}
	return syncOutcome(task)
}
