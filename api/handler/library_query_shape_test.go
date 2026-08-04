package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestListLibrariesQueryUsesGroupedJoins(t *testing.T) {
	src, err := os.ReadFile("library.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	f, err := parser.ParseFile(token.NewFileSet(), "library.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var block string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "ListLibraries" {
			block = text[fsetOffset(src, fn.Pos()):fsetOffset(src, fn.End())]
			break
		}
	}
	if block == "" {
		t.Fatal("ListLibraries not found")
	}
	if strings.Contains(block, "SELECT COUNT(1) FROM media m WHERE m.library_id = l.id") || strings.Count(block, "FROM scan_task st WHERE st.library_id = l.id") != 0 {
		t.Fatalf("correlated subqueries remain in ListLibraries:\n%s", block)
	}
	for _, fragment := range []string{"GROUP BY library_id", "MAX(id)", "LEFT JOIN"} {
		if !strings.Contains(block, fragment) {
			t.Fatalf("missing %q grouped join", fragment)
		}
	}
}

func fsetOffset(src []byte, pos token.Pos) int {
	// parser positions start at one for a single source file.
	off := int(pos) - 1
	if off < 0 {
		return 0
	}
	if off > len(src) {
		return len(src)
	}
	return off
}
