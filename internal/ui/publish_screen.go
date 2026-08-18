// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// Recipe publication screen (R-40, UI-SPEC §6): paste or drop a recipe
// document, publish it into a cookbook as the RECIPE-SPEC §11.2 artifact,
// read back the digest.
//
// This screen is the interface half of `tobby recipe push` (internal/cli/
// recipe.go) and the return leg of R-37: the document is copied off a
// recipe's manifest page, edited offline, and comes back in without a
// terminal. It is deliberately thin. What a publishable recipe IS belongs
// to the format and lives in the recipe-spec SDK (cookbook.Build, which
// validates the cooked profile, the §11.3 name/version agreement, and the
// document bound); how to talk to a registry lives in engine.Publisher.
// Neither is re-implemented here, because a second implementation of
// "which documents are refused" is a second answer to that question.
//
// The screen is operator-gated: publishing writes into another zone's
// registry. It never suggests that Tobby signs anything — Tobby holds no
// private key (ADR-0007) — so the success state shows the published digest
// and the `cosign sign` line that takes it, exactly like the subcommand.

package ui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/audit"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/engine"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// publishTimeout bounds one publication: a validation, one HEAD, two blob
// uploads and a manifest PUT against a registry that may be behind a
// corporate proxy. Deliberately its own constant rather than the import
// screen's inspection budget — those are two different remote shapes, and
// sharing a number is how one of them silently gets the wrong one.
const publishTimeout = 90 * time.Second

// Publisher is the publishing side of the engine, as this screen needs
// it. An interface rather than *engine.Publisher so the screen is
// testable without a registry — and so the screen holds exactly one verb,
// which is all a surface should be able to do.
type Publisher interface {
	PublishRecipe(ctx context.Context, ref string, doc []byte) (*engine.PublishResult, error)
}

// publishData feeds the /recipes/publish page.
type publishData struct {
	// Reference is the destination, preserved across a refusal so a
	// rejected submission is edited rather than retyped.
	Reference string
	// Document is the submitted YAML, likewise preserved. A recipe
	// document is not a secret — it is published to a registry — so
	// echoing it back is a convenience, not an exposure.
	Document string
	// Result carries the success state: what was published, and the
	// signing line that follows it.
	Result *publishResult
	// Configured is false when this instance was wired without a
	// publisher; the form is then rendered inert rather than accepting a
	// submission that could not go anywhere.
	Configured bool
}

// publishResult is the success state of one publication.
type publishResult struct {
	Reference string
	Digest    string
	// Unchanged marks the §8 no-op: the tag already pointed at this exact
	// content. Said explicitly rather than reported as a fresh
	// publication — an operator re-submitting a document needs to know
	// nothing moved.
	Unchanged bool
	// CosignCommand is the ready-to-run signing line. It targets the
	// DIGEST, because cosign signs digests and because a tag would let
	// the signature drift off the content it vouches for.
	CosignCommand string
}

// recipePublishScreen serves GET /recipes/publish.
func (u *UI) recipePublishScreen(w http.ResponseWriter, r *http.Request) {
	u.render.Page(w, r, "recipe-publish", u.publishScreenData())
}

// publishScreenData is the empty form state.
func (u *UI) publishScreenData() *publishData {
	d := &publishData{Configured: u.publisher != nil}
	if u.destination != "" && u.cookbook != "" {
		// The prefix, not a full reference: the name and the version are
		// carried by the document's own metadata (§11.3), so proposing
		// them here would be proposing a value the SDK is about to check
		// against the file.
		d.Reference = u.destination + "/" + strings.Trim(u.cookbook, "/") + "/"
	}
	return d
}

// recipePublishSubmit serves POST /recipes/publish: validate and publish
// through engine.Publisher, audit either way (FR-094 — publishing is
// outbound writing), and render the digest.
//
// Every refusal reaches the screen as its catalog entry with its stable
// code, because every refusal is already taxonomized upstream: a document
// that is not a cooked recipe or whose name contradicts the destination
// comes back as TBY-VAL-001, a tag that already holds different content as
// TBY-POL-004 (§8 immutability), a destination outside the allowlist as
// TBY-POL-001 (FR-030), an unusable reference as TBY-REG-001.
// The submitted body is already bounded by the time this runs, twice
// over: the anti-forgery check in the session gate has parsed the form,
// which caps a urlencoded body at net/http's own limit, and
// cookbook.Build refuses any document above the format's bound. There is
// deliberately no third bound here — a http.MaxBytesReader installed
// after the parse would read as a control while doing nothing.
func (u *UI) recipePublishSubmit(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.PostFormValue("reference"))
	doc := r.PostFormValue("document")
	id, _ := auth.IdentityFrom(r.Context())

	if u.publisher == nil {
		u.publishRefusal(w, r, ref, doc, taxonomy.New(taxonomy.CodeInternal, nil).
			WithCause(errors.New("no publisher is wired on this instance")))
		return
	}
	if ref == "" || strings.TrimSpace(doc) == "" {
		u.auditPublish(r, id.Name, ref, audit.OutcomeFailure)
		u.publishRefusal(w, r, ref, doc, taxonomy.New(taxonomy.CodeValidation, taxonomy.Params{
			"file":       "the submitted document",
			"path":       "metadata",
			"constraint": "a destination reference and a recipe document are both required",
		}))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), publishTimeout)
	defer cancel()
	res, err := u.publisher.PublishRecipe(ctx, ref, []byte(doc))
	if err != nil {
		u.auditPublish(r, id.Name, ref, publishOutcome(err))
		u.publishRefusal(w, r, ref, doc, publishError(ref, err))
		return
	}
	u.auditPublish(r, id.Name, res.Reference, audit.OutcomeSuccess)

	d := u.publishScreenData()
	d.Reference, d.Document = res.Reference, doc
	d.Result = &publishResult{
		Reference:     res.Reference,
		Digest:        res.Digest,
		Unchanged:     res.Unchanged,
		CosignCommand: cosignCommand(res.Reference, res.Digest),
	}
	v := u.render.view(r, d)
	key := "publish.toast_published"
	if res.Unchanged {
		key = "publish.toast_unchanged"
	}
	v.Toasts = append(v.Toasts, v.T(key, "Reference", res.Reference))
	u.render.render(w, r, "recipe-publish", http.StatusOK, v)
}

// publishRefusal re-renders the screen with the taxonomized block and the
// entry's real HTTP status, the submitted values preserved.
func (u *UI) publishRefusal(w http.ResponseWriter, r *http.Request, ref, doc string, e *taxonomy.Error) {
	d := u.publishScreenData()
	d.Reference, d.Document = ref, doc
	v := u.render.view(r, d)
	v.Err = errView(v.Lang, e)
	u.render.render(w, r, "recipe-publish", v.Err.Status, v)
}

// publishError maps a publication failure onto the catalog. Everything
// the SDK and the publisher refuse is already a taxonomy error and is
// passed through untouched; what is left is a registry that did not
// answer, which is a transport condition and is named as one rather than
// hidden behind the generic internal error.
func publishError(ref string, err error) *taxonomy.Error {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te
	}
	return taxonomy.New(taxonomy.CodeRegistryUnreachable,
		taxonomy.Params{"host": registryHostOf(ref)}).WithCause(err)
}

// publishOutcome distinguishes the two negative outcomes of the FR-094
// schema: a policy barrier refused the write (denied) — the allowlist and
// the §8 immutability rule are barriers — or it simply did not work.
func publishOutcome(err error) string {
	var te *taxonomy.Error
	if errors.As(err, &te) && te.Entry().Class == taxonomy.ClassPolicy {
		return audit.OutcomeDenied
	}
	return audit.OutcomeFailure
}

// auditPublish emits the FR-094 record: the authenticated identity as
// actor, the destination reference as target, the real network origin.
func (u *UI) auditPublish(r *http.Request, actor, ref, outcome string) {
	audit.Log(r.Context(), u.logger, &audit.Event{
		Actor: actor, Action: audit.ActionRecipePublish, Target: ref,
		Outcome: outcome, Origin: auth.ClientOrigin(r),
	})
}

// cosignCommand renders the signing line for a published artifact.
//
// It exists so the screen states plainly what Tobby did NOT do. Tobby
// never holds a private key (ADR-0007), so publishing produces an unsigned
// artifact and the operator signs it themselves; the command targets the
// digest for the same reason the subcommand does — cosign signs digests,
// and a tag can be moved.
func cosignCommand(ref, digest string) string {
	return "cosign sign --key <key> --use-signing-config=false --tlog-upload=false " +
		repositoryOf(ref) + "@" + digest
}

// repositoryOf strips the tag from a published reference. The tag is what
// follows the LAST colon, and only when that colon comes after the last
// slash — otherwise it is the registry's port.
func repositoryOf(ref string) string {
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i]
	}
	return ref
}

// registryHostOf is the first path segment of a reference — the host a
// failed publication could not reach.
func registryHostOf(ref string) string {
	host, _, found := strings.Cut(ref, "/")
	if !found || host == "" {
		return ref
	}
	return host
}
