// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/fileserve"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// newFileSetCmd wires `tobby fileset` (FR-048): the operator-side answer
// to "I have a few files to serve in the isolated zone and I am not
// standing up an infrastructure for that". The answer is not an upload
// surface — SRS §5.2 keeps that door shut — it is a FileSet: the files
// become an OCI image, pinned by digest, imported through the store, and
// served read-only by FR-047 once explicitly enabled.
func newFileSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fileset",
		Short: "Package local files as a FileSet in the store (FR-048)",
	}
	cmd.AddCommand(newFileSetPackCmd())
	return cmd
}

// packOutput selects the report format (R-08: a stable machine contract
// on every command that reports anything).
const (
	outputText = "text"
	outputJSON = "json"
)

func newFileSetPackCmd() *cobra.Command {
	flags := &commonFlags{}
	var output string
	cmd := &cobra.Command{
		Use:   "pack <directory> <name>:<version>",
		Short: "Package a local directory as a FileSet imported in the store",
		Long: `Package a local directory as a FileSet — a standard OCI image whose layer is
the directory's file tree — and import it into this host's store, pinned by
its digest.

This is the sanctioned way to serve a handful of local files in an isolated
zone: no upload endpoint is opened, the content is addressed by digest,
inventoried, scannable and removable like everything else in the store.

Packing is reproducible: the same directory always produces the same digest,
so packing twice transfers nothing the second time. Timestamps and ownership
are deliberately not carried — only the file tree, its permissions and its
symbolic links are.

The directory is refused, naming the entry, when it holds something a FileSet
extraction would have to refuse anyway: a symbolic link leaving the directory,
a setuid bit, a device node or a socket, or a name reserved by the image layer
format.

A packed FileSet is UNSIGNED and recorded as a manual import of local origin —
Tobby holds no signing key (ADR-0007) — and listings say so.

Serving it is a separate, explicit step (FR-047): add it to the configuration
file and restart the instance. The command prints the block to add.

  tobby fileset pack ./apt-repo debs:1.0.0`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != outputText && output != outputJSON {
				return &usageError{
					err:  fmt.Errorf("unknown --output %q: use %q or %q", output, outputText, outputJSON),
					hint: "see '" + cmd.CommandPath() + " --help'",
				}
			}
			name, version, err := splitPackTarget(args[1])
			if err != nil {
				return fileserve.PackProblem(err)
			}
			// Store-facing scope: the store root and the relocation base
			// prefix, no mode — packing a directory into the local store
			// says nothing about how this host serves (R-34, B-006).
			cfg, err := flags.loadFor(cmd, config.ScopeStorage)
			if err != nil {
				return err
			}
			return runFileSetPack(cmd, &cfg, args[0], name, version, output)
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&output, "output", outputText,
		`report format: "text" or "json" (the JSON document is the only thing on stdout)`)
	return cmd
}

func runFileSetPack(cmd *cobra.Command, cfg *config.Config, source, name, version, output string) error {
	// The command writes into the store directly (ADR-0005), so it works
	// on a host with no instance running — which is the air-gapped case
	// this feature exists for. Against a running instance the writes are
	// the same class as a standard client pushing through /v2/: outside
	// the process-level FR-044 lock, covered by the sweep grace period,
	// exactly as store.go documents for that path.
	st, err := store.Open(cmd.Context(), cfg.Storage.Root, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	defer st.Close() //nolint:errcheck // read/write handles are flushed at each operation

	// The CLI is unconfined on purpose: whoever runs it already holds the
	// host's filesystem rights. files.packRoots bounds the API and the UI
	// (FR-075), which are reached over the network.
	packer := fileserve.NewPacker(st, cfg.Storage.BasePrefix, slog.New(slog.DiscardHandler))
	res, err := packer.Pack(cmd.Context(), fileserve.PackRequest{Source: source, Name: name, Version: version})
	if err != nil {
		auditPack(cmd.Context(), cmd.ErrOrStderr(), fileserve.PackReference(name)+":"+version, audit.OutcomeFailure)
		return fileserve.PackProblem(err)
	}
	auditPack(cmd.Context(), cmd.ErrOrStderr(), res.Reference+":"+res.Version, audit.OutcomeSuccess)

	if output == outputJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	reportPack(cmd.ErrOrStderr(), res)
	// The digest alone on stdout: the command composes into a script
	// (B-010 — machine output never goes to stderr).
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), res.Digest)
	return nil
}

// reportPack writes the human half of the report: what landed, the fact
// that it is unsigned, and the configuration block that turns it into a
// served FileSet — FR-047 enablement is explicit and stays explicit, so
// the command hands over the exact text rather than editing the
// operator's configuration behind their back.
func reportPack(w io.Writer, res *fileserve.PackResult) {
	_, _ = fmt.Fprintf(w, "packed %s:%s — files: %d, directories: %d, symbolic links: %d, bytes: %d\n",
		res.Reference, res.Version, res.Files, res.Directories, res.Symlinks, res.Bytes)
	_, _ = fmt.Fprintf(w, "manual import: this FileSet is unsigned and of local origin — Tobby holds no signing key\n")
	_, _ = fmt.Fprintf(w, "serve it by adding this to the configuration file, then restarting the instance:\n")
	_, _ = fmt.Fprintf(w, "  files:\n    filesets:\n      - name: %s\n        ref: %s\n        version: %s\n",
		res.Name, res.Reference, res.Version)
}

// auditPack records the packing (FR-094). Unlike `tobby user`, the record
// goes to stderr: this command's stdout is its report — the JSON document
// under --output json, the digest otherwise (R-08, B-010) — and a log
// record must not land in the middle of it.
func auditPack(ctx context.Context, w io.Writer, target, outcome string) {
	audit.Log(ctx, logging.New(w, slog.LevelInfo), &audit.Event{
		Actor:   audit.ActorLocal,
		Action:  audit.ActionFileSetPack,
		Target:  target,
		Outcome: outcome,
		Origin:  audit.OriginLocal,
	})
}

// splitPackTarget parses the "<name>:<version>" argument. Both halves are
// required: a FileSet with no version has no tag to serve, and Tobby
// never invents a ":latest" (B-009).
func splitPackTarget(target string) (name, version string, err error) {
	name, version, found := strings.Cut(target, ":")
	if !found {
		return "", "", &fileserve.PackRejection{
			Reason: "the target must be <name>:<version>, for example debs:1.0.0",
		}
	}
	return name, version, nil
}
