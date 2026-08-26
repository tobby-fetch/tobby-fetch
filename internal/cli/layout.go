// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/config"
	"github.com/tobby-fetch/tobby-fetch/internal/interop"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// `tobby export` and `tobby import` — the OCI image layout half of the
// command-line contract (FR-066, ADR-0006).
//
// They work on the storage directory, not through a running instance:
// that is what makes them usable on the destination side of a physical
// transfer, where the medium is plugged into a workstation and there may
// be nothing serving at all. The consequence is stated in the help text
// rather than hidden — an import writing into the store of a live
// instance is two writers on one directory, and the API endpoints exist
// precisely for that case.

// formatOCILayout is the only interoperability format there is today.
// The flag exists all the same, because ADR-0006 documents the command
// line with it and because a second format must be additive rather than
// a breaking change to a command automation already calls.
const formatOCILayout = "oci-layout"

func newExportCmd() *cobra.Command {
	flags := &commonFlags{}
	report := newReportFlag(outputText, outputJSON)
	var (
		format     string
		directory  bool
		overwrite  bool
		dryRun     bool
		recipes    []string
		repository []string
	)
	cmd := &cobra.Command{
		Use:   "export <path>",
		Short: "Export the store to a standard OCI image layout (FR-051)",
		Long: `Write the local store — or a selection of it — as a standard OCI image
layout, readable by skopeo, oras and crane.

This is the interoperability exit ramp, and it is deliberate: the content
belongs to whoever stored it, and it must be recoverable without Tobby.
The layout is written as a single uncompressed tar by default (one file
crosses a physical gap more reliably than a tree), or as a directory with
--directory.

Selection: without --recipe or --repository the whole store is exported.
A recipe selection carries its ingredients, the recipe artifact, and the
cosign signature artifacts of both, in either of the layouts cosign
publishes — signatures travel with the content they attest.

Addressing an entry afterwards: each index entry is annotated with its
full repository and tag, which is what skopeo matches
("skopeo copy oci:<path>:<repository>:<tag> ..."). oras splits a layout
reference on its last colon and therefore addresses entries by digest
("oras manifest fetch --oci-layout <path>@<digest>").

The destination is a positional argument, symmetric with "tobby import
<path>": --output names the report FORMAT on every Tobby command (R-08),
and one flag cannot mean two things.

Run it against a stopped instance, or use POST /api/v1/oci-layout/export
on a running one: two processes writing one storage directory is one
process too many.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			if err := report.validate(cmd); err != nil {
				return err
			}
			svc, closeStore, err := openInteropService(cmd, flags)
			if err != nil {
				return err
			}
			defer closeStore()

			req := &interop.ExportRequest{
				Selector:  interop.Selector{Recipes: recipes, Repositories: repository},
				Output:    args[0],
				Format:    ocilayout.FormatTar,
				Overwrite: overwrite,
			}
			if directory {
				req.Format = ocilayout.FormatDirectory
			}
			if dryRun {
				return runExportPlan(cmd, svc, req, report)
			}
			return runExport(cmd, svc, req, report)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&format, "format", formatOCILayout, "interoperability format (only "+formatOCILayout+" today)")
	fs.BoolVar(&directory, "directory", false, "write the layout as a directory instead of a single tar")
	fs.BoolVar(&overwrite, "overwrite", false, "replace the destination if it already exists")
	fs.BoolVar(&dryRun, "dry-run", false, "report what the export would contain and how big it would be, writing nothing")
	fs.StringArrayVar(&recipes, "recipe", nil, `export one recipe and everything it manages ("name" or "name@version"); repeatable`)
	fs.StringArrayVar(&repository, "repository", nil, "export one relocated repository; repeatable")
	report.register(cmd)
	flags.register(cmd)
	return cmd
}

func newImportCmd() *cobra.Command {
	flags := &commonFlags{}
	report := newReportFlag(outputText, outputJSON)
	var (
		format     string
		repository string
	)
	cmd := &cobra.Command{
		Use:   "import <path>",
		Short: "Import a standard OCI image layout into the store (FR-051)",
		Long: `Restore a standard OCI image layout — a directory or an uncompressed tar
of one — into the local store, at identical digests.

The layout is untrusted data: every manifest is accepted only if its bytes
hash to the digest addressing it, every blob is committed against the
digest its manifest pins, and an archive carrying anything other than
"oci-layout", "index.json" and "blobs/<algorithm>/<digest>" files is
refused before it is read. Compressed archives are refused too: decompress
first.

A layout produced by Tobby names the full repository of every entry and
needs nothing else. A layout produced by "skopeo copy" names the tag
alone, which is not a location — give --repository to say where the whole
archive belongs.

Entries are independent: one image that did not survive the medium fails
on its own line and the rest still lands.

Run it against a stopped instance, or use POST /api/v1/oci-layout/import
on a running one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			if err := report.validate(cmd); err != nil {
				return err
			}
			svc, closeStore, err := openInteropService(cmd, flags)
			if err != nil {
				return err
			}
			defer closeStore()
			return runImport(cmd, svc, &interop.ImportRequest{
				Input: args[0], Repository: repository,
			}, report)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&format, "format", formatOCILayout, "interoperability format (only "+formatOCILayout+" today)")
	fs.StringVar(&repository, "repository", "", "repository the entries belong to, for layouts that name only a tag")
	report.register(cmd)
	flags.register(cmd)
	return cmd
}

func checkFormat(format string) error {
	if format != formatOCILayout {
		return &usageError{
			err:  fmt.Errorf("unknown format %q", format),
			hint: "the only interoperability format is " + formatOCILayout,
		}
	}
	return nil
}

// openInteropService opens the configured store and wires the service on
// it. The returned function closes the store.
func openInteropService(cmd *cobra.Command, flags *commonFlags) (*interop.Service, func(), error) {
	cfg, err := flags.loadFor(cmd, config.ScopeStorage)
	if err != nil {
		return nil, nil, err
	}
	logger := cliLogger(cmd.ErrOrStderr(), cfg.Logging.Level)
	st, err := store.Open(cmd.Context(), cfg.Storage.Root, logger)
	if err != nil {
		return nil, nil, err
	}
	// No queue: the command IS the worker, and a task history nobody will
	// read is not what an operator standing in front of a medium needs.
	return interop.New(st, nil, cfg.Storage.BasePrefix, logger), func() {
		if err := st.Close(); err != nil {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "closing the store",
				slog.String("error", err.Error()))
		}
	}, nil
}

// cliLogger builds the command's logger on stderr: machine output stays
// on stdout, so `tobby export … --output json | jq` composes (B-010).
func cliLogger(w io.Writer, level string) *slog.Logger {
	lvl := slog.LevelInfo
	if level == "debug" {
		lvl = slog.LevelDebug
	}
	return logging.New(w, lvl)
}

// exportPlanReport is the machine form of a dry run — the FR-055
// pre-flight numbers, computed by the code that will do the writing.
type exportPlanReport struct {
	Format           string   `json:"format"`
	References       []string `json:"references"`
	Manifests        int      `json:"manifests"`
	Blobs            int      `json:"blobs"`
	Files            int      `json:"files"`
	ContentBytes     int64    `json:"contentBytes"`
	TotalBytes       int64    `json:"totalBytes"`
	LargestFileBytes int64    `json:"largestFileBytes"`
	Missing          []string `json:"missing,omitempty"`
}

func runExportPlan(cmd *cobra.Command, svc *interop.Service, req *interop.ExportRequest, report *reportFlag) error {
	plan, projection, err := svc.Plan(cmd.Context(), req)
	if err != nil {
		return err
	}
	doc := exportPlanReport{
		Format:           string(projection.Format),
		Manifests:        projection.Manifests,
		Blobs:            projection.Blobs,
		Files:            projection.Files,
		ContentBytes:     projection.ContentBytes,
		TotalBytes:       projection.TotalBytes,
		LargestFileBytes: projection.LargestFileBytes,
	}
	for _, ref := range plan.Refs {
		doc.References = append(doc.References, ref.String())
	}
	doc.Missing = missingLines(plan.Missing)
	if report.json() {
		return writeJSON(cmd, doc)
	}
	w := report.human(cmd)
	_, _ = fmt.Fprintf(w, "%d references, %d manifests, %d blobs\n",
		len(doc.References), doc.Manifests, doc.Blobs)
	_, _ = fmt.Fprintf(w, "%d bytes to write, largest single file %d bytes\n",
		doc.TotalBytes, doc.LargestFileBytes)
	for _, line := range doc.Missing {
		_, _ = fmt.Fprintf(w, "missing: %s\n", line)
	}
	return nil
}

// exportReport is the machine form of a completed export.
type exportReport struct {
	Output           string   `json:"output"`
	Format           string   `json:"format"`
	References       int      `json:"references"`
	Manifests        int      `json:"manifests"`
	Blobs            int      `json:"blobs"`
	Bytes            int64    `json:"bytes"`
	LargestFileBytes int64    `json:"largestFileBytes"`
	Missing          []string `json:"missing,omitempty"`
}

func runExport(cmd *cobra.Command, svc *interop.Service, req *interop.ExportRequest, report *reportFlag) error {
	logger := cliLogger(cmd.ErrOrStderr(), "info")
	event := &audit.Event{
		Actor: audit.ActorLocal, Action: audit.ActionLayoutExport,
		Target: req.Output, Origin: audit.OriginLocal,
	}
	res, err := svc.Export(cmd.Context(), req, logger)
	if err != nil {
		event.Outcome = audit.OutcomeFailure
		audit.Log(cmd.Context(), logger, event)
		return err
	}
	event.Outcome = audit.OutcomeSuccess
	audit.Log(cmd.Context(), logger, event)

	doc := exportReport{
		Output: res.Output, Format: string(req.Format), References: res.Refs,
		Manifests: res.Manifests, Blobs: res.Blobs, Bytes: res.Bytes,
		LargestFileBytes: res.LargestFileBytes, Missing: missingLines(res.Missing),
	}
	if report.json() {
		return writeJSON(cmd, doc)
	}
	_, _ = fmt.Fprintf(report.human(cmd), "%s: %d references, %d manifests, %d blobs, %d bytes\n",
		doc.Output, doc.References, doc.Manifests, doc.Blobs, doc.Bytes)
	for _, line := range doc.Missing {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "missing: %s\n", line)
	}
	return nil
}

// importReport is the machine form of a completed import. Entries carry
// their own outcome: a medium that lost one image is not a failed import,
// it is an import with one named failure.
type importReport struct {
	Input     string            `json:"input"`
	Manifests int               `json:"manifests"`
	Blobs     int               `json:"blobs"`
	Bytes     int64             `json:"bytes"`
	Entries   []importEntryLine `json:"entries"`
	Ignored   []string          `json:"ignored,omitempty"`
	Missing   []string          `json:"missing,omitempty"`
	Failed    int               `json:"failed"`
}

type importEntryLine struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	Error     string `json:"error,omitempty"`
}

func runImport(cmd *cobra.Command, svc *interop.Service, req *interop.ImportRequest, report *reportFlag) error {
	logger := cliLogger(cmd.ErrOrStderr(), "info")
	event := &audit.Event{
		Actor: audit.ActorLocal, Action: audit.ActionLayoutImport,
		Target: req.Input, Origin: audit.OriginLocal,
	}
	res, err := svc.Import(cmd.Context(), req, logger, nil)
	if err != nil {
		event.Outcome = audit.OutcomeFailure
		audit.Log(cmd.Context(), logger, event)
		return err
	}
	event.Outcome = audit.OutcomeSuccess
	audit.Log(cmd.Context(), logger, event)

	doc := importReport{
		Input: req.Input, Manifests: res.Manifests, Blobs: res.Blobs,
		Bytes: res.Bytes, Ignored: res.Ignored, Missing: missingLines(res.Missing),
		Failed: res.Failed(),
	}
	for i := range res.Entries {
		e := &res.Entries[i]
		line := importEntryLine{Reference: e.Ref.String(), Digest: e.Digest}
		if e.Err != nil {
			line.Error = e.Err.Error()
		}
		doc.Entries = append(doc.Entries, line)
	}
	if report.json() {
		if err := writeJSON(cmd, doc); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(report.human(cmd), "%s: %d entries (%d failed), %d manifests, %d blobs, %d bytes\n",
			req.Input, len(doc.Entries), doc.Failed, doc.Manifests, doc.Blobs, doc.Bytes)
		for _, entry := range doc.Entries {
			if entry.Error != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "failed: %s: %s\n", entry.Reference, entry.Error)
			}
		}
	}
	if doc.Failed > 0 {
		// FR-066 exit codes: an import that lost entries is an
		// operational failure, and a script must be able to branch on it
		// without parsing prose.
		return fmt.Errorf("%d of %d entries could not be imported", doc.Failed, len(doc.Entries))
	}
	return nil
}

// missingLines renders the absences a plan or a report collected.
func missingLines(missing []ocilayout.Missing) []string {
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for _, m := range missing {
		subject := m.Digest
		if subject == "" {
			subject = m.Ref.String()
		}
		out = append(out, m.Reason+" "+m.Ref.Repo+" "+subject)
	}
	return out
}
