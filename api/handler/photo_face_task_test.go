package handler

import (
	"context"
	"image"
	"image/jpeg"
	"os"

	"knox-media/internal/app"
	"knox-media/internal/config"
	"knox-media/internal/imagethumb"
	"knox-media/internal/photoface"
	"knox-media/internal/store"
	"path/filepath"
	"sync"
	"testing"
)

func TestPhotoFaceLoopDiscoversUnqueuedImagesBounded(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-discovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','E:/photos'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'a','E:/photos/a.jpg','image','active'),(11,1,'b','E:/photos/b.jpg','image','active')`)
	if err != nil {
		t.Fatal(err)
	}
	worker := photoface.NewWorker(db, nil, nil, "", "", "", nil)
	h := &Handler{App: &app.App{DB: db}, PhotoFaceWorker: worker}
	h.discoverPendingPhotoFaces(context.Background(), 1)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM photo_face_task WHERE status='pending'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued=%d want bounded 1", n)
	}
	h.discoverPendingPhotoFaces(context.Background(), 1)
	_ = db.QueryRow(`SELECT COUNT(*) FROM photo_face_task WHERE status='pending'`).Scan(&n)
	if n != 2 {
		t.Fatalf("queued=%d want 2 after second discovery", n)
	}
	var mu sync.Mutex
	h.runPhotoFaceOnce(context.Background(), &mu)
}

func TestRunPhotoFaceOnceUsesWorkerFailedRetryPolicy(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','E:/photos');
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(20,1,'old','E:/photos/missing-old.jpg','image','active'),(21,1,'recent','E:/photos/missing-recent.jpg','image','active');
		INSERT INTO photo_face_task(media_id,library_id,status,message,updated_at) VALUES(20,1,'failed','old-marker',datetime('now','-31 minutes')),(21,1,'failed','recent-marker',datetime('now','-29 minutes'))`)
	if err != nil {
		t.Fatal(err)
	}
	worker := photoface.NewWorker(db, nil, nil, "", "", "", func() config.PhotoFaceConfig {
		return config.PhotoFaceConfig{FailedRetryMinutes: 30, BatchLimit: 2, MaxConcurrent: 1}
	})
	h := &Handler{App: &app.App{DB: db}, PhotoFaceWorker: worker}
	var mu sync.Mutex
	h.runPhotoFaceOnce(context.Background(), &mu)
	var oldMessage, recentMessage string
	if err := db.QueryRow(`SELECT message FROM photo_face_task WHERE media_id=20`).Scan(&oldMessage); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT message FROM photo_face_task WHERE media_id=21`).Scan(&recentMessage); err != nil {
		t.Fatal(err)
	}
	if oldMessage == "old-marker" {
		t.Fatal("retryable failed task was not run")
	}
	if recentMessage != "recent-marker" {
		t.Fatalf("recent failed task ran early: %q", recentMessage)
	}
}

func TestRunPhotoFaceOnceRepairsThumbnailWithoutPendingTasks(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-repair-loop.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	preview := t.TempDir()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x');
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'f','missing.jpg','image','active');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.5,.5);
		INSERT INTO photo_face_task(media_id,library_id,status) VALUES(10,1,'done')`)
	if err != nil {
		t.Fatal(err)
	}
	source := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 10).Thumb
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	cfg := &config.Config{Data: config.DataConfig{Preview: preview}, PhotoFace: config.PhotoFaceConfig{ThumbnailRepairBatch: 1}}
	worker := photoface.NewWorker(db, nil, nil, "", "", preview, func() config.PhotoFaceConfig { return cfg.PhotoFace })
	h := &Handler{App: &app.App{DB: db, Config: cfg}, PhotoFaceWorker: worker}
	var mu sync.Mutex
	h.runPhotoFaceOnce(context.Background(), &mu)
	if st, err := os.Stat(photoface.ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)); err != nil || st.Size() == 0 {
		t.Fatalf("repair missing: %v", err)
	}
}

func TestRunPhotoFaceOncePrioritizesPendingDetectionOverRepair(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "face-priority.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	preview := t.TempDir()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'photos','photo','x');
		INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(10,1,'repair','missing.jpg','image','active'),(20,1,'pending','missing-pending.jpg','image','active');
		INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h) VALUES(7,10,1,.1,.1,.5,.5);
		INSERT INTO photo_face_task(media_id,library_id,status) VALUES(20,1,'pending')`)
	if err != nil {
		t.Fatal(err)
	}
	source := imagethumb.ExpectedPaths(filepath.Join(preview, "photos"), 10).Thumb
	if err = os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	f, _ := os.Create(source)
	_ = jpeg.Encode(f, img, nil)
	_ = f.Close()
	cfg := &config.Config{Data: config.DataConfig{Preview: preview}, PhotoFace: config.PhotoFaceConfig{ThumbnailRepairBatch: 1, MaxConcurrent: 1, BatchLimit: 1}}
	worker := photoface.NewWorker(db, nil, nil, "", "", preview, func() config.PhotoFaceConfig { return cfg.PhotoFace })
	h := &Handler{App: &app.App{DB: db, Config: cfg}, PhotoFaceWorker: worker}
	var mu sync.Mutex
	h.runPhotoFaceOnce(context.Background(), &mu)
	if _, err = os.Stat(photoface.ExpectedFaceThumbnailPath(filepath.Join(preview, "photos"), 7)); !os.IsNotExist(err) {
		t.Fatalf("repair ran in detection tick: %v", err)
	}
}
