package photoface

import (
	"context"
	"knox-media/internal/store"
	"path/filepath"
	"testing"
)

func TestReplaceFacesCropFailurePreservesExistingLifecycle(t *testing.T) {
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "replace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO library(id,name,type,path) VALUES(1,'p','photo','x'); INSERT INTO media(id,library_id,file_id,file_path,file_type,status) VALUES(1,1,'f','x.jpg','image','active'); INSERT INTO photo_face(id,media_id,library_id,bbox_x,bbox_y,bbox_w,bbox_h,embedding,quality) VALUES(9,1,1,0,0,1,1,x'0000',1)`)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker(db, nil, nil, "", "", t.TempDir(), nil)
	res := &DetectResult{Faces: []DetectedFace{{BBox: [4]float64{0, 0, 1, 1}, Embedding: []float64{1, 0}, Score: 1}}}
	if err := w.replaceFaces(context.Background(), 1, 1, filepath.Join(t.TempDir(), "missing.jpg"), res); err == nil {
		t.Fatal("expected crop error")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM photo_face WHERE id=9 AND media_id=1`).Scan(&n)
	if n != 1 {
		t.Fatalf("old face lost after crop failure")
	}
}
