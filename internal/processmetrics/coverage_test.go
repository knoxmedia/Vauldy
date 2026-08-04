package processmetrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionFFmpegSitesUseMetricWrapper(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		clean := filepath.ToSlash(path)
		if entry.IsDir() {
			if strings.Contains(clean, "/vendor") || strings.Contains(clean, "/tools") || strings.Contains(clean, "/scripts") || strings.Contains(clean, "/web") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
				return true
			}
			if likelyFFmpegCall(file, call) {
				violations = append(violations, fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("direct FFmpeg constructors remain: %s", strings.Join(violations, ", "))
	}
}

func likelyFFmpegCall(file *ast.File, call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		switch value := arg.(type) {
		case *ast.Ident:
			name := strings.ToLower(value.Name)
			if strings.Contains(name, "ffmpeg") && !strings.Contains(name, "ffprobe") {
				return true
			}
		case *ast.SelectorExpr:
			if strings.Contains(strings.ToLower(value.Sel.Name), "ffmpeg") && !strings.Contains(strings.ToLower(value.Sel.Name), "ffprobe") {
				return true
			}
		}
	}
	_ = file
	return false
}
