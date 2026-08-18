// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
)

// TestRetrieverScreenShowsTheDestinationAndInterval: an instance that
// promotes says where and how often, on the screen an administrator
// already opens to read its desired-state configuration.
func TestRetrieverScreenShowsTheDestinationAndInterval(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u := newTestUIWithOptions(t, &Options{
		Mode:            "passthrough",
		RetrieverSource: "oci://cookbook.example/retriever:1",
		Destination:     "registry.example.com",
		Cookbook:        "cookbook",
		Interval:        iv,
	}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/admin/retriever", nil).Body.String()
	for _, want := range []string{
		"registry.example.com",
		"every 15m0s",
		`action="/admin/retriever/interval"`,
		`name="interval"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/admin/retriever misses %q", want)
		}
	}
}

// TestRetrieverIntervalForm covers the FR-013 change from the UI, its
// FR-094 audit record, and the refusals — with the API mirror (FR-061)
// asserted by the openapi route cross-check and the api package's own
// tests.
func TestRetrieverIntervalForm(t *testing.T) {
	dir := t.TempDir()
	iv, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	logs := &strings.Builder{}
	u := newTestUIWithOptions(t, &Options{
		Mode:            "passthrough",
		RetrieverSource: "oci://cookbook.example/retriever:1",
		Destination:     "registry.example.com",
		Interval:        iv,
	}, logs)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	w := postForm(t, mux, c, "/admin/retriever/interval",
		"csrf="+csrfOf(t, u, c)+"&interval=45m", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("set interval = %d: %s", w.Code, w.Body.String())
	}
	if iv.Effective() != 45*time.Minute {
		t.Errorf("effective = %s, want 45m", iv.Effective())
	}
	if !strings.Contains(w.Body.String(), "45m0s") {
		t.Error("the confirmation does not state the new interval")
	}
	if !strings.Contains(logs.String(), `"action":"config.promotion_interval"`) ||
		!strings.Contains(logs.String(), `"actor":"alexis"`) {
		t.Errorf("no FR-094 record for the change: %s", logs.String())
	}
	// It persists: a restart finds the same value (FR-013).
	restarted, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Effective() != 45*time.Minute {
		t.Errorf("after restart: %s, want 45m", restarted.Effective())
	}

	// An empty field clears the override — the DELETE mirror.
	w = postForm(t, mux, c, "/admin/retriever/interval", "csrf="+csrfOf(t, u, c)+"&interval=", nil)
	if w.Code != http.StatusOK || iv.Overridden() {
		t.Errorf("clear = %d, overridden=%v", w.Code, iv.Overridden())
	}

	// Refusals re-render the form with a localized message and keep the
	// rejected input, rather than dropping it on an error page.
	for _, tc := range []struct{ name, value, want string }{
		{"not a duration", "soon", "not a duration"},
		{"under the floor", "1s", "Too short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postForm(t, mux, c, "/admin/retriever/interval",
				"csrf="+csrfOf(t, u, c)+"&interval="+tc.value, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("= %d, want 400", w.Code)
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("no inline message %q in %s", tc.want, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `value="`+tc.value+`"`) {
				t.Error("the rejected input was not preserved in the field")
			}
		})
	}
	if iv.Overridden() {
		t.Error("a refused change took effect")
	}

	// Operator role: the admin gate answers the taxonomized 403, exactly
	// as the API mirror does.
	co := login(t, mux, "op", "pw-op")
	w = postForm(t, mux, co, "/admin/retriever/interval", "csrf="+csrfOf(t, u, co)+"&interval=45m", nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "TBY-AUTH-003") {
		t.Errorf("operator = %d, want 403 TBY-AUTH-003", w.Code)
	}
}

// TestRetrieverIntervalWithoutAStateDirectory: an override that could not
// survive a restart is refused and explained, never accepted silently.
func TestRetrieverIntervalWithoutAStateDirectory(t *testing.T) {
	iv, err := schedule.Open("", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u := newTestUIWithOptions(t, &Options{Mode: "passthrough", Interval: iv}, nil)
	mux := mount(u)
	c := login(t, mux, "alexis", "pw-admin")

	body := get(t, mux, c, "/admin/retriever", nil).Body.String()
	if strings.Contains(body, `name="interval"`) {
		t.Error("the form is offered although no override could persist")
	}
	if !strings.Contains(body, "No state directory is configured") {
		t.Errorf("the screen does not explain why: %s", body)
	}
}
