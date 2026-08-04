package scanner

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestScannerHotPathHasNoRelationshipBackfill(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "scanner.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && (sel.Sel.Name == "BackfillLibraryTV" || sel.Sel.Name == "BackfillLibraryMusic" || sel.Sel.Name == "MergeUnknownAlbums") {
			t.Errorf("scanner hot path calls %s", sel.Sel.Name)
		}
		return true
	})
}
