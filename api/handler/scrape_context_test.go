package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestScrapeBackgroundPathNeverUsesContextBackground(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "scrape_task.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{"StartScrapeTaskLoop": true, "runScrapeWorkerOnce": true, "runScrapeTasksWithLimit": true}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !names[fn.Name.Name] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Background" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "context" {
				t.Errorf("%s uses context.Background", fn.Name.Name)
			}
			return true
		})
	}
}
