// Package archtest holds tests over the source tree itself: rules about who may call what that the compiler
// cannot enforce, because the symbol has to stay exported.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// activeSubjectName is adminz.ActiveSubject: it builds the only Subject that adminz.Decide can allow.
	activeSubjectName = "ActiveSubject"
	// portalDir is the only package allowed to call it: portal.Permissions.Subject does, for active users alone.
	portalDir = "internal/portal"
)

// moduleRoot finds the directory of go.mod above the test's working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// references returns the position of every use of the identifier name in src, except the declaration of a
// function of that name. Selector expressions (adminz.ActiveSubject, an aliased import) and dot-imported bare
// calls both appear as identifiers, so one walk covers calls and references taken as values.
func references(fset *token.FileSet, file *ast.File, name string) []token.Position {
	var found []token.Position
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			// Skip the declared name but still look into the body and the signature.
			if n.Name.Name == name {
				if n.Body != nil {
					ast.Inspect(n.Body, func(m ast.Node) bool {
						if id, ok := m.(*ast.Ident); ok && id.Name == name {
							found = append(found, fset.Position(id.Pos()))
						}
						return true
					})
				}
				return false
			}
		case *ast.Ident:
			if n.Name == name {
				found = append(found, fset.Position(n.Pos()))
			}
		}
		return true
	})
	return found
}

// violations scans the non-test Go files under root (test files never ship in a binary and legitimately build
// subjects) and reports every use of name outside allowedDir (a slash-separated path relative to root).
func violations(t *testing.T, root, name, allowedDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.ToSlash(filepath.Dir(rel)) == allowedDir {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		for _, pos := range references(fset, file, name) {
			out = append(out, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), pos.Line))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Only portal.Permissions.Subject may build an active Subject: anywhere else, a disabled user could be given
// one by a forgotten status check.
func TestActiveSubjectIsOnlyUsedInThePortalPackage(t *testing.T) {
	t.Parallel()
	if got := violations(t, moduleRoot(t), activeSubjectName, portalDir); len(got) > 0 {
		t.Fatalf("adminz.%s is used outside %s (build subjects with portal.Permissions.Subject):\n  %s",
			activeSubjectName, portalDir, strings.Join(got, "\n  "))
	}
}

// The detector itself: a rule that cannot fail proves nothing.
func TestDetectorFindsEveryShapeOfUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"qualified call", `package x; import "adminz"; var _ = adminz.ActiveSubject("u", true, nil)`, 1},
		{"aliased import", `package x; import az "adminz"; var _ = az.ActiveSubject("u", true, nil)`, 1},
		{"dot import", `package x; import . "adminz"; var _ = ActiveSubject("u", true, nil)`, 1},
		{"taken as a value", `package x; import "adminz"; var f = adminz.ActiveSubject`, 1},
		{"inside a function", `package x; import "adminz"; func f() { _ = adminz.ActiveSubject("u", true, nil) }`, 1},
		{"its own declaration", `package adminz; func ActiveSubject(id string) int { return 0 }`, 0},
		{"a recursive use inside the declaration", `package adminz; func ActiveSubject(id string) int { return ActiveSubject(id) }`, 1},
		{"declaring a method of that name is not a use", `package x; type T struct{}; func (T) ActiveSubject() {}`, 0},
		{"only mentioned in a comment", "package x\n// adminz.ActiveSubject builds subjects\nvar _ = 1", 0},
		{"unrelated code", `package x; var _ = Subject{}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "x.go", tt.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := references(fset, file, activeSubjectName); len(got) != tt.want {
				t.Fatalf("found %d uses, want %d", len(got), tt.want)
			}
		})
	}

	// And the walk: a use in a sibling package is reported, one in the allowed directory is not.
	root := t.TempDir()
	write := func(rel, src string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const use = "package p\nvar _ = adminz.ActiveSubject\n"
	write("internal/portal/ok.go", use)
	write("internal/portal/ok_test.go", use)
	write("internal/portalhttp/bad.go", use)
	write("cmd/portal/bad.go", use)
	write("internal/portalhttp/bad_test.go", use) // test files are exempt
	got := violations(t, root, activeSubjectName, portalDir)
	if len(got) != 2 {
		t.Fatalf("violations = %v, want the two outside %s", got, portalDir)
	}
}
