// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

// The Media screen (FR-062 amendment R-02).
//
// What is pinned here is the screen's four promises, and nothing about
// the engine: the guided sequence is legibility over an order FR-054
// already makes normative, so a test that asserted the engine's behaviour
// through the screen would be testing the wrong object.
//
//  1. The Push control is UNREACHABLE until verification has completed —
//     absent from the document, not disabled in it.
//  2. Per-recipe verdicts name the blocked delivery AND the file that
//     failed.
//  3. A zone refusal and a stale-medium refusal render in plain language
//     with the course of action, and the waiver is offered to an
//     administrator only.
//  4. A verification in flight shows progress and stops polling by itself.

package ui

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/media"
	"github.com/tobby-fetch/tobby-fetch/internal/mediagate"
	"github.com/tobby-fetch/tobby-fetch/internal/store"
	"github.com/tobby-fetch/tobby-fetch/internal/tasks"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

const testMediaZone = "zone-destination"

// mediaZoneStub is the destination-side context of the screen.
type mediaZoneStub struct {
	zone string
	last *media.ImportRecord
}

func (m mediaZoneStub) Zone() string { return m.zone }

func (m mediaZoneStub) LastMediaImport() (media.ImportRecord, bool) {
	if m.last == nil {
		return media.ImportRecord{}, false
	}
	return *m.last, true
}

// dgst builds a well-formed digest from a short seed. The media manifest
// validates the digests it carries before anything else looks at them
// (R-19: an internally inconsistent manifest blocks the medium as a
// whole), so the abbreviated fixtures the other screens use would make
// this one render a refusal instead of a summary.
func dgst(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed
	}
	return "sha256:" + hex.EncodeToString(b)
}

// mediumStore opens a store that is also a transport medium: the manifest
// is what makes the screen — and the FR-054 serving gate — treat it as
// something that changed hands.
func mediumStore(t *testing.T, zone string) *store.Store {
	t.Helper()
	st := openTestStore(t)
	fixtures := []store.RecipeRecord{
		{
			Name: "wordpress", Version: "6.8.2",
			CookbookRepo: "cookbook.example/recipes/wordpress",
			ArtifactRepo: "cookbook.example/recipes/wordpress", ArtifactTag: "6.8.2",
			Digest: dgst(0xa1), Zone: zone, ResolvedAt: t0, Verified: true,
			Ingredients: []store.IngredientRecord{
				{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "6.8.2", Digest: dgst(0x01)},
				{Name: "chart", Kind: "HelmChart", Repo: "docker.io/bitnamicharts/wordpress", Tag: "26.0.0", Digest: dgst(0x02)},
			},
		},
		{
			Name: "redis", Version: "7.2.5",
			CookbookRepo: "cookbook.example/recipes/redis",
			ArtifactRepo: "cookbook.example/recipes/redis", ArtifactTag: "7.2.5",
			Digest: dgst(0xc3), Zone: zone, ResolvedAt: t0, Verified: true,
			Ingredients: []store.IngredientRecord{
				{Name: "redis", Kind: "ContainerImage", Repo: "docker.io/library/redis", Tag: "7.2.5", Digest: dgst(0x03)},
			},
		},
	}
	for i := range fixtures {
		if err := st.PutRecipeRecord(&fixtures[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := media.Write(context.Background(), st, media.WriteOptions{
		Zone: zone, RunID: "run_fixture", ResolvedAt: t0,
	}); err != nil {
		t.Fatalf("writing the media manifest: %v", err)
	}
	return st
}

// mediaUI wires the screen over a real medium and a real gate.
func mediaUI(t *testing.T) (*UI, *mediagate.Gate) {
	t.Helper()
	st := mediumStore(t, testMediaZone)
	gate := mediagate.Open(context.Background(), st.Root(), testMediaZone, slog.New(slog.DiscardHandler))
	return newTestUIWithOptions(t, &Options{
		Store: st, Queue: newTestQueue(t),
		MediaZone: mediaZoneStub{zone: testMediaZone}, MediaGate: gate,
	}, nil), gate
}

// wholeReport clears both deliveries of the fixture graph.
func wholeReport() *media.Report {
	return &media.Report{
		Verdict: media.VerdictPushable,
		Checked: media.Totals{Files: 12, Bytes: 4096},
		Recipes: []media.RecipeVerdict{
			{Name: "wordpress", Version: "6.8.2", Digest: dgst(0xa1), Pushable: true,
				KeyFingerprint: "SHA256:zoneA", Files: 8, Bytes: 3072},
			{Name: "redis", Version: "7.2.5", Digest: dgst(0xc3), Pushable: true,
				KeyFingerprint: "SHA256:zoneA", Files: 4, Bytes: 1024},
		},
	}
}

// TestPushIsUnreachableUntilVerificationCompletes is the R-02 acceptance:
// "on the destination side the Push control is unreachable until
// verification has completed".
//
// Unreachable means ABSENT. A disabled button is a control an operator can
// re-enable from the developer tools, and — the reason that matters — a
// control that is merely greyed out reads as "not yet", while the screen's
// job is to say that pushing is not a thing that exists before a verdict.
//
// Proved fallible: rendering the Push form unconditionally makes the first
// assertion fail; dropping the blocked-verdict guard makes the third fail.
func TestPushIsUnreachableUntilVerificationCompletes(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/media", nil).Body.String()
	if strings.Contains(body, `action="/media/import"`) {
		t.Error("the Push control is on the screen before any verification (R-02)")
	}
	if !strings.Contains(body, `action="/media/verify"`) {
		t.Error("the Verify control is missing: the sequence cannot start")
	}

	gate.Observe(wholeReport())
	body = get(t, mux, c, "/media", nil).Body.String()
	if !strings.Contains(body, `action="/media/import"`) {
		t.Error("the Push control is still unreachable after verification cleared the medium")
	}

	// A blocked medium takes it away again — and the report deliberately
	// still carries a delivery that verified, because that is the case
	// the rule is about: when a global block stands, nothing is pushed,
	// not even a delivery that would have made it (R-19, and the same
	// invariant media.Report.Pushable encodes). A screen that offered
	// Push here would contradict the engine one click later.
	gate.Observe(&media.Report{
		Verdict: media.VerdictBlocked,
		Blocks: []media.Block{{
			Code:   taxonomy.CodeMediaZoneMismatch,
			Params: map[string]string{"expected": testMediaZone, "found": "zone-elsewhere"},
		}},
		Recipes: []media.RecipeVerdict{
			{Name: "wordpress", Version: "6.8.2", Digest: dgst(0xa1), Pushable: true},
		},
	})
	body = get(t, mux, c, "/media", nil).Body.String()
	if strings.Contains(body, `action="/media/import"`) {
		t.Error("the Push control survived a blocked verdict (R-19: nothing is pushed off a blocked medium)")
	}
}

// TestBlockedDeliveryNamesItsOffendingFile is the FR-054 acceptance
// restated on the screen: the refusal names the file. A verdict that says
// only "blocked" sends an operator to the logs; one that says "this
// delivery, this file" sends them to the source instance.
func TestBlockedDeliveryNamesItsOffendingFile(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	const offending = "docker/registry/v2/blobs/sha256/ab/abcd/data"
	gate.Observe(&media.Report{
		Verdict: media.VerdictPartial,
		Checked: media.Totals{Files: 12, Bytes: 4096},
		Recipes: []media.RecipeVerdict{
			{Name: "wordpress", Version: "6.8.2", Pushable: true, KeyFingerprint: "SHA256:zoneA"},
			{Name: "redis", Version: "7.2.5", Reason: &media.Reason{
				Code: taxonomy.CodeMediaFileDigest,
				Path: offending,
				Params: map[string]string{
					"path": offending, "expected": "sha256:aaaa", "actual": "sha256:bbbb",
				},
			}},
		},
	})

	body := get(t, mux, c, "/media", nil).Body.String()
	for _, want := range []string{
		"redis@7.2.5",
		offending,
		string(taxonomy.CodeMediaFileDigest),
		// The stage verdicts of R-02, named separately: the operator has
		// to be able to tell a damaged disk from a missing trust root.
		"Ingredient digests", "Recipe signatures", "Manifest completeness",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the report does not carry %q", want)
		}
	}
	// The cleared delivery is still shown as cleared: a partially damaged
	// medium delivers its intact recipes, and hiding them would make the
	// screen argue against R-19.
	if !strings.Contains(body, "wordpress@6.8.2") {
		t.Error("the report drops the deliveries that survived")
	}
}

// TestZoneAndStaleRefusalsStateTheCourseOfAction is the other half of the
// R-02 acceptance: "a zone refusal and a stale-media refusal SHALL be
// stated in plain language with the course of action, not as an error code
// alone".
//
// It also pins who is offered the way out. The waiver is an
// administrator's (FR-054), so an operator reads that one exists and an
// administrator gets the control — never a dead checkbox.
func TestZoneAndStaleRefusalsStateTheCourseOfAction(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)

	gate.Observe(&media.Report{
		Verdict: media.VerdictBlocked,
		Blocks: []media.Block{
			{Code: taxonomy.CodeMediaZoneMismatch, Overridable: true, Params: map[string]string{
				"expected": testMediaZone, "found": "zone-elsewhere",
			}},
			{Code: taxonomy.CodeMediaStale, Overridable: true, Params: map[string]string{
				"zone": testMediaZone, "resolved": "2026-07-01T00:00:00Z",
				"recorded": "2026-08-01T00:00:00Z", "media": "med_older",
			}},
		},
	})

	admin := login(t, mux, "alexis", "pw-admin")
	body := get(t, mux, admin, "/media", nil).Body.String()
	for _, want := range []string{
		// Plain language, not a code alone: the catalog's what / cause /
		// action triple, with both zone names and both timestamps.
		"addressed to another zone", "zone-elsewhere", testMediaZone,
		"older than the last one imported", "med_older",
		string(taxonomy.CodeMediaZoneMismatch), string(taxonomy.CodeMediaStale),
		// The way out, and the control that takes it.
		`name="allowZoneMismatch"`, `name="allowStale"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the zone/stale refusal does not carry %q", want)
		}
	}
	if !strings.Contains(body, "audit") {
		t.Error("the waiver does not say it is audited (FR-094)")
	}

	op := login(t, mux, "op", "pw-op")
	body = get(t, mux, op, "/media", nil).Body.String()
	if strings.Contains(body, `name="allowZoneMismatch"`) {
		t.Error("an operator is offered the administrator waiver (FR-054)")
	}
	if !strings.Contains(body, "administrator") {
		t.Error("an operator is not told who can waive the refusal")
	}
}

// TestVerificationProgressPollsThenStops: verifying a full medium is minutes of I/O, so the screen must not be a
// frozen page (FR-054: verification progress is displayed). The polling
// contract is the auto-terminating one every other polled zone here uses
// (UI-SPEC §8.3): the attributes are re-emitted only while a run is in
// flight, and the swap style is the morph one the shell's hx-ext enables
// (B-012 — a swap style htmx does not recognise degrades to innerHTML and
// nests the zone inside itself, which never stops polling).
func TestVerificationProgressPollsThenStops(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	release := make(chan struct{})
	gate.SetVerify(func(_ context.Context, _ mediagate.Options, progress func(media.Progress)) (*media.Report, error) {
		progress(media.Progress{
			Stage: media.StageRecipes, Recipe: "redis@7.2.5",
			Bytes: 512, TotalBytes: 2048, Files: 3, TotalFiles: 12,
		})
		<-release
		return wholeReport(), nil
	})

	w := postForm(t, mux, c, "/media/verify", "csrf="+csrfOf(t, u, c), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /media/verify = %d, want 200", w.Code)
	}
	waitRunning(t, gate, true)

	body := get(t, mux, c, "/media", nil).Body.String()
	for _, want := range []string{
		`id="media-body"`,
		`hx-get="/media"`,
		`hx-trigger="every 2s"`,
		`hx-swap="morph:outerHTML"`,
		"<progress", "redis@7.2.5", "25%",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the running screen does not carry %q", want)
		}
	}

	close(release)
	waitRunning(t, gate, false)
	// A second verification is not started while the first still runs: the
	// gate refuses it rather than queueing a second walk of the disk.
	gate.Observe(wholeReport())

	body = get(t, mux, c, "/media", nil).Body.String()
	if strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Error("the screen keeps polling after the verification settled (UI-SPEC §8.3)")
	}
	if !strings.Contains(body, "12 files read and hashed") {
		t.Error("the settled screen does not report what was actually read")
	}
}

// waitRunning blocks until the gate's run state matches want.
func waitRunning(t *testing.T, g *mediagate.Gate, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if g.Status().Running == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("the verification never reached running=%v", want)
}

// TestMediaScreenOnTheSourceSide: the screen exists on both sides (R-02).
// On the source there is nothing to verify — the medium was written here —
// so it is the packing list and the handover instructions, and it offers
// neither Verify nor Push.
func TestMediaScreenOnTheSourceSide(t *testing.T) {
	st := mediumStore(t, testMediaZone)
	u := newTestUIWithOptions(t, &Options{
		Store: st, MediaZone: mediaZoneStub{zone: ""},
		MediaGate: mediagate.Open(context.Background(), st.Root(), "", slog.New(slog.DiscardHandler)),
	}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/media", nil).Body.String()
	for _, want := range []string{testMediaZone, "wordpress@6.8.2", "Handing the medium over"} {
		if !strings.Contains(body, want) {
			t.Errorf("the source-side screen does not carry %q", want)
		}
	}
	for _, unwanted := range []string{`action="/media/verify"`, `action="/media/import"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the source-side screen offers %q, which belongs to the destination", unwanted)
		}
	}
}

// TestMediaScreenNeverClaimsToEstablishAuthenticity (ADR-0006, ADR-0007).
//
// The manifest is unsigned and Tobby signs nothing. A screen that showed
// a green tick beside "the medium" without saying where authenticity
// actually comes from would be teaching the operator the wrong lesson, in
// the one place they are most likely to learn it.
func TestMediaScreenNeverClaimsToEstablishAuthenticity(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	gate.Observe(wholeReport())

	body := get(t, mux, c, "/media", nil).Body.String()
	if !strings.Contains(body, "unsigned") {
		t.Error("the screen does not say the manifest is unsigned")
	}
	if !strings.Contains(body, "trust roots") {
		t.Error("the screen does not say where authenticity comes from")
	}
	if !strings.Contains(body, "SHA256:zoneA") {
		t.Error("the screen does not name the trust root that verified each delivery (FR-033)")
	}
}

// TestReportDownloadIsNotBoosted locks B-013 on this screen: hx-boost
// hijacks same-origin links and never looks at the download attribute, so
// a raw-file link without hx-boost="false" renders its JSON into the page
// instead of saving it. The bug's own note asked for the remaining links
// of this kind to be audited; this one was written after it.
func TestReportDownloadIsNotBoosted(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	gate.Observe(wholeReport())

	body := get(t, mux, c, "/media", nil).Body.String()
	idx := strings.Index(body, `href="/api/v1/media/verification"`)
	if idx < 0 {
		t.Fatal("the report download link is missing")
	}
	anchor := body[idx:min(idx+220, len(body))]
	if !strings.Contains(anchor, `hx-boost="false"`) {
		t.Errorf("the report download link is boosted (B-013): %s", anchor)
	}
}

// TestMediaWaiversNeedAdminOnBothSurfaces: the role floor on the route
// cannot express the FR-054 rule that verifying is an operator action
// while WAIVING a guard is an administrator's, so the handler enforces it
// — with the same taxonomy entry the API mirror answers, because a caller
// should meet one shape of "you may not" and not two.
func TestMediaWaiversNeedAdmin(t *testing.T) {
	u, _ := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "op", "pw-op")
	csrf := csrfOf(t, u, c)

	for _, target := range []string{"/media/verify", "/media/import"} {
		w := postForm(t, mux, c, target, "csrf="+csrf+"&allowZoneMismatch=1", nil)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s with a waiver as operator = %d, want 403", target, w.Code)
		}
		if !strings.Contains(w.Body.String(), string(taxonomy.CodeRoleDenied)) {
			t.Errorf("POST %s does not answer %s", target, taxonomy.CodeRoleDenied)
		}
	}
}

// TestMediaImportEnqueuesTheJourney: the Push step enqueues the FR-052
// task and sends the operator to it, carrying the waivers it was granted
// so the task runs under the same terms the verdict was reached under.
//
// The handler deliberately does not re-check the verdict: the engine
// re-verifies before it pushes anything, unconditionally (FR-054), and a
// screen that decided whether a push is safe would be a second
// implementation of the rule.
func TestMediaImportEnqueuesTheJourney(t *testing.T) {
	u, gate := mediaUI(t)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")
	gate.Observe(wholeReport())

	w := postForm(t, mux, c, "/media/import",
		"csrf="+csrfOf(t, u, c)+"&allowStale=1", map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK {
		t.Fatalf("POST /media/import = %d, want 200 with HX-Redirect", w.Code)
	}
	target := w.Header().Get("HX-Redirect")
	if !strings.HasPrefix(target, "/tasks/") {
		t.Fatalf("HX-Redirect = %q, want the task detail", target)
	}

	list := u.queue.List("", "", "")
	if len(list) != 1 || list[0].Type != tasks.TypeMediaImport {
		t.Fatalf("queue holds %d tasks, want one media import", len(list))
	}
	if len(list[0].MediaOverrides) != 1 || list[0].MediaOverrides[0] != tasks.OverrideFreshness {
		t.Errorf("the task carries %v, want the granted freshness waiver", list[0].MediaOverrides)
	}
}
