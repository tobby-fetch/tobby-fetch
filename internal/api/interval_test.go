// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/api"
	"github.com/tobby-fetch/tobby-fetch/internal/auth"
	"github.com/tobby-fetch/tobby-fetch/internal/schedule"
)

// newIntervalAPI mounts the retriever endpoints over a real interval
// backed by a real state directory: the override has to actually persist
// for the test to mean anything.
func newIntervalAPI(t *testing.T, iv *schedule.Interval) (*http.ServeMux, *strings.Builder) {
	t.Helper()
	accounts, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, a := range []struct {
		name string
		role auth.Role
		pass string
	}{
		{"root", auth.RoleAdmin, "pw-admin"},
		{"op", auth.RoleOperator, "pw-op"},
	} {
		if err := accounts.AddAccount(a.name, a.role, a.pass, now); err != nil {
			t.Fatal(err)
		}
	}
	logs := &strings.Builder{}
	authn := &auth.Authenticator{
		Store:    accounts,
		Sessions: auth.NewSessions(time.Hour),
		Logger:   slog.New(slog.DiscardHandler),
	}
	a := api.New(authn, slog.New(slog.NewJSONHandler(logs, nil)))
	api.RegisterRecipes(a, &api.RecipeOptions{
		Source:      "oci://cookbook.example/retriever:1",
		Destination: "registry.example.com",
		Cookbook:    "cookbook",
		Interval:    iv,
		Now:         func() time.Time { return now },
	})
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", a.Handler())
	return mux, logs
}

// intervalOf decodes the interval object of a response.
func intervalOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp struct {
		Interval    map[string]any `json:"interval"`
		Destination string         `json:"destination"`
		Cookbook    string         `json:"cookbook"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding %s: %v", w.Body.String(), err)
	}
	return resp.Interval
}

// TestRetrieverReportsTheDestinationAndInterval: the FR-013 cadence and
// the FR-034 target are part of what an administrator reads back, in the
// same document as the rest of the desired-state configuration.
func TestRetrieverReportsTheDestinationAndInterval(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mux, _ := newIntervalAPI(t, iv)

	w := call(t, mux, http.MethodGet, "/api/v1/retriever", "root", "pw-admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("retriever = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Destination string `json:"destination"`
		Cookbook    string `json:"cookbook"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Destination != "registry.example.com" || resp.Cookbook != "cookbook" {
		t.Errorf("destination=%q cookbook=%q", resp.Destination, resp.Cookbook)
	}
	got := intervalOf(t, w)
	if got["effective"] != "15m0s" || got["configured"] != "15m0s" ||
		got["overridden"] != false || got["enabled"] != true {
		t.Errorf("interval = %+v", got)
	}
	if got["minimum"] != schedule.MinOverride.String() {
		t.Errorf("minimum = %v, want %s", got["minimum"], schedule.MinOverride)
	}
}

// TestSetIntervalPersistsAndAudits covers the FR-013 hot change and its
// FR-094 record: an operator who can make an instance reach into another
// zone twice as often leaves a trail saying so.
func TestSetIntervalPersistsAndAudits(t *testing.T) {
	dir := t.TempDir()
	iv, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mux, logs := newIntervalAPI(t, iv)

	w := call(t, mux, http.MethodPut, "/api/v1/retriever/interval", "root", "pw-admin", `{"interval":"45m"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put interval = %d: %s", w.Code, w.Body.String())
	}
	if got := intervalOf(t, w)["effective"]; got != "45m0s" {
		t.Errorf("effective = %v, want 45m0s", got)
	}
	// It survives a restart: that is what "without redeployment" means.
	restarted, err := schedule.Open(dir, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Effective() != 45*time.Minute {
		t.Errorf("after restart: %s, want 45m", restarted.Effective())
	}
	if !strings.Contains(logs.String(), `"action":"config.promotion_interval"`) ||
		!strings.Contains(logs.String(), `"actor":"root"`) {
		t.Errorf("no FR-094 audit record for the change: %s", logs.String())
	}

	// Clearing returns to the configured value, audited the same way.
	w = call(t, mux, http.MethodDelete, "/api/v1/retriever/interval", "root", "pw-admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete interval = %d: %s", w.Code, w.Body.String())
	}
	if got := intervalOf(t, w); got["effective"] != "15m0s" || got["overridden"] != false {
		t.Errorf("after clear: %+v", got)
	}
}

// TestSetIntervalRefusals: a malformed body, a value under the floor, and
// the operator role all answer a taxonomized problem rather than changing
// anything.
func TestSetIntervalRefusals(t *testing.T) {
	iv, err := schedule.Open(t.TempDir(), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mux, _ := newIntervalAPI(t, iv)

	for _, tc := range []struct {
		name, user, pass, body string
		status                 int
		code                   string
	}{
		{"not a duration", "root", "pw-admin", `{"interval":"soon"}`, http.StatusUnprocessableEntity, "TBY-VAL-001"},
		{"not JSON", "root", "pw-admin", `nonsense`, http.StatusUnprocessableEntity, "TBY-VAL-001"},
		{"under the floor", "root", "pw-admin", `{"interval":"1s"}`, http.StatusUnprocessableEntity, "TBY-VAL-001"},
		{"operator role", "op", "pw-op", `{"interval":"45m"}`, http.StatusForbidden, "TBY-AUTH-003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(t, mux, http.MethodPut, "/api/v1/retriever/interval", tc.user, tc.pass, tc.body)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Errorf("= %d %s, want %d %s", w.Code, w.Body.String(), tc.status, tc.code)
			}
		})
	}
	if iv.Overridden() {
		t.Error("a refused request changed the interval")
	}
}

// TestIntervalUnavailableWithoutAScheduler: in mirror mode FR-014
// requires manual triggering, so there is no loop to pace — and the
// endpoint says that rather than accepting a setting nothing reads.
func TestIntervalUnavailableWithoutAScheduler(t *testing.T) {
	mux, _ := newIntervalAPI(t, nil)

	w := call(t, mux, http.MethodGet, "/api/v1/retriever", "root", "pw-admin", "")
	if w.Code != http.StatusOK {
		t.Fatalf("retriever = %d", w.Code)
	}
	if got := intervalOf(t, w); got != nil {
		t.Errorf("interval = %+v, want absent", got)
	}
	// "This instance has no loop" is a statement about the instance, not
	// about the request: same code the sync trigger uses when no Retriever
	// source is configured.
	w = call(t, mux, http.MethodPut, "/api/v1/retriever/interval", "root", "pw-admin", `{"interval":"45m"}`)
	if !strings.Contains(w.Body.String(), "TBY-CFG-001") {
		t.Errorf("put interval = %d %s, want TBY-CFG-001", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "FR-014") {
		t.Errorf("the refusal does not say why: %s", w.Body.String())
	}
}
