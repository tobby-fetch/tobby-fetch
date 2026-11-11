// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Package check walks a store and reports its integrity (FR-057, roadmap
// R-31): the verification the product already performs in pieces — at
// import (NFR-010), before a push (FR-033), on a medium that arrives
// (FR-054) — made available on demand, on any store, in both modes.
//
// Four checks, in this order, each independent of the others' verdict:
//
//  1. Blobs. Every blob of the content-addressed tree hashes to the digest
//     it is stored under.
//  2. Manifests. Every manifest reachable from a recorded recipe — the
//     recipe artifact and its signature artifacts, every ingredient — is
//     complete: the manifest itself, its config and layers, and the child
//     manifests of an index, are present. A platform-filtered index
//     legitimately keeps its pinned digest while carrying only some of its
//     children (FR-022), so an absent child is not a defect.
//  3. Signatures. Every recorded recipe's signature verifies against the
//     trust roots configured on THIS instance now, scopes applied. A key
//     that has since been removed from the roots, or a scope that no
//     longer admits unsigned recipes, shows up here. Reduced assurance is
//     re-reported (FR-037).
//  4. Orphans. Content that no recorded recipe and no protected root
//     reaches is listed. Protected roots are the ones the prune keeps
//     (FR-045): unit imports, seeded content, the offline vulnerability
//     database — every repository whose provenance is not "recipe".
//
// The walk is read-only. It repairs nothing and deletes nothing (the
// store is byte-identical afterwards — a property the tests assert on the
// whole tree), and the one lock it takes is the shared side of the
// content lock, so a garbage collection cannot remove a blob under it.
package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"

	"github.com/tobby-fetch/tobby-fetch/internal/assurance"
	"github.com/tobby-fetch/tobby-fetch/internal/integrity"
	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxManifestBytes bounds a manifest read during the walk (NFR-007).
const maxManifestBytes = 1 << 20

// Options parameterizes one verification.
type Options struct {
	// Trust is THIS instance's trust policy (FR-033). Nil means no trust
	// root is configured: no signature verifies, and every recorded recipe
	// is reported as refused — the secure reading (FR-075), not a pass.
	Trust media.Trust
	// RunID correlates the report with the task's logs (FR-090).
	RunID string
	// Progress receives the walk's progress, if non-nil. It is called
	// often (once per blob); a consumer that persists should throttle.
	Progress func(integrity.Progress)
	// Logger records the walk. Nil logs nothing.
	Logger *slog.Logger
	// Limit bounds each listed section of the report; zero is
	// integrity.MaxListed. It exists for the tests, which should not have
	// to write five hundred blobs to prove a bound.
	Limit int
}

// Verify walks the store and returns the report. The error return is for
// what stops the walk altogether — the store's own listing failing, a
// cancelled context — never for a defect of the store, which comes back
// inside the report.
func Verify(ctx context.Context, st *store.Store, opts Options) (*integrity.Report, error) {
	w := &walker{
		ctx: ctx, st: st, opts: opts,
		rep: &integrity.Report{
			RunID: opts.RunID, StartedAt: time.Now().UTC(),
			Store: integrity.Store{
				Root: st.Root(), MediaID: st.MediaID(), Transportable: media.IsMedium(st.Root()),
			},
		},
		seen: map[string]bool{},
		memo: map[string]bool{},
	}
	// The content lock is shared and held for the whole walk (see
	// store.HoldContent for what that costs and why it is worth it).
	release := st.HoldContent()
	defer release()

	blobs, err := w.blobs()
	if err != nil {
		return nil, err
	}
	records, err := st.RecipeRecords()
	if err != nil {
		return nil, fmt.Errorf("integrity: reading the recipe graph: %w", err)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].Version < records[j].Version
	})
	w.rep.Checked.Recipes = len(records)
	if err := w.recipes(records); err != nil {
		return nil, err
	}
	if err := w.orphans(blobs, records); err != nil {
		return nil, err
	}
	return w.finish(), nil
}

// walker holds the state of one verification.
type walker struct {
	ctx  context.Context
	st   *store.Store
	opts Options
	rep  *integrity.Report

	defects  []integrity.Defect
	findings []integrity.Defect
	// seen and memo make the manifest walk visit each manifest once however
	// many recipes reach it: seen marks a visit, memo keeps its verdict for
	// the recipes that reach it later.
	seen map[string]bool
	memo map[string]bool
}

func (w *walker) logger() *slog.Logger {
	if w.opts.Logger != nil {
		return w.opts.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (w *walker) progress(p integrity.Progress) {
	if w.opts.Progress != nil {
		w.opts.Progress(p)
	}
}

func (w *walker) defect(code taxonomy.Code, path string, params map[string]string) {
	w.defects = append(w.defects, integrity.Defect{Code: code, Path: path, Params: params})
}

func (w *walker) finding(code taxonomy.Code, path string, params map[string]string) {
	w.findings = append(w.findings, integrity.Defect{Code: code, Path: path, Params: params})
}

// blobs is the first check: re-hash every blob against the digest it is
// stored under. It returns the blobs it walked, which the orphan check
// reads again.
func (w *walker) blobs() ([]store.BlobFile, error) {
	blobs, err := w.st.Blobs()
	if err != nil {
		return nil, fmt.Errorf("integrity: listing the blobs: %w", err)
	}
	for i := range blobs {
		if err := w.ctx.Err(); err != nil {
			return nil, err
		}
		res := w.st.CheckBlob(w.ctx, blobs[i])
		if err := w.ctx.Err(); err != nil {
			return nil, err
		}
		w.rep.Checked.Blobs++
		w.rep.Checked.Bytes += res.Size
		w.progress(integrity.Progress{
			Stage: integrity.StageBlobs, Done: i + 1, Total: len(blobs), Bytes: w.rep.Checked.Bytes,
		})
		if res.OK() {
			continue
		}
		actual := string(res.Actual)
		if res.Err != nil {
			actual = "unreadable (" + res.Err.Error() + ")"
		}
		w.defect(taxonomy.CodeIntegrityBlobDigest, res.Path, map[string]string{
			"path": res.Path, "expected": string(res.Digest), "actual": actual,
		})
	}
	return blobs, nil
}

// recipes runs the two recipe-scoped checks, per recipe: completeness,
// then the signature.
func (w *walker) recipes(records []store.RecipeRecord) error {
	for i := range records {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		rec := &records[i]
		id := rec.Name + "@" + rec.Version
		w.progress(integrity.Progress{
			Stage: integrity.StageManifests, Done: i, Total: len(records), Bytes: w.rep.Checked.Bytes,
		})
		row := integrity.Recipe{
			Name: rec.Name, Version: rec.Version, CookbookRepo: rec.CookbookRepo,
			Signature: integrity.SignatureNotChecked,
		}
		located := w.locate(rec, id)
		if located {
			row.Complete = w.complete(rec)
		}
		if located && w.present(rec.ArtifactRepo, rec.Digest) {
			w.signature(rec, &row)
		}
		w.rep.Recipes = append(w.rep.Recipes, row)
	}
	w.progress(integrity.Progress{
		Stage: integrity.StageSignatures, Done: len(records), Total: len(records), Bytes: w.rep.Checked.Bytes,
	})
	return nil
}

// locate reports whether the record says where the recipe artifact is. A
// record that does not (written before the field existed) cannot be
// walked, and says so: it is a graph entry this build cannot read.
func (w *walker) locate(rec *store.RecipeRecord, id string) bool {
	if rec.ArtifactRepo != "" && rec.ArtifactTag != "" && rec.Digest != "" {
		return true
	}
	w.defect(taxonomy.CodeIntegrityUnreadable, "meta/recipes.json", map[string]string{
		"path": "meta/recipes.json",
		"detail": "the recipe graph entry of " + id + " records no artifact location, so what it reaches cannot be checked " +
			"— it was written by a release that predates the field; a synchronization rewrites it",
	})
	return false
}

// present reports whether a manifest is in the repository.
func (w *walker) present(repo, dgst string) bool { return w.st.HasManifest(w.ctx, repo, dgst) }

// complete is the second check for one recipe: every manifest reachable
// from it is complete. The roots are the recipe artifact, the signature
// artifacts that travel with it, and every ingredient.
func (w *walker) complete(rec *store.RecipeRecord) bool {
	ok := w.manifest(rec.ArtifactRepo, rec.Digest, true)
	for _, tag := range signatureTags(rec.Digest) {
		if d, found := w.st.ResolveTag(w.ctx, rec.ArtifactRepo, tag); found {
			ok = w.manifest(rec.ArtifactRepo, d, true) && ok
		}
	}
	for i := range rec.Ingredients {
		ing := &rec.Ingredients[i]
		if ing.Repo == "" || ing.Digest == "" {
			continue
		}
		ok = w.manifest(ing.Repo, ing.Digest, true) && ok
		for _, tag := range signatureTags(ing.Digest) {
			if d, found := w.st.ResolveTag(w.ctx, ing.Repo, tag); found {
				ok = w.manifest(ing.Repo, d, true) && ok
			}
		}
	}
	return ok
}

// signatureTags are the tags a manifest's signature artifacts travel
// under: the tag-attached cosign layout and the referrers fallback index
// of the bundle layout (RECIPE-SPEC §12.2).
func signatureTags(manifestDigest string) []string {
	h, ok := strings.CutPrefix(manifestDigest, "sha256:")
	if !ok {
		return nil
	}
	return []string{"sha256-" + h + ".sig", "sha256-" + h}
}

// ociDocument is the subset of an OCI manifest or index the walk reads.
type ociDocument struct {
	Config    *ociDescriptor  `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
	Manifests []ociDescriptor `json:"manifests"`
}

type ociDescriptor struct {
	Digest string `json:"digest"`
}

// manifest checks one manifest and everything it names, and returns
// whether it is complete. required says whether its absence is a defect:
// index children are not required (a platform-filtered index keeps its
// pinned digest while carrying only some of its children, FR-022).
func (w *walker) manifest(repo, dgst string, required bool) bool {
	key := repo + "@" + dgst
	if w.seen[key] {
		return w.memo[key]
	}
	w.seen[key] = true
	w.memo[key] = true
	ref := key

	if !w.present(repo, dgst) {
		if !required {
			return true // a sparse index child the store legitimately lacks
		}
		w.memo[key] = false
		w.defect(taxonomy.CodeIntegrityIncomplete, ref, map[string]string{
			"reference": ref, "missing": "the manifest itself",
		})
		return false
	}
	w.rep.Checked.Manifests++

	raw, _, _, err := w.st.RawManifest(w.ctx, repo, dgst)
	if err == nil && len(raw) > maxManifestBytes {
		err = fmt.Errorf("larger than the %d-byte manifest bound", maxManifestBytes)
	}
	var doc ociDocument
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		w.memo[key] = false
		w.defect(taxonomy.CodeIntegrityUnreadable, ref, map[string]string{"path": ref, "detail": err.Error()})
		return false
	}

	var missing []string
	need := func(d string) {
		if _, perr := digest.Parse(d); perr != nil {
			missing = append(missing, d+" (not a digest)")
			return
		}
		if !w.st.HasBlob(w.ctx, repo, digest.Digest(d)) {
			missing = append(missing, d)
		}
	}
	if doc.Config != nil {
		need(doc.Config.Digest)
	}
	for i := range doc.Layers {
		need(doc.Layers[i].Digest)
	}
	complete := true
	for i := range doc.Manifests {
		if !w.manifest(repo, doc.Manifests[i].Digest, false) {
			complete = false
		}
	}
	if len(missing) > 0 {
		complete = false
		w.defect(taxonomy.CodeIntegrityIncomplete, ref, map[string]string{
			"reference": ref, "missing": listMissing(missing),
		})
	}
	w.memo[key] = complete
	return complete
}

// listMissing names the first few missing references and counts the rest:
// a manifest missing every layer is one defect, not a hundred.
func listMissing(missing []string) string {
	const shown = 5
	if len(missing) <= shown {
		return strings.Join(missing, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(missing[:shown], ", "), len(missing)-shown)
}

// signature is the third check for one recipe: the signature verifies
// against the trust roots of THIS instance, under the scope its nominal
// repository matches.
func (w *walker) signature(rec *store.RecipeRecord, row *integrity.Recipe) {
	// Trust scopes match the canonical nominal repository, never the
	// relocated path the store happens to use (FR-036, ADR-0013).
	nominal, err := relocate.Path(rec.CookbookRepo)
	if err != nil {
		nominal = rec.CookbookRepo
	}
	var d media.TrustDecision
	if w.opts.Trust != nil {
		d = w.opts.Trust.Decide(nominal)
	}
	row.TrustScope = d.Scope
	subject := rec.CookbookRepo + ":" + rec.ArtifactTag

	refuse := func(fingerprints string) {
		row.Signature = integrity.SignatureRefused
		w.defect(taxonomy.CodeSignature, rec.Name+"@"+rec.Version, map[string]string{
			"recipe": subject, "fingerprints": fingerprints, "scope": d.Scope,
		})
	}
	admitUnsigned := func() {
		row.Signature = integrity.SignatureUnsignedAdmitted
		row.Assurance = assurance.ForUnsigned(d.Scope)
	}

	if d.Keys == nil {
		if d.AllowUnsigned {
			admitUnsigned()
			return
		}
		refuse("none — no trust root is configured")
		return
	}
	fingerprint, err := sigverify.Verify(w.ctx, &manifests{st: w.st, repo: rec.ArtifactRepo}, rec.ArtifactRepo, rec.Digest, d.Keys)
	switch {
	case err == nil:
		row.Signature, row.KeyFingerprint = integrity.SignatureVerified, fingerprint
	case errors.Is(err, sigverify.ErrNoSignature) && d.AllowUnsigned:
		admitUnsigned()
	default:
		refuse(fingerprintsOf(err))
	}
}

// fingerprintsOf renders a signature failure's detail the way FR-033 asks
// for it: the keys tried, or what was wrong with the artifact.
func fingerprintsOf(err error) string {
	var noKey *sigverify.NoTrustedKeyError
	if errors.As(err, &noKey) {
		return strings.Join(noKey.Tried, ", ")
	}
	var bad *sigverify.BadSignatureError
	if errors.As(err, &bad) {
		return bad.Reason
	}
	if errors.Is(err, sigverify.ErrNoSignature) {
		return "no signature artifact found"
	}
	return err.Error()
}

// manifests adapts the store to sigverify.Manifests, so the signature
// checks that ran at fetch time run again here on the same code path
// (FR-033: the verifier is reusable offline on an opened store).
type manifests struct {
	st   *store.Store
	repo string
}

func (m *manifests) Manifest(ctx context.Context, _, reference string) (payload []byte, mediaType, dgst string, err error) {
	payload, mediaType, dgst, err = m.st.RawManifest(ctx, m.repo, reference)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", "", sigverify.ErrNotFound
	}
	return payload, mediaType, dgst, err
}

func (m *manifests) Blob(ctx context.Context, _, dgst string) ([]byte, error) {
	rc, err := m.st.BlobReader(ctx, m.repo, dgst)
	if errors.Is(err, store.ErrNotFound) {
		return nil, sigverify.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(io.LimitReader(rc, sigverify.MaxPayloadSize+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > sigverify.MaxPayloadSize {
		return nil, fmt.Errorf("integrity: blob %s exceeds the %d-byte signature payload bound", dgst, sigverify.MaxPayloadSize)
	}
	return raw, nil
}

// orphans is the fourth check: content nothing reaches, listed and never
// touched. Two kinds, one code:
//
//   - a blob of the store that no manifest reachable from any tag
//     references — what a garbage collection would reclaim once its grace
//     period passes, and what it has not yet reclaimed (the grace period
//     defers, FR-044);
//   - a tag of a recipe-managed repository that no recorded recipe
//     reaches: content a removed or superseded recipe left behind.
//
// Content under a protected root — a repository whose provenance is not
// "recipe": a unit import (FR-023), content seeded through /v2/ (UC3),
// the offline vulnerability database (FR-032) — is never an orphan
// (FR-045), whatever the recipe graph says.
func (w *walker) orphans(blobs []store.BlobFile, records []store.RecipeRecord) error {
	w.progress(integrity.Progress{Stage: integrity.StageOrphans, Bytes: w.rep.Checked.Bytes})
	reach, err := w.st.Reachable(w.ctx)
	if err != nil {
		return fmt.Errorf("integrity: marking the reachable content: %w", err)
	}
	for i := range blobs {
		if !reach[blobs[i].Digest] {
			w.finding(taxonomy.CodeIntegrityOrphan, blobs[i].Path, map[string]string{"path": blobs[i].Path})
		}
	}

	// Digests each recipe-managed repository is meant to hold: the
	// artifacts and the ingredients the recipe graph names.
	roots := map[string]map[string]bool{}
	add := func(repo, dgst string) {
		if repo == "" || dgst == "" {
			return
		}
		if roots[repo] == nil {
			roots[repo] = map[string]bool{}
		}
		roots[repo][dgst] = true
	}
	for i := range records {
		add(records[i].ArtifactRepo, records[i].Digest)
		for _, ing := range records[i].Ingredients {
			add(ing.Repo, ing.Digest)
		}
	}
	repos, err := w.st.Repositories(w.ctx)
	if err != nil {
		return fmt.Errorf("integrity: listing the repositories: %w", err)
	}
	for _, repo := range repos {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		if prov, _ := w.st.ProvenanceOf(repo); prov.Class != store.ProvenanceRecipe {
			continue // a protected root
		}
		tags, err := w.st.Tags(w.ctx, repo)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return fmt.Errorf("integrity: listing the tags of %s: %w", repo, err)
		}
		for _, tag := range tags {
			if w.attached(repo, tag, roots[repo]) {
				continue
			}
			d, ok := w.st.ResolveTag(w.ctx, repo, tag)
			if ok && roots[repo][d] {
				continue // another tag of content a recipe reaches
			}
			ref := repo + ":" + tag
			w.finding(taxonomy.CodeIntegrityOrphan, ref, map[string]string{"path": ref})
		}
	}
	return nil
}

// attached reports whether a tag is the signature or attestation artifact
// of a digest the repository is meant to hold: "sha256-<hex>" with an
// optional suffix (.sig, .att, .sbom). It travels with its subject, so it
// is reachable exactly when its subject is.
func (w *walker) attached(repo, tag string, held map[string]bool) bool {
	rest, ok := strings.CutPrefix(tag, "sha256-")
	if !ok || len(rest) < 64 {
		return false
	}
	return held["sha256:"+rest[:64]]
}

// finish closes the report: deterministic order, the bounded lists with
// their exact counts, and the verdict.
func (w *walker) finish() *integrity.Report {
	rep := w.rep
	order := func(l []integrity.Defect) {
		sort.SliceStable(l, func(i, j int) bool {
			if l[i].Code != l[j].Code {
				return l[i].Code < l[j].Code
			}
			return l[i].Path < l[j].Path
		})
	}
	order(w.defects)
	order(w.findings)
	rep.DefectCount, rep.FindingCount = len(w.defects), len(w.findings)
	limit := w.opts.Limit
	if limit <= 0 {
		limit = integrity.MaxListed
	}
	rep.Defects, rep.DefectsOmitted = bounded(w.defects, limit)
	rep.Findings, rep.FindingsOmitted = bounded(w.findings, limit)
	rep.Verdict = integrity.VerdictClean
	if rep.DefectCount > 0 {
		rep.Verdict = integrity.VerdictDefects
	}
	rep.FinishedAt = time.Now().UTC()
	w.logger().LogAttrs(w.ctx, slog.LevelInfo, "store verification complete",
		slog.String("verdict", string(rep.Verdict)),
		slog.Int("blobs", rep.Checked.Blobs), slog.Int64("bytes", rep.Checked.Bytes),
		slog.Int("manifests", rep.Checked.Manifests), slog.Int("recipes", rep.Checked.Recipes),
		slog.Int("defects", rep.DefectCount), slog.Int("orphans", rep.FindingCount),
		slog.String("requirement", "FR-057"))
	return rep
}

// bounded keeps the first limit entries and counts the rest.
func bounded(l []integrity.Defect, limit int) (listed []integrity.Defect, omitted int) {
	if len(l) <= limit {
		return l, 0
	}
	return l[:limit], len(l) - limit
}
