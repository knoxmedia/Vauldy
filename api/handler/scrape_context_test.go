package handler

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

func TestRunScrapeTasksPreCancelledContextDoesNotEnqueuePoster(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "scrape-context.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lr, err := db.Exec(`INSERT INTO library(name,type,path,image_providers) VALUES('ctx','movie',?,'embedded')`, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lid, _ := lr.LastInsertId()
	mr, err := db.Exec(`INSERT INTO media(library_id,file_id,file_path,title,file_type,status,meta_json) VALUES(?,'ctx-media','x.mp4','x','video','active','{}')`, lid)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := mr.LastInsertId()
	if _, err = db.Exec(`INSERT INTO scrape_task(media_id,source,status,progress) VALUES(?,'auto','waiting',0)`, mid); err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: &app.App{DB: db, Config: &config.Config{}}, Queue: postingest.NewQueue(db, "handler", nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done, failed := h.runScrapeTasksWithLimit(ctx, nil, 10)
	if done != 0 || failed != 0 {
		t.Fatalf("result=(%d,%d), want zero", done, failed)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND task_type='poster'`, mid).Scan(&n); err != nil || n != 0 {
		t.Fatalf("queue=(%d,%v), want zero", n, err)
	}
}

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
