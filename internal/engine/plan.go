// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"

	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/preflight"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// The plan engine (FR-055, amendment 2026-08-11 / R-04).
//
// A plan answers "what would a synchronization do" without doing any of
// it. The requirement states the guarantee as an absolute — a plan writes
// nothing to the store, pushes nothing, and does not touch the
// passthrough refresh schedule — and this file is built so that the
// guarantee is structural rather than reviewed:
//
//   - The planner holds a PlanStore, not the MetaStore the engine writes
//     through. There is no write method on the value it holds, so a plan
//     that wrote to the store would not compile.
//   - It holds no *schedule.Interval, no task queue, and no task sink.
//     There is nothing here to advance a cadence with (FR-013).
//   - The Destination is used through Status and Repository only — one
//     HEAD and one name check, both of which the promotion path already
//     performs before it decides to push anything.
//
// The remaining risk is the network: a plan reads source registries, and
// reading is not writing. That is deliberate and unavoidable — a plan
// that did not resolve versions would report nothing FR-021 asks for —
// and it is bounded by the same allow-list, the same credentials and the
// same single outbound transport as every other path (FR-030, FR-080).

// PlanStore is the READ-ONLY store surface a plan runs against.
//
// It is deliberately not MetaStore minus a few methods: it is its own
// interface, listing exactly what a plan reads. That is what makes the
// "a plan writes nothing" guarantee a compile-time property instead of a
// convention — the strongest form the requirement can take, and the one
// the lot's central test is there to defend against future edits.
type PlanStore interface {
	// FR-026 inputs: is this digest here, and is the tag on it.
	HasManifest(ctx context.Context, repo, dgst string) bool
	ResolveTag(ctx context.Context, repo, tag string) (string, bool)
	HasBlob(ctx context.Context, repo string, dgst digest.Digest) bool
	// FR-055 inputs: what the store weighs today, and where it lives.
	PhysicalBytes() (int64, error)
	Root() string
	// FR-045 inputs: what is here, who put it there, and how big it is.
	RecipeRecords() ([]store.RecipeRecord, error)
	ProvenanceOf(repo string) (store.Provenance, bool)
	RepoInfo(ctx context.Context, name string) (*store.RepoInfo, error)
}

// PlanConfig is what a plan needs from the instance configuration.
type PlanConfig struct {
	// Mode is the operating mode, reported in the plan (FR-001).
	Mode string
	// BasePrefix is the relocation base prefix (FR-035).
	BasePrefix string
	// MarginPercent is the FR-055 safety margin; zero uses the default.
	MarginPercent int
	// MarginDisabled turns the space and filesystem gate into a report
	// (config.Preflight.Disabled — the FR-075-shaped opt-out).
	MarginDisabled bool
}

// PlanOptions parameterizes one run.
type PlanOptions struct {
	// Retriever overrides the configured source with a candidate: a file
	// path, a URL, an OCI reference — anything LoadRetriever accepts.
	// Empty runs over the configured Retriever (FR-010).
	Retriever string
	// RetrieverDocument is a candidate Retriever supplied inline, as
	// bytes. It wins over Retriever, and it is how the API and the UI
	// accept a document from a form without ever writing it to disk —
	// a plan that spooled its input to a temporary file would be a plan
	// with a side effect.
	RetrieverDocument []byte
	// SkipDestination stops the plan before it contacts the destination.
	// The plan sets it itself on a policy refusal (FR-055 acceptance:
	// "exits with the policy code without contacting the destination");
	// a caller may set it to keep a plan strictly one-sided.
	SkipDestination bool
}

// Planner produces plans. One per instance, safe for concurrent use: the
// only mutable state is the size cache, which is guarded.
type Planner struct {
	store   PlanStore
	remotes *Remotes
	trust   *TrustPolicy
	dest    *Destination
	source  string
	cfg     PlanConfig

	// sizes caches the source-side size facts of one pinned digest.
	//
	// FR-055 allows the computation to be cached because pinned digests
	// make sizes stable, and this cache takes that literally: the key is
	// the digest itself (plus the platform selection that narrows it), so
	// a cached entry cannot describe different content than the one asked
	// for. There is no expiry because there is nothing to expire — an
	// immutable key cannot go stale. What is deliberately NOT cached is
	// the other half of the arithmetic: whether the target already holds
	// a blob changes under the instance's feet, and is re-read on every
	// plan.
	sizesMu sync.Mutex
	sizes   map[string]*contentSizes
}

// planCacheEntries caps the size cache. A store with thousands of pinned
// ingredients would otherwise accumulate one entry per digest ever seen
// for the life of the process; when the cap is reached the cache is
// dropped whole rather than evicted entry by entry, which costs one
// re-walk and keeps the bookkeeping to a line.
const planCacheEntries = 4096

// NewPlanner assembles the plan engine over the read-only store surface.
func NewPlanner(st PlanStore, remotes *Remotes, trust *TrustPolicy, retrieverSource string, cfg PlanConfig) *Planner {
	return &Planner{
		store:   st,
		remotes: remotes,
		trust:   trust,
		source:  retrieverSource,
		cfg:     cfg,
		sizes:   map[string]*contentSizes{},
	}
}

// SetDestination installs the promotion target the plan probes for
// destination-side statuses (FR-026). Nil — the default — means this
// instance promotes nothing, and the plan says so rather than reporting
// an empty destination.
func (p *Planner) SetDestination(d *Destination) { p.dest = d }

// contentSizes is the source-side size fact of one pinned artifact: every
// blob it is made of, by digest, plus the manifest payloads themselves.
type contentSizes struct {
	// blobs maps blob digest to size. Deduplicated by construction.
	blobs map[string]int64
	// manifests maps manifest digest to payload size. Manifests occupy
	// store space too, and on an index with fifty platform children the
	// payloads are not noise.
	manifests map[string]int64
}

// Plan produces the report. It never returns a nil plan: a run that fails
// outright still has an outcome, a source and a problem list to show, and
// a caller that has to distinguish "no report" from "a report saying it
// failed" is a caller that will get it wrong.
func (p *Planner) Plan(ctx context.Context, opts PlanOptions) (*Plan, error) {
	plan := &Plan{
		Mode:              p.cfg.Mode,
		Source:            p.source,
		GeneratedAt:       time.Now().UTC(),
		Destination:       p.dest.Host(),
		Outcome:           OutcomeUpToDate,
		Prune:             PrunePlan{},
		Policy:            PolicyReport{AllowlistDeclared: p.remotes.Allowlist().Declared(), AllowlistPatterns: p.remotes.Allowlist().Patterns()},
		Totals:            PlanTotals{},
		SourceIsCandidate: opts.Retriever != "" || len(opts.RetrieverDocument) > 0,
	}
	if opts.Retriever != "" {
		plan.Source = opts.Retriever
	}
	if len(opts.RetrieverDocument) > 0 {
		plan.Source = "(candidate document)"
	}

	retr, err := p.loadRetriever(ctx, opts)
	if err != nil {
		p.fail(plan, plan.Source, mapEngineError(err, plan.Source))
		return plan, nil
	}
	plan.Zone = retr.Metadata.Name

	// The deduplication ledger of the whole plan: one entry per blob
	// digest, remembering whether ANY target repository is missing it.
	// FR-055 asks for "deduplicated by digest, net of what is already
	// present", and a digest needed by two relocated repositories crosses
	// the wire once — so it counts once, and it counts as needed as soon
	// as one of its repositories lacks it.
	ledger := newDedup()
	hosts := map[string]HostVerdict{}

	resolvedRepos := map[string]bool{}
	allResolved := true
	for i := range retr.Spec.Recipes {
		entry := &retr.Spec.Recipes[i]
		cookbookRef := entry.Cookbook
		if cookbookRef == "" {
			cookbookRef = retr.Spec.Cookbook
		}
		pr := p.planRecipe(ctx, cookbookRef, entry, ledger, hosts, resolvedRepos)
		if pr.Problem != nil {
			allResolved = false
		}
		plan.Recipes = append(plan.Recipes, pr)
	}

	p.summarize(plan, ledger)
	p.recordProblems(plan)
	plan.Policy.Hosts = sortedVerdicts(hosts)

	// FR-055 acceptance: a plan refused by policy exits with the policy
	// code WITHOUT contacting the destination. The check is here, before
	// the destination pass, because that is the only place it can be
	// true rather than merely intended.
	if !opts.SkipDestination && p.dest != nil && plan.Outcome != OutcomePolicyRefused {
		p.probeDestination(ctx, plan, hosts)
		plan.Policy.Hosts = sortedVerdicts(hosts)
	}

	p.planPrune(ctx, plan, resolvedRepos, allResolved)
	p.check(plan)
	p.decideOutcome(plan)
	return plan, nil
}

// loadRetriever resolves the plan's desired-state document: the inline
// candidate, the candidate source, or the configured one (FR-010).
func (p *Planner) loadRetriever(ctx context.Context, opts PlanOptions) (*spec.Retriever, error) {
	if len(opts.RetrieverDocument) > 0 {
		return spec.ParseRetriever(opts.RetrieverDocument)
	}
	source := p.source
	if opts.Retriever != "" {
		source = opts.Retriever
	}
	return LoadRetriever(ctx, p.remotes, source)
}

// planRecipe resolves and measures one Retriever entry. Failures are
// isolated on the entry, exactly as a real run isolates them (§12.3
// point 4): a cookbook that is down does not hide the volume of the four
// recipes that resolved.
func (p *Planner) planRecipe(ctx context.Context, cookbookRef string, entry *spec.RecipeSelector,
	ledger *dedup, hosts map[string]HostVerdict, resolvedRepos map[string]bool,
) PlanRecipe {
	pr := PlanRecipe{
		Name:      entry.Name,
		Requested: entry.Version,
		Cookbook:  cookbookRef,
		Signature: SignatureNotEvaluated,
	}
	p.noteHost(hosts, cookbookRef+"/"+entry.Name, "cookbook")

	cb := NewCookbook(p.remotes, cookbookRef)
	fail := func(err error) PlanRecipe {
		pr.Problem = newProblem(entry.Name, mapEngineError(err, entry.Name))
		return pr
	}

	// FR-021: version resolution, reported as requested → resolved.
	versions, err := cb.Versions(ctx, entry.Name)
	if err != nil {
		return fail(err)
	}
	tag, err := ResolveVersion(entry.Version, versions)
	if err != nil {
		return fail(err)
	}
	pr.Resolved = tag

	fetched, err := cb.FetchArtifact(ctx, entry.Name, tag)
	if err != nil {
		return fail(err)
	}
	pr.Digest = fetched.ManifestDigest

	// FR-033, and the part of it R-04 asks a plan to report: the recipe's
	// own signature verdict is reachable without pulling one byte of
	// ingredient content, so a plan reports it.
	trustRepo, err := nominalRepoOf(cookbookRef, entry.Name)
	if err != nil {
		return fail(err)
	}
	decision := p.trust.Decide(trustRepo)
	pr.TrustScope = decision.Scope
	verified, scope, err := p.verifyRecipe(ctx, fetched, decision)
	pr.TrustScope = scope
	switch {
	case err != nil:
		pr.Signature = SignatureRefused
		return fail(err)
	case verified:
		pr.Signature = SignatureVerified
	default:
		pr.Signature = SignatureUnsignedAdmitted
	}

	if err := cb.LoadDocument(ctx, fetched, entry.Name); err != nil {
		return fail(err)
	}

	// The recipe artifact itself lands in the store as the cookbook of
	// the zone below (cascade), so it is part of the volume.
	if local, lerr := relocate.PathWithBase(p.cfg.BasePrefix, fetched.NominalRepo); lerr == nil {
		resolvedRepos[local] = true
	}

	local := newDedup()
	for i := range fetched.Recipe.Spec.Ingredients {
		ing := &fetched.Recipe.Spec.Ingredients[i]
		pi := p.planIngredient(ctx, ing, ledger, local, hosts)
		if repo := pi.Repo; repo != "" {
			resolvedRepos[repo] = true
		}
		pr.Ingredients = append(pr.Ingredients, pi)
	}
	pr.TotalBytes, pr.TransferBytes, pr.LargestFileBytes = local.totals()
	return pr
}

// verifyRecipe is the plan's copy of the FR-033 verdict. It reads exactly
// what the engine reads and decides exactly what the engine decides — the
// point of a plan is that its verdict is the one the run will reach.
func (p *Planner) verifyRecipe(ctx context.Context, f *FetchedRecipe, d Decision) (verified bool, scope string, err error) {
	e := &Engine{remotes: p.remotes, trust: p.trust}
	return e.verifyRecipe(ctx, f, d)
}

// planIngredient measures one ingredient against the local store.
func (p *Planner) planIngredient(ctx context.Context, ing *spec.Ingredient,
	ledger, local *dedup, hosts map[string]HostVerdict,
) PlanIngredient {
	pi := PlanIngredient{
		Name: ing.Name, Kind: string(ing.Kind), Ref: ing.Ref,
		Version: ing.Version, Digest: ing.Digest,
		Status: StatusUnknown, PushStatus: StatusNotProbed,
	}
	repo, err := relocate.PathWithBase(p.cfg.BasePrefix, ing.Ref)
	if err != nil {
		pi.Problem = newProblem(ing.Ref, mapEngineError(err, ing.Ref))
		return pi
	}
	pi.Repo = repo

	// FR-036: the plan names the endpoint that would actually be
	// contacted, because that is the one FR-030 is evaluated on.
	effective, err := p.remotes.Effective(ing.Ref)
	if err != nil {
		pi.Problem = newProblem(ing.Ref, mapEngineError(err, ing.Ref))
		return pi
	}
	if effective != ing.Ref {
		pi.Effective = effective
	}
	p.noteHost(hosts, ing.Ref, "source")

	// FR-026, computed exactly as the engine computes it before a
	// transfer.
	switch {
	case !p.store.HasManifest(ctx, repo, ing.Digest):
		pi.Status = StatusNew
	default:
		if d, ok := p.store.ResolveTag(ctx, repo, ing.Version); ok && d == ing.Digest {
			pi.Status = StatusUpToDate
		} else {
			pi.Status = StatusOutdated
		}
	}

	sizes, err := p.contentOf(ctx, ing)
	if err != nil {
		pi.Problem = newProblem(ing.Ref, mapEngineError(err, ing.Ref))
		return pi
	}

	// Presence is asked of the store per blob, not inferred from the
	// status: an interrupted run leaves an ingredient "new" with most of
	// its layers already committed, and charging the operator for those
	// bytes again would overstate every resumed transfer (FR-029).
	for d, size := range sizes.blobs {
		present := p.store.HasBlob(ctx, repo, digest.Digest(d))
		ledger.add(d, size, !present)
		local.add(d, size, !present)
	}
	for d, size := range sizes.manifests {
		present := p.store.HasManifest(ctx, repo, d)
		ledger.add(d, size, !present)
		local.add(d, size, !present)
	}
	pi.TotalBytes, pi.TransferBytes, pi.LargestFileBytes = sizes.ingredientTotals(ctx, p.store, repo)
	return pi
}

// contentOf resolves the source-side size facts of one ingredient,
// through the digest-keyed cache.
func (p *Planner) contentOf(ctx context.Context, ing *spec.Ingredient) (*contentSizes, error) {
	key := ing.Digest + "|" + strings.Join(ing.Platforms, ",")
	p.sizesMu.Lock()
	if c, ok := p.sizes[key]; ok {
		p.sizesMu.Unlock()
		return c, nil
	}
	p.sizesMu.Unlock()

	c, err := p.walk(ctx, ing)
	if err != nil {
		return nil, err
	}

	p.sizesMu.Lock()
	if len(p.sizes) >= planCacheEntries {
		p.sizes = map[string]*contentSizes{}
	}
	p.sizes[key] = c
	p.sizesMu.Unlock()
	return c, nil
}

// walk reads the manifest tree of one ingredient from the source and
// collects every blob it is made of. Manifests only: not one content byte
// is fetched, which is what makes the volume computation cheap enough to
// run before every synchronization.
func (p *Planner) walk(ctx context.Context, ing *spec.Ingredient) (*contentSizes, error) {
	c := &contentSizes{blobs: map[string]int64{}, manifests: map[string]int64{}}
	desc, err := p.remotes.Get(ctx, ing.Ref, ing.Digest)
	if err != nil {
		return nil, err
	}
	// The same verdict the transfer path reaches (FR-033): a source that
	// answers a different digest than the recipe pinned is refused here,
	// before the plan reports a volume for content nobody vouched for.
	if desc.Digest.String() != ing.Digest {
		return nil, taxonomy.New(taxonomy.CodeDigestMismatch, taxonomy.Params{
			"reference": ing.Ref, "expected": ing.Digest, "actual": desc.Digest.String()})
	}

	var selected map[string]bool
	if desc.MediaType.IsIndex() {
		selected, err = selectPlatforms(desc, ing)
		if err != nil {
			return nil, err
		}
	}
	if err := p.collect(ctx, c, ing.Ref, desc.Digest.String(), desc.Manifest, string(desc.MediaType), selected, 0); err != nil {
		return nil, err
	}
	return c, nil
}

// maxIndexDepth bounds the recursion of collect. Nested indexes are legal
// and rare (an attestation index inside a platform index is two levels);
// a source that answers an index referring to itself would otherwise walk
// forever, and a plan is reachable from an authenticated HTTP request.
const maxIndexDepth = 8

// collect walks one manifest payload, recursing through index children.
func (p *Planner) collect(ctx context.Context, c *contentSizes, ref, dgst string,
	payload []byte, mediaType string, selected map[string]bool, depth int,
) error {
	if depth > maxIndexDepth {
		return fmt.Errorf("index nesting of %s exceeds %d levels", ref, maxIndexDepth)
	}
	if _, seen := c.manifests[dgst]; seen {
		return nil
	}
	c.manifests[dgst] = int64(len(payload))

	if isIndexMediaType(mediaType) {
		var idx struct {
			Manifests []blobDescriptor `json:"manifests"`
		}
		if err := json.Unmarshal(payload, &idx); err != nil {
			return fmt.Errorf("parsing index %s: %w", ref, err)
		}
		for _, child := range idx.Manifests {
			if selected != nil && !selected[child.Digest] {
				continue
			}
			cd, err := p.remotes.Get(ctx, ref, child.Digest)
			if err != nil {
				return err
			}
			// Platform selection applies to the top level only: once a
			// child has been selected, everything under it travels.
			if err := p.collect(ctx, c, ref, cd.Digest.String(), cd.Manifest, string(cd.MediaType), nil, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	var man artifactManifest
	if err := json.Unmarshal(payload, &man); err != nil {
		return fmt.Errorf("parsing manifest %s: %w", ref, err)
	}
	if man.Config.Digest != "" {
		c.blobs[man.Config.Digest] = man.Config.Size
	}
	for _, l := range man.Layers {
		if l.Digest != "" {
			c.blobs[l.Digest] = l.Size
		}
	}
	return nil
}

// isIndexMediaType reports an image index or a Docker manifest list.
func isIndexMediaType(mt string) bool {
	return strings.Contains(mt, "image.index") || strings.Contains(mt, "manifest.list")
}

// ingredientTotals sums one ingredient's own figures against a target
// repository: the gross size, the part the store is missing, and the
// largest single file that would be written.
func (c *contentSizes) ingredientTotals(ctx context.Context, st PlanStore, repo string) (total, transfer, largest int64) {
	for d, size := range c.blobs {
		total += size
		if !st.HasBlob(ctx, repo, digest.Digest(d)) {
			transfer += size
			if size > largest {
				largest = size
			}
		}
	}
	for d, size := range c.manifests {
		total += size
		if !st.HasManifest(ctx, repo, d) {
			transfer += size
		}
	}
	return total, transfer, largest
}

// dedup is the by-digest ledger FR-055 asks the totals to be computed on.
type dedup struct {
	entries map[string]*dedupEntry
}

type dedupEntry struct {
	size   int64
	needed bool
}

func newDedup() *dedup { return &dedup{entries: map[string]*dedupEntry{}} }

// add records one digest. A digest already recorded keeps its size — two
// answers for one digest cannot both be right, and the first is as good
// as the second — and becomes "needed" as soon as one target lacks it.
func (d *dedup) add(dgst string, size int64, needed bool) {
	e, ok := d.entries[dgst]
	if !ok {
		d.entries[dgst] = &dedupEntry{size: size, needed: needed}
		return
	}
	e.needed = e.needed || needed
}

// totals returns the gross size, the size to transfer, and the largest
// single file among the ones that would be written.
func (d *dedup) totals() (total, transfer, largest int64) {
	for _, e := range d.entries {
		total += e.size
		if e.needed {
			transfer += e.size
			if e.size > largest {
				largest = e.size
			}
		}
	}
	return total, transfer, largest
}

// summarize fills the plan totals from the deduplicated ledger.
func (p *Planner) summarize(plan *Plan, ledger *dedup) {
	t := &plan.Totals
	t.Recipes = len(plan.Recipes)
	for i := range plan.Recipes {
		for j := range plan.Recipes[i].Ingredients {
			t.Ingredients++
			switch plan.Recipes[i].Ingredients[j].Status {
			case StatusNew:
				t.New++
			case StatusOutdated:
				t.Outdated++
			case StatusUpToDate:
				t.UpToDate++
			case StatusUnknown, StatusNotProbed:
				// Counted in Ingredients, in no status bucket: a status
				// that could not be established is not a fourth kind of
				// answer, it is the absence of one.
			}
		}
	}
	t.TotalBytes, t.TransferBytes, t.LargestFileBytes = ledger.totals()

	current, err := p.store.PhysicalBytes()
	if err != nil {
		plan.Problems = append(plan.Problems, *newProblem(p.store.Root(),
			taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": err.Error()})))
		return
	}
	t.StoreBytes = current
	t.ProjectedStoreBytes = current + t.TransferBytes
}

// recordProblems lifts every recipe- and ingredient-scoped problem into
// the flat list, so a consumer never has to walk the tree to find out
// whether anything went wrong.
func (p *Planner) recordProblems(plan *Plan) {
	for i := range plan.Recipes {
		if pb := plan.Recipes[i].Problem; pb != nil {
			plan.Problems = append(plan.Problems, *pb)
		}
		for j := range plan.Recipes[i].Ingredients {
			if pb := plan.Recipes[i].Ingredients[j].Problem; pb != nil {
				plan.Problems = append(plan.Problems, *pb)
			}
		}
	}
	// The outcome is provisional here: the destination pass and the
	// pre-flight checks can still worsen it, never improve it.
	plan.Outcome = worstOutcome(plan.Problems)
}

// worstOutcome folds the problem list into an outcome, severity first.
func worstOutcome(problems []Problem) Outcome {
	out := OutcomeUpToDate
	rank := map[Outcome]int{OutcomeUpToDate: 0, OutcomeChangesPlanned: 0, OutcomeFailed: 1, OutcomePolicyRefused: 2, OutcomeVerificationFailed: 3}
	for i := range problems {
		var candidate Outcome
		switch problems[i].Class {
		case "policy":
			candidate = OutcomePolicyRefused
		case "verification":
			candidate = OutcomeVerificationFailed
		default:
			candidate = OutcomeFailed
		}
		if rank[candidate] > rank[out] {
			out = candidate
		}
	}
	return out
}

// probeDestination adds the destination-side FR-026 statuses.
//
// One HEAD per artifact and nothing else: that is what the promotion path
// itself does to decide whether to push (push.go), so a plan that did
// more would report a cost the run does not pay. It is also why the
// destination figure is an upper bound rather than a measurement — see
// PlanTotals.PushUpperBoundBytes.
func (p *Planner) probeDestination(ctx context.Context, plan *Plan, hosts map[string]HostVerdict) {
	plan.Totals.PushEvaluated = true
	for i := range plan.Recipes {
		for j := range plan.Recipes[i].Ingredients {
			pi := &plan.Recipes[i].Ingredients[j]
			if pi.Repo == "" || pi.Digest == "" {
				continue
			}
			repo, err := p.dest.Repository(pi.Repo)
			if err != nil {
				pi.PushStatus = StatusUnknown
				pb := newProblem(pi.Ref, mapEngineError(err, pi.Ref))
				plan.Problems = append(plan.Problems, *pb)
				if pi.Problem == nil {
					pi.Problem = pb
				}
				continue
			}
			hosts[repo.RegistryStr()] = HostVerdict{
				Host: repo.RegistryStr(), Role: "destination",
				Allowed: p.remotes.Allowlist().Allows(repo.RegistryStr()),
			}
			status, err := p.dest.Status(ctx, repo, pi.Version, pi.Digest)
			if err != nil {
				pi.PushStatus = StatusUnknown
				pb := newProblem(pi.Ref, mapEngineError(err, pi.Ref))
				plan.Problems = append(plan.Problems, *pb)
				continue
			}
			pi.PushStatus = statusOf(status)
			if pi.PushStatus != StatusUpToDate {
				plan.Totals.PushUpperBoundBytes += pi.TotalBytes
			}
		}
	}
	plan.Outcome = worstOutcome(plan.Problems)
}

// statusOf maps an importer status onto the plan's glossary. They are the
// same words; the conversion exists so the report does not depend on the
// importer's type.
func statusOf(s importer.PlatformStatus) ItemStatus {
	switch s {
	case importer.StatusNew:
		return StatusNew
	case importer.StatusOutdated:
		return StatusOutdated
	case importer.StatusUpToDate:
		return StatusUpToDate
	default:
		return StatusUnknown
	}
}

// planPrune projects the FR-045 prune: recipe-managed content the newly
// resolved Retriever no longer references.
//
// It refuses to project anything when even one recipe failed to resolve,
// and that refusal is the whole point of the function. A prune list is
// read as "this is no longer wanted"; computed from a partial resolution
// it would say exactly that about content whose only crime was that its
// cookbook was unreachable for thirty seconds. Better a report that says
// "not evaluated, and here is why" than a confident wrong list.
func (p *Planner) planPrune(ctx context.Context, plan *Plan, resolved map[string]bool, allResolved bool) {
	if !allResolved {
		plan.Prune.Reason = "at least one recipe of the Retriever did not resolve: projecting a prune from a partial resolution would propose deleting content that is still wanted"
		return
	}
	records, err := p.store.RecipeRecords()
	if err != nil {
		plan.Prune.Reason = "the recipe graph could not be read: " + err.Error()
		plan.Problems = append(plan.Problems, *newProblem(p.store.Root(),
			taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": err.Error()})))
		return
	}
	plan.Prune.Evaluated = true

	// Every repository the store currently holds on behalf of a recipe,
	// with the recipe that brought it.
	held := map[string]string{}
	for i := range records {
		rec := &records[i]
		if local, err := relocate.PathWithBase(p.cfg.BasePrefix, rec.CookbookRepo); err == nil {
			held[local] = rec.Name
		}
		for j := range rec.Ingredients {
			if repo := rec.Ingredients[j].Repo; repo != "" {
				held[repo] = rec.Name
			}
		}
	}

	var candidates []PruneEntry
	for repo, recipe := range held {
		if resolved[repo] {
			continue
		}
		// FR-045 protects everything that did not come from a recipe run:
		// unit imports, the offline vulnerability database, and content
		// seeded through /v2/. The provenance ledger is the authority.
		if prov, ok := p.store.ProvenanceOf(repo); ok && prov.Class != store.ProvenanceRecipe {
			continue
		}
		entry := PruneEntry{Repo: repo, Recipe: recipe}
		if info, err := p.store.RepoInfo(ctx, repo); err == nil {
			entry.Bytes = info.Size
		} else if !errors.Is(err, store.ErrNotFound) {
			plan.Problems = append(plan.Problems, *newProblem(repo,
				taxonomy.New(taxonomy.CodeStoreRead, taxonomy.Params{"detail": err.Error()})))
			continue
		} else {
			// The graph names a repository the store no longer holds:
			// nothing to prune, and nothing to report either.
			continue
		}
		candidates = append(candidates, entry)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Repo < candidates[j].Repo })
	plan.Prune.Repositories = candidates
	for i := range candidates {
		plan.Prune.TotalBytes += candidates[i].Bytes
	}
}

// check runs the FR-055 space and filesystem verdicts on every target the
// plan would write to.
func (p *Planner) check(plan *Plan) {
	c, refusal := preflight.Evaluate(preflight.System, preflight.Request{
		Target:           preflight.TargetStore,
		Path:             p.store.Root(),
		ProjectedBytes:   plan.Totals.TransferBytes,
		LargestFileBytes: plan.Totals.LargestFileBytes,
		MarginPercent:    p.cfg.MarginPercent,
	})
	if refusal != nil && p.cfg.MarginDisabled {
		// The FR-075-shaped opt-out: the verdict is still computed and
		// still reported — with its refusal code intact, so nobody can
		// mistake a disabled gate for a passed one — and it no longer
		// stops anything.
		plan.Checks = append(plan.Checks, c)
		return
	}
	if refusal != nil {
		plan.Problems = append(plan.Problems, *newProblem(p.store.Root(), refusal))
	}
	plan.Checks = append(plan.Checks, c)
}

// decideOutcome settles the plan's verdict once everything is known.
func (p *Planner) decideOutcome(plan *Plan) {
	plan.Outcome = worstOutcome(plan.Problems)
	if plan.Outcome == OutcomeUpToDate && plan.HasChanges() {
		plan.Outcome = OutcomeChangesPlanned
	}
}

// fail records a whole-plan failure — a Retriever that cannot be loaded
// at all, which is the one condition a real run fails on outright.
func (p *Planner) fail(plan *Plan, subject string, e *taxonomy.Error) {
	plan.Problems = append(plan.Problems, *newProblem(subject, e))
	plan.Outcome = worstOutcome(plan.Problems)
}

// noteHost records the allow-list verdict on the host of a reference.
func (p *Planner) noteHost(hosts map[string]HostVerdict, ref, role string) {
	effective, err := p.remotes.Effective(ref)
	if err != nil {
		return
	}
	host, _, _ := strings.Cut(effective, "/")
	if host == "" {
		return
	}
	hosts[host] = HostVerdict{Host: host, Role: role, Allowed: p.remotes.Allowlist().Allows(host)}
}

// sortedVerdicts renders the host map in a stable order.
func sortedVerdicts(hosts map[string]HostVerdict) []HostVerdict {
	out := make([]HostVerdict, 0, len(hosts))
	for _, v := range hosts {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}
