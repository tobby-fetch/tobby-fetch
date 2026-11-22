// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
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
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity/check"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/netx"
	"github.com/tobby-fetch/tobby-fetch/internal/runid"
	"github.com/tobby-fetch/tobby-fetch/internal/statedir"
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

Without --instance (and with ` + EnvInstanceURL + ` unset) it runs in this process, against the directory, and
works on a host where no instance is running. When an instance IS serving
the store, this form refuses (it cannot see the instance's content lock, so
a removal in progress would read as a defect) and tells you to use
--instance. A serving instance is recognised by the operating-system lock it
holds on its state directory, whatever its listener (plain HTTP, TLS,
self-signed included); one that crashed holds nothing and does not block
this form. With --instance: the verification then runs on that instance as a tracked task,
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
			if instanceRequested(cmd) {
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
	if url := servedAt(cfg); url != "" {
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

	// Opened for inspection: the registry library's upload purger, which
	// deletes week-old upload sessions, does not start (B-042).
	st, err := store.OpenWith(cmd.Context(), root, logger, store.Options{Inspect: true})
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
		trustCache = statedir.Path(cfg.State.Root, statedir.TrustCache)
	}
	trust, err := engine.LoadTrustLogging(logger, cfg.Trust, trustCache, egress)
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
// verification must leave alone. Its content (store, URL, PID) is for the
// messages; whether an instance is alive is the operating-system lock the
// instance holds on it for its lifetime (auth.StateLock), never a probe of
// its URL — a probe fails on a self-signed listener and on any address the
// host cannot reach, and a guard that fails open is no guard (B-093).
const servingFile = statedir.Serving

type servingMarker struct {
	StorageRoot string `json:"storage_root"`
	URL         string `json:"url"`
	PID         int    `json:"pid"`
}

// holdServingWait bounds how long a starting instance waits for a host
// command (`tobby user add`, `tobby user passwd`) that holds the state
// directory while it writes it.
const holdServingWait = 5 * time.Second

// servingHold is this instance's hold on its state directory. The zero
// value (no state directory) holds nothing and every method is a no-op.
type servingHold struct {
	lock     *auth.StateLock
	mu       sync.Mutex
	marker   servingMarker
	released bool
}

// holdServing takes cfg's state directory for this instance's lifetime
// (B-082, B-093). It is taken before the accounts are loaded: from then
// on the instance's in-memory copy is the truth, and the host commands
// that would write accounts.yaml behind its back refuse. A second
// instance on the same state directory is refused for the same reason —
// each would revert the other's account changes. The hold is released by
// the operating system if the process dies, so a crash leaves no stale
// guard behind.
func holdServing(cfg *config.Config) (*servingHold, error) {
	h := &servingHold{}
	if cfg.State.Root == "" {
		return h, nil
	}
	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil {
		root = cfg.Storage.Root
	}
	h.marker = servingMarker{StorageRoot: root, PID: os.Getpid()}
	deadline := time.Now().Add(holdServingWait)
	for {
		lock, lerr := auth.LockState(cfg.State.Root)
		if lerr == nil {
			h.lock = lock
			break
		}
		if !errors.Is(lerr, auth.ErrStateHeld) {
			return nil, lerr
		}
		if time.Now().After(deadline) {
			holder := ""
			if m, ok := readServingMarker(cfg.State.Root); ok && m.PID != 0 {
				holder = fmt.Sprintf(" (process %d per %s)", m.PID, servingFile)
			}
			return nil, taxonomy.New(taxonomy.CodeConfigInvalid, taxonomy.Params{
				"detail": "state.root " + cfg.State.Root + " is held by another process" + holder +
					": a running instance, or a `tobby user` command writing the accounts. Two instances must never share a state directory — " +
					"each keeps the accounts in memory and would revert the other's changes. Stop the other instance, or wait for the command to finish, " +
					"or give this instance a state directory of its own",
			}).WithCause(lerr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.writeLocked()
	return h, nil
}

// writeLocked writes the marker through the held lock. Callers hold h.mu
// or have not shared h yet.
func (h *servingHold) writeLocked() {
	if h.lock == nil || h.released {
		return
	}
	if raw, err := json.Marshal(h.marker); err == nil {
		_ = h.lock.Write(raw)
	}
}

// publish records the address the listener bound (the configured one may
// say port 0) as soon as it is known, so the refusals can name the URL
// to use instead. The scheme follows whether the listener serves TLS at
// all — a supplied pair, an adopted one or the self-signed fallback.
func (h *servingHold) publish(cfg *config.Config, addr func() string) {
	if h.lock == nil {
		return
	}
	scheme := "http://"
	if cfg.Server.TLS.Serves() {
		scheme = "https://"
	}
	go func() {
		for range 250 {
			if a := addr(); a != "" {
				h.mu.Lock()
				h.marker.URL = scheme + a
				h.writeLocked()
				h.mu.Unlock()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
}

// release empties the marker and gives the state directory back.
func (h *servingHold) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock == nil || h.released {
		return
	}
	h.released = true
	_ = h.lock.Release()
}

// readServingMarker reads the marker's content, which may be empty or
// stale: it describes the holder, it does not prove there is one.
func readServingMarker(stateRoot string) (servingMarker, bool) {
	var m servingMarker
	raw, err := os.ReadFile(statedir.Path(stateRoot, servingFile))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		return servingMarker{}, false
	}
	return m, true
}

// servingInstance reports whether a running instance (or a host command
// writing the accounts) holds cfg's state directory, with the marker it
// published. A state directory that does not exist is held by nobody, and
// is not created by asking.
func servingInstance(cfg *config.Config) (bool, servingMarker, error) {
	if cfg.State.Root == "" {
		return false, servingMarker{}, nil
	}
	if _, err := os.Stat(cfg.State.Root); errors.Is(err, os.ErrNotExist) {
		return false, servingMarker{}, nil
	}
	held, err := auth.StateHeld(cfg.State.Root)
	if err != nil || !held {
		return false, servingMarker{}, err
	}
	m, _ := readServingMarker(cfg.State.Root)
	return true, m, nil
}

// servedAt returns the URL of a running instance serving cfg's store, or
// "". An instance that holds the state directory for another store does
// not count; one whose address is not published yet answers a placeholder
// rather than "" — the guard must not fail open while it starts.
func servedAt(cfg *config.Config) string {
	held, m, err := servingInstance(cfg)
	if err != nil || !held {
		return ""
	}
	if m.StorageRoot != "" {
		root, aerr := filepath.Abs(cfg.Storage.Root)
		if aerr != nil || filepath.Clean(m.StorageRoot) != filepath.Clean(root) {
			return ""
		}
	}
	if m.URL == "" {
		return "<the instance's URL>"
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
