// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// maxManifestBytes bounds a manifest or index read from the medium. OCI
// manifests are kilobytes; anything past a megabyte is hostile or corrupt,
// and the walk refuses it rather than allocating for it (NFR-007).
const maxManifestBytes = 1 << 20

// Reader is the read surface verification needs from the transported
// store: the directory itself, and the manifest/blob accessors the
// signature verifier runs on. *store.Store implements it.
type Reader interface {
	// Root is the store directory — the medium (FR-050).
	Root() string
	// RawManifest returns the exact stored bytes of a manifest.
	RawManifest(ctx context.Context, name, reference string) (payload []byte, mediaType, dgst string, err error)
	// BlobReader opens one blob of a repository.
	BlobReader(ctx context.Context, name, dgst string) (io.ReadCloser, error)
}

// TrustDecision is the destination instance's verdict on one nominal
// cookbook repository: which keys apply, and whether a declared scope
// admits the recipe unsigned.
type TrustDecision struct {
	// AllowUnsigned admits an unsigned recipe — only ever true inside the
	// named declared scope (FR-033: no undeclared bypass exists).
	AllowUnsigned bool
	// Scope names the matching declared scope; empty for the strict
	// default.
	Scope string
	// Keys is the key set to verify against. Nil means no trust root
	// applies, which — outside a declared scope — blocks the recipe.
	Keys *sigverify.Keys
}

// Trust is the destination instance's trust policy.
//
// It is an interface, and it is the destination's: FR-054 is explicit that
// trust roots present on the medium are ignored, so nothing in this
// package reads key material from the store it verifies. The engine's
// TrustPolicy satisfies this shape through a three-line adapter; the
// indirection exists so the media package and the recipe engine stay
// independent of each other.
type Trust interface {
	// Decide returns the verdict for a recipe's nominal cookbook
	// repository, in the canonical form trust scopes match (ADR-0013).
	Decide(nominalRepo string) TrustDecision
}

// VerifyOptions parameterizes one verification.
type VerifyOptions struct {
	// Zone is the identity of the zone THIS instance serves. A medium
	// addressed elsewhere is blocked (FR-054).
	Zone string
	// Trust is the destination instance's trust policy. Nil means no
	// trust root is configured, and then nothing verifies: every recipe is
	// blocked, which is the secure default (FR-075).
	Trust Trust
	// LastImport is the freshness record for Zone, from the state
	// directory's register (R-28). Nil when the zone has never imported.
	LastImport *ImportRecord
	// AllowZoneMismatch proceeds despite a zone mismatch. An explicit,
	// visible, logged administrator override; the caller writes the FR-094
	// audit record, which needs the actor and the origin this package does
	// not have.
	AllowZoneMismatch bool
	// AllowStale proceeds despite the medium being older than the last
	// import. Same override discipline.
	AllowStale bool
	// Progress receives progress notifications, if non-nil (FR-054:
	// verification progress is displayed).
	Progress func(Progress)
	// Logger records the overrides that were applied. Nil logs nothing.
	Logger *slog.Logger
}

// Verify checks a transported store and returns the structured report the
// UI, the API and the CLI all render (FR-061, FR-066).
//
// It writes nothing and pushes nothing: it is the step that runs BEFORE
// any push, any serving and any local write (FR-054). The error return is
// for infrastructure failures only — a store directory that cannot be
// opened at all. Everything the medium itself got wrong comes back inside
// the report.
func Verify(ctx context.Context, src Reader, opts VerifyOptions) (*Report, error) {
	rep := &Report{StartedAt: time.Now().UTC(), Verdict: VerdictBlocked}
	root, err := os.OpenRoot(src.Root())
	if err != nil {
		return nil, fmt.Errorf("media: opening the transported store: %w", err)
	}
	defer root.Close() //nolint:errcheck // read side

	report(opts.Progress, Progress{Stage: StageManifest})
	m, block := readManifest(root)
	if block != nil {
		rep.Blocks = append(rep.Blocks, *block)
		return finish(rep), nil
	}
	rep.Media = &Info{
		MediaID: m.MediaID, Zone: m.Zone,
		MediaFormat: m.MediaFormat, StoreFormat: m.StoreFormat,
		ProducedBy: m.ProducedBy, ResolvedAt: m.ResolvedAt, WrittenAt: m.WrittenAt,
		Totals: m.Totals,
	}
	rep.Zone = ZoneCheck{Expected: opts.Zone, Found: m.Zone, Match: m.Zone == opts.Zone}

	c := &checker{
		ctx: ctx, root: root, progress: opts.Progress,
		inventory: make(map[string]File, len(m.Inventory)),
		results:   map[string]*Reason{},
		reached:   map[string]bool{},
		total:     m.Totals,
	}
	for i := range m.Inventory {
		c.inventory[m.Inventory[i].Path] = m.Inventory[i]
	}

	rep.Blocks = append(rep.Blocks, globalBlocks(c, m, &opts, rep)...)
	if standing(rep.Blocks) {
		// A blocked medium is not walked: with no inventory to reason
		// about, or a medium addressed to another zone, there is nothing
		// a per-recipe verdict could mean (R-19).
		rep.Checked = Totals{Files: c.files, Bytes: c.bytes}
		return finish(rep), nil
	}

	rep.Recipes = verifyRecipes(ctx, src, c, m, opts)
	rep.Findings = append(rep.Findings, c.findings(m)...)
	rep.Checked = Totals{Files: c.files, Bytes: c.bytes}
	return finish(rep), nil
}

// finish computes the medium-wide verdict and closes the report.
func finish(rep *Report) *Report {
	rep.FinishedAt = time.Now().UTC()
	if standing(rep.Blocks) {
		rep.Verdict = VerdictBlocked
		return rep
	}
	pushable, blocked := 0, 0
	for i := range rep.Recipes {
		if rep.Recipes[i].Pushable {
			pushable++
		} else {
			blocked++
		}
	}
	switch {
	case blocked == 0:
		rep.Verdict = VerdictPushable
	case pushable == 0:
		rep.Verdict = VerdictBlocked
	default:
		rep.Verdict = VerdictPartial
	}
	return rep
}

// standing reports whether any block was not overridden.
func standing(blocks []Block) bool {
	for _, b := range blocks {
		if !b.Overridden {
			return true
		}
	}
	return false
}

// readManifest reads and validates meta/media.json. A nil block means the
// manifest is usable.
func readManifest(root *os.Root) (*Manifest, *Block) {
	f, err := root.Open(filepath.FromSlash(ManifestPath))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &Block{Code: taxonomy.CodeMediaManifestMissing, Params: map[string]string{"path": ManifestPath}}
	case err != nil:
		return nil, unreadable(err.Error())
	}
	defer f.Close() //nolint:errcheck // read side

	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes*64))
	if err != nil {
		return nil, unreadable(err.Error())
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		// Unknown fields are refused deliberately: a manifest carrying a
		// field this build does not understand is a manifest this build
		// cannot vouch for, and the format version below is how a future
		// layout announces itself.
		return nil, unreadable(err.Error())
	}
	if err := validate(&m); err != nil {
		return nil, unreadable(err.Error())
	}
	if m.MediaFormat != ManifestFormat {
		return nil, &Block{Code: taxonomy.CodeMediaFormatUnsupported, Params: map[string]string{
			"found": strconv.Itoa(m.MediaFormat), "supported": strconv.Itoa(ManifestFormat),
		}}
	}
	if m.StoreFormat != store.FormatVersion {
		return nil, &Block{Code: taxonomy.CodeMediaStoreFormat, Params: map[string]string{
			"found": strconv.Itoa(m.StoreFormat), "supported": strconv.Itoa(store.FormatVersion),
		}}
	}
	return &m, nil
}

func unreadable(detail string) *Block {
	return &Block{Code: taxonomy.CodeMediaManifestUnreadable, Params: map[string]string{
		"path": ManifestPath, "detail": detail,
	}}
}

// validate refuses a manifest that is internally inconsistent: a path
// escaping the store, a duplicated inventory entry, a repository name that
// is really a traversal, a digest that is not one. Every such refusal is
// "unparseable, or internally inconsistent" in the sense of R-19 — it
// blocks the medium as a whole, before anything is opened.
func validate(m *Manifest) error {
	seen := make(map[string]bool, len(m.Inventory))
	for i := range m.Inventory {
		e := &m.Inventory[i]
		if err := checkInventoryPath(e.Path); err != nil {
			return err
		}
		if seen[e.Path] {
			return fmt.Errorf("inventory lists %q twice", e.Path)
		}
		seen[e.Path] = true
		if e.Size < 0 {
			return fmt.Errorf("inventory entry %q declares a negative size", e.Path)
		}
		if _, err := digestHex(e.Digest); err != nil {
			return fmt.Errorf("inventory entry %q: %w", e.Path, err)
		}
	}
	for i := range m.Recipes {
		if err := validateRecipe(&m.Recipes[i]); err != nil {
			return err
		}
	}
	return nil
}

func validateRecipe(r *Recipe) error {
	if r.Name == "" || r.Version == "" {
		return fmt.Errorf("a recipe entry has no name or no version")
	}
	id := r.Name + "@" + r.Version
	if _, err := relocate.Path(r.CookbookRepo); err != nil {
		return fmt.Errorf("recipe %s: cookbook repository %q is not a nominal reference: %w", id, r.CookbookRepo, err)
	}
	if err := checkRepoName(r.ArtifactRepo); err != nil {
		return fmt.Errorf("recipe %s: %w", id, err)
	}
	if r.ArtifactTag != "" {
		if err := checkTag(r.ArtifactTag); err != nil {
			return fmt.Errorf("recipe %s: %w", id, err)
		}
	}
	if _, err := digestHex(r.Digest); err != nil {
		return fmt.Errorf("recipe %s: %w", id, err)
	}
	for i := range r.Ingredients {
		ing := &r.Ingredients[i]
		if err := checkRepoName(ing.Repo); err != nil {
			return fmt.Errorf("recipe %s, ingredient %s: %w", id, ing.Name, err)
		}
		if ing.Tag != "" {
			if err := checkTag(ing.Tag); err != nil {
				return fmt.Errorf("recipe %s, ingredient %s: %w", id, ing.Name, err)
			}
		}
		if _, err := digestHex(ing.Digest); err != nil {
			return fmt.Errorf("recipe %s, ingredient %s: %w", id, ing.Name, err)
		}
	}
	return nil
}

// globalBlocks runs the medium-wide checks in the order FR-054 fixes: the
// recipe graph, then the zone, then the freshness guard.
func globalBlocks(c *checker, m *Manifest, opts *VerifyOptions, rep *Report) []Block {
	var blocks []Block

	// The recipe graph is the reachability set every per-recipe verdict is
	// computed from, so an altered one leaves nothing to reason about: it
	// blocks the whole medium, with no override (R-19).
	if reason := c.check(recipesFile, ""); reason != nil {
		params := map[string]string{"path": recipesFile, "expected": "", "actual": ""}
		params["expected"] = reason.Params["expected"]
		params["actual"] = reason.Params["actual"]
		blocks = append(blocks, Block{Code: taxonomy.CodeMediaGraphAltered, Params: params})
	}

	if !rep.Zone.Match {
		b := Block{
			Code:        taxonomy.CodeMediaZoneMismatch,
			Params:      map[string]string{"expected": opts.Zone, "found": m.Zone},
			Overridable: true, Overridden: opts.AllowZoneMismatch,
		}
		logOverride(opts, b, "zone identity")
		blocks = append(blocks, b)
	}

	if opts.LastImport != nil {
		fresh := &FreshnessCheck{
			Resolved: m.ResolvedAt, Recorded: opts.LastImport.ResolvedAt,
			RecordedMediaID: opts.LastImport.MediaID,
			Stale:           m.ResolvedAt.Before(opts.LastImport.ResolvedAt),
		}
		rep.Freshness = fresh
		if fresh.Stale {
			b := Block{
				Code: taxonomy.CodeMediaStale,
				Params: map[string]string{
					"zone": opts.Zone, "media": m.MediaID,
					"resolved": fresh.Resolved.Format(time.RFC3339),
					"recorded": fresh.Recorded.Format(time.RFC3339),
				},
				Overridable: true, Overridden: opts.AllowStale,
			}
			logOverride(opts, b, "media freshness")
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// logOverride makes an applied override visible in the instance logs
// (FR-075: a removal of a barrier is explicit, visible and journalled).
// The FR-094 audit record stays with the caller, which knows the actor.
func logOverride(opts *VerifyOptions, b Block, guard string) {
	if !b.Overridden || opts.Logger == nil {
		return
	}
	opts.Logger.LogAttrs(context.Background(), slog.LevelWarn, "media verification guard overridden",
		slog.String("guard", guard), slog.String("code", string(b.Code)))
}

// verifyRecipes takes the per-recipe decisions (R-19): completeness and
// checksums first, then the signature, in the order FR-054 fixes.
func verifyRecipes(ctx context.Context, src Reader, c *checker, m *Manifest, opts VerifyOptions) []RecipeVerdict {
	out := make([]RecipeVerdict, 0, len(m.Recipes))
	for i := range m.Recipes {
		r := &m.Recipes[i]
		id := r.Name + "@" + r.Version
		report(opts.Progress, Progress{
			Stage: StageRecipes, Recipe: id,
			Files: c.files, TotalFiles: c.total.Files,
			Bytes: c.bytes, TotalBytes: c.total.Bytes,
		})
		v := RecipeVerdict{
			Name: r.Name, Version: r.Version,
			CookbookRepo: r.CookbookRepo, ArtifactRepo: r.ArtifactRepo,
			Digest: r.Digest, ResolvedAt: r.ResolvedAt,
		}
		w := &walk{c: c, recipe: id, seen: map[string]bool{}}
		w.delivery(r)
		v.Files, v.Bytes = w.files, w.bytes
		if w.reason != nil {
			v.Reason = w.reason
			out = append(out, v)
			continue
		}
		if err := ctx.Err(); err != nil {
			v.Reason = &Reason{Code: taxonomy.CodeMediaContentUnreadable, Params: map[string]string{
				"path": r.ArtifactRepo, "detail": err.Error(),
			}}
			out = append(out, v)
			continue
		}
		decide(ctx, src, r, &v, opts.Trust)
		out = append(out, v)
	}
	return out
}

// decide applies the signature verdict to a recipe whose content already
// checked out: the signature is verified against the DESTINATION's trust
// roots (FR-054), over the bytes this walk has just re-hashed.
func decide(ctx context.Context, src Reader, r *Recipe, v *RecipeVerdict, trust Trust) {
	// Trust scopes match the canonical nominal repository, never the
	// relocated path the medium happens to use (FR-036, ADR-0013).
	nominal, err := relocate.Path(r.CookbookRepo)
	if err != nil {
		nominal = r.CookbookRepo
	}
	var d TrustDecision
	if trust != nil {
		d = trust.Decide(nominal)
	}
	v.TrustScope = d.Scope

	if d.Keys == nil {
		if d.AllowUnsigned {
			v.Pushable, v.Unsigned = true, true
			return
		}
		v.Reason = &Reason{Code: taxonomy.CodeSignature, Params: map[string]string{
			"recipe": nominal, "fingerprints": "none — no trust root is configured",
		}}
		return
	}
	fingerprint, err := sigverify.Verify(ctx, &manifestSource{src: src, repo: r.ArtifactRepo}, r.ArtifactRepo, r.Digest, d.Keys)
	switch {
	case err == nil:
		v.Pushable, v.KeyFingerprint = true, fingerprint
	case errors.Is(err, sigverify.ErrNoSignature) && d.AllowUnsigned:
		v.Pushable, v.Unsigned = true, true
	default:
		v.Reason = &Reason{Code: taxonomy.CodeSignature, Params: map[string]string{
			"recipe": nominal, "fingerprints": fingerprintsOf(err),
		}}
	}
}

// fingerprintsOf renders the signature failure's detail the way FR-033
// asks for it: the keys tried, or what was wrong with the artifact.
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

// checker re-hashes the medium, once per file however many recipes reach
// it, and remembers what it found.
type checker struct {
	ctx       context.Context
	root      *os.Root
	inventory map[string]File
	// results caches each path's verdict: nil means it checked out.
	results map[string]*Reason
	// reached is the union over every recipe, pushable or not — the
	// counter-set of the "reached by no recipe" finding.
	reached  map[string]bool
	progress func(Progress)
	total    Totals
	files    int
	bytes    int64
}

// check verifies one covered file against its inventory entry and, for a
// blob, against the digest its own path claims. It returns nil when the
// file is sound. recipe names the delivery for the message; it may be
// empty for the store's own bookkeeping.
func (c *checker) check(p, recipe string) *Reason {
	c.reached[p] = true
	if r, done := c.results[p]; done {
		return withRecipe(r, recipe)
	}
	reason := c.checkUncached(p, recipe)
	c.results[p] = reason
	return reason
}

func (c *checker) checkUncached(p, recipe string) *Reason {
	entry, listed := c.inventory[p]
	if !listed {
		return &Reason{Code: taxonomy.CodeMediaFileUninventoried, Path: p,
			Params: map[string]string{"path": p, "recipe": recipe}}
	}
	f, err := c.root.Open(filepath.FromSlash(p))
	if err != nil {
		// Anything that does not open — absent, a dangling symlink, a
		// path the os.Root refused to follow out of the store — is
		// missing content as far as the push is concerned.
		return &Reason{Code: taxonomy.CodeMediaFileMissing, Path: p,
			Params: map[string]string{"path": p, "recipe": recipe}}
	}
	defer f.Close() //nolint:errcheck // read side

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return &Reason{Code: taxonomy.CodeMediaFileMissing, Path: p,
			Params: map[string]string{"path": p, "recipe": recipe}}
	}
	if info.Size() != entry.Size {
		return &Reason{Code: taxonomy.CodeMediaFileSize, Path: p, Params: map[string]string{
			"path": p, "expected": strconv.FormatInt(entry.Size, 10), "actual": strconv.FormatInt(info.Size(), 10),
		}}
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return &Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: p, Params: map[string]string{
			"path": p, "detail": err.Error(),
		}}
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	c.files++
	c.bytes += n
	report(c.progress, Progress{
		Stage: StageRecipes, Files: c.files, TotalFiles: c.total.Files,
		Bytes: c.bytes, TotalBytes: c.total.Bytes,
	})
	if got != entry.Digest {
		return &Reason{Code: taxonomy.CodeMediaFileDigest, Path: p, Params: map[string]string{
			"path": p, "expected": entry.Digest, "actual": got,
		}}
	}
	// Content-addressed storage must also agree with itself: a blob whose
	// bytes do not hash to the digest its path claims would be served
	// under someone else's identity, inventory or no inventory.
	if want, ok := hexOfBlobPath(p); ok && got != "sha256:"+want {
		return &Reason{Code: taxonomy.CodeMediaContentAddress, Path: p, Params: map[string]string{
			"path": p, "expected": "sha256:" + want, "actual": got,
		}}
	}
	return nil
}

// withRecipe stamps a cached verdict with the delivery asking about it, so
// a shared blob names the recipe the operator is looking at.
func withRecipe(r *Reason, recipe string) *Reason {
	if r == nil || recipe == "" {
		return r
	}
	if _, ok := r.Params["recipe"]; !ok {
		return r
	}
	clone := *r
	clone.Params = make(map[string]string, len(r.Params))
	for k, v := range r.Params {
		clone.Params[k] = v
	}
	clone.Params["recipe"] = recipe
	return &clone
}

// exists reports whether an optional file is on the medium at all.
func (c *checker) exists(p string) bool {
	info, err := c.root.Stat(filepath.FromSlash(p))
	return err == nil && info.Mode().IsRegular()
}

// findings collects the non-blocking observations: content under coverage
// the inventory does not list, inventoried content no recipe reaches, and
// bookkeeping that does not match its entry.
//
// Content reached only by a BLOCKED recipe is deliberately not repeated
// here: the recipe verdict already names it and states that it will not be
// pushed. Repeating it file by file would bury the one line an operator
// needs under thousands.
func (c *checker) findings(m *Manifest) []Finding {
	var out []Finding

	for i := range m.Inventory {
		p := m.Inventory[i].Path
		switch {
		case strings.HasPrefix(p, metaPrefix+"/"):
			// The store's own bookkeeping: verified, never pushed, and
			// never "reached" by a recipe — it IS the graph.
			if p == recipesFile {
				continue // already decided, globally
			}
			if reason := c.check(p, ""); reason != nil {
				out = append(out, Finding{Code: taxonomy.CodeMediaMetadataAltered, Path: p,
					Params: map[string]string{"path": p}})
			}
		case !c.reached[p]:
			out = append(out, Finding{Code: taxonomy.CodeMediaUnreachable, Path: p,
				Params: map[string]string{"path": p}})
		}
	}

	report(c.progress, Progress{Stage: StageExtraneous, Files: c.files, Bytes: c.bytes})
	for _, p := range c.uncovered() {
		out = append(out, Finding{Code: taxonomy.CodeMediaUncovered, Path: p,
			Params: map[string]string{"path": p}})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Code != out[b].Code {
			return out[a].Code < out[b].Code
		}
		return out[a].Path < out[b].Path
	})
	return out
}

// uncovered walks the covered roots on disk and returns the files the
// inventory does not list.
func (c *checker) uncovered() []string {
	var out []string
	for _, sub := range coveredRoots {
		root := c.root.FS()
		err := fs.WalkDir(root, sub, func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return fs.SkipAll
			case err != nil:
				return err
			}
			if err := c.ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !covered(p) {
				return nil
			}
			if _, listed := c.inventory[p]; !listed {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			// A directory that cannot be walked is reported as one
			// uncovered entry rather than swallowed: the operator has to
			// know the sweep was incomplete, and it blocks nothing.
			out = append(out, sub+" (not fully readable: "+err.Error()+")")
		}
	}
	sort.Strings(out)
	return out
}

// walk is the reachability walk of one recipe: what this delivery reaches
// on the medium, checked as it is discovered. It stops at the first
// failure — the recipe is blocked whole either way (R-19), and the report
// names the file that decided it.
type walk struct {
	c      *checker
	recipe string
	seen   map[string]bool
	reason *Reason
	files  int
	bytes  int64
}

// delivery walks the four things a delivery is made of: the recipe
// artifact, its signature artifacts, its ingredients, and everything those
// reach.
func (w *walk) delivery(r *Recipe) {
	w.root(r.ArtifactRepo, r.Digest, r.ArtifactTag)
	w.signatures(r.ArtifactRepo, r.Digest)
	for i := range r.Ingredients {
		ing := &r.Ingredients[i]
		w.root(ing.Repo, ing.Digest, ing.Tag)
	}
}

// root walks one required manifest root and its tag references.
func (w *walk) root(repo, dgst, tag string) {
	if w.reason != nil {
		return
	}
	if tag != "" {
		w.file(tagCurrentPath(repo, tag))
		// The tag's index directory holds what the tag has pointed at
		// over time. Present entries are checked and counted as reached;
		// an absent one is neither missing content nor extraneous, so
		// nothing here is required.
		w.optionalTree(tagIndexDir(repo, tag))
	}
	w.manifest(repo, dgst, true)
}

// signatures walks the cosign artifacts that travel with the recipe
// (RECIPE-SPEC §12.2), in both published layouts. Their absence is not a
// completeness failure — the signature verdict is taken separately, and an
// unsigned recipe fails there with a message an operator can act on
// instead of "a file is missing".
func (w *walk) signatures(repo, dgst string) {
	h, err := digestHex(dgst)
	if err != nil {
		return
	}
	for _, tag := range []string{"sha256-" + h + ".sig", "sha256-" + h} {
		if !w.c.exists(tagCurrentPath(repo, tag)) {
			continue
		}
		w.file(tagCurrentPath(repo, tag))
		w.optionalTree(tagIndexDir(repo, tag))
		if d, ok := w.currentDigest(repo, tag); ok {
			w.manifest(repo, d, true)
		}
	}
}

// currentDigest reads what a tag points at, from the link file the backend
// writes ("sha256:<hex>").
func (w *walk) currentDigest(repo, tag string) (string, bool) {
	f, err := w.c.root.Open(filepath.FromSlash(tagCurrentPath(repo, tag)))
	if err != nil {
		return "", false
	}
	defer f.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "", false
	}
	d := strings.TrimSpace(string(raw))
	if _, err := digestHex(d); err != nil {
		return "", false
	}
	return d, true
}

// manifest walks one manifest: its per-repository reference, its bytes,
// and everything it names. required says whether its absence blocks —
// index children are NOT required, because a platform-filtered index
// legitimately keeps its pinned digest while carrying only some of its
// children (FR-022).
func (w *walk) manifest(repo, dgst string, required bool) {
	if w.reason != nil {
		return
	}
	key := repo + "@" + dgst
	if w.seen[key] {
		return
	}
	w.seen[key] = true

	h, err := digestHex(dgst)
	if err != nil {
		w.fail(&Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: repo, Params: map[string]string{
			"path": repo, "detail": err.Error(),
		}})
		return
	}
	link, blob := manifestLinkPath(repo, h), blobPath(h)
	if !required && !w.c.exists(blob) {
		// A sparse index child the medium legitimately does not carry.
		return
	}
	w.file(link)
	w.file(blob)
	if w.reason != nil {
		return
	}

	raw, err := w.read(blob)
	if err != nil {
		w.fail(&Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: blob, Params: map[string]string{
			"path": blob, "detail": err.Error(),
		}})
		return
	}
	var doc ociDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		w.fail(&Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: blob, Params: map[string]string{
			"path": blob, "detail": err.Error(),
		}})
		return
	}
	for i := range doc.Manifests {
		w.manifest(repo, doc.Manifests[i].Digest, false)
	}
	if doc.Config != nil {
		w.blob(repo, doc.Config.Digest)
	}
	for i := range doc.Layers {
		w.blob(repo, doc.Layers[i].Digest)
	}
}

// blob walks one non-manifest blob: its per-repository link and its bytes.
// It is never parsed — a layer is content, not structure.
func (w *walk) blob(repo, dgst string) {
	if w.reason != nil {
		return
	}
	h, err := digestHex(dgst)
	if err != nil {
		w.fail(&Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: repo, Params: map[string]string{
			"path": repo, "detail": err.Error(),
		}})
		return
	}
	w.file(layerLinkPath(repo, h))
	w.file(blobPath(h))
}

// file checks one required file and accounts for it.
func (w *walk) file(p string) {
	if w.reason != nil {
		return
	}
	if err := w.c.ctx.Err(); err != nil {
		w.fail(&Reason{Code: taxonomy.CodeMediaContentUnreadable, Path: p, Params: map[string]string{
			"path": p, "detail": err.Error(),
		}})
		return
	}
	if entry, ok := w.c.inventory[p]; ok {
		w.files++
		w.bytes += entry.Size
	}
	if reason := w.c.check(p, w.recipe); reason != nil {
		w.fail(reason)
	}
}

// optionalTree marks every file under a directory as reached and checks
// the ones that are there, without requiring any of them.
func (w *walk) optionalTree(dir string) {
	entries, err := fs.ReadDir(w.c.root.FS(), dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			w.optionalTree(dir + "/" + e.Name())
			continue
		}
		p := dir + "/" + e.Name()
		if _, listed := w.c.inventory[p]; !listed {
			// Not inventoried: the extraneous sweep reports it; requiring
			// it here would block a recipe over a stale tag entry.
			w.c.reached[p] = true
			continue
		}
		if reason := w.c.check(p, w.recipe); reason != nil {
			w.fail(reason)
			return
		}
	}
}

// read returns the bytes of a file already checked against its inventory
// entry, bounded (NFR-007).
func (w *walk) read(p string) ([]byte, error) {
	f, err := w.c.root.Open(filepath.FromSlash(p))
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read side
	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("larger than the %d-byte manifest bound", maxManifestBytes)
	}
	return raw, nil
}

func (w *walk) fail(r *Reason) {
	if w.reason == nil {
		w.reason = r
	}
}

// ociDocument is the subset of an OCI manifest or index the walk reads: it
// only ever needs to know what a document points at.
type ociDocument struct {
	Config    *ociDescriptor  `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
	Manifests []ociDescriptor `json:"manifests"`
}

type ociDescriptor struct {
	Digest string `json:"digest"`
}

// manifestSource adapts a transported store to sigverify.Manifests, so the
// signature checks that ran at fetch time run again here, on the same code
// path (FR-052: the verifier is reusable offline on an opened store).
//
// It deliberately implements no ReferrersLister: the embedded store has no
// referrers index, and the bundle layout still resolves through the OCI
// fallback tag the fetch side copies alongside the content.
type manifestSource struct {
	src  Reader
	repo string
}

func (m *manifestSource) Manifest(ctx context.Context, _, reference string) (payload []byte, mediaType, dgst string, err error) {
	payload, mediaType, dgst, err = m.src.RawManifest(ctx, m.repo, reference)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", "", sigverify.ErrNotFound
	}
	return payload, mediaType, dgst, err
}

func (m *manifestSource) Blob(ctx context.Context, _, dgst string) ([]byte, error) {
	rc, err := m.src.BlobReader(ctx, m.repo, dgst)
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
		return nil, fmt.Errorf("media: blob %s exceeds the %d-byte signature payload bound", dgst, sigverify.MaxPayloadSize)
	}
	return raw, nil
}
