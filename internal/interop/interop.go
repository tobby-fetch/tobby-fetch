// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package interop wires the OCI image layout export/import (FR-051) and
// the store reset (FR-046) onto the instance: the task queue, the store,
// and the taxonomy the CLI, the API and the web UI all answer with.
//
// It exists so the three surfaces share one implementation rather than
// three that agree today. FR-061 asks the UI and the API to be capable of
// the same things; the cheapest way to keep that true is to give them the
// same object and let the difference be presentation.
package interop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/hardening"
	"github.com/tobby-fetch/tobby-fetch/internal/logging"
	"github.com/tobby-fetch/tobby-fetch/internal/ocilayout"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// ConfirmationPhrase is what an operator must type to reset the store
// (FR-046). Frozen and untranslated, like the kind glossary: the phrase
// is quoted in the audit trail and in the operations documentation, and a
// confirmation that reads differently per language is a confirmation two
// people cannot talk about.
const ConfirmationPhrase = "RESET"

// Service is the instance-side of the interoperability features.
type Service struct {
	store  *store.Store
	queue  *tasks.Queue
	logger *slog.Logger
	// base is the destination base prefix of the relocation convention
	// (ADR-0013): the same value the engine computes local repository
	// paths with, so a recipe-scoped export finds the recipe artifact
	// where the synchronization put it.
	base string
	// afterReset runs once a reset has touched the store (SetAfterReset).
	afterReset func(ctx context.Context)
	// unitImportDisabled is hardening.disableUnitImport (FR-098): an OCI
	// layout import is a unit import, refused with TBY-POL-005.
	unitImportDisabled bool
}

// New builds the service. The queue may be nil on a surface that runs
// operations synchronously (the CLI): only the Start* methods need it.
func New(st *store.Store, queue *tasks.Queue, base string, logger *slog.Logger) *Service {
	return &Service{store: st, queue: queue, base: base, logger: logger}
}

// SetAfterReset installs what runs once a reset has touched the store,
// whether it completed or failed part-way: state derived from the store's
// content and kept beside it must follow it. The FileSet cache is that
// state — it lives in meta/, which the reset keeps, and it kept serving
// FileSets whose blobs the reset had removed (B-059).
func (s *Service) SetAfterReset(f func(ctx context.Context)) { s.afterReset = f }

// SetUnitImportDisabled carries hardening.disableUnitImport (FR-098,
// B-071). An OCI layout import brings content no recipe manages into the
// store, exactly like a unit import from a registry, so the switch closes
// it too: StartImport refuses to enqueue, and Import refuses to run —
// which covers the CLI, and the runner resuming a task queued before the
// switch was set. The surfaces write the hardening.refused audit record,
// since the actor and the origin belong to them. Export is untouched: it
// brings nothing in.
func (s *Service) SetUnitImportDisabled(disabled bool) { s.unitImportDisabled = disabled }

// UnitImportDisabled reports whether layout imports are refused.
func (s *Service) UnitImportDisabled() bool { return s.unitImportDisabled }

// Register installs the export and import runners on the queue. Call
// before the queue is started.
func (s *Service) Register() {
	s.queue.Register(tasks.TypeLayoutExport, s.runExport)
	s.queue.Register(tasks.TypeLayoutImport, s.runImport)
}

// Selector narrows an export. Both fields empty exports the whole store —
// which is the interoperability escape hatch in its most useful form:
// everything, readable by anyone.
type Selector struct {
	// Recipes names recipes as "name@version", or "name" for every
	// version of it.
	Recipes []string
	// Repositories names relocated repository paths.
	Repositories []string
}

// ExportRequest is one export.
type ExportRequest struct {
	Selector
	Output    string
	Format    ocilayout.Format
	Overwrite bool
}

// ImportRequest is one import.
type ImportRequest struct {
	Input string
	// Repository places entries a third-party layout names by tag alone.
	Repository string
}

// Plan resolves an export without producing anything: the projection of
// FR-055 (bytes to write, largest single file) and the list of what the
// selection resolved to. Side-effect free by construction — nothing here
// opens the destination.
func (s *Service) Plan(ctx context.Context, req *ExportRequest) (*ocilayout.Plan, ocilayout.Projection, error) {
	format, err := ocilayout.ParseFormat(string(req.Format))
	if err != nil {
		return nil, ocilayout.Projection{}, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file": "the export request", "path": "format", "constraint": err.Error(),
		})
	}
	sel, err := s.selection(ctx, &req.Selector)
	if err != nil {
		return nil, ocilayout.Projection{}, err
	}
	plan, err := ocilayout.NewPlan(ctx, s.source(), sel)
	if err != nil {
		return nil, ocilayout.Projection{}, s.problem(err, req.Output)
	}
	projection := plan.Project(format)
	return plan, projection, nil
}

// Export runs one export to completion, reporting through logger. The
// CLI calls it directly; the queue calls it through the runner.
func (s *Service) Export(ctx context.Context, req *ExportRequest, logger *slog.Logger) (*ocilayout.Report, error) {
	plan, projection, err := s.Plan(ctx, req)
	if err != nil {
		return nil, err
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "exporting to an OCI image layout",
		slog.String("output", req.Output), slog.String("format", string(projection.Format)),
		slog.Int("references", len(plan.Refs)),
		slog.Int64("projected_bytes", projection.TotalBytes),
		slog.Int64("largest_file_bytes", projection.LargestFileBytes),
		slog.String("requirement", "FR-051"))

	report, err := ocilayout.Write(ctx, s.source(), plan, ocilayout.ExportOptions{
		Output: req.Output, Format: projection.Format, Overwrite: req.Overwrite,
	})
	if err != nil {
		return nil, s.problem(err, req.Output)
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "OCI image layout written",
		slog.String("output", report.Output), slog.Int("manifests", report.Manifests),
		slog.Int("blobs", report.Blobs), slog.Int64("bytes", report.Bytes),
		slog.Int("missing", len(report.Missing)),
		slog.String("requirement", "FR-051"))
	return report, nil
}

// Import runs one import to completion.
//
// B-017: every byte lands through the store's own write path, which
// holds the garbage-collection lock shared for the duration. An import
// IS a content write, and a sweep started from another surface must wait
// for it rather than collect underneath it.
func (s *Service) Import(ctx context.Context, req *ImportRequest, logger *slog.Logger, onEntry func(*ocilayout.Entry)) (*ocilayout.ImportReport, error) {
	if s.unitImportDisabled {
		return nil, hardening.UnitImportRefusal()
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "importing an OCI image layout",
		slog.String("input", req.Input), slog.String("requirement", "FR-051"))
	report, err := ocilayout.Import(ctx, s.store, ocilayout.ImportOptions{
		Input: req.Input, Repository: req.Repository,
	}, onEntry)
	if err != nil {
		return nil, s.problem(err, req.Input)
	}
	s.recordProvenance(ctx, report, logger)
	logger.LogAttrs(ctx, slog.LevelInfo, "OCI image layout imported",
		slog.String("input", req.Input), slog.Int("entries", len(report.Entries)),
		slog.Int("failed", report.Failed()), slog.Int("manifests", report.Manifests),
		slog.Int("blobs", report.Blobs), slog.Int64("bytes", report.Bytes),
		slog.Int("ignored", len(report.Ignored)),
		slog.String("requirement", "FR-051"))
	return report, nil
}

// recordProvenance records every repository an entry of the import
// landed in as a unit import of origin oci-layout (FR-098, B-071): without
// an entry it would read as seeded content pushed through /v2/ — not
// removable, counted as seed by the unmanaged-content metric. The store's
// never-downgrade rule applies: a repository already known (seeded, unit
// imported, or managed by a recipe) keeps its class.
func (s *Service) recordProvenance(ctx context.Context, report *ocilayout.ImportReport, logger *slog.Logger) {
	seen := map[string]bool{}
	for i := range report.Entries {
		e := &report.Entries[i]
		repo := e.Ref.Repo
		if e.Err != nil || repo == "" || seen[repo] {
			continue
		}
		seen[repo] = true
		if _, err := s.store.RecordUnitImportFrom(repo, store.OriginOCILayout); err != nil {
			logger.LogAttrs(ctx, slog.LevelWarn, "the provenance of the imported repository could not be recorded",
				slog.String("repository", repo), slog.String("error", err.Error()),
				slog.String("requirement", "FR-098"))
			continue
		}
		// The class the repository now has: the ledger may have known
		// better (never-downgrade rule).
		c := s.store.Classify(repo)
		logger.LogAttrs(ctx, slog.LevelInfo, "layout import recorded",
			slog.String("repository", repo),
			slog.String(logging.KeyProvenance, string(c.Class)), slog.String("origin", string(c.Origin)),
			slog.String("requirement", "FR-051/FR-098"))
	}
}

// Reset empties the store (FR-046).
//
// The typed confirmation is checked here, not by each surface: the
// requirement is about the operation, and a confirmation that one caller
// could skip would not be one. The audit record carries the actor as the
// surface knows it — an authenticated identity, or the explicit
// unauthenticated context of an instance running under the FR-075
// override, which is exactly the case FR-046 says must still be recorded.
func (s *Service) Reset(ctx context.Context, actor, origin, confirmation string) (store.ResetResult, error) {
	event := &audit.Event{
		Actor: actor, Action: audit.ActionStoreReset, Target: s.store.Root(),
		Origin: origin,
	}
	if strings.TrimSpace(confirmation) != ConfirmationPhrase {
		event.Outcome = audit.OutcomeDenied
		audit.Log(ctx, s.logger, event)
		return store.ResetResult{}, taxonomy.New(taxonomy.CodeResetConfirmation,
			taxonomy.Params{"phrase": ConfirmationPhrase})
	}
	res, err := s.store.Reset(ctx, s.logger)
	if s.afterReset != nil {
		s.afterReset(ctx)
	}
	if err != nil {
		event.Outcome = audit.OutcomeFailure
		audit.Log(ctx, s.logger, event)
		return res, taxonomy.New(taxonomy.CodeStoreWrite,
			taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
	event.Outcome = audit.OutcomeSuccess
	audit.Log(ctx, s.logger, event)
	return res, nil
}

// selection resolves a Selector against the store.
func (s *Service) selection(ctx context.Context, sel *Selector) (ocilayout.Selection, error) {
	src := s.source()
	if len(sel.Recipes) == 0 && len(sel.Repositories) == 0 {
		out, err := ocilayout.SelectAll(ctx, src)
		if err != nil {
			return out, s.problem(err, "")
		}
		return out, nil
	}

	var out ocilayout.Selection
	if len(sel.Repositories) > 0 {
		repos, err := ocilayout.SelectRepositories(ctx, src, sel.Repositories)
		if err != nil {
			return out, s.problem(err, "")
		}
		out.Add(repos.Refs...)
	}
	for _, name := range sel.Recipes {
		refs, err := s.recipeRefs(ctx, name)
		if err != nil {
			return out, err
		}
		out.Add(refs...)
	}
	return out, nil
}

// recipeRefs lists what one recorded recipe owns: its ingredients at the
// tags they were stored under, the recipe artifact itself, and — for
// every one of them — the two tags a cosign signature may sit under
// (RECIPE-SPEC §12.2).
//
// Naming both signature tags unconditionally is the lesson of B-015: the
// layout a publisher chose is not knowable here, an absent tag is simply
// reported as missing, and a selection that guessed would ship content
// whose signature verifies upstream and is gone one hop down.
func (s *Service) recipeRefs(ctx context.Context, name string) ([]ocilayout.Ref, error) {
	records, err := s.store.RecipeRecords()
	if err != nil {
		return nil, s.problem(err, name)
	}
	wanted, version, pinned := strings.Cut(name, "@")
	var refs []ocilayout.Ref
	matched := false
	for i := range records {
		rec := &records[i]
		if rec.Name != wanted || (pinned && rec.Version != version) {
			continue
		}
		matched = true
		for _, ing := range rec.Ingredients {
			if ing.Repo == "" {
				continue
			}
			refs = append(refs, ocilayout.Ref{Repo: ing.Repo, Tag: ing.Tag})
			refs = append(refs, ocilayout.SignatureRefs(ing.Repo, ing.Digest)...)
		}
		recipeRefs, err := s.recipeArtifactRefs(ctx, rec)
		if err != nil {
			return nil, err
		}
		refs = append(refs, recipeRefs...)
	}
	if !matched {
		return nil, taxonomy.New(taxonomy.CodeNotFound, nil).
			WithCause(fmt.Errorf("no recipe named %q is recorded in this store", name))
	}
	return refs, nil
}

// recipeArtifactRefs finds the tags the recipe artifact itself sits
// under, by asking which of the repository's tags resolve to the
// recorded digest. Asking rather than deriving: the cookbook tag
// convention belongs to the format (RECIPE-SPEC §11.3), and a second
// implementation of it here would be one more thing to keep in step.
func (s *Service) recipeArtifactRefs(ctx context.Context, rec *store.RecipeRecord) ([]ocilayout.Ref, error) {
	local, err := relocate.PathWithBase(s.base, rec.CookbookRepo)
	if err != nil {
		// A cookbook repository the relocation convention cannot express
		// is a recorded state this build cannot act on; the recipe's
		// ingredients still travel, and the artifact is reported absent
		// by the plan rather than silently dropped here.
		return nil, nil
	}
	tags, err := s.store.Tags(ctx, local)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, s.problem(err, local)
	}
	refs := ocilayout.SignatureRefs(local, rec.Digest)
	for _, tag := range tags {
		if dgst, ok := s.store.ResolveTag(ctx, local, tag); ok && dgst == rec.Digest {
			refs = append(refs, ocilayout.Ref{Repo: local, Tag: tag})
		}
	}
	return refs, nil
}

// problem maps a package-level failure onto the taxonomy every surface
// renders (R-03). Everything already taxonomized passes through.
func (s *Service) problem(err error, subject string) error {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te
	}
	var unsafeEntry *ocilayout.UnsafeEntryError
	switch {
	case errors.As(err, &unsafeEntry):
		return taxonomy.New(taxonomy.CodeLayoutUnsafe, taxonomy.Params{
			"path": subject, "entry": unsafeEntry.Entry, "reason": unsafeEntry.Reason,
		}).WithCause(err)
	case errors.Is(err, ocilayout.ErrTargetExists):
		return taxonomy.New(taxonomy.CodeLayoutTarget, taxonomy.Params{"path": subject}).WithCause(err)
	case errors.Is(err, ocilayout.ErrNotLayout):
		return taxonomy.New(taxonomy.CodeLayoutInvalid, taxonomy.Params{
			"path": subject, "detail": err.Error(),
		}).WithCause(err)
	case errors.Is(err, store.ErrNotFound):
		return taxonomy.New(taxonomy.CodeNotFound, nil).WithCause(err)
	default:
		return taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": err.Error()}).WithCause(err)
	}
}

// source adapts the embedded store to the exporter's read surface,
// mapping the store's "not found" onto the package's own — the seam that
// keeps ocilayout a format implementation rather than a store consumer.
func (s *Service) source() ocilayout.Source { return &storeSource{st: s.store} }

type storeSource struct{ st *store.Store }

func (s *storeSource) Repositories(ctx context.Context) ([]string, error) {
	return s.st.Repositories(ctx)
}

func (s *storeSource) Tags(ctx context.Context, repo string) ([]string, error) {
	tags, err := s.st.Tags(ctx, repo)
	return tags, mapNotFound(err)
}

func (s *storeSource) RawManifest(ctx context.Context, repo, reference string) (payload []byte, mediaType, dgst string, err error) {
	payload, mediaType, dgst, err = s.st.RawManifest(ctx, repo, reference)
	return payload, mediaType, dgst, mapNotFound(err)
}

func (s *storeSource) BlobReader(ctx context.Context, repo, dgst string) (io.ReadCloser, error) {
	rc, err := s.st.BlobReader(ctx, repo, dgst)
	return rc, mapNotFound(err)
}

func mapNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %w", ocilayout.ErrNotFound, err)
	}
	return err
}

// Compile-time proof that the embedded store is a valid import sink: the
// import path writes through the store's own verified, lock-holding
// accessors and nothing else.
var _ ocilayout.Sink = (*store.Store)(nil)
