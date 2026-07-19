package handler

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"knox-media/internal/app"
	"knox-media/internal/store"
)

func openCountingDocumentDB(t *testing.T) (*sql.DB, interface {
	Load() int64
	Store(int64)
}) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "documents-count.sqlite")
	bootstrap, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bootstrap.Exec(`INSERT INTO library(id,name,type,path,enabled) VALUES(1,'docs','document','E:/docs',1)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		if _, err = bootstrap.Exec(`INSERT INTO media(id,library_id,file_id,title,file_path,file_type,status) VALUES(?,1,?,?,?,'document','active')`, i, fmt.Sprintf("d%d", i), fmt.Sprintf("Doc %02d", i), fmt.Sprintf("E:/docs/%d.pdf", i)); err != nil {
			t.Fatal(err)
		}
		if _, err = bootstrap.Exec(`INSERT INTO document_tag(media_id,tag) VALUES(?,?)`, i, fmt.Sprintf("tag-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	db, counter := openCountingSQLitePath(t, path)
	return db, counter
}

func TestQueryDocumentsBatchesTagsWithConstantQueryCount(t *testing.T) {
	db, counter := openCountingDocumentDB(t)
	h := &Handler{App: &app.App{DB: db}}
	counts := make([]int64, 0, 2)
	for _, limit := range []int{1, 20} {
		counter.Store(0)
		items, err := h.queryDocumentsContext(context.Background(), 1, documentListQuery{}, limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != limit {
			t.Fatalf("limit=%d rows=%d", limit, len(items))
		}
		if got := items[0]["tags"].([]string); len(got) != 1 {
			t.Fatalf("tags=%v", got)
		}
		counts = append(counts, counter.Load())
	}
	if counts[0] != 2 || counts[1] != 2 {
		t.Fatalf("query counts for 1/20 rows=%v, want [2 2]", counts)
	}
}

func TestQueryDocumentsEmptyResultSkipsTagQuery(t *testing.T) {
	db, counter := openCountingDocumentDB(t)
	h := &Handler{App: &app.App{DB: db}}
	counter.Store(0)
	items, err := h.queryDocumentsContext(context.Background(), 999, documentListQuery{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items=%v", items)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("queries=%d want 1", got)
	}
}

func TestQueryDocumentsHonorsCanceledContext(t *testing.T) {
	db, _ := openCountingDocumentDB(t)
	h := &Handler{App: &app.App{DB: db}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.queryDocumentsContext(ctx, 1, documentListQuery{}, 20); err == nil {
		t.Fatal("expected cancellation error")
	}
}
