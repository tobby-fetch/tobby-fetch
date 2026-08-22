// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/tobby-fetch/recipe-spec/cookbook"
	spec "github.com/tobby-fetch/recipe-spec/recipe/v1alpha1"

	"github.com/tobby-fetch/tobby-fetch/internal/importer"
	"github.com/tobby-fetch/tobby-fetch/internal/relocate"
	"github.com/tobby-fetch/tobby-fetch/internal/sigverify"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// pushItemPrefix names the promotion half of a synchronization in the
// task's item list. Fetching and promoting one ingredient are two
// operations with two outcomes — content can land locally and fail to
// cross — and collapsing them onto one item would hide exactly the
// failure an operator of a promotion service needs to see.
const pushItemPrefix = "push/"

// promoteRecipe pushes one synchronized recipe into the destination zone:
// its ingredients first, then the recipe artifact itself with its
// signatures (FR-034), so the zone's cookbook never advertises a recipe
// whose content has not arrived.
//
// Every push is preceded, in this order, by the three refusals that must
// happen before a connection or a byte: the allowlist (FR-030) and the
// name bounds (FR-035) inside Destination.Repository, the destination's
// own acceptance of the relocated name (FR-035), and the re-verification
// of the signature over the exact bytes about to leave (FR-033).
//
// Failures are isolated per item, like the fetch side: one ingredient
// that cannot cross does not stop the others, and the recipe artifact is
// simply not published — which is the point of publishing it last.
func (e *Engine) promoteRecipe(ctx context.Context, sink *taskSink, logger *slog.Logger, f *FetchedRecipe, rid string) {
	if e.dest == nil {
		return
	}
	logger = logger.With(slog.String("destination", e.dest.Host()))

	// FR-033/ADR-0007: the recipe's signature is re-verified against the
	// local copy before anything of this recipe is pushed. "It was
	// verified at import" is not an argument on a service that runs
	// unattended for months: what is on disk now is what is about to
	// cross, and between the import and this cycle the store has been
	// writable, backed up, restored, and possibly transported.
	local, err := relocate.PathWithBase(e.base, f.NominalRepo)
	if err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", err, rid)
		return
	}
	if err := e.verifyStored(ctx, local, f.NominalRepo, f.ManifestDigest, f.NominalRepo+":"+f.Tag); err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", err, rid)
		return
	}

	allPushed := true
	for i := range f.Recipe.Spec.Ingredients {
		ing := &f.Recipe.Spec.Ingredients[i]
		if err := e.promoteIngredient(ctx, sink, logger, ing, rid); err != nil {
			allPushed = false
		}
	}
	if !allPushed {
		// FR-034 says each zone's cookbook reflects what the zone actually
		// holds. Publishing the recipe now would state that the zone holds
		// content it does not, and the next zone down would resolve the
		// recipe and fail on the missing ingredient instead of simply not
		// seeing the version yet.
		logger.LogAttrs(ctx, slog.LevelWarn, "recipe not propagated: some ingredients did not cross",
			slog.String("recipe", rid), slog.String("requirement", "FR-034"))
		return
	}
	e.propagateRecipe(ctx, sink, logger, f, local, rid)
}

// promoteIngredient pushes one ingredient to its relocated destination
// repository, transferring only what is missing (FR-028).
//
// Unlike the fetch side, a resumed task (FR-029) does NOT skip an item
// that was already marked done: it re-asks the destination. The two are
// not inconsistent — they trust the cheapest reliable authority in each
// case. The fetch side owns its store and can believe its own record; on
// this side the record says what a previous process believed about a
// registry it does not own, and one HEAD replaces that belief with the
// current truth. Re-asking costs a request and can only ever prevent a
// missing push; believing the record can only ever hide one.
func (e *Engine) promoteIngredient(ctx context.Context, sink *taskSink, logger *slog.Logger, ing *spec.Ingredient, rid string) error {
	logger = logger.With(slog.String("ingredient", ing.Name), slog.String("digest", ing.Digest))
	itemName := pushItemPrefix + rid + "/" + ing.Name

	local, err := relocate.PathWithBase(e.base, ing.Ref)
	if err != nil {
		return e.pushFailed(sink, logger, rid, ing.Name, err, ing.Ref)
	}
	dst, err := e.dest.Repository(local)
	if err != nil {
		return e.pushFailed(sink, logger, rid, ing.Name, err, ing.Ref)
	}
	if err := e.dest.Accepts(ctx, dst); err != nil {
		return e.pushFailed(sink, logger, rid, ing.Name, err, ing.Ref)
	}
	// An ingredient that carries a signature locally must still verify
	// before it crosses. One that carries none rides on the recipe's own
	// signature, which pins this exact digest (§12.2): that chain was
	// established at import and re-established above, on this same cycle.
	if err := e.verifySignedStored(ctx, local, ing.Ref, ing.Digest, ing.Ref+"@"+ing.Digest); err != nil {
		return e.pushFailed(sink, logger, rid, ing.Name, err, ing.Ref)
	}

	// FR-026, against the destination and before any transfer.
	status, err := e.dest.Status(ctx, dst, ing.Version, ing.Digest)
	if err != nil {
		return e.pushFailed(sink, logger, rid, ing.Name, err, ing.Ref)
	}
	res := tasks.Resolution{
		Recipe: rid, Ingredient: ing.Name, Requested: ing.Version, Resolved: ing.Version,
		Digest: ing.Digest, Destination: dst.String(), DestinationStatus: string(status),
	}
	if status == importer.StatusUpToDate {
		sink.update(func(t *tasks.Task) bool {
			itemFor(t, itemName).Status = tasks.StatusSkipped
			t.Resolutions = append(t.Resolutions, res)
			return true
		})
		e.meter(e.meters.PushSkipped)
		logger.LogAttrs(ctx, slog.LevelDebug, "ingredient already up to date on the destination",
			slog.String("repository", dst.String()), slog.String("requirement", "FR-026"))
		return nil
	}

	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, itemName)
		item.Status = tasks.StatusRunning
		item.Digest = ing.Digest
		return true
	})

	var moved int64
	push := func() error {
		n, perr := e.dest.PushManifest(ctx, e.store, local, dst, ing.Digest, ing.Version)
		moved += n
		return perr
	}
	err = withRetries(ctx, e.cfg.Retries, push)
	if err != nil && sparseIndexRefused(err) {
		// FR-022, second sentence: the sparse index preserves the pinned
		// digest, "if the destination registry rejects sparse indexes, all
		// platforms SHALL be transferred instead". The refusal is the only
		// way to learn which kind of destination this is — no API states
		// it — so the widening happens here, once the destination has
		// actually said no, and never pre-emptively.
		n, ferr := e.completeIndex(ctx, ing, local)
		if ferr != nil {
			return e.pushFailed(sink, logger, rid, ing.Name, ferr, ing.Ref)
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "destination rejects sparse indexes: transferring every platform",
			slog.String("repository", dst.String()), slog.Any("selected", ing.Platforms),
			slog.Int64("completed_bytes", n), slog.String("requirement", "FR-022"))
		if e.meters.BytesMoved != nil && n > 0 {
			e.meters.BytesMoved(n)
		}
		err = withRetries(ctx, e.cfg.Retries, push)
	}
	if err != nil {
		return e.pushFailed(sink, logger, rid, ing.Name, err, ing.Ref)
	}
	// §12.2: signature artifacts travel with the content they attest.
	moved += e.pushAttachedSignatures(ctx, logger, local, dst, ing.Digest)

	res.PushedBytes = moved
	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, itemName)
		item.Status = tasks.StatusDone
		item.SizeBytes = moved
		t.Resolutions = append(t.Resolutions, res)
		return true
	})
	e.meter(e.meters.PushDone)
	if e.meters.PushedBytes != nil && moved > 0 {
		e.meters.PushedBytes(moved)
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "ingredient promoted",
		slog.String("repository", dst.String()), slog.String("status", string(status)),
		slog.Int64("pushed_bytes", moved), slog.String("requirement", "FR-028"))
	return nil
}

// sparseIndexRefused reports the destination refusing an index because it
// does not hold every manifest the index lists.
//
// A registry that validates index members answers the manifest PUT with
// one of these codes — the OCI distribution error catalog has no code
// meaning "sparse indexes unsupported", so the shape of the refusal is
// what identifies it. Deliberately narrow: only a manifest write can
// produce these, and a blob upload failing for an unrelated reason must
// not be mistaken for a policy this registry does not actually have.
func sparseIndexRefused(err error) bool {
	var te *transport.Error
	if !errors.As(err, &te) {
		return false
	}
	for _, d := range te.Errors {
		switch string(d.Code) {
		case "MANIFEST_BLOB_UNKNOWN", "MANIFEST_UNKNOWN", "MANIFEST_INVALID", "MANIFEST_UNVERIFIED":
			return true
		}
	}
	return false
}

// completeIndex fetches every platform of an ingredient's index into the
// store, filling in the ones a platform selection left out (FR-022).
// Children already present are skipped, so this only ever moves what the
// selection excluded — and on the next cycle it moves nothing at all,
// because the store now holds the complete index.
func (e *Engine) completeIndex(ctx context.Context, ing *spec.Ingredient, localRepo string) (int64, error) {
	desc, err := e.remotes.Get(ctx, ing.Ref, ing.Digest)
	if err != nil {
		return 0, err
	}
	if !desc.MediaType.IsIndex() {
		// Not an index, so sparseness cannot be what the destination
		// objected to. Nothing is fetched and the retry re-raises the
		// destination's own refusal, which says more than a guess would.
		return 0, nil
	}
	// No task item watches this repair, so it carries no progress sink —
	// the transfer is still resumable, it is simply not narrated.
	bl, err := e.blobsFor(ing.Ref, nil)
	if err != nil {
		return 0, err
	}
	return copyIndexChildren(ctx, e.store, localRepo, ing.Version, desc, nil, bl)
}

// propagateRecipe re-publishes the recipe artifact in the zone's own
// cookbook (FR-034), with its signatures.
//
// What a recipe artifact must look like is the format's business, so the
// SDK owns both halves used here: VerifyManifest refuses to push anything
// that is not a §11.2 artifact — a promotion service must not be the way
// arbitrary content enters a cookbook — and DecideRepublication states
// what writing to an existing tag means. A cooked recipe is immutable
// (§8): the same digest is a no-op, a different one is a conflict that
// requires a new metadata.version, and inventing a third answer here
// would make this implementation disagree with every other.
func (e *Engine) propagateRecipe(ctx context.Context, sink *taskSink, logger *slog.Logger, f *FetchedRecipe, local, rid string) {
	itemName := pushItemPrefix + rid + "/recipe"
	tag, err := e.dest.CookbookTag(f.Recipe.Metadata.Name, f.Recipe.Metadata.Version)
	if err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", err, rid)
		return
	}
	dst := tag.Context()
	if err := e.dest.Accepts(ctx, dst); err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", err, rid)
		return
	}

	payload, _, dgst, err := e.store.RawManifest(ctx, local, f.Tag)
	if err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", err, rid)
		return
	}
	layout, err := cookbook.VerifyManifest(payload)
	if err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", validationError(tag.String(), err), rid)
		return
	}

	res := tasks.Resolution{
		Recipe: rid, Requested: f.Tag, Resolved: f.Tag, Digest: dgst,
		Destination: tag.String(),
	}
	switch existing, headErr := remote.Head(tag, e.dest.options(ctx)...); {
	case headErr == nil:
		// An equal manifest digest — the steady state, since this engine
		// published the tag on an earlier cycle — settles it for one HEAD.
		// An unequal one is not yet a conflict: manifest bytes are not
		// stable across publishing tools (§11.2, B-018), so the format's
		// comparison is on the document layer, read back with one GET.
		identical := existing.Digest.String() == dgst
		if !identical {
			published, getErr := remote.Get(tag, e.dest.options(ctx)...)
			if getErr != nil {
				_ = e.pushFailed(sink, logger, rid, "recipe", getErr, rid)
				return
			}
			remoteLayout, layoutErr := cookbook.VerifyManifest(published.Manifest)
			if layoutErr != nil {
				// The tag is held by something that is not a recipe
				// artifact; §8 forbids re-pointing it all the same.
				_ = e.pushFailed(sink, logger, rid, "recipe", taxonomy.New(taxonomy.CodeTagImmutable, taxonomy.Params{
					"reference": tag.String(),
					"published": existing.Digest.String(),
					"candidate": dgst,
				}), rid)
				return
			}
			identical = cookbook.DecideRepublication(remoteLayout.Document.Digest, layout.Document.Digest) == cookbook.RepublicationIdentical
			if !identical {
				_ = e.pushFailed(sink, logger, rid, "recipe", taxonomy.New(taxonomy.CodeTagImmutable, taxonomy.Params{
					"reference": tag.String(),
					"published": remoteLayout.Document.Digest,
					"candidate": layout.Document.Digest,
				}), rid)
				return
			}
		}
		res.DestinationStatus = string(importer.StatusUpToDate)
		sink.update(func(t *tasks.Task) bool {
			itemFor(t, itemName).Status = tasks.StatusSkipped
			t.Resolutions = append(t.Resolutions, res)
			return true
		})
		e.meter(e.meters.PushSkipped)
		return
	case !isNotFound(headErr):
		_ = e.pushFailed(sink, logger, rid, "recipe", headErr, rid)
		return
	}
	res.DestinationStatus = string(importer.StatusNew)

	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, itemName)
		item.Status = tasks.StatusRunning
		item.Digest = dgst
		return true
	})

	moved, err := e.dest.PushManifest(ctx, e.store, local, dst, f.Tag, f.Tag)
	if err != nil {
		_ = e.pushFailed(sink, logger, rid, "recipe", err, rid)
		return
	}
	// FR-034 acceptance: the propagated recipe is verifiable with cosign
	// at its destination, which requires its signature to travel with it.
	moved += e.pushAttachedSignatures(ctx, logger, local, dst, dgst)

	res.PushedBytes = moved
	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, itemName)
		item.Status = tasks.StatusDone
		item.SizeBytes = moved
		t.Resolutions = append(t.Resolutions, res)
		return true
	})
	e.meter(e.meters.PushDone)
	if e.meters.PushedBytes != nil && moved > 0 {
		e.meters.PushedBytes(moved)
	}
	logger.LogAttrs(ctx, slog.LevelInfo, "recipe propagated to the zone cookbook",
		slog.String("reference", tag.String()), slog.String("digest", dgst),
		slog.Int64("pushed_bytes", moved), slog.String("requirement", "FR-034"))
}

// pushAttachedSignatures copies whatever signature artifacts the local
// store holds for a subject onto the destination (§12.2, ADR-0007), so
// that FR-034's acceptance holds where it is checked: the promoted
// artifact is verifiable with cosign AT ITS DESTINATION.
//
// Two tags are tried, because a store acquires content two ways. The
// recipe engine copies the classic attached tag, so that is what a
// promoted recipe normally carries; content pushed into this instance by
// a standard client (FR-041) may instead carry the OCI referrers fallback
// index, and dropping it on the way out would strip a signature this
// instance never had a hand in.
//
// Absence is not an error — the trust decision already ran, twice — and
// neither is a failure to copy: the content itself has landed, and
// refusing the whole promotion over a signature artifact would trade a
// verifiable destination for an empty one. The failure is logged, which
// is what lets an operator notice a destination that keeps losing them.
func (e *Engine) pushAttachedSignatures(ctx context.Context, logger *slog.Logger, local string, dst name.Repository, subjectDigest string) int64 {
	var moved int64
	for _, tag := range signatureTags(subjectDigest) {
		n, err := e.dest.PushManifest(ctx, e.store, local, dst, tag, tag)
		moved += n
		switch {
		case err == nil:
		case errors.Is(err, store.ErrNotFound):
			// This layout is simply not the one the publisher used.
		default:
			logger.LogAttrs(ctx, slog.LevelWarn, "signature artifact not propagated",
				slog.String("repository", dst.String()), slog.String("tag", tag),
				slog.String("error", err.Error()))
		}
	}
	return moved
}

// signatureTags lists the tags under which a subject's signatures may sit
// in a repository: the classic cosign attached tag (§12.2) and the OCI
// referrers fallback tag (cosign 3.x bundles on registries without the
// Referrers API).
func signatureTags(subjectDigest string) []string {
	fallback := referrersFallbackTag(subjectDigest)
	return []string{SignatureTag(subjectDigest), fallback}
}

// referrersFallbackTag is the OCI 1.1 fallback tag of a subject digest:
// "sha256-<hex>", an index of the manifests referring to it.
func referrersFallbackTag(subjectDigest string) string {
	return strings.Replace(subjectDigest, "sha256:", "sha256-", 1)
}

// verifyStored re-verifies a subject's signature against the LOCAL copy,
// under the same trust decision that admitted it (FR-033, RECIPE-SPEC
// §12.3). Strict: an unsigned subject passes only inside a declared
// allowUnsigned scope, exactly as at import — there is no "already
// checked" shortcut, because the requirement asks for a check before
// every push and a promotion service performs a great many of them.
func (e *Engine) verifyStored(ctx context.Context, localRepo, nominalRepo, manifestDigest, subject string) error {
	d := e.trust.Decide(nominalRepo)
	src := &storeManifests{src: e.store, repo: localRepo}
	if d.Keys != nil {
		_, err := sigverify.Verify(ctx, src, localRepo, manifestDigest, d.Keys)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, sigverify.ErrNoSignature) && d.AllowUnsigned:
			return nil
		default:
			return err
		}
	}
	if d.AllowUnsigned {
		return nil
	}
	return taxonomy.New(taxonomy.CodeSignature, taxonomy.Params{
		"recipe":       subject,
		"fingerprints": "none — no trust root is configured",
	})
}

// verifySignedStored is verifyStored for subjects whose signature is
// optional: it verifies what is there and stays silent about what is
// not. Ingredients are pinned by digest inside a recipe whose own
// signature covers them, so an unsigned ingredient is not an unverified
// one; an ingredient with a signature that does NOT verify is, and that
// one never crosses.
func (e *Engine) verifySignedStored(ctx context.Context, localRepo, nominalRepo, manifestDigest, subject string) error {
	d := e.trust.Decide(nominalRepo)
	if d.Keys == nil {
		return nil
	}
	src := &storeManifests{src: e.store, repo: localRepo}
	_, err := sigverify.Verify(ctx, src, localRepo, manifestDigest, d.Keys)
	if err == nil || errors.Is(err, sigverify.ErrNoSignature) {
		return nil
	}
	return fmt.Errorf("re-verifying %s before pushing it: %w", subject, err)
}

// pushFailed records one promotion failure on its task item and returns
// the error, so callers can both report and propagate in one statement.
func (e *Engine) pushFailed(sink *taskSink, logger *slog.Logger, rid, itemSuffix string, err error, subject string) error {
	te := e.mapError(err, subject)
	sink.update(func(t *tasks.Task) bool {
		item := itemFor(t, pushItemPrefix+rid+"/"+itemSuffix)
		item.Status = tasks.StatusFailed
		item.Error = tasks.FromTaxonomy(te)
		return true
	})
	if e.meters.PushRefused != nil {
		e.meters.PushRefused(string(te.Code()))
	}
	logger.LogAttrs(context.Background(), slog.LevelWarn, "promotion refused",
		slog.String("subject", subject), slog.String("code", string(te.Code())),
		slog.String("error", te.Error()))
	return te
}

// meter calls an optional counter hook.
func (e *Engine) meter(f func()) {
	if f != nil {
		f()
	}
}

// storeManifests adapts the embedded store to sigverify.Manifests: the
// verifier is source-agnostic by design (FR-052 replays the same checks
// destination-side), so re-verifying what is on disk is the same code
// path that verified it over the network, not a second implementation
// that could drift from it.
//
// It deliberately does NOT implement ReferrersLister: the embedded store
// has no referrers index. The bundle layout still resolves, through the
// OCI fallback tag the verifier reads on its own — which is the tag the
// fetch side copies alongside the content.
type storeManifests struct {
	src  StoreReader
	repo string
}

func (m *storeManifests) Manifest(ctx context.Context, _, reference string) (payload []byte, mediaType, dgst string, err error) {
	payload, mediaType, dgst, err = m.src.RawManifest(ctx, m.repo, reference)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", "", sigverify.ErrNotFound
	}
	return payload, mediaType, dgst, err
}

func (m *storeManifests) Blob(ctx context.Context, _, dgst string) ([]byte, error) {
	rc, err := m.src.BlobReader(ctx, m.repo, dgst)
	if errors.Is(err, store.ErrNotFound) {
		return nil, sigverify.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read side
	return readBounded(rc, sigverify.MaxPayloadSize)
}
