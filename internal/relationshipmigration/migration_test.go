package relationshipmigration_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"knox-media/internal/relationshipmigration"
	"knox-media/internal/store"
)

func TestMigrateMediaRelationshipsRecoversMusicTVAndSkipsMovies(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "relations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
        INSERT INTO library(id,name,type,path) VALUES
          (101,'music','music','E:/music'),(102,'tv','tv','E:/tv'),(103,'movies','movie','E:/movies');
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
          (1001,101,'a','Song','E:/music/Artist/Album/01 Song.mp3','audio','{"music":{"title":"Song","artist":"Artist","album_artist":"Artist","album":"Album","track_number":1}}','active'),
          (1002,102,'e','Show S01E02','E:/tv/Show/Season 01/Show.S01E02.mkv','video','{"tv":{"series_title":"Show","season":1,"episode":2}}','active'),
          (1003,103,'m','Movie','E:/movies/Movie.2026.mkv','video','{"scrape":{"title":"Movie","year":2026}}','active')`)
	if err != nil {
		t.Fatal(err)
	}

	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM music_track WHERE media_id=1001`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM episode_media WHERE media_id=1002`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM episode_media WHERE media_id=1003`, 0)
}

func TestMigrateMediaRelationshipsResumesAndOnlyScansHigherIDs(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "resume.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(201,'music','music','E:/music');
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
        (2001,201,'a','One','E:/music/One.mp3','audio','{"music":{"title":"One","album":"A"}}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`DELETE FROM music_track WHERE media_id=2001;
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
        (2002,201,'b','Two','E:/music/Two.mp3','audio','{"music":{"title":"Two","album":"B"}}','active')`); err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM music_track WHERE media_id=2001`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM music_track WHERE media_id=2002`, 1)
}

func TestOpenSQLiteRunsRelationshipMigrationBeforeReturning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup.sqlite")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(301,'tv','tv','E:/tv');
        INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES
        (3001,301,'e','Show S01E01','E:/tv/Show/Show.S01E01.mkv','video','{"tv":{"series_title":"Show","season":1,"episode":1}}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.OpenSQLiteContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, `SELECT COUNT(*) FROM episode_media WHERE media_id=3001`, 1)
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count=%d want=%d query=%s", got, want, query)
	}
}

func TestMigrateMediaRelationshipsRecoversLooseTVFolderEpisodes(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "loose.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(401,'tv','tv','E:/tv'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(4001,401,'a','Part A','E:/tv/Show/Part A.mkv','video','{}','active'),(4002,401,'b','Part B','E:/tv/Show/Part B.mkv','video','{}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM episode_media WHERE media_id IN (4001,4002)`, 2)
}

func TestMigrationRowAndCursorRollbackTogether(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "atomic.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(501,'music','music','E:/music'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(5001,501,'a','Song','E:/music/Song.mp3','audio','{"music":{"title":"Song","album":"Album"}}','active'); CREATE TRIGGER fail_music_track BEFORE INSERT ON music_track BEGIN SELECT RAISE(ABORT,'forced relation failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err == nil {
		t.Fatal("expected migration failure")
	}
	assertCount(t, db, `SELECT COUNT(*) FROM music_artist WHERE library_id=501`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM music_album WHERE library_id=501`, 0)
	var marker int64
	if err := db.QueryRow(`SELECT last_media_id FROM relationship_migration_state WHERE name='media_relationships_v1'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != 0 {
		t.Fatalf("marker=%d", marker)
	}
}

func TestLooseMigrationPersistsBoundedPhaseAndDeterministicWork(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "loose-state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(601,'tv','tv','E:/tv'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(6001,601,'a','A','E:/tv/Show/A.mkv','video','{}','active'),(6002,601,'b','B','E:/tv/Show/B.mkv','video','{}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var phase string
	var populate, process int64
	if err := db.QueryRow(`SELECT phase,loose_last_media_id,loose_work_last_media_id FROM relationship_migration_state WHERE name='media_relationships_v1'`).Scan(&phase, &populate, &process); err != nil {
		t.Fatal(err)
	}
	if phase != "complete" || populate != 6002 || process != 6002 {
		t.Fatalf("phase=%s populate=%d process=%d", phase, populate, process)
	}
	rows, err := db.Query(`SELECT media_id,episode_num,status FROM relationship_migration_loose_work ORDER BY media_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var id int64
		var ep int
		var status string
		if err := rows.Scan(&id, &ep, &status); err != nil {
			t.Fatal(err)
		}
		if ep != want || status != "done" {
			t.Fatalf("id=%d ep=%d status=%s wantEp=%d", id, ep, status, want)
		}
		want++
	}
	if want != 3 {
		t.Fatalf("work rows=%d", want-1)
	}
}

func TestLooseMigrationResumesPopulationWithoutRenumbering(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "loose-resume.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(701,'tv','tv','E:/tv'); INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(7001,701,'a','A','E:/tv/Show/A.mkv','video','{}','active'),(7002,701,'b','B','E:/tv/Show/B.mkv','video','{}','active'); UPDATE relationship_migration_state SET last_media_id=7002,phase='loose_populate',loose_last_media_id=7001,loose_work_last_media_id=0 WHERE name='media_relationships_v1'; CREATE TABLE IF NOT EXISTS relationship_migration_loose_counter(library_id INTEGER,folder_key TEXT,next_episode INTEGER,PRIMARY KEY(library_id,folder_key)); INSERT INTO relationship_migration_loose_counter VALUES(701,'e:/tv/show',2); CREATE TABLE IF NOT EXISTS relationship_migration_loose_work(media_id INTEGER PRIMARY KEY,library_id INTEGER,folder_key TEXT,show_name TEXT,file_path TEXT,episode_num INTEGER,status TEXT); INSERT INTO relationship_migration_loose_work VALUES(7001,701,'e:/tv/show','Show','E:/tv/Show/A.mkv',1,'pending')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var ep1, ep2 int
	if err := db.QueryRow(`SELECT episode_num FROM relationship_migration_loose_work WHERE media_id=7001`).Scan(&ep1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT episode_num FROM relationship_migration_loose_work WHERE media_id=7002`).Scan(&ep2); err != nil {
		t.Fatal(err)
	}
	if ep1 != 1 || ep2 != 2 {
		t.Fatalf("episodes=%d,%d", ep1, ep2)
	}
}

func TestMigrationOfficialTVLinkerMergesExternalIDsAndFolders(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "external.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(801,'tv','tv','E:/tv');INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(8001,801,'a','A','E:/tv/FolderA/A.mkv','video','{"tv":{"series_title":"Name A","season":1,"episode":1,"tmdb_id":"123","source_folder":"E:/tv/FolderA"}}','active'),(8002,801,'b','B','E:/tv/FolderB/B.mkv','video','{"tv":{"series_title":"Name B","season":1,"episode":2,"tmdb_id":"123","source_folder":"E:/tv/FolderB"}}','active')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM series WHERE library_id=801`, 1)
	var folders string
	if err := db.QueryRow(`SELECT folder_paths FROM series WHERE library_id=801`).Scan(&folders); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(folders, "FolderA") || !strings.Contains(folders, "FolderB") {
		t.Fatalf("folders=%s", folders)
	}
}

func TestMigrateMediaRelationshipsConcurrentSingleOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	db, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(901,'tv','tv','E:/tv');INSERT INTO media(id,library_id,file_id,title,file_path,file_type,meta_json,status) VALUES(9001,901,'a','A','E:/tv/Show/A.mkv','video','{}','active'),(9002,901,'b','B','E:/tv/Show/B.mkv','video','{}','active'),(9003,901,'c','C','E:/tv/Show/C.mkv','video','{}','active');UPDATE relationship_migration_state SET phase='precise',last_media_id=0,loose_last_media_id=0,loose_work_last_media_id=0 WHERE name='media_relationships_v1'`)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- relationshipmigration.MigrateMediaRelationships(context.Background(), db)
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	rows, err := db.Query(`SELECT episode_num FROM relationship_migration_loose_work ORDER BY media_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var got int
		if err := rows.Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("episode=%d want=%d", got, want)
		}
		want++
	}
	if want != 4 {
		t.Fatalf("rows=%d", want-1)
	}
	var marker int64
	if err := db.QueryRow(`SELECT last_media_id FROM relationship_migration_state WHERE name='media_relationships_v1'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != 9003 {
		t.Fatalf("marker=%d", marker)
	}
}
func TestMigrateMediaRelationshipsPropagatesDDLFailure(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bad.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE VIEW relationship_migration_state AS SELECT 1 AS name`); err != nil {
		t.Fatal(err)
	}
	if err := relationshipmigration.MigrateMediaRelationships(context.Background(), db); err == nil {
		t.Fatal("expected DDL failure")
	}
}
