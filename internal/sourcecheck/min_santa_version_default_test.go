package sourcecheck

// One stored minimum Santa version (owner decision 1): when none is stored, the web view uses Rudolph's
// fleethealth.DefaultMinSantaVersion, "never a copy of your own" (Rudolph's testdata/webcontract/README.md). So no
// non-test Go file of rudolph-web declares a DefaultMinSantaVersion of its own, or spells today's default, "2026.9", as
// a string literal: a later Rudolph that moves the default would leave such a copy behind. Tests may spell it, as the
// contract's value; prose in comments and templates may name the release that fixed what it fixed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoMinSantaVersionDefaultOfItsOwn(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDir[d.Name()] || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".terraform")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING && (n.Value == `"2026.9"` || n.Value == "`2026.9`") {
					t.Errorf("%s: the string literal %s copies Rudolph's default minimum Santa version; use fleethealth.DefaultMinSantaVersion", fset.Position(n.Pos()), n.Value)
				}
			case *ast.ValueSpec:
				for _, name := range n.Names {
					if name.Name == "DefaultMinSantaVersion" {
						t.Errorf("%s: declares a DefaultMinSantaVersion of its own; the one default is Rudolph's fleethealth.DefaultMinSantaVersion", fset.Position(name.Pos()))
					}
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
