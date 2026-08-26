// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package interop

import (
	"context"
	"errors"
	"log/slog"

	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The queue-backed halves of FR-051. Export and import are tracked tasks
// like every other operation of the product: they can take an hour on a
// real medium, they belong in the history a transported store carries
// (FR-050), and their per-item outcome is what tells an operator which
// image did not make it rather than that "the export failed".

// archiveItem is the task item that carries the write itself — the one
// whose byte count is the answer to "how big is the medium now".
const archiveItem = "archive"

// StartExport enqueues an export (FR-051).
func (s *Service) StartExport(actor string, req *ExportRequest) (*tasks.Task, error) {
	if req.Output == "" {
		return nil, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the export request", "path": "output",
			"constraint": "a destination path is required",
		})
	}
	format, err := ocilayout.ParseFormat(string(req.Format))
	if err != nil {
		return nil, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the export request", "path": "format", "constraint": err.Error(),
		})
	}
	return s.queue.Create(tasks.TypeLayoutExport, req.Output, actor, nil,
		tasks.WithLayout(&tasks.Layout{
			Format:       string(format),
			Recipes:      req.Recipes,
			Repositories: req.Repositories,
			Overwrite:    req.Overwrite,
		}))
}

// StartImport enqueues an import (FR-051).
func (s *Service) StartImport(actor string, req *ImportRequest) (*tasks.Task, error) {
	if req.Input == "" {
		return nil, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the import request", "path": "input",
			"constraint": "a source path is required",
		})
	}
	return s.queue.Create(tasks.TypeLayoutImport, req.Input, actor, nil,
		tasks.WithLayout(&tasks.Layout{Repository: req.Repository}))
}

// runExport is the tasks.Runner for TypeLayoutExport.
func (s *Service) runExport(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
	req := &ExportRequest{Output: t.Reference}
	if t.Layout != nil {
		req.Format = ocilayout.Format(t.Layout.Format)
		req.Recipes = t.Layout.Recipes
		req.Repositories = t.Layout.Repositories
		req.Overwrite = t.Layout.Overwrite
	}

	plan, projection, err := s.Plan(ctx, req)
	if err != nil {
		return err
	}
	for _, ref := range plan.Refs {
		t.Items = append(t.Items, tasks.Item{Name: ref.String(), Status: tasks.StatusRunning})
	}
	t.Items = append(t.Items, tasks.Item{Name: archiveItem, Status: tasks.StatusRunning})
	save()
	for _, missing := range plan.Missing {
		logger.LogAttrs(ctx, slog.LevelWarn, "selected content is not in the store",
			slog.String("repository", missing.Ref.Repo), slog.String("tag", missing.Ref.Tag),
			slog.String("digest", missing.Digest), slog.String("reason", missing.Reason))
	}

	report, err := ocilayout.Write(ctx, s.source(), plan, ocilayout.ExportOptions{
		Output: req.Output, Format: projection.Format, Overwrite: req.Overwrite,
	})
	if err != nil {
		return s.problem(err, req.Output)
	}
	for i := range t.Items {
		t.Items[i].Status = tasks.StatusDone
	}
	t.Items[len(t.Items)-1].SizeBytes = report.Bytes
	save()

	logger.LogAttrs(ctx, slog.LevelInfo, "OCI image layout written",
		slog.String("output", report.Output), slog.String("format", string(projection.Format)),
		slog.Int("references", report.Refs), slog.Int("manifests", report.Manifests),
		slog.Int("blobs", report.Blobs), slog.Int64("bytes", report.Bytes),
		slog.Int64("largest_file_bytes", report.LargestFileBytes),
		slog.Int("missing", len(report.Missing)),
		slog.String("requirement", "FR-051"))
	return nil
}

// runImport is the tasks.Runner for TypeLayoutImport.
//
// Entries are isolated from one another, as the media rules of FR-054
// ask of a transported payload: one image whose blob did not survive the
// medium fails on its own line, named, and everything that did survive
// still lands.
func (s *Service) runImport(ctx context.Context, t *tasks.Task, logger *slog.Logger, save func()) error {
	req := &ImportRequest{Input: t.Reference}
	if t.Layout != nil {
		req.Repository = t.Layout.Repository
	}
	report, err := s.Import(ctx, req, logger, func(entry *ocilayout.Entry) {
		item := tasks.Item{Name: entryItemName(entry), Digest: entry.Digest, Status: tasks.StatusDone}
		if entry.Err != nil {
			item.Status = tasks.StatusFailed
			item.Error = tasks.FromTaxonomy(s.entryProblem(entry, req.Input))
			logger.LogAttrs(ctx, slog.LevelWarn, "layout entry not imported",
				slog.String("entry", item.Name), slog.String("digest", entry.Digest),
				slog.String("error", entry.Err.Error()))
		}
		t.Items = append(t.Items, item)
		save()
	})
	if err != nil {
		return err
	}
	for _, ignored := range report.Ignored {
		logger.LogAttrs(ctx, slog.LevelWarn, "archive entry ignored: not part of the layout",
			slog.String("entry", ignored))
	}
	return nil
}

// entryItemName names one import item. An entry the layout could not
// place has no reference to show, so its digest stands in — the item
// still has to be identifiable in the report that says why it failed.
func entryItemName(entry *ocilayout.Entry) string {
	if entry.Ref.Repo == "" {
		return entry.Digest
	}
	if entry.Ref.Tag == "" {
		return entry.Ref.Repo + "@" + entry.Digest
	}
	return entry.Ref.String()
}

// entryProblem taxonomizes one entry failure.
//
// The fallback is the layout code, not the store-read code the service
// uses elsewhere: what failed here is something the medium said, and
// telling an operator to check their disk when their archive is
// inconsistent sends them to the wrong problem.
func (s *Service) entryProblem(entry *ocilayout.Entry, input string) *taxonomy.Error {
	err := s.problem(entry.Err, input)
	var te *taxonomy.Error
	if !errors.As(err, &te) {
		return taxonomy.New(taxonomy.CodeInternal, nil).WithCause(err)
	}
	if te.Code() == taxonomy.CodeStoreRead {
		return taxonomy.New(taxonomy.CodeLayoutInvalid, taxonomy.Params{
			"path": input, "detail": entry.Err.Error(),
		}).WithCause(entry.Err)
	}
	return te
}
