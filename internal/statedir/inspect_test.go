// SPDX-License-Identifier: GPL-3.0-only
// Copyright © 2026 infraBuilder SASU and contributors

package statedir

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/tobby-fetch/tobby-fetch/internal/taxonomy"
)

func write(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	p := Path(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func codeOf(err error) taxonomy.Code {
	var te *taxonomy.Error
	if errors.As(err, &te) {
		return te.Code()
	}
	return ""
}

func TestPrepareAdoptsANewDirectoryAndRecordsTheLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	rep, err := Prepare(root, Expect{Accounts: true, Certificate: true, ImportRecord: true}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Adopted || len(rep.Missing) != 0 {
		t.Errorf("a new directory reports nothing missing: %+v", rep)
	}
	if v, _ := ReadLayout(root); v != LayoutVersion {
		t.Errorf("layout recorded = %d, want %d", v, LayoutVersion)
	}
	// Second start: no longer new, and the expected entries are missing.
	rep, err = Prepare(root, Expect{Accounts: true, Certificate: true, ImportRecord: true}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Adopted {
		t.Error("the second start adopts again")
	}
	if got := strings.Join(rep.Missing, ","); got != "accounts.yaml,tls,media-imports.json" {
		t.Errorf("Missing = %q (TBY-STA-003 names every absent identity entry)", got)
	}
	// What the configuration does not depend on is not reported.
	rep, _ = Prepare(root, Expect{}, "v1")
	if len(rep.Missing) != 0 {
		t.Errorf("unexpected entries reported: %v", rep.Missing)
	}
}

func TestPrepareRefusesAnUnknownLayout(t *testing.T) {
	for name, content := range map[string]string{
		"future":     `{"layout": 99}`,
		"unreadable": `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, LayoutFile, content, 0o600)
			_, err := Prepare(root, Expect{}, "v1")
			if codeOf(err) != taxonomy.CodeStateLayoutUnknown {
				t.Fatalf("Prepare = %v, want TBY-STA-001", err)
			}
		})
	}
}

func TestPrepareRefusesASecretEntryReadableByEveryone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits; the Windows ACL verdict is covered by access_windows_test.go")
	}
	root := t.TempDir()
	write(t, root, Accounts, "version: 1\n", 0o644)
	_, err := Prepare(root, Expect{}, "v1")
	if codeOf(err) != taxonomy.CodeStatePermissions {
		t.Fatalf("Prepare = %v, want TBY-STA-002", err)
	}
	if !strings.Contains(err.Error(), Accounts) && !strings.Contains(taxonomy.Text("en", err.(*taxonomy.Error)), Accounts) { //nolint:errorlint // the assertion is the test's point
		t.Errorf("the refusal does not name the entry: %v", err)
	}
	// An entry nobody else may read, or only the group (fsGroup), passes.
	for _, mode := range []os.FileMode{0o600, 0o640, 0o660} {
		write(t, root, Accounts, "version: 1\n", mode)
		if _, err := Prepare(root, Expect{}, "v1"); err != nil {
			t.Errorf("mode %04o refused: %v", mode, err)
		}
	}
	// A key file under the secret directory counts, the certificate does not.
	write(t, root, "tls/self-signed.crt", "cert", 0o644)
	if _, err := Prepare(root, Expect{}, "v1"); err != nil {
		t.Errorf("a world-readable certificate refused: %v", err)
	}
	write(t, root, "tls/self-signed.key", "key", 0o604)
	if _, err := Prepare(root, Expect{}, "v1"); codeOf(err) != taxonomy.CodeStatePermissions {
		t.Errorf("a world-readable key passed: %v", err)
	}
}

func TestInspectListsDocumentedAndUnknownEntriesWithoutContents(t *testing.T) {
	root := t.TempDir()
	write(t, root, Accounts, "SECRET-HASH", 0o600)
	write(t, root, "tls/self-signed.key", "KEY", 0o600)
	write(t, root, "tls/self-signed.crt", "CERT", 0o644)
	write(t, root, "stray.tmp", "x", 0o600)
	inv, err := Inspect(root)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Item{}
	for _, it := range inv.Items {
		by[it.Name] = it
	}
	if len(inv.Items) != len(Entries())+1 {
		t.Errorf("%d items, want the %d documented entries plus the stray file", len(inv.Items), len(Entries()))
	}
	if a := by[Accounts]; !a.Present || !a.Secret || a.Size != int64(len("SECRET-HASH")) || a.Access != AccessOwner {
		t.Errorf("accounts item = %+v", a)
	}
	if tl := by[TLS]; !tl.Present || tl.Kind != KindDir || tl.Files != 2 || tl.Size != 7 {
		t.Errorf("tls item = %+v", tl)
	}
	if s := by["stray.tmp"]; s.Documented || !s.Present {
		t.Errorf("stray item = %+v", s)
	}
	if p := by[MediaImports]; p.Present || !p.Documented {
		t.Errorf("an absent documented entry must be listed as absent: %+v", p)
	}
	for _, it := range inv.Items {
		if strings.Contains(it.Purpose+it.Mode+string(it.Access), "SECRET-HASH") {
			t.Errorf("contents leaked into the inventory: %+v", it)
		}
	}
	if _, err := Inspect(""); err == nil {
		t.Error("Inspect accepted an empty root")
	}
	if _, err := Inspect(filepath.Join(root, "nope")); err == nil {
		t.Error("Inspect accepted a missing root")
	}
}

// TestWritersTakeTheirNamesFromTheLayout is the guard of FR-097: a package
// that joins a documented entry name as a literal instead of taking it
// from this package has an entry the layout table cannot vouch for. The
// scan reads every non-test source of the module.
func TestWritersTakeTheirNamesFromTheLayout(t *testing.T) {
	names := map[string]bool{}
	for _, e := range Entries() {
		names[e.Name] = true
	}
	internal := ".."
	fset := token.NewFileSet()
	err := filepath.WalkDir(internal, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		if strings.Contains(filepath.ToSlash(p), "/statedir/") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Join" {
				return true
			}
			for _, a := range call.Args[min(1, len(call.Args)):] {
				lit, ok := a.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, _ := strconv.Unquote(lit.Value)
				if names[v] || (strings.Contains(v, "/") && names[strings.SplitN(v, "/", 2)[0]]) {
					t.Errorf("%s joins the state entry %q as a literal: take it from internal/statedir", fset.Position(lit.Pos()), v)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestASymlinkedSecretEntryIsJudgedByItsTarget: the link reads 0777 whatever
// it points to, and a projected volume is exactly that.
func TestASymlinkedSecretEntryIsJudgedByItsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	root, elsewhere := t.TempDir(), t.TempDir()
	write(t, elsewhere, "real-accounts.yaml", "version: 1\n", 0o600)
	if err := os.Symlink(filepath.Join(elsewhere, "real-accounts.yaml"), Path(root, Accounts)); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "v1"); err != nil {
		t.Fatalf("a link to an owner-only file was refused: %v", err)
	}
	if err := os.Chmod(filepath.Join(elsewhere, "real-accounts.yaml"), 0o644); err != nil { //nolint:gosec // G302: the test loosens the file on purpose
		t.Fatal(err)
	}
	if _, err := Prepare(root, Expect{}, "v1"); codeOf(err) != taxonomy.CodeStatePermissions {
		t.Fatalf("a link to a world-readable file passed: %v", err)
	}
}
