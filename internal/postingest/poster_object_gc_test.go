package postingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"knox-media/internal/storage"
)

func writePosterObject(t *testing.T, root, body string, age time.Duration) (string, string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "p.jpg")
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, hash, err := hashPath(tmp)
	if err != nil {
		t.Fatal(err)
	}
	p := storage.PosterObjectPath(root, hash, ".jpg")
	if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err = os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	return p, storage.PosterObjectURL(hash)
}

func TestManagedPosterPathAcceptsOnlyExactCASURL(t *testing.T) {
	root := t.TempDir()
	p, url := writePosterObject(t, root, "valid", time.Hour)
	if got := managedPosterPath(url, root); !sameResolvedPath(got, p) {
		t.Fatalf("got=%q want=%q", got, p)
	}
	bad := []string{"/uploads/posters/objects/sha256/AA/" + filepath.Base(p), "/uploads/posters/objects/sha256/aa/not-a-hash.jpg", "/uploads/posters/objects/sha256/aa/" + filepath.Base(p) + "/x", "/uploads/posters/objects/sha256/../" + filepath.Base(p)}
	for _, u := range bad {
		if got := managedPosterPath(u, root); got != "" {
			t.Fatalf("url=%q got=%q", u, got)
		}
	}
}
func TestCleanupPosterPathsDeletesOnlyUnreferencedCASObject(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	old, oldURL := writePosterObject(t, upload, "old-object", time.Hour)
	newPath, newURL := writePosterObject(t, upload, "new-object", time.Hour)
	if old == newPath {
		t.Fatal("hashes equal")
	}
	if _, e := db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, newURL, task.MediaID); e != nil {
		t.Fatal(e)
	}
	if e := cleanupPosterPaths(context.Background(), db, []string{oldURL}, upload); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(old); !os.IsNotExist(e) {
		t.Fatalf("old retained: %v", e)
	}
	if _, e := os.Stat(newPath); e != nil {
		t.Fatalf("new removed: %v", e)
	}
	if _, e := os.Stat(filepath.Dir(old)); !os.IsNotExist(e) {
		t.Fatalf("empty prefix retained: %v", e)
	}
}

func TestCleanupPosterPathsDeletesOldCASAfterPointerSwitch(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	old, oldURL := writePosterObject(t, upload, "old-hash", time.Hour)
	newPath, newURL := writePosterObject(t, upload, "new-and-different-hash", time.Hour)
	oldRefs, _ := json.Marshal(map[string]any{"path": old, "url": oldURL})
	_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('old-cas',?,?,?,?,?,'fp','poster','committed',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, filepath.Dir(old), string(oldRefs))
	_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, newURL, task.MediaID)
	if e := cleanupPosterPaths(context.Background(), db, []string{oldURL}, upload); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(old); !os.IsNotExist(e) {
		t.Fatalf("old object retained: %v", e)
	}
	if _, e := os.Stat(newPath); e != nil {
		t.Fatalf("new object removed: %v", e)
	}
}
func TestCleanupPosterPathsRetainsOtherCASReferences(t *testing.T) {
	for _, kind := range []string{"evidence", "journal", "meta"} {
		t.Run(kind, func(t *testing.T) {
			db, upload, task := seedCurrentLinkedPosterTask(t)
			p, url := writePosterObject(t, upload, "shared-"+kind, time.Hour)
			refs, _ := json.Marshal(map[string]any{"path": p, "url": url})
			switch kind {
			case "evidence":
				_, _ = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,?, 'poster','fp',?,'test',CURRENT_TIMESTAMP,'other')`, *task.RunID, *task.StepID, task.MediaID, task.Generation, string(refs))
			case "journal":
				_, _ = db.Exec(`INSERT INTO media_asset_stage_journal(stage_id,media_id,run_id,step_id,generation,owner_token,source_fingerprint,artifact_kind,state,staged_path,hashes_sizes_json) VALUES('other',?,?,?,?,?,'fp','poster','staged',?,?)`, task.MediaID, *task.RunID, *task.StepID, task.Generation, task.LeaseOwner, filepath.Dir(p), string(refs))
			case "meta":
				_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, url, task.MediaID)
			}
			if e := cleanupPosterPaths(context.Background(), db, []string{url}, upload); e != nil {
				t.Fatal(e)
			}
			if _, e := os.Stat(p); e != nil {
				t.Fatalf("removed: %v", e)
			}
		})
	}
}
func TestReconcilePosterObjectsSafetyAgeAndProgress(t *testing.T) {
	db, upload, _ := seedCurrentLinkedPosterTask(t)
	old := make([]string, 3)
	for i := range old {
		old[i], _ = writePosterObject(t, upload, fmt.Sprintf("orphan-%d", i), 2*time.Hour)
	}
	fresh, _ := writePosterObject(t, upload, "fresh", time.Minute)
	malformed := filepath.Join(upload, "posters", "objects", "sha256", "zz", "bad.jpg")
	_ = os.MkdirAll(filepath.Dir(malformed), 0755)
	_ = os.WriteFile(malformed, []byte("bad"), 0644)
	total := 0
	for i := 0; i < 4; i++ {
		_, n, e := ReconcilePosterObjects(context.Background(), db, upload, 1, time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		total += n
	}
	if total != 3 {
		t.Fatalf("cleaned=%d", total)
	}
	for _, p := range old {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatalf("old retained %s: %v", p, e)
		}
	}
	for _, p := range []string{fresh, malformed} {
		if _, e := os.Stat(p); e != nil {
			t.Fatalf("safe file removed %s: %v", p, e)
		}
	}
}

func TestReconcilePosterObjectsRetainsReferencedAndUnsafeEntries(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	referenced, url := writePosterObject(t, upload, "referenced-old", 2*time.Hour)
	_, _ = db.Exec(`UPDATE media SET meta_json=json_object('scrape',json_object('poster',?)) WHERE id=?`, url, task.MediaID)
	target, _ := writePosterObject(t, upload, "symlink-target", 2*time.Hour)
	link := filepath.Join(filepath.Dir(target), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.jpg")
	if e := os.Symlink(target, link); e != nil {
		t.Skipf("symlink unavailable: %v", e)
	}
	_, cleaned, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned=%d", cleaned)
	}
	if _, e = os.Stat(referenced); e != nil {
		t.Fatalf("referenced removed: %v", e)
	}
	if st, e := os.Lstat(link); e != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink changed: %v", e)
	}
}
func TestReconcilePosterObjectsCleansOldSealTemps(t *testing.T) {
	db, upload, _ := seedCurrentLinkedPosterTask(t)
	dir := filepath.Join(upload, "posters", "objects", "sha256", "ab")
	_ = os.MkdirAll(dir, 0755)
	old := filepath.Join(dir, ".seal-old.tmp")
	fresh := filepath.Join(dir, ".seal-fresh.tmp")
	_ = os.WriteFile(old, []byte("x"), 0644)
	_ = os.WriteFile(fresh, []byte("x"), 0644)
	when := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, when, when)
	_, cleaned, e := ReconcilePosterObjects(context.Background(), db, upload, 100, time.Hour)
	if e != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, e)
	}
	if _, e = os.Stat(old); !os.IsNotExist(e) {
		t.Fatalf("old temp retained: %v", e)
	}
	if _, e = os.Stat(fresh); e != nil {
		t.Fatalf("fresh temp removed: %v", e)
	}
}
func TestCommittedPosterRecoveryCleansGenerationTempOnly(t *testing.T) {
	db, upload, task := seedCurrentLinkedPosterTask(t)
	r := realPosterStageRunner(t, db, upload)
	s, e := r.StagePoster(context.Background(), posterRequest(t, db, task), 1, screenGrabberConfig())
	if e != nil {
		t.Fatal(e)
	}
	generation := s.Path
	if e = commitStagedPoster(context.Background(), db, task, s); e != nil {
		t.Fatal(e)
	}
	object := storage.PosterObjectPath(upload, s.Hash, ".jpg")
	if _, _, e = ReconcilePosterStages(context.Background(), db, PosterRecoveryRoots{Upload: upload}, 100); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(generation); !os.IsNotExist(e) {
		t.Fatalf("generation retained: %v", e)
	}
	if _, e = os.Stat(object); e != nil {
		t.Fatalf("object removed: %v", e)
	}
}
