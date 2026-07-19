package mediastore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"knox-media/internal/store"
)

func testEmbedding(v ...float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func TestDeleteCatalogRefreshesPersonCoverCountsAndCentroidInTransaction(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "delete-person.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x');
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','a.jpg','image','active'),(11,1,'b','b.jpg','image','active');
		INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',7,2,2);
		INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h,quality) VALUES(7,10,1,5,0,0,1,1,.9),(8,11,1,5,0,0,1,1,.7)`)
	if err == nil {
		_, err = db.Exec(`UPDATE photo_person SET embedding=? WHERE id=5`, testEmbedding(1, 1))
	}
	if err == nil {
		_, err = db.Exec(`UPDATE photo_face SET embedding=? WHERE id=7`, testEmbedding(1, 0))
	}
	if err == nil {
		_, err = db.Exec(`UPDATE photo_face SET embedding=? WHERE id=8`, testEmbedding(0, 1))
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = DeleteCatalog(db, 10, "a"); err != nil {
		t.Fatal(err)
	}
	var cover, faces, media int
	var centroid []byte
	if err = db.QueryRow(`SELECT cover_face_id,face_count,media_count,embedding FROM photo_person WHERE id=5`).Scan(&cover, &faces, &media, &centroid); err != nil {
		t.Fatal(err)
	}
	if cover != 8 || faces != 1 || media != 1 {
		t.Fatalf("stats=%d/%d/%d", cover, faces, media)
	}
	if string(centroid) != string(testEmbedding(0, 1)) {
		t.Fatalf("centroid not recomputed: %v", centroid)
	}
}

func TestDeleteCatalogDeletesPersonAfterLastFace(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "delete-last-person.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','a.jpg','image','active'); INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',7,1,1); INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h,quality) VALUES(7,10,1,5,0,0,1,1,.9)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = DeleteCatalog(db, 10, "a"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM photo_person WHERE id=5`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("person remains")
	}
}

var _ *sql.Tx

func TestDeleteCatalogAndCollectReturnsFaceAndDerivedPaths(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "delete-artifacts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enc := filepath.Join(t.TempDir(), "face.enc")
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','a.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,0,0,1,1); INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'photo_face_thumb','face:7',?,'w','i')`, enc)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := DeleteCatalogAndCollect(context.Background(), db, 10, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.FaceIDs) != 1 || cleanup.FaceIDs[0] != 7 || len(cleanup.DerivedPaths) != 1 || cleanup.DerivedPaths[0] != enc {
		t.Fatalf("cleanup=%+v", cleanup)
	}
}

func TestDeleteLibraryAndCollectRemovesPhotoCatalogAndCollectsArtifacts(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "delete-library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enc := filepath.Join(t.TempDir(), "face.enc")
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO library_folder(library_id,path) VALUES(1,'x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','a.jpg','image','active'),(11,1,'b','b.jpg','image','active'); INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',7,2,2); INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,5,0,0,1,1),(8,11,1,5,0,0,1,1); INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'photo_face_thumb','face:7',?,'w','i')`, enc)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := DeleteLibraryAndCollect(context.Background(), db, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.FaceIDs) != 2 || len(cleanup.DerivedPaths) != 1 || cleanup.DerivedPaths[0] != enc {
		t.Fatalf("cleanup=%+v", cleanup)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM library WHERE id=1`, `SELECT COUNT(*) FROM media WHERE library_id=1`, `SELECT COUNT(*) FROM photo_person WHERE library_id=1`} {
		var n int
		if err = db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("remaining rows query=%s count=%d", q, n)
		}
	}
}

func TestDeleteLibraryAndCollectRollbackRetainsRowsAndFiles(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "delete-library-rollback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enc := filepath.Join(t.TempDir(), "face.enc")
	plainRoot := t.TempDir()
	plain := filepath.Join(plainRoot, "faces", "7.jpg")
	if err = os.MkdirAll(filepath.Dir(plain), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(plain, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(enc, []byte("enc"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','a.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,0,0,1,1); INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'photo_face_thumb','face:7',?,'w','i'); CREATE TRIGGER reject_library_delete BEFORE DELETE ON library BEGIN SELECT RAISE(FAIL,'reject library'); END`, enc)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := DeleteLibraryAndCollect(context.Background(), db, 1, "")
	if err == nil {
		t.Fatal("expected delete failure")
	}
	if len(cleanup.FaceIDs) != 0 || len(cleanup.DerivedPaths) != 0 {
		t.Fatalf("cleanup exposed before commit=%+v", cleanup)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM library WHERE id=1`, `SELECT COUNT(*) FROM media WHERE id=10`, `SELECT COUNT(*) FROM photo_face WHERE id=7`, `SELECT COUNT(*) FROM media_derived_assets WHERE media_id=10`} {
		var n int
		if err = db.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("rollback count=%d query=%s", n, q)
		}
	}
	if _, err = os.Stat(plain); err != nil {
		t.Fatalf("plain removed: %v", err)
	}
	if _, err = os.Stat(enc); err != nil {
		t.Fatalf("derived removed: %v", err)
	}
}

func TestDeleteCatalogQueuesCleanupTasksInSameTransaction(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cleanup-task.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	preview := t.TempDir()
	enc := filepath.Join(t.TempDir(), "derived", "7.enc")
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','a.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,0,0,1,1); INSERT INTO media_derived_assets(media_id,artifact_kind,logical_name,enc_path,wrapped_dek,iv) VALUES(10,'photo_face_thumb','face:7',?,'w','i')`, enc)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := DeleteCatalogAndCollect(context.Background(), db, 10, "a", filepath.Join(preview, "photos"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cleanup.Paths) != 2 {
		t.Fatalf("paths=%v", cleanup.Paths)
	}
	var tasks int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_file_cleanup_task`).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 2 {
		t.Fatalf("tasks=%d", tasks)
	}
}

func TestRunCleanupBatchRejectsOutsideRootsAndRetries(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cleanup-boundary.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.enc")
	if err = os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO media_file_cleanup_task(path,status,attempts,next_retry_at) VALUES(?,'pending',0,CURRENT_TIMESTAMP)`, outside)
	if err != nil {
		t.Fatal(err)
	}
	done, failed, err := RunCleanupBatch(context.Background(), db, []string{root}, 64)
	if err != nil || done != 0 || failed != 1 {
		t.Fatalf("result=%d/%d err=%v", done, failed, err)
	}
	if _, err = os.Stat(outside); err != nil {
		t.Fatalf("outside file removed: %v", err)
	}
	var attempts int
	var last string
	if err = db.QueryRow(`SELECT attempts,last_error FROM media_file_cleanup_task WHERE path=?`, outside).Scan(&attempts, &last); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || last == "" {
		t.Fatalf("attempts=%d last=%q", attempts, last)
	}
}

func TestCleanupFilesCompletesDurableTask(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "cleanup-success.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	path := filepath.Join(root, "faces", "7.jpg")
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO media_file_cleanup_task(path,status,attempts,next_retry_at) VALUES(?,'pending',0,CURRENT_TIMESTAMP)`, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = CleanupFiles(context.Background(), db, CleanupInfo{Paths: []string{path}}, []string{root}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM media_file_cleanup_task WHERE path=?`, path).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("task remains=%d", n)
	}
}
