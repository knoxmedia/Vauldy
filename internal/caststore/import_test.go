package caststore

import (
	"context"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"path/filepath"
	"testing"
)

func TestImportCreditsExecutorPropagatesWriteFailures(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{{"person", `CREATE TRIGGER fail_credit BEFORE INSERT ON cast_person BEGIN SELECT RAISE(ABORT,'person');END`}, {"patch", `CREATE TRIGGER fail_credit BEFORE UPDATE ON cast_person BEGIN SELECT RAISE(ABORT,'patch');END`}, {"link", `CREATE TRIGGER fail_credit BEFORE INSERT ON media_person BEGIN SELECT RAISE(ABORT,'link');END`}} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, _ = db.Exec(`INSERT INTO library(id,name,type,path)VALUES(1,'l','movie','/l');INSERT INTO media(id,library_id,file_id,file_type)VALUES(1,1,'f','video')`)
			if _, err = db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err = ImportCreditsExecutor(context.Background(), db, 1, []scraper.CreditMember{{TMDBPersonID: "1", Name: "A", Occupation: "actor"}}, ""); err == nil {
				t.Fatal("expected database failure")
			}
		})
	}
}
