package postingest

import (
	"context"
	"knox-media/internal/storage"
	"path/filepath"
	"testing"
)

func TestEncryptionStageRootResolverModesAndDrift(t *testing.T) {
	db, _ := openQueueTestDB(t)
	library := t.TempDir()
	data := t.TempDir()
	custom := t.TempDir()
	res, e := db.Exec(`INSERT INTO library(name,type,path,encrypted_assets_dir_mode) VALUES('roots','photo',?,'library')`, library)
	if e != nil {
		t.Fatal(e)
	}
	lid, _ := res.LastInsertId()
	res, e = db.Exec(`INSERT INTO media(library_id,file_id,file_path,file_type,status,publication_state) VALUES(?,'m',?,'image','active','published')`, lid, filepath.Join(library, "photo.jpg"))
	if e != nil {
		t.Fatal(e)
	}
	mid, _ := res.LastInsertId()
	enc := &storage.AssetEncryptor{DB: db, DataDir: data}
	cases := []struct{ name, mode, custom, want string }{{"library", "library", "", filepath.Join(library, ".encrypted", "image", "stages")}, {"data", "data", "", filepath.Join(data, ".encrypted", "image", "stages")}, {"custom", "custom", custom, filepath.Join(custom, "image", "stages")}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e = db.Exec(`UPDATE library SET encrypted_assets_dir_mode=?,encrypted_assets_custom_dir=? WHERE id=?`, tc.mode, tc.custom, lid)
			if e != nil {
				t.Fatal(e)
			}
			got, e := enc.ResolveEncryptionStageRoot(context.Background(), mid, filepath.Join(library, "photo.jpg"))
			if e != nil || !samePathForEvidence(got, tc.want) {
				t.Fatalf("got=%q want=%q err=%v", got, tc.want, e)
			}
		})
	}
	_, _ = db.Exec(`UPDATE library SET encrypted_assets_dir_mode='custom',encrypted_assets_custom_dir=? WHERE id=?`, custom, lid)
	got, _ := enc.ResolveEncryptionStageRoot(context.Background(), mid, filepath.Join(library, "photo.jpg"))
	if managedEncryptionPath(got, filepath.Join(library, ".encrypted", "image", "stages", "old.enc")) {
		t.Fatal("configuration drift accepted stale stage root")
	}
}
