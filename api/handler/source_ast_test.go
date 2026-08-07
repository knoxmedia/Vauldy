package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestPostIngestCallbacksContainNoGoroutineOrHeavyWorkerBypass(t *testing.T) {
	mainPath := filepath.Join("..", "..", "cmd", "server", "main.go")
	mainFile, err := parser.ParseFile(token.NewFileSet(), mainPath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", mainPath, err)
	}
	ast.Inspect(mainFile, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "OnMediaAdded" {
				t.Errorf("%s installs shared Scanner.OnMediaAdded callback", mainPath)
			}
		}
		return true
	})

	for _, function := range []string{"EnqueuePostIngestForNewMedia", "enqueuePostIngestForNewMedia"} {
		file, err := parser.ParseFile(token.NewFileSet(), "scan_task.go", nil, 0)
		if err != nil {
			t.Fatalf("parse scan_task.go: %v", err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != function {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.GoStmt:
					t.Errorf("scan_task.go.%s contains go statement at %d", function, n.Pos())
				case *ast.CallExpr:
					switch name := callName(n.Fun); name {
					case "capturePosterFromVideo", "capturePosterOnScan", "ensureAutoPreviewGeneration", "EnsurePendingSubtitleTask", "KickEncryptMedia", "Kick", "Enqueue":
						t.Errorf("scan_task.go.%s contains heavy bypass call %s", function, name)
					}
				}
				return true
			})
		}
	}
}

func callName(expr ast.Expr) string {
	switch n := expr.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.SelectorExpr:
		return n.Sel.Name
	default:
		return ""
	}
}
