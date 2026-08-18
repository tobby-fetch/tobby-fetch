//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

// TestRecipeDocumentCopyAndDownload locks R-37 at the only level that can
// check it.
//
// Reading what a zone actually received must not require leaving the tool
// for an `oras pull`, so the manifest page of a recipe offers the document
// two ways: copied whole, or downloaded as the file the next version is
// derived from. internal/ui already proves the bytes are right and the
// headers are right. What it cannot prove is that the clipboard receives
// the whole document rather than the rendered excerpt, and that clicking
// the link produces a FILE — a boosted anchor would fetch the YAML and
// swap it into the page instead, with the same href and the same handler.
func TestRecipeDocumentCopyAndDownload(t *testing.T) {
	inst := newInstance(t, withRecipe())
	s := newSession(t)
	s.grantClipboard(t, inst.URL)
	inst.signIn(t, s, "/content/"+recipeRepo+"/-/tags/"+recipeTag)

	// The document's copy button is the wide one; the digest chips next to
	// it share the t-chip-copy class but never the button styling.
	s.copyChip(t, "copying the recipe document", `button.t-btn.t-chip-copy[data-copy]`)
	s.wait(t, "the copy toast confirms the action",
		`document.querySelectorAll("#toasts .t-toast").length >= 1`)
	if got, want := s.readClipboard(t), string(inst.RecipeDoc); got != want {
		t.Errorf("the clipboard holds %d bytes, the published document is %d bytes: "+
			"the copy button does not carry the whole document (R-37)\n got: %q\nwant: %q",
			len(got), len(want), got, want)
	}

	// The download must land on disk as a file the operator can edit into
	// the next version — never as a page.
	dir := t.TempDir()
	dl := s.captureDownloads(t, dir)
	s.click(t, "downloading the recipe document", `a[download]`)
	name := dl.wait(t)

	// The name comes from Content-Disposition, so it says which recipe and
	// which version this is once it sits in a downloads folder.
	if name != "wordpress-"+recipeTag+".yaml" {
		t.Errorf("the document downloaded as %q, want %q (R-37)", name, "wordpress-"+recipeTag+".yaml")
	}
	// Read the file through a root confined to the download directory: the
	// name comes from the browser, and a test has no business following it
	// anywhere else.
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("opening the download directory: %v", err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(name)
	if err != nil {
		t.Fatalf("opening the downloaded document: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the downloaded document: %v", err)
	}
	if !bytes.Equal(got, inst.RecipeDoc) {
		t.Errorf("the downloaded document is not byte-identical to the published one (R-37): "+
			"%d bytes against %d", len(got), len(inst.RecipeDoc))
	}

	// A file was downloaded AND the page stayed the page: an anchor the
	// client hijacks would have replaced the document with raw YAML.
	s.wait(t, "the manifest page survived the download",
		`location.pathname.endsWith("/-/tags/`+recipeTag+`") &&
		 document.querySelector(".t-header") !== null`)
}

// downloads collects the browser's download events for one directory.
type downloads struct {
	mu       sync.Mutex
	name     string
	failure  string
	finished chan struct{}
	once     sync.Once
}

// captureDownloads routes downloads to dir and starts listening. Called
// BEFORE the click: the events begin as soon as the navigation is turned
// into a download.
func (s *session) captureDownloads(t *testing.T, dir string) *downloads {
	t.Helper()
	d := &downloads{finished: make(chan struct{})}
	chromedp.ListenTarget(s.ctx, func(ev any) {
		switch e := ev.(type) {
		case *cdpbrowser.EventDownloadWillBegin:
			d.mu.Lock()
			d.name = e.SuggestedFilename
			d.mu.Unlock()
		case *cdpbrowser.EventDownloadProgress:
			switch e.State {
			case cdpbrowser.DownloadProgressStateCompleted:
				d.done("")
			case cdpbrowser.DownloadProgressStateCanceled:
				d.done("the browser canceled the download")
			case cdpbrowser.DownloadProgressStateInProgress:
			}
		}
	})
	s.run(t, "routing downloads to a temporary directory",
		cdpbrowser.SetDownloadBehavior(cdpbrowser.SetDownloadBehaviorBehaviorAllow).
			WithDownloadPath(dir).WithEventsEnabled(true))
	return d
}

func (d *downloads) done(failure string) {
	d.mu.Lock()
	if failure != "" {
		d.failure = failure
	}
	d.mu.Unlock()
	d.once.Do(func() { close(d.finished) })
}

// wait blocks until a download completes and returns its file name.
func (d *downloads) wait(t *testing.T) string {
	t.Helper()
	select {
	case <-d.finished:
	case <-time.After(waitBudget):
		t.Fatalf("no download completed within %s: the link did not produce a file — a boosted "+
			"anchor fetches the document and swaps it into the page instead (R-37)", waitBudget)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failure != "" {
		t.Fatalf("downloading the document: %s", d.failure)
	}
	return d.name
}
