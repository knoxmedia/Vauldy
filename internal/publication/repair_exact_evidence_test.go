package publication

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func seedPublishedPlainVideoEvidence(t *testing.T, db *sql.DB) (int64, string, string, string) {
	t.Helper()
	mediaID := seedLegacyVideo(t, db, 1, "published")
	root := t.TempDir()
	source := filepath.Join(root, "legacy.mp4")
	posterBytes := []byte("plain poster")
	posterHash := sha256.Sum256(posterBytes)
	poster := filepath.Join(root, hex.EncodeToString(posterHash[:])+".jpg")
	if err := os.WriteFile(source, []byte("legacy source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poster, posterBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("/uploads/posters/objects/sha256/%s/%s.jpg", hex.EncodeToString(posterHash[:2]), hex.EncodeToString(posterHash[:]))
	if _, err := db.Exec(`UPDATE media SET file_path=?,ingest_generation=1,meta_json=? WHERE id=?`, source, fmt.Sprintf(`{"scrape":{"poster":%q}}`, url), mediaID); err != nil {
		t.Fatal(err)
	}
	fp, err := SourceFingerprint(source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.Exec(`INSERT INTO media_ingest_run(media_id,generation,reason,status,preserve_visibility,config_snapshot_json,policy_version) VALUES(?,1,'scan','published',1,'{}',2)`, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	result, err = db.Exec(`INSERT INTO media_ingest_step(run_id,media_id,generation,step_type,required,status) VALUES(?,?,1,'poster',1,'done')`, runID, mediaID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := result.LastInsertId()
	refs := fmt.Sprintf(`{"path":%q,"url":%q,"size":%d,"sha256":%q}`, poster, url, len(posterBytes), hex.EncodeToString(posterHash[:]))
	if _, err = db.Exec(`INSERT INTO media_ingest_evidence(run_id,step_id,media_id,generation,kind,source_fingerprint,artifact_refs_json,reason,verified_at,stage_id) VALUES(?,?,?,1,'poster',?,?,'test',CURRENT_TIMESTAMP,'plain-stage')`, runID, stepID, mediaID, fp, refs); err != nil {
		t.Fatal(err)
	}
	return mediaID, source, poster, url
}

func TestRepairLegacyPublishedPlainVideoRevalidatesPosterEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, int64, string, string, string)
	}{
		{"poster deleted", func(t *testing.T, _ *sql.DB, _ int64, _ string, poster string, _ string) {
			if err := os.Remove(poster); err != nil {
				t.Fatal(err)
			}
		}},
		{"metadata URL missing", func(t *testing.T, db *sql.DB, id int64, _, _, _ string) {
			if _, err := db.Exec(`UPDATE media SET meta_json='{}' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
		}},
		{"metadata URL mismatch", func(t *testing.T, db *sql.DB, id int64, _, _, _ string) {
			if _, err := db.Exec(`UPDATE media SET meta_json='{"scrape":{"poster":"/wrong.jpg"}}' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
		}},
		{"poster hash changed", func(t *testing.T, _ *sql.DB, _ int64, _ string, poster string, _ string) {
			if err := os.WriteFile(poster, []byte("changed poster"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"source replaced", func(t *testing.T, _ *sql.DB, _ int64, source, _, _ string) {
			if err := os.WriteFile(source, []byte("replacement source"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openRepairTestDB(t)
			id, source, poster, url := seedPublishedPlainVideoEvidence(t, db)
			tc.mutate(t, db, id, source, poster, url)
			if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 1 {
				t.Fatalf("repair=%d err=%v", n, err)
			}
			var state string
			var preserve int
			if err := db.QueryRow(`SELECT m.publication_state,r.preserve_visibility FROM media m JOIN media_ingest_run r ON r.media_id=m.id AND r.generation=m.ingest_generation WHERE m.id=?`, id).Scan(&state, &preserve); err != nil {
				t.Fatal(err)
			}
			if state != "published" || preserve != 1 {
				t.Fatalf("state=%s preserve=%d", state, preserve)
			}
			if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 0 {
				t.Fatalf("restart repair=%d err=%v", n, err)
			}
		})
	}
}

func TestRepairLegacyPublishedPlainVideoExactEvidenceSkips(t *testing.T) {
	db := openRepairTestDB(t)
	id, _, _, _ := seedPublishedPlainVideoEvidence(t, db)
	if n, err := RepairLegacyMedia(context.Background(), db, NewPlanner(PlanOptions{}), 8); err != nil || n != 0 {
		t.Fatalf("repair=%d err=%v", n, err)
	}
	if runs := repairRunCount(t, db, id); runs != 0 {
		t.Fatalf("repair runs=%d", runs)
	}
}
