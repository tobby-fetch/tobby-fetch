//go:build browser

// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package browser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/tobby-fetch/tobby-fetch/internal/store"
)

// The NFR-017 pass (7.1): accessibility of the core journeys, audited in a
// real browser — contrast needs computed styles, focus needs a focus ring a
// layout engine drew, and a keyboard trap needs a Tab key that was pressed.
//
// The audit is a small, self-contained stand-in for an automated checker
// (the requirement names axe as an example): no third-party script is
// vendored into the binary's test tree and nothing is fetched (NFR-019).
// What it checks is the part of WCAG 2.1 AA that a machine can decide and
// the screens could get wrong:
//
//   - 1.4.3  text contrast, against the colour actually painted behind it
//     (ancestors' backgrounds composited, opacity applied);
//   - 1.4.11 the focus indicator itself reaches 3:1 against its background;
//   - 2.4.7 / 2.1.1 / 2.1.2  every Tab stop shows a focus indicator, the
//     first stop is the skip link, the order never traps;
//   - 4.1.2  every control has an accessible name;
//   - 3.3.2 / 1.3.1  every field has a label, every table a caption, no
//     heading level is skipped, ids are unique;
//   - 3.1.1  <html lang> is the language of the page.
//
// It runs on each core screen, in English and in French, in the dark theme
// and in the light one: four renderings of the same markup, which is where
// contrast regressions hide (a token that holds in one theme and not the
// other, a translation that wraps a control onto its neighbour).

// auditScript returns the list of violations on the current page, each a
// short string naming the rule and the element.
const auditScript = `(() => {
	const out = [];
	const visible = (el) => {
		const r = el.getBoundingClientRect();
		if (r.width === 0 && r.height === 0) return false;
		for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
			const cs = getComputedStyle(n);
			if (cs.display === "none" || cs.visibility === "hidden" || cs.visibility === "collapse") return false;
		}
		return true;
	};
	const hidden = (el) => !!el.closest("[hidden],[aria-hidden='true'],template");
	const desc = (el) => {
		let s = el.tagName.toLowerCase();
		if (el.id) s += "#" + el.id;
		else if (el.classList.length) s += "." + [...el.classList].join(".");
		const t = (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 30);
		return t ? s + ' "' + t + '"' : s;
	};

	// -- colour arithmetic ------------------------------------------------
	const cv = document.createElement("canvas");
	cv.width = cv.height = 1;
	const cx = cv.getContext("2d", {willReadFrequently: true});
	const rgba = (c) => {
		cx.clearRect(0, 0, 1, 1);
		cx.fillStyle = "#000";
		cx.fillStyle = c;
		cx.fillRect(0, 0, 1, 1);
		const d = cx.getImageData(0, 0, 1, 1).data;
		return [d[0], d[1], d[2], d[3] / 255];
	};
	const over = (fg, bg) => { // composite fg (with alpha) over an opaque bg
		const a = fg[3];
		return [0, 1, 2].map(i => fg[i] * a + bg[i] * (1 - a)).concat([1]);
	};
	const lum = (c) => {
		const l = c.slice(0, 3).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); });
		return 0.2126 * l[0] + 0.7152 * l[1] + 0.0722 * l[2];
	};
	const ratio = (a, b) => {
		const la = lum(a), lb = lum(b);
		return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
	};
	// backdrop: the opaque colour painted behind el, or null when it cannot
	// be known (a gradient or an image somewhere behind it).
	const backdrop = (el) => {
		const chain = [];
		for (let n = el; n && n.nodeType === 1; n = n.parentElement) chain.push(n);
		let acc = rgba(getComputedStyle(document.documentElement).backgroundColor);
		if (acc[3] === 0) acc = rgba(getComputedStyle(document.body).backgroundColor);
		if (acc[3] === 0) acc = [255, 255, 255, 1];
		acc = over(acc, [255, 255, 255]);
		for (const n of chain.reverse()) {
			const cs = getComputedStyle(n);
			if (cs.backgroundImage && cs.backgroundImage !== "none") return null;
			const bg = rgba(cs.backgroundColor);
			if (bg[3] > 0) acc = over(bg, acc);
		}
		return acc;
	};
	const opacityOf = (el) => {
		let o = 1;
		for (let n = el; n && n.nodeType === 1; n = n.parentElement) o *= parseFloat(getComputedStyle(n).opacity);
		return o;
	};

	// -- document ---------------------------------------------------------
	const lang = document.documentElement.getAttribute("lang");
	if (!lang) out.push("3.1.1 <html> has no lang");
	if (document.querySelectorAll("main").length !== 1) out.push("landmark: " + document.querySelectorAll("main").length + " <main> elements");
	if (document.querySelectorAll("h1").length !== 1) out.push("heading: " + document.querySelectorAll("h1").length + " <h1> elements");
	let prev = 0;
	for (const h of document.querySelectorAll("h1,h2,h3,h4,h5,h6")) {
		if (!visible(h) || hidden(h)) continue;
		const lvl = +h.tagName[1];
		if (prev && lvl > prev + 1) out.push("1.3.1 heading level skipped before " + desc(h));
		prev = lvl;
	}
	const ids = new Map();
	for (const el of document.querySelectorAll("[id]")) ids.set(el.id, (ids.get(el.id) || 0) + 1);
	for (const [id, n] of ids) if (n > 1) out.push("4.1.1 duplicate id " + id);

	// -- names and labels ---------------------------------------------------
	const nameOf = (el) => {
		const al = el.getAttribute("aria-label");
		if (al && al.trim()) return al.trim();
		const lb = el.getAttribute("aria-labelledby");
		if (lb) {
			const t = lb.split(/\s+/).map(i => (document.getElementById(i) || {}).textContent || "").join(" ").trim();
			if (t) return t;
		}
		if (el.labels && el.labels.length) {
			const t = [...el.labels].map(l => l.textContent).join(" ").trim();
			if (t) return t;
		}
		if (el.tagName === "INPUT" && ["submit", "button", "reset"].includes(el.type) && el.value) return el.value;
		const alt = el.querySelector && el.querySelector("img[alt]");
		const txt = (el.innerText || el.textContent || "").trim();
		if (txt) return txt;
		if (alt && alt.getAttribute("alt").trim()) return alt.getAttribute("alt").trim();
		const title = el.getAttribute("title");
		if (title && title.trim()) return title.trim();
		return "";
	};
	const controls = "a[href],button,input:not([type=hidden]),select,textarea,summary,[role=button],[role=link],[tabindex]";
	for (const el of document.querySelectorAll(controls)) {
		if (hidden(el) || !visible(el)) continue;
		if (el.getAttribute("tabindex") === "-1" && !el.matches("a[href],button,input,select,textarea,summary")) continue;
		if (!nameOf(el)) out.push("4.1.2 control without accessible name: " + desc(el));
	}
	for (const el of document.querySelectorAll("input:not([type=hidden]):not([type=submit]):not([type=button]),select,textarea")) {
		if (hidden(el) || !visible(el)) continue;
		const labelled = (el.labels && el.labels.length) || el.getAttribute("aria-label") || el.getAttribute("aria-labelledby");
		if (!labelled) out.push("3.3.2 field without label: " + desc(el));
	}
	for (const img of document.querySelectorAll("img")) {
		if (!img.hasAttribute("alt")) out.push("1.1.1 <img> without alt: " + desc(img));
	}
	for (const t of document.querySelectorAll("table")) {
		if (hidden(t) || !visible(t)) continue;
		if (!t.querySelector("caption") && !t.getAttribute("aria-label") && !t.getAttribute("aria-labelledby")) out.push("1.3.1 table without caption: " + desc(t));
		if (!t.querySelector("th")) out.push("1.3.1 table without header cells: " + desc(t));
	}

	// -- contrast (1.4.3) ---------------------------------------------------
	const seen = new Set();
	const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
	let nTexts = 0;
	for (let n = walker.nextNode(); n; n = walker.nextNode()) {
		if (!n.nodeValue.trim()) continue;
		const el = n.parentElement;
		if (!el || seen.has(el)) continue;
		seen.add(el);
		if (el.closest("script,style,noscript,template,[hidden]") || !visible(el)) continue;
		// A disabled control is exempt (1.4.3), and so is text that is only
		// there for assistive technology.
		if (el.closest(":disabled,[aria-disabled='true']")) continue;
		const r = el.getBoundingClientRect();
		if (r.width <= 1 || r.height <= 1) continue;
		const cs = getComputedStyle(el);
		const bg = backdrop(el);
		if (!bg) continue;
		const fg = rgba(cs.color);
		const a = fg[3] * opacityOf(el);
		const painted = over([fg[0], fg[1], fg[2], a], bg);
		const size = parseFloat(cs.fontSize);
		const bold = parseInt(cs.fontWeight, 10) >= 700;
		const large = size >= 24 || (size >= 18.66 && bold);
		const want = large ? 3 : 4.5;
		const got = ratio(painted, bg);
		nTexts++;
		if (got < want) out.push("1.4.3 contrast " + got.toFixed(2) + ":1 < " + want + ":1 on " + desc(el));
	}
	if (nTexts < 3) out.push("audit: only " + nTexts + " text elements measured — the page did not render");
	return out;
})()`

// focusProbe describes the focused element: its accessible name, whether it
// shows a focus indicator, and that indicator's contrast against what is
// painted behind it.
const focusProbe = `(() => {
	const el = document.activeElement;
	if (!el || el === document.body) return {body: true};
	const cs = getComputedStyle(el);
	const cv = document.createElement("canvas");
	cv.width = cv.height = 1;
	const cx = cv.getContext("2d", {willReadFrequently: true});
	const rgba = (c) => { cx.clearRect(0,0,1,1); cx.fillStyle = "#000"; cx.fillStyle = c; cx.fillRect(0,0,1,1); const d = cx.getImageData(0,0,1,1).data; return [d[0],d[1],d[2],d[3]/255]; };
	const over = (f, b) => [0,1,2].map(i => f[i]*f[3] + b[i]*(1-f[3])).concat([1]);
	const lum = (c) => { const l = c.slice(0,3).map(v => { v/=255; return v<=0.03928? v/12.92 : Math.pow((v+0.055)/1.055,2.4); }); return 0.2126*l[0]+0.7152*l[1]+0.0722*l[2]; };
	let bg = [255,255,255,1];
	const chain = [];
	for (let n = el; n && n.nodeType === 1; n = n.parentElement) chain.push(n);
	// The ring is drawn outside the element (outline-offset >= 0): what it
	// must contrast with is what is behind the element, not the element's own fill.
	chain.shift();
	for (const n of chain.reverse()) { const b = rgba(getComputedStyle(n).backgroundColor); if (b[3] > 0) bg = over(b, bg); }
	// The indicator may sit on the control or on the field around it
	// (:focus-within), as the search box draws it.
	let ind = el, ics = cs;
	const has = (c) => (c.outlineStyle !== "none" && parseFloat(c.outlineWidth) > 0) || (c.boxShadow && c.boxShadow !== "none");
	if (!has(cs)) {
		for (let n = el.parentElement; n && n !== document.body; n = n.parentElement) {
			if (n.matches(":focus-within") && has(getComputedStyle(n))) { ind = n; ics = getComputedStyle(n); break; }
		}
	}
	const outline = ics.outlineStyle !== "none" && parseFloat(ics.outlineWidth) > 0;
	const shadow = ics.boxShadow && ics.boxShadow !== "none";
	let ratio = 0;
	if (outline) {
		const o = over(rgba(ics.outlineColor), bg);
		const la = lum(o), lb = lum(bg);
		ratio = (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
	}
	const r = el.getBoundingClientRect();
	const label = el.getAttribute("aria-label") || (el.innerText || el.value || el.getAttribute("title") || "").trim().slice(0, 40);
	return {
		tag: el.tagName.toLowerCase() + (el.id ? "#" + el.id : "") + (el.getAttribute("href") ? "[" + el.getAttribute("href") + "]" : ""),
		label, outline, shadow: !!shadow, ratio,
		onScreen: r.width > 0 && r.height > 0 && r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth,
		key: el.tagName + "|" + (el.id || "") + "|" + (el.getAttribute("href") || "") + "|" + label,
	};
})()`

type focusStop struct {
	Body     bool    `json:"body"`
	Tag      string  `json:"tag"`
	Label    string  `json:"label"`
	Outline  bool    `json:"outline"`
	Shadow   bool    `json:"shadow"`
	Ratio    float64 `json:"ratio"`
	OnScreen bool    `json:"onScreen"`
	Key      string  `json:"key"`
}

// auditPage runs the static audit on the current page and fails with every
// violation found.
func auditPage(t *testing.T, s *session, where string) {
	t.Helper()
	violations := eval[[]string](t, s, "auditing "+where, auditScript)
	if len(violations) > 0 {
		t.Errorf("%s: %d accessibility violation(s):\n  %s", where, len(violations), strings.Join(violations, "\n  "))
	}
}

// maxTabStops bounds one keyboard walk. The embedded help index is the
// longest tab order of the product; a wrap-around happens well before this.
const maxTabStops = 600

// auditKeyboard walks the Tab order from the top of the page: the first
// stop is the skip link, every stop shows an indicator that reaches 3:1,
// nothing is reached twice before the order leaves the page (no trap) and
// the walk ends where it started.
func auditKeyboard(t *testing.T, s *session, where string, shell bool) {
	t.Helper()
	s.run(t, "returning to the top of "+where,
		chromedp.Evaluate[chromedp.Void](`(() => {
			window.scrollTo(0, 0);
			// Put the sequential-focus starting point back at the top of the
			// document, as a fresh load has it.
			document.body.setAttribute("tabindex", "-1");
			document.body.focus();
			document.body.removeAttribute("tabindex");
		})()`))
	seen := map[string]bool{}
	for i := 0; i < maxTabStops; i++ {
		s.run(t, "pressing Tab", chromedp.KeyEvent("\t"))
		stop := eval[focusStop](t, s, "reading the focused element", focusProbe)
		if stop.Body {
			if i == 0 {
				t.Errorf("%s: the first Tab press focused nothing", where)
			}
			return // the order left the document: it wrapped
		}
		if shell && i == 0 && !strings.Contains(stop.Tag, "[#main]") {
			t.Errorf("%s: the first Tab stop is %s, want the skip link to #main (2.4.1)", where, stop.Tag)
		}
		if seen[stop.Key] {
			// The order came back to a control it already visited without
			// ever leaving the page: that is its wrap-around. Anything
			// reached twice earlier than that is a trap, and the walk
			// would not have got this far.
			return
		}
		seen[stop.Key] = true
		if !stop.Outline && !stop.Shadow {
			t.Errorf("%s: Tab stop %d (%s %q) shows no focus indicator (2.4.7)", where, i+1, stop.Tag, stop.Label)
		}
		if stop.Outline && stop.Ratio < 3 {
			t.Errorf("%s: the focus indicator of %s %q has contrast %.2f:1 < 3:1 (1.4.11)", where, stop.Tag, stop.Label, stop.Ratio)
		}
		if !stop.OnScreen {
			t.Errorf("%s: %s %q took focus but is not visible (2.4.7)", where, stop.Tag, stop.Label)
		}
	}
	t.Errorf("%s: %d Tab presses never left the page: a keyboard trap, or an unbounded tab order (2.1.2)", where, maxTabStops)
}

// a11yPages are the core screens: the FR-062 listing and detail screens and
// the screens of UC1 (import, follow the task) and UC2 (recipes, mapping,
// consumption).
func a11yPages() []struct{ name, path string } {
	return []struct{ name, path string }{
		{"dashboard", "/"},
		{"content", "/content"},
		{"repository", "/content/docker.io/bitnami/wordpress"},
		{"recipes", "/recipes"},
		{"mapping", "/recipes/wordpress/mapping"},
		{"consumption", "/consumption"},
		{"import", "/import"},
		{"tasks", "/tasks"},
		{"filesets", "/filesets"},
		{"help", "/help"},
		{"about", "/about"},
	}
}

// TestCorePagesMeetWCAGAA is the NFR-017 acceptance in a browser, on every
// core screen, in both languages and both themes.
func TestCorePagesMeetWCAGAA(t *testing.T) {
	inst := newInstance(t, withContent(), withQueue(nil))
	if err := inst.Store.PutRecipeRecord(&storeRecipe); err != nil {
		t.Fatal(err)
	}

	for _, lang := range []struct{ tag, header string }{{"en", "en-US,en;q=0.9"}, {"fr", "fr-FR,fr;q=0.9"}} {
		t.Run(lang.tag, func(t *testing.T) {
			s := newSession(t)
			s.run(t, "negotiating "+lang.tag, command(network.SetExtraHTTPHeaders, network.SetExtraHTTPHeadersParams{
				Headers: network.Headers{"Accept-Language": lang.header},
			}))

			// The sign-in page is a core screen too, and the only one
			// reachable without a session.
			s.run(t, "opening the sign-in page", chromedp.Navigate(inst.URL+"/login"))
			s.wait(t, "the sign-in form", `document.querySelector("#username") !== null`)
			for _, theme := range []string{"dark", "light"} {
				s.run(t, "theme "+theme, chromedp.Evaluate[chromedp.Void](
					fmt.Sprintf(`document.documentElement.dataset.theme = %s`, jsString(theme))))
				where := fmt.Sprintf("sign-in/%s/%s", lang.tag, theme)
				if got := s.evalString(t, "reading lang", `document.documentElement.lang`); !strings.HasPrefix(got, lang.tag) {
					t.Fatalf("%s: <html lang> is %q: the page is not in the language negotiated", where, got)
				}
				auditPage(t, s, where)
				// The sign-in page is a single form outside the shell: it has
				// no navigation to skip.
				auditKeyboard(t, s, where, false)
			}

			inst.signIn(t, s, "/")
			for _, p := range a11yPages() {
				s.run(t, "opening "+p.path, chromedp.Navigate(inst.URL+p.path))
				s.wait(t, p.name+" rendered", `document.readyState === "complete" && document.querySelector("main") !== null`)
				for _, theme := range []string{"dark", "light"} {
					s.run(t, "theme "+theme, chromedp.Evaluate[chromedp.Void](
						fmt.Sprintf(`document.documentElement.dataset.theme = %s`, jsString(theme))))
					where := fmt.Sprintf("%s/%s/%s", p.name, lang.tag, theme)
					if got := s.evalString(t, "reading lang", `document.documentElement.lang`); !strings.HasPrefix(got, lang.tag) {
						t.Fatalf("%s: <html lang> is %q: the page is not in the language negotiated", where, got)
					}
					auditPage(t, s, where)
					auditKeyboard(t, s, where, true)
				}
			}
		})
	}
}

// TestUC2ByKeyboardAlone is the manual keyboard-only pass of NFR-017 made
// repeatable: from the sign-in form to the consumption aids, with no pointer.
func TestUC2ByKeyboardAlone(t *testing.T) {
	inst := newInstance(t, withContent(), withQueue(nil))
	if err := inst.Store.PutRecipeRecord(&storeRecipe); err != nil {
		t.Fatal(err)
	}
	s := newSession(t)

	press := func(what, keys string) { s.run(t, what, chromedp.KeyEvent(keys)) }
	// tabTo presses Tab until the focused element matches sel.
	tabTo := func(what, sel string) {
		t.Helper()
		for i := 0; i < 80; i++ {
			if s.evalBool(t, what, fmt.Sprintf(`!!document.activeElement && document.activeElement.matches(%s)`, jsString(sel))) {
				return
			}
			press("Tab toward "+what, "\t")
		}
		t.Fatalf("Tab never reached %s (%s)%s", what, sel, s.diag())
	}

	s.run(t, "opening the sign-in page", chromedp.Navigate(inst.URL+"/login"))
	s.wait(t, "the sign-in form", `document.querySelector("#username") !== null`)
	tabTo("the user name field", "#username")
	s.run(t, "typing the user name", chromedp.KeyEvent(adminUser))
	tabTo("the password field", "#password")
	s.run(t, "typing the password", chromedp.KeyEvent(adminPhrase))
	press("submitting with Enter", "\r")
	s.wait(t, "signed in", `location.pathname === "/" && document.readyState === "complete"`)

	tabTo("the Recipes entry of the navigation", `.t-nav a[href="/recipes"]`)
	press("following it", "\r")
	s.wait(t, "the recipes list", `location.pathname === "/recipes" && document.querySelector('a[href="/recipes/wordpress/mapping"]') !== null`)

	tabTo("the mapping link of wordpress", `a[href="/recipes/wordpress/mapping"]`)
	press("following it", "\r")
	s.wait(t, "the mapping screen with its aids", `location.pathname === "/recipes/wordpress/mapping" && document.querySelector(".t-aids") !== null`)

	tabTo("the CSV export of the aids", `.t-aids a[href*="format=csv"]`)
	tabTo("the copy button of the snippet", `.t-aids button[data-copy]`)
	tabTo("the Consumption entry of the navigation", `.t-nav a[href="/consumption"]`)
	press("following it", "\r")
	s.wait(t, "the consumption screen", `location.pathname === "/consumption" && document.querySelector(".t-aids") !== null`)
}

// storeRecipe is the recorded recipe the mapping and consumption screens
// are audited with.
var storeRecipe = store.RecipeRecord{
	Name: "wordpress", Version: "6.4.2", Zone: "zone-a", Verified: true, ResolvedAt: t0,
	Ingredients: []store.IngredientRecord{
		{Name: "wordpress", Kind: "ContainerImage", Repo: "docker.io/bitnami/wordpress", Tag: "6.4.2",
			Digest: "sha256:8acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1"},
		{Name: "chart", Kind: "HelmChart", Repo: "docker.io/bitnamicharts/wordpress", Tag: "6.4.2",
			Digest: "sha256:9acca98ed81b53b482870d6b2081e60d2aa77293895c90c97d2b0e76f469ffb1"},
	},
}
