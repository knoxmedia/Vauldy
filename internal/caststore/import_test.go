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

func TestImportCreditsExecutorPropagatesNameLookupAndOccupationFailures(t *testing.T) {
	for _, tc := range []struct {
		name, setup string
		credits     []scraper.CreditMember
	}{{"name_lookup", `DROP TABLE cast_person;CREATE TABLE cast_person(id INTEGER PRIMARY KEY,name TEXT,name_norm TEXT,occupation_json TEXT,deleted_at TEXT);CREATE VIEW cast_person_fault AS SELECT 1`, []scraper.CreditMember{{Name: "Name Only", Occupation: "actor"}}}, {"occupation_select", `INSERT INTO cast_person(id,name,name_norm,tmdb_id,occupation_json)VALUES(2,'A','a','2','[]');CREATE TRIGGER remove_person AFTER UPDATE ON cast_person BEGIN DELETE FROM cast_person WHERE id=NEW.id;END`, []scraper.CreditMember{{TMDBPersonID: "2", Name: "A", Occupation: "actor"}}}, {"occupation_update", `INSERT INTO cast_person(id,name,name_norm,tmdb_id,occupation_json)VALUES(3,'B','b','3','[]');CREATE TRIGGER fail_occ BEFORE UPDATE OF occupation_json ON cast_person BEGIN SELECT RAISE(ABORT,'occupation');END`, []scraper.CreditMember{{TMDBPersonID: "3", Name: "B", Occupation: "actor"}}}} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "f.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, _ = db.Exec(`INSERT INTO library(id,name,type,path)VALUES(1,'l','movie','/l');INSERT INTO media(id,library_id,file_id,file_type)VALUES(1,1,'f','video')`)
			if _, err = db.Exec(tc.setup); err != nil {
				t.Fatal(err)
			}
			if _, err = ImportCreditsExecutor(context.Background(), db, 1, tc.credits, ""); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}
