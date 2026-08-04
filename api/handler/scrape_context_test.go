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

func TestClaimScrapeTaskEnforcesMediaVisibleDependency(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "dependency.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'l','video','/l');
INSERT INTO media(id,library_id,file_id,file_type,ingest_generation,publication_state) VALUES(10,1,'f','video',1,'processing');
INSERT INTO media_ingest_run(id,media_id,generation,reason,status,config_snapshot_json,policy_version) VALUES(20,10,1,'scan','processing','{}',3);
INSERT INTO media_ingest_step(id,run_id,media_id,generation,step_type,required,status) VALUES
 (29,20,10,1,'media_visible',0,'waiting'),
 (30,20,10,1,'scrape',0,'waiting');
INSERT INTO media_ingest_step_dependency(step_id,depends_on_step_id,dependency_kind) VALUES(30,29,'success');
INSERT INTO scrape_task(id,media_id,status,ingest_run_id,ingest_step_id,generation) VALUES(40,10,'waiting',20,30,1)`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := claimScrapeTaskWithOwner(context.Background(), db, 40); err != nil || got != nil {
		t.Fatalf("hidden claim=%+v err=%v", got, err)
	}
	_, err = db.Exec(`UPDATE media SET publication_state='published',published_at=CURRENT_TIMESTAMP WHERE id=10;
UPDATE media_ingest_run SET status='published' WHERE id=20;
UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP WHERE id=29`)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := claimScrapeTaskWithOwner(context.Background(), db, 40); err != nil || got == nil {
		t.Fatalf("visible claim=%+v err=%v", got, err)
	}
}
