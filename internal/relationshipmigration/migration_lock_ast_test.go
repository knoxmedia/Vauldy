package relationshipmigration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestPhaseBatchesReadStateAndSelectThroughTransaction(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "migration.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{"runPreciseBatch": true, "populateLooseBatch": true, "processLooseBatch": true}
	seen := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || !targets[fn.Name.Name] {
			continue
		}
		seen[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "QueryContext" || sel.Sel.Name == "QueryRowContext" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "db" {
					t.Errorf("%s selects outside transaction", fn.Name.Name)
				}
			}
			return true
		})
	}
	for name := range targets {
		if !seen[name] {
			t.Errorf("missing transactional phase batch %s", name)
		}
	}
}
