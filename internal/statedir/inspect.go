// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tobby-fetch/tobby-fetch/internal/secretfile"
	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

// Item is one entry of the inventory: names, classes, sizes, modes —
// never contents (FR-097).
type Item struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Class is "identity" or "discardable"; empty for an entry the layout
	// does not document.
	Class Class `json:"class,omitempty"`
	// Secret entries hold credential material or a private key.
	Secret  bool   `json:"secret"`
	Purpose string `json:"purpose,omitempty"`
	// Documented is false for something found in the directory that the
	// layout does not name (a leftover temporary file, a foreign file).
	Documented bool `json:"documented"`
	// Present is false for a documented entry the directory does not hold.
	Present bool `json:"present"`
	// Size is the file's size, or the sum of the files under a directory.
	Size int64 `json:"size"`
	// Files is the number of files under a directory entry.
	Files int `json:"files,omitempty"`
	// Mode is the Unix permission ("0600"); on Windows it names the ACL
	// verdict instead ("acl", or "acl:<SID>" of the broad principal).
	Mode string `json:"mode,omitempty"`
	// Access says who besides the owner can read it (secret entries and
	// the secret files of a directory: the broadest of them).
	Access Access `json:"access,omitempty"`
	// Modified is the last modification time.
	Modified time.Time `json:"modified,omitzero"`
}

// Inventory is the state directory as `tobby state inspect` reports it.
type Inventory struct {
	Root string `json:"root"`
	// Layout is the version recorded in the directory; 0 when none is.
	Layout int `json:"layout"`
	// Supported is the version this build writes.
	Supported int `json:"supported"`
	// Items lists the documented entries in documentation order, present
	// or not, then whatever else was found.
	Items []Item `json:"items"`
	// OpenDirs names the directories holding a secret entry — the root,
	// the secret directories and those under them that hold a key — that
	// other users of the host can write to, or whose access could not be
	// read: whoever can write there can replace the secret, which refuses
	// the start (TBY-STA-002) as surely as reading it.
	OpenDirs []OpenDir `json:"openDirs,omitempty"`
}

// OpenDir is a directory of OpenDirs.
type OpenDir struct {
	// Path is relative to the root ("." for the root itself).
	Path string `json:"path"`
	// Mode says why: the Unix mode and what makes it open, or the broad
	// principal of the access list.
	Mode string `json:"mode"`
}

// Expect says which identity entries THIS configuration makes the
// instance depend on. A directory that has run before and lacks one of
// them was probably restored incompletely (TBY-STA-003); a new directory
// lacks all of them and says nothing.
type Expect struct {
	// Accounts: local authentication is on.
	Accounts bool
	// Certificate: the listener serves the generated self-signed pair, so
	// losing it silently changes the fingerprint clients pinned.
	Certificate bool
	// ImportRecord: a zone identity is configured, so the freshness
	// register is in use.
	ImportRecord bool
}

// Layout is the content of LayoutFile.
type layoutDoc struct {
	Layout    int       `json:"layout"`
	WrittenBy string    `json:"writtenBy,omitempty"`
	WrittenAt time.Time `json:"writtenAt"`
}

// ReadLayout returns the layout version recorded in root, 0 and no error
// when the directory has none.
func ReadLayout(root string) (int, error) {
	raw, err := os.ReadFile(Path(root, LayoutFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 0, nil
	case err != nil:
		return 0, err
	}
	var d layoutDoc
	if err := json.Unmarshal(raw, &d); err != nil || d.Layout <= 0 {
		return -1, nil //nolint:nilerr // an unreadable layout file is an unknown layout, reported as such
	}
	return d.Layout, nil
}

// Inspect lists the directory. It reads metadata only, and never fails
// on an entry it cannot classify: the point of an inventory is to be
// readable on a broken directory.
func Inspect(root string) (*Inventory, error) {
	if root == "" {
		return nil, errors.New("statedir: no state directory is configured")
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("statedir: %w", err)
	}
	layout, err := ReadLayout(root)
	if err != nil {
		return nil, fmt.Errorf("statedir: reading %s: %w", LayoutFile, err)
	}
	inv := &Inventory{Root: root, Layout: layout, Supported: LayoutVersion}
	known := map[string]bool{}
	for _, e := range entries {
		known[e.Name] = true
		inv.Items = append(inv.Items, describe(root, e.Name, &e))
	}
	dir, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("statedir: %w", err)
	}
	var extra []Item
	for _, d := range dir {
		if known[d.Name()] {
			continue
		}
		extra = append(extra, describe(root, d.Name(), nil))
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Name < extra[j].Name })
	inv.Items = append(inv.Items, extra...)
	inv.OpenDirs = openDirs(root)
	return inv, nil
}

// openDirs judges the directories that hold a secret entry: the root (it
// holds accounts.yaml), each secret directory, and every directory under
// one on the way to a key file — the key's own directory once a symbolic
// link is followed included.
func openDirs(root string) []OpenDir {
	dirs := []string{root}
	seen := map[string]bool{root: true}
	addDir := func(d string) {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	for _, e := range entries {
		if !e.Secret || e.Kind != KindDir {
			continue
		}
		top := Path(root, e.Name)
		if _, err := os.Stat(top); err != nil {
			continue
		}
		addDir(top)
		for _, f := range secretFiles(top) {
			for d := filepath.Dir(f); d != top && strings.HasPrefix(d, top); d = filepath.Dir(d) {
				addDir(d)
			}
			if target, err := filepath.EvalSymlinks(f); err == nil {
				addDir(filepath.Dir(target))
			}
		}
	}
	var out []OpenDir
	for _, d := range dirs {
		rel, err := filepath.Rel(root, d)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = d
		}
		info, err := os.Stat(d)
		if err != nil {
			continue
		}
		open, mode, err := dirOpen(d, info)
		switch {
		case err != nil:
			out = append(out, OpenDir{Path: filepath.ToSlash(rel), Mode: "access list unreadable: " + err.Error()})
		case open:
			out = append(out, OpenDir{Path: filepath.ToSlash(rel), Mode: mode})
		}
	}
	return out
}

func describe(root, name string, e *Entry) Item {
	it := Item{Name: name}
	if e != nil {
		it.Kind, it.Class, it.Secret, it.Purpose, it.Documented = e.Kind, e.Class, e.Secret, e.Purpose, true
	}
	full := Path(root, name)
	info, err := os.Lstat(full)
	if err != nil {
		return it
	}
	it.Present = true
	it.Modified = info.ModTime().UTC()
	// An entry that is a symbolic link (a projected volume, a mount) is
	// judged by what it points to: the link itself always reads 0777.
	if info.Mode()&os.ModeSymlink != 0 {
		if target, serr := os.Stat(full); serr == nil {
			info = target
		}
	}
	if info.IsDir() {
		it.Kind = KindDir
		_ = filepath.WalkDir(full, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil //nolint:nilerr // a vanished or unreadable file only shortens the count
			}
			if fi, ierr := d.Info(); ierr == nil && fi.Mode().IsRegular() {
				it.Size += fi.Size()
				it.Files++
			}
			return nil
		})
	} else {
		it.Kind = KindFile
		it.Size = info.Size()
	}
	if it.Secret || e == nil {
		it.Access, it.Mode = itemAccess(full, info, it.Secret)
	} else if a, m, aerr := accessOf(full, info); aerr == nil {
		it.Access, it.Mode = a, m
	}
	return it
}

// unreadable is the verdict on a secret file whose access could not be
// read: refused, never assumed narrow.
func unreadable(err error) (access Access, mode string) {
	return AccessUnknown, "access list unreadable: " + err.Error()
}

// itemAccess is the access verdict of an entry: the file's own, or for a
// directory the broadest of its secret files.
func itemAccess(full string, info fs.FileInfo, secret bool) (access Access, mode string) {
	if !info.IsDir() {
		a, m, err := accessOf(full, info)
		if err != nil {
			if secret {
				return unreadable(err)
			}
			return "", ""
		}
		return a, m
	}
	// The directory itself is a container: its mode is shown, and only its
	// key files are judged.
	if _, m, err := accessOf(full, info); err == nil {
		mode = m
	}
	if !secret {
		return "", mode
	}
	access = AccessOwner
	for _, f := range secretFiles(full) {
		// A key adopted as a symbolic link (a projected Kubernetes volume:
		// tls.key -> ..data/tls.key) is judged by what it points to: the
		// link itself always reads 0777. A dangling one holds nothing.
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		a, m, aerr := accessOf(f, fi)
		if aerr != nil {
			a, m = unreadable(aerr)
		}
		if rank(a) > rank(access) {
			access, mode = a, m
		}
	}
	return access, mode
}

func rank(a Access) int {
	switch a {
	case AccessUnknown:
		return 3
	case AccessBroad:
		return 2
	case AccessGroup:
		return 1
	}
	return 0
}

// secretFiles lists the secret files under a secret directory entry.
func secretFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && SecretFileInDir(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// Missing lists the identity entries an instance with this configuration
// depends on and the directory lacks.
func (inv *Inventory) Missing(e Expect) []string {
	want := map[string]bool{}
	if e.Accounts {
		want[Accounts] = true
	}
	if e.Certificate {
		want[TLS] = true
	}
	if e.ImportRecord {
		want[MediaImports] = true
	}
	var out []string
	for i := range inv.Items {
		if it := &inv.Items[i]; want[it.Name] && !it.Present {
			out = append(out, it.Name)
		}
	}
	return out
}

// Report is what the startup check found.
type Report struct {
	// Adopted is true when the directory had no layout version and this
	// start recorded one — a new directory, or one that predates R-27.
	Adopted bool `json:"adopted"`
	// Missing names the identity entries absent from a directory that has
	// run before (TBY-STA-003). Non-blocking.
	Missing []string `json:"missing,omitempty"`
}

// Prepare creates root owner-only when needed and runs the startup check
// of FR-097: the layout version must be one this build knows
// (TBY-STA-001); no secret entry may be readable by every user of the
// host, nor any directory holding one writable by them, and an access
// list that cannot be read counts as broad (TBY-STA-002); and an identity
// entry the configuration depends on that a directory with a history
// lacks is reported (TBY-STA-003, not an error). version is the build that
// records the layout on adoption.
//
// A directory with a history is one that records a layout, or that holds
// any documented entry: a directory whose layout.json was lost in a
// partial restore is adopted again, but what it lacks is still reported.
// Only a directory holding none of the layout — this start created it, or
// an empty volume was mounted — is new and says nothing.
//
// An existing root is judged as found, before it is narrowed to its owner:
// narrowing it first would hide that someone else could write there
// until this start.
func Prepare(root string, expect Expect, version string) (*Report, error) {
	if _, err := os.Stat(root); err != nil {
		if err := secretfile.MkdirAll(root); err != nil {
			return nil, err
		}
	}
	inv, err := Inspect(root)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	switch {
	case inv.Layout > LayoutVersion || inv.Layout < 0:
		v := strconv.Itoa(inv.Layout)
		if inv.Layout < 0 {
			v = "unreadable " + LayoutFile
		}
		return nil, taxonomy.New(taxonomy.CodeStateLayoutUnknown, taxonomy.Params{"root": root, "version": v})
	case inv.Layout == 0:
		rep.Adopted = true
	}
	for i := range inv.Items {
		it := &inv.Items[i]
		if !it.Present || !it.Secret || !it.Access.Refused() {
			continue
		}
		mode := it.Mode
		if mode != "" && mode[0] >= '0' && mode[0] <= '9' {
			mode = "mode " + mode
		}
		return nil, taxonomy.New(taxonomy.CodeStatePermissions, taxonomy.Params{
			"path": Path(root, it.Name), "mode": mode,
		})
	}
	if len(inv.OpenDirs) > 0 {
		d := inv.OpenDirs[0]
		return nil, taxonomy.New(taxonomy.CodeStatePermissions, taxonomy.Params{
			"path": Path(root, filepath.FromSlash(d.Path)), "mode": d.Mode,
		})
	}
	if err := secretfile.MkdirAll(root); err != nil {
		return nil, err
	}
	if rep.Adopted {
		if err := writeLayout(root, version); err != nil {
			return nil, err
		}
		if !inv.hasHistory() {
			return rep, nil
		}
	}
	rep.Missing = inv.Missing(expect)
	return rep, nil
}

// hasHistory reports whether the directory holds any documented entry
// besides the layout record.
func (inv *Inventory) hasHistory() bool {
	for i := range inv.Items {
		if it := &inv.Items[i]; it.Documented && it.Present && it.Name != LayoutFile {
			return true
		}
	}
	return false
}

// writeLayout records the layout version. The file is created, never
// opened: an entry already at that name — a symbolic link planted to make
// the write land elsewhere included — fails the start instead of being
// followed.
func writeLayout(root, version string) error {
	doc, _ := json.MarshalIndent(layoutDoc{Layout: LayoutVersion, WrittenBy: version, WrittenAt: time.Now().UTC()}, "", "  ")
	f, err := os.OpenFile(Path(root, LayoutFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("statedir: recording the layout version: %w", err)
	}
	_, werr := f.Write(append(doc, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("statedir: recording the layout version: %w", werr)
	}
	return nil
}

// Describe renders a report line for the startup log.
func (r *Report) Describe() string {
	if r == nil || len(r.Missing) == 0 {
		return ""
	}
	return strings.Join(r.Missing, ", ")
}
