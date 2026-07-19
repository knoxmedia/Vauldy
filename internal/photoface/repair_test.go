package photoface

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"knox-media/internal/imagethumb"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

func openRepairTestDB(t *testing.T) (*Worker, string) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "repair.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	preview := t.TempDir()
	return NewWorker(db, nil, nil, "", "", preview, nil), preview
}

func repairJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 80, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 80; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func seedRepairFace(t *testing.T, w *Worker, preview string, faceID, mediaID, personID int64, quality float64, source bool) {
	t.Helper()
	if _, err := w.DB.Exec(`INSERT OR IGNORE INTO library(id,name,type,path) VALUES(1,'photos','photo','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.DB.Exec(`INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(?,1,?,?,'image','active')`, mediaID, fmt.Sprint(mediaID), filepath.Join(t.TempDir(), "missing.jpg")); err != nil {
		t.Fatal(err)
	}
	var person any
	if personID > 0 {
		person = personID
	}
	if _, err := w.DB.Exec(`INSERT INTO photo_face(id,media_id,library_id,person_id,bbox_x,bbox_y,bbox_w,bbox_h,quality) VALUES(?,?,1,?,.1,.1,.7,.7,?)`, faceID, mediaID, person, quality); err != nil {
		t.Fatal(err)
	}
	if source {
		p := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), mediaID).Thumb
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, repairJPEG(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRepairMissingThumbnailsCreatesJPEGWithoutChangingRelations(t *testing.T) {
	w, preview := openRepairTestDB(t)
	if _, err := w.DB.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x'); INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',7,1,1)`); err != nil {
		t.Fatal(err)
	}
	seedRepairFace(t, w, preview, 7, 10, 5, .9, true)
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("counts=(%d,%d,%d)", checked, repaired, failed)
	}
	data, err := os.ReadFile(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("invalid jpeg: %v", err)
	}
	var mediaID, personID, coverID int64
	if err := w.DB.QueryRow(`SELECT media_id,person_id FROM photo_face WHERE id=7`).Scan(&mediaID, &personID); err != nil {
		t.Fatal(err)
	}
	if err := w.DB.QueryRow(`SELECT cover_face_id FROM photo_person WHERE id=5`).Scan(&coverID); err != nil {
		t.Fatal(err)
	}
	if mediaID != 10 || personID != 5 || coverID != 7 {
		t.Fatalf("relations changed: %d %d %d", mediaID, personID, coverID)
	}
}

func TestRepairMissingThumbnailsSkipsUsableExistingFile(t *testing.T) {
	w, preview := openRepairTestDB(t)
	seedRepairFace(t, w, preview, 7, 10, 0, .9, true)
	target := ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := repairJPEG(t)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(123456, 0)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 || repaired != 0 || failed != 0 {
		t.Fatalf("counts=(%d,%d,%d)", checked, repaired, failed)
	}
	got, _ := os.ReadFile(target)
	st, _ := os.Stat(target)
	if !bytes.Equal(got, original) || !st.ModTime().Equal(old) {
		t.Fatal("usable thumbnail was rewritten")
	}
}

func TestRepairMissingThumbnailsCursorIsBoundedAndWrapsPastFailures(t *testing.T) {
	w, preview := openRepairTestDB(t)
	for i := int64(1); i <= 5; i++ {
		seedRepairFace(t, w, preview, i, 100+i, 0, float64(i), i != 2)
	}
	checked, _, failed, err := w.RepairMissingThumbnails(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 2 || failed != 1 {
		t.Fatalf("first counts checked=%d failed=%d", checked, failed)
	}
	var phase string
	var lastFace int64
	if err := w.DB.QueryRow(`SELECT phase,last_face_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&phase, &lastFace); err != nil {
		t.Fatal(err)
	}
	if phase != "all_faces" || lastFace != 2 {
		t.Fatalf("state=%s/%d", phase, lastFace)
	}
	checked, _, _, _ = w.RepairMissingThumbnails(context.Background(), 2)
	if checked != 2 {
		t.Fatalf("second checked=%d", checked)
	}
	_ = w.DB.QueryRow(`SELECT last_face_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&lastFace)
	if lastFace != 4 {
		t.Fatalf("second cursor=%d", lastFace)
	}
	checked, _, _, _ = w.RepairMissingThumbnails(context.Background(), 2)
	if checked != 1 {
		t.Fatalf("third checked=%d", checked)
	}
	checked, _, _, _ = w.RepairMissingThumbnails(context.Background(), 2)
	if checked != 0 {
		t.Fatalf("wrap transition checked=%d", checked)
	}
	checked, _, failed, _ = w.RepairMissingThumbnails(context.Background(), 2)
	if checked != 0 || failed != 0 {
		t.Fatalf("complete counts=%d/%d", checked, failed)
	}
}

func TestRepairMissingThumbnailsRepairsDanglingCoverByQuality(t *testing.T) {
	w, preview := openRepairTestDB(t)
	if _, err := w.DB.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x'); INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',999,2,2)`); err != nil {
		t.Fatal(err)
	}
	seedRepairFace(t, w, preview, 7, 10, 5, .4, true)
	seedRepairFace(t, w, preview, 8, 11, 5, .9, true)
	checked, _, _, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 {
		t.Fatalf("checked=%d", checked)
	}
	var cover int64
	if err := w.DB.QueryRow(`SELECT cover_face_id FROM photo_person WHERE id=5`).Scan(&cover); err != nil {
		t.Fatal(err)
	}
	if cover != 8 {
		t.Fatalf("cover=%d want 8", cover)
	}
	if _, err := os.Stat(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 8)); err != nil {
		t.Fatal(err)
	}
}

func TestRepairMissingThumbnailsCanceledDoesNotWriteOrAdvance(t *testing.T) {
	w, preview := openRepairTestDB(t)
	seedRepairFace(t, w, preview, 7, 10, 0, .9, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checked, repaired, failed, err := w.RepairMissingThumbnails(ctx, 1)
	if err != context.Canceled || checked != 0 || repaired != 0 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if _, err := os.Stat(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)); !os.IsNotExist(err) {
		t.Fatalf("face file err=%v", err)
	}
	var n int
	if err := w.DB.QueryRow(`SELECT COUNT(*) FROM photo_face_thumb_repair_state`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("state rows=%d", n)
	}
}

func TestRepairMissingThumbnailsUsesDirectSourceWithoutFFmpeg(t *testing.T) {
	w, preview := openRepairTestDB(t)
	source := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(source, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x');
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f',?,'image','active');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.7,.7)`, source)
	if err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if _, err := os.Stat(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)); err != nil {
		t.Fatal(err)
	}
}

func encryptedRepairWorker(t *testing.T) (*Worker, string) {
	t.Helper()
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "encrypted-repair.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := keystore.NewVault("face-thumb-test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "derived")
	derived := &storage.DerivedAssetStore{DB: db, Vault: vault, BaseDir: base}
	preview := t.TempDir()
	return NewWorker(db, vault, derived, "", "", preview, nil), preview
}

func TestRepairMissingThumbnailEncryptedStoresOnlyDerivedCiphertext(t *testing.T) {
	w, preview := encryptedRepairWorker(t)
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES(1,'photos','photo','x',1,1);
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','missing.jpg','image','active');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.7,.7)`)
	if err != nil {
		t.Fatal(err)
	}
	thumb := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 10).Thumb
	if err = os.MkdirAll(filepath.Dir(thumb), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(thumb, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if _, err = os.Stat(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)); !os.IsNotExist(err) {
		t.Fatalf("persistent plaintext exists: %v", err)
	}
	var enc string
	if err = w.DB.QueryRow(`SELECT enc_path FROM media_derived_assets WHERE media_id=10 AND artifact_kind='photo_face_thumb' AND logical_name='face:7'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(enc); err != nil || st.Size() == 0 {
		t.Fatalf("encrypted artifact: %v", err)
	}
	seeker, err := storage.OpenDerivedArtifactSeeker(w.DB, w.Vault, 10, enc, "photo_face_thumb", "face:7")
	if err != nil {
		t.Fatal(err)
	}
	defer seeker.Close()
	if _, err = jpeg.Decode(seeker); err != nil {
		t.Fatalf("decrypted jpeg: %v", err)
	}
}

func TestReplaceFacesEncryptedStoresNoPlaintextThumbnail(t *testing.T) {
	w, preview := encryptedRepairWorker(t)
	source := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(source, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled,encrypted_assets_cleanup_plaintext) VALUES(1,'photos','photo','x',1,1); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f',?,'image','active')`, source)
	if err != nil {
		t.Fatal(err)
	}
	res := &DetectResult{Faces: []DetectedFace{{BBox: [4]float64{.1, .1, .8, .8}, Embedding: []float64{1, 0}, Score: .9}}}
	if err = w.replaceFaces(context.Background(), 1, 10, source, res); err != nil {
		t.Fatal(err)
	}
	var faceID int64
	if err = w.DB.QueryRow(`SELECT id FROM photo_face WHERE media_id=10`).Scan(&faceID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), faceID)); !os.IsNotExist(err) {
		t.Fatalf("persistent plaintext exists: %v", err)
	}
	var n int
	if err = w.DB.QueryRow(`SELECT COUNT(*) FROM media_derived_assets WHERE media_id=10 AND artifact_kind='photo_face_thumb' AND logical_name=?`, fmt.Sprintf("face:%d", faceID)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("derived rows=%d err=%v", n, err)
	}
}

func TestRepairCoversPersonWithStaleZeroMediaCount(t *testing.T) {
	w, preview := openRepairTestDB(t)
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x'); INSERT INTO photo_person(id,library_id,label,face_count,media_count) VALUES(5,1,'p',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	seedRepairFace(t, w, preview, 7, 10, 5, .9, true)
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	var cover, faces, media int
	if err = w.DB.QueryRow(`SELECT cover_face_id,face_count,media_count FROM photo_person WHERE id=5`).Scan(&cover, &faces, &media); err != nil {
		t.Fatal(err)
	}
	if cover != 7 || faces != 1 || media != 1 {
		t.Fatalf("stats=%d/%d/%d", cover, faces, media)
	}
}

func TestRepairCompleteStateDoesNotWriteBeforeAuditDue(t *testing.T) {
	w, _ := openRepairTestDB(t)
	if err := w.EnsureRepairSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := w.DB.Exec(`INSERT INTO photo_face_thumb_repair_state(name,phase,last_person_id,last_face_id,completed_at,next_audit_at,updated_at) VALUES('singleton','complete',0,0,CURRENT_TIMESTAMP,datetime('now','+1 day'),'2001-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = w.DB.QueryRow(`SELECT updated_at FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 32)
	if err != nil || checked+repaired+failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	var after string
	_ = w.DB.QueryRow(`SELECT updated_at FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&after)
	if after != before {
		t.Fatalf("state rewritten before due: %s -> %s", before, after)
	}
}

func TestRepairDueAuditRestartsCoverPhase(t *testing.T) {
	w, _ := openRepairTestDB(t)
	if err := w.EnsureRepairSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := w.DB.Exec(`INSERT INTO photo_face_thumb_repair_state(name,phase,last_person_id,last_face_id,completed_at,next_audit_at) VALUES('singleton','complete',99,99,datetime('now','-1 day'),datetime('now','-1 minute'))`)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var phase string
	var person, face int64
	if err = w.DB.QueryRow(`SELECT phase,last_person_id,last_face_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&phase, &person, &face); err != nil {
		t.Fatal(err)
	}
	if phase != "complete" || person != 0 || face != 0 {
		t.Fatalf("state=%s/%d/%d", phase, person, face)
	}
}

func TestRepairCoverCancellationAfterThumbnailGenerationDoesNotCommitCoverOrCursor(t *testing.T) {
	w, preview := openRepairTestDB(t)
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x'); INSERT INTO photo_person(id,library_id,label,cover_face_id,face_count,media_count) VALUES(5,1,'p',999,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	seedRepairFace(t, w, preview, 7, 10, 5, .9, true)
	ctx, cancel := context.WithCancel(context.Background())
	w.afterThumbnailCommit = cancel
	_, _, _, err = w.RepairMissingThumbnails(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	var cover int64
	if err = w.DB.QueryRow(`SELECT cover_face_id FROM photo_person WHERE id=5`).Scan(&cover); err != nil {
		t.Fatal(err)
	}
	if cover != 999 {
		t.Fatalf("cover committed=%d", cover)
	}
	if _, statErr := os.Stat(ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)); !os.IsNotExist(statErr) {
		t.Fatalf("plaintext remains after cancel: %v", statErr)
	}
	var n int
	_ = w.DB.QueryRow(`SELECT COUNT(*) FROM photo_face_thumb_repair_state`).Scan(&n)
	if n != 0 {
		t.Fatalf("cursor state rows=%d", n)
	}
}

func TestReplaceFacesEncryptedRemovesReplacedFaceDerivedArtifact(t *testing.T) {
	w, _ := encryptedRepairWorker(t)
	source := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(source, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'photos','photo','x',1); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f',?,'image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h,embedding) VALUES(7,10,1,.1,.1,.7,.7,x'0000')`, source)
	if err != nil {
		t.Fatal(err)
	}
	old, err := w.Derived.Write(context.Background(), 10, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(7), bytes.NewReader(repairJPEG(t)))
	if err != nil {
		t.Fatal(err)
	}
	res := &DetectResult{Faces: []DetectedFace{{BBox: [4]float64{.1, .1, .8, .8}, Embedding: []float64{1, 0}, Score: .9}}}
	if err = w.replaceFaces(context.Background(), 1, 10, source, res); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old encrypted artifact remains: %v", err)
	}
	var n int
	_ = w.DB.QueryRow(`SELECT COUNT(*) FROM media_derived_assets WHERE media_id=10 AND logical_name='face:7'`).Scan(&n)
	if n != 0 {
		t.Fatalf("old row remains")
	}
}

func TestReplaceFacesPlainTransactionFailureRemovesPublishedThumbnail(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "replace-plain-rollback.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	preview := t.TempDir()
	source := filepath.Join(t.TempDir(), "source.jpg")
	if err = os.WriteFile(source, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f',?,'image','active'); CREATE TRIGGER reject_face_person BEFORE UPDATE OF person_id ON photo_face BEGIN SELECT RAISE(FAIL,'reject relation'); END`, source)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "", "", preview, nil)
	res := &DetectResult{Faces: []DetectedFace{{BBox: [4]float64{.1, .1, .8, .8}, Embedding: []float64{1, 0}, Score: .9}}}
	if err = w.replaceFaces(context.Background(), 1, 10, source, res); err == nil {
		t.Fatal("expected transaction failure")
	}
	entries, err := os.ReadDir(filepath.Join(preview, "photos", "faces"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("published plaintext remains: %v", entries)
	}
}

func TestRepairEncryptedMigratesLegacyPlainFaceWithoutRecropping(t *testing.T) {
	w, preview := encryptedRepairWorker(t)
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'photos','photo','x',1); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','missing-source.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.2,.2)`)
	if err != nil {
		t.Fatal(err)
	}
	plain := ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)
	if err = os.MkdirAll(filepath.Dir(plain), 0o755); err != nil {
		t.Fatal(err)
	}
	want := repairJPEG(t)
	if err = os.WriteFile(plain, want, 0o644); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if _, err = os.Stat(plain); !os.IsNotExist(err) {
		t.Fatalf("legacy plain remains: %v", err)
	}
	var enc string
	if err = w.DB.QueryRow(`SELECT enc_path FROM media_derived_assets WHERE media_id=10 AND artifact_kind=? AND logical_name=?`, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(7)).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	seeker, err := storage.OpenDerivedArtifactSeeker(w.DB, w.Vault, 10, enc, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(7))
	if err != nil {
		t.Fatal(err)
	}
	defer seeker.Close()
	got, err := io.ReadAll(seeker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("legacy face was recropped or changed")
	}
}

func TestRepairEncryptedMigrationFailureKeepsPlainAndCursor(t *testing.T) {
	w, preview := encryptedRepairWorker(t)
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'photos','photo','x',1); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','missing-source.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.2,.2); INSERT INTO photo_face_thumb_repair_state(name,phase,last_face_id,last_person_id) VALUES('singleton','all_faces',0,0); CREATE TRIGGER reject_face_derived BEFORE INSERT ON media_derived_assets WHEN NEW.artifact_kind='photo_face_thumb' BEGIN SELECT RAISE(FAIL,'reject derived'); END`)
	if err != nil {
		t.Fatal(err)
	}
	plain := ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)
	if err = os.MkdirAll(filepath.Dir(plain), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(plain, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 0 || failed != 1 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if _, err = os.Stat(plain); err != nil {
		t.Fatalf("legacy plain removed on failure: %v", err)
	}
	var cursor int64
	if err = w.DB.QueryRow(`SELECT last_face_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 7 {
		t.Fatalf("cursor=%d want queued face advancement", cursor)
	}
	var queued int
	if err = w.DB.QueryRow(`SELECT COUNT(*) FROM photo_face_thumb_repair_failure WHERE face_id=7`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued failures=%d", queued)
	}
}

func TestRepairFailureQueueAdvancesMainCursorAndRetriesDueFailure(t *testing.T) {
	w, preview := openRepairTestDB(t)
	for i := int64(1); i <= 3; i++ {
		seedRepairFace(t, w, preview, i, 100+i, 0, float64(i), i != 1)
	}
	if _, err := w.DB.Exec(`INSERT INTO photo_face_thumb_repair_state(name,phase,last_person_id,last_face_id) VALUES('singleton','all_faces',0,0)`); err != nil {
		t.Fatal(err)
	}
	checked, _, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || failed != 1 {
		t.Fatalf("first=%d/%d err=%v", checked, failed, err)
	}
	var cursor, failures int64
	if err = w.DB.QueryRow(`SELECT last_face_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 1 {
		t.Fatalf("cursor=%d", cursor)
	}
	if err = w.DB.QueryRow(`SELECT COUNT(*) FROM photo_face_thumb_repair_failure WHERE face_id=1`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("failures=%d", failures)
	}
	for want := int64(2); want <= 3; want++ {
		checked, _, _, err = w.RepairMissingThumbnails(context.Background(), 1)
		if err != nil || checked != 1 {
			t.Fatalf("face%d checked=%d err=%v", want, checked, err)
		}
		if err = w.DB.QueryRow(`SELECT last_face_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&cursor); err != nil {
			t.Fatal(err)
		}
		if cursor != want {
			t.Fatalf("cursor=%d want=%d", cursor, want)
		}
	}
	seedPath := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 101).Thumb
	if err = os.MkdirAll(filepath.Dir(seedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(seedPath, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = w.DB.Exec(`UPDATE photo_face_thumb_repair_failure SET next_retry_at=datetime('now','-1 minute') WHERE face_id=1`); err != nil {
		t.Fatal(err)
	}
	_, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || repaired != 1 || failed != 0 {
		t.Fatalf("retry repaired=%d failed=%d err=%v", repaired, failed, err)
	}
	if err = w.DB.QueryRow(`SELECT COUNT(*) FROM photo_face_thumb_repair_failure WHERE face_id=1`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("failure remains=%d", failures)
	}
}

func TestRepairCoverFailureQueuesPersonAndAdvancesPersonCursor(t *testing.T) {
	w, preview := openRepairTestDB(t)
	if _, err := w.DB.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x'); INSERT INTO photo_person(id,library_id,label,face_count,media_count) VALUES(5,1,'p',1,1)`); err != nil {
		t.Fatal(err)
	}
	seedRepairFace(t, w, preview, 7, 10, 5, .9, false)
	checked, _, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || failed != 1 {
		t.Fatalf("result=%d/%d err=%v", checked, failed, err)
	}
	var cursor, person int64
	if err = w.DB.QueryRow(`SELECT last_person_id FROM photo_face_thumb_repair_state WHERE name='singleton'`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 5 {
		t.Fatalf("person cursor=%d", cursor)
	}
	if err = w.DB.QueryRow(`SELECT person_id FROM photo_face_thumb_repair_failure WHERE face_id=7`).Scan(&person); err != nil {
		t.Fatal(err)
	}
	if person != 5 {
		t.Fatalf("queued person=%d", person)
	}
}

func TestRepairMissingThumbnailsReplacesCorruptNonemptyJPEG(t *testing.T) {
	w, preview := openRepairTestDB(t)
	seedRepairFace(t, w, preview, 7, 10, 0, .9, true)
	target := ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("not-a-jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if err := ValidateFaceJPEG(target); err != nil {
		t.Fatalf("rebuilt file invalid: %v", err)
	}
}

func TestRepairEncryptedReplacesDecryptableNonJPEGArtifact(t *testing.T) {
	w, preview := encryptedRepairWorker(t)
	_, err := w.DB.Exec(`INSERT INTO library(id,name,type,path,encrypted_assets_enabled) VALUES(1,'photos','photo','x',1); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','missing.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.7,.7)`)
	if err != nil {
		t.Fatal(err)
	}
	source := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 10).Thumb
	if err = os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, repairJPEG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Derived.Write(context.Background(), 10, FaceThumbnailArtifactKind, FaceThumbnailLogicalName(7), bytes.NewReader([]byte("not-jpeg"))); err != nil {
		t.Fatal(err)
	}
	checked, repaired, failed, err := w.RepairMissingThumbnails(context.Background(), 1)
	if err != nil || checked != 1 || repaired != 1 || failed != 0 {
		t.Fatalf("result=%d/%d/%d err=%v", checked, repaired, failed, err)
	}
	if err = w.validateEncryptedFaceArtifact(context.Background(), 10, 7); err != nil {
		t.Fatalf("rebuilt encrypted artifact invalid: %v", err)
	}
}
