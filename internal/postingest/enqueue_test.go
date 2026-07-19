package postingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"knox-media/internal/config"
)

func boolPtr(v bool) *bool { return &v }

func seedEnqueueMedia(t *testing.T, db *sql.DB, fileType string, preview, encrypted int) (int64, int64) {
	t.Helper()
	res, err := db.Exec(`INSERT INTO library (name,type,path,preview_extract,encrypted_assets_enabled) VALUES ('enqueue','video','/enqueue',?,?)`, preview, encrypted)
	if err != nil {
		t.Fatalf("insert library: %v", err)
	}
	libraryID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO media (library_id,file_id,file_type,duration) VALUES (?,?,?,120)`, libraryID, fmt.Sprintf("enqueue-%d-%s", libraryID, fileType), fileType)
	if err != nil {
		t.Fatalf("insert media: %v", err)
	}
	mediaID, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO scan_task (library_id,status,source) VALUES (?,'done','manual')`, libraryID)
	if err != nil {
		t.Fatalf("insert scan: %v", err)
	}
	scanID, _ := res.LastInsertId()
	return mediaID, scanID
}

func TestEnqueuer_RespectsCapabilities(t *testing.T) {
	cases := []struct {
		name                            string
		fileType                        string
		preview                         int
		subtitle, atrack, globalEncrypt bool
		libraryEncrypt                  int
		want                            []TaskType
	}{
		{"video defaults", "video", 0, false, false, false, 0, []TaskType{TaskPoster, TaskKeyframe}},
		{"all video capabilities", "video", 1, true, true, true, 1, []TaskType{TaskPoster, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt}},
		{"library disables encryption", "video", 0, true, true, true, 0, []TaskType{TaskPoster, TaskKeyframe, TaskSubtitle, TaskAtrack}},
		{"global disables encryption", "video", 0, true, true, false, 1, []TaskType{TaskPoster, TaskKeyframe, TaskSubtitle, TaskAtrack}},
		{"non video has no first batch tasks", "image", 1, true, true, true, 1, nil},
	}
	t.Run("zero config uses true defaults", func(t *testing.T) {
		db, _ := openQueueTestDB(t)
		mediaID, scanID := seedEnqueueMedia(t, db, "video", 0, 1)
		got, err := NewEnqueuer(db, &config.Config{}, nil).EnqueueMedia(context.Background(), mediaID, &scanID, "video")
		want := []TaskType{TaskPoster, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("zero config=(%v,%v) want %v", got, err, want)
		}
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := openQueueTestDB(t)
			mediaID, scanID := seedEnqueueMedia(t, db, tc.fileType, tc.preview, tc.libraryEncrypt)
			cfg := &config.Config{
				Subtitle:        config.SubtitleProcessingConfig{AutoOnScan: boolPtr(tc.subtitle)},
				ATrack:          config.ATrackConfig{AutoOnScan: boolPtr(tc.atrack)},
				EncryptedAssets: config.EncryptedAssetsConfig{Enabled: boolPtr(tc.globalEncrypt)},
			}
			got, err := NewEnqueuer(db, cfg, nil).EnqueueMedia(context.Background(), mediaID, &scanID, tc.fileType)
			if err != nil {
				t.Fatalf("EnqueueMedia: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("types=%v want %v", got, tc.want)
			}
			rows, err := db.Query(`SELECT task_type FROM post_ingest_task WHERE media_id=? ORDER BY id`, mediaID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var persisted []TaskType
			for rows.Next() {
				var typ TaskType
				if err := rows.Scan(&typ); err != nil {
					t.Fatal(err)
				}
				persisted = append(persisted, typ)
			}
			if !reflect.DeepEqual(persisted, tc.want) {
				t.Fatalf("persisted=%v want %v", persisted, tc.want)
			}
		})
	}
}

func TestEnqueuer_UsesLoadedConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	if !cfg.SubtitleAutoOnScan() || !cfg.ATrackAutoOnScan() || !cfg.EncryptedAssetsEnabled() {
		t.Fatalf("loaded defaults subtitle=%v atrack=%v encrypt=%v", cfg.SubtitleAutoOnScan(), cfg.ATrackAutoOnScan(), cfg.EncryptedAssetsEnabled())
	}
	db, _ := openQueueTestDB(t)
	mediaID, scanID := seedEnqueueMedia(t, db, "video", 1, 1)
	got, err := NewEnqueuer(db, cfg, nil).EnqueueMedia(context.Background(), mediaID, &scanID, "video")
	want := []TaskType{TaskPoster, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded defaults=(%v,%v) want %v", got, err, want)
	}
}

func TestEnqueuer_KeepsOriginalScanOwnership(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, scanOne := seedEnqueueMedia(t, db, "video", 1, 1)
	var libraryID int64
	if err := db.QueryRow(`SELECT library_id FROM media WHERE id=?`, mediaID).Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO scan_task (library_id,status,source) VALUES (?,'running','manual')`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	scanTwo, _ := res.LastInsertId()
	cfg := &config.Config{Subtitle: config.SubtitleProcessingConfig{AutoOnScan: boolPtr(true)}, ATrack: config.ATrackConfig{AutoOnScan: boolPtr(true)}, EncryptedAssets: config.EncryptedAssetsConfig{Enabled: boolPtr(true)}}
	e := NewEnqueuer(db, cfg, nil)
	want := []TaskType{TaskPoster, TaskPreview, TaskKeyframe, TaskSubtitle, TaskAtrack, TaskEncrypt}
	got, err := e.EnqueueMedia(context.Background(), mediaID, &scanOne, "video")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("first=(%v,%v)", got, err)
	}
	got, err = e.EnqueueMedia(context.Background(), mediaID, &scanTwo, "video")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("repeat=(%v,%v)", got, err)
	}
	var count, wrong int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN scan_task_id<>? THEN 1 ELSE 0 END) FROM post_ingest_task WHERE media_id=?`, scanOne, mediaID).Scan(&count, &wrong); err != nil {
		t.Fatal(err)
	}
	if count != len(want) || wrong != 0 {
		t.Fatalf("count=%d wrong owner=%d", count, wrong)
	}

	otherMedia, otherScan := seedEnqueueMedia(t, db, "video", 0, 0)
	_, err = e.EnqueueMedia(context.Background(), mediaID, &otherScan, "video")
	var ce ClassifiedError
	if !errors.As(err, &ce) || ce.Kind != FailurePermanent {
		t.Fatalf("cross-library scan error=%v want permanent", err)
	}
	var otherCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, otherMedia).Scan(&otherCount); err != nil {
		t.Fatal(err)
	}
	if otherCount != 0 {
		t.Fatalf("cross-library validation inserted %d rows", otherCount)
	}

	mediaTwo, ownScan := seedEnqueueMedia(t, db, "video", 0, 0)
	if _, err := e.EnqueueMedia(context.Background(), mediaTwo, nil, "video"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EnqueueMedia(context.Background(), mediaTwo, &ownScan, "video"); err != nil {
		t.Fatal(err)
	}
	var nonNull int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=? AND scan_task_id IS NOT NULL`, mediaTwo).Scan(&nonNull); err != nil {
		t.Fatal(err)
	}
	if nonNull != 0 {
		t.Fatalf("nil ownership was overwritten on %d rows", nonNull)
	}
}

func TestEnqueuer_PreservesDomainTables(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, scanID := seedEnqueueMedia(t, db, "video", 1, 1)
	if _, err := db.Exec(`INSERT INTO preview_task(media_id,status) VALUES (?,'done'); INSERT INTO subtitle_task(media_id,status) VALUES (?,'failed'); INSERT INTO atrack_task(media_id,status) VALUES (?,'done'); INSERT INTO keyframe_task(media_id,status) VALUES (?,'failed')`, mediaID, mediaID, mediaID, mediaID); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Subtitle: config.SubtitleProcessingConfig{AutoOnScan: boolPtr(true)}, ATrack: config.ATrackConfig{AutoOnScan: boolPtr(true)}, EncryptedAssets: config.EncryptedAssetsConfig{Enabled: boolPtr(true)}}
	if _, err := NewEnqueuer(db, cfg, nil).EnqueueMedia(context.Background(), mediaID, &scanID, "video"); err != nil {
		t.Fatal(err)
	}
	var waitingDefaults, attemptsDefaults, maxAttemptsDefaults, availableDefaults int
	if err := db.QueryRow(`SELECT
		SUM(CASE WHEN status='waiting' THEN 1 ELSE 0 END),
		SUM(CASE WHEN attempts=0 THEN 1 ELSE 0 END),
		SUM(CASE WHEN max_attempts=3 THEN 1 ELSE 0 END),
		SUM(CASE WHEN available_at IS NOT NULL THEN 1 ELSE 0 END)
		FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&waitingDefaults, &attemptsDefaults, &maxAttemptsDefaults, &availableDefaults); err != nil {
		t.Fatal(err)
	}
	if waitingDefaults != 6 || attemptsDefaults != 6 || maxAttemptsDefaults != 6 || availableDefaults != 6 {
		t.Fatalf("schema defaults waiting=%d attempts=%d max=%d available=%d", waitingDefaults, attemptsDefaults, maxAttemptsDefaults, availableDefaults)
	}
	for table, want := range map[string]string{"preview_task": "done", "subtitle_task": "failed", "atrack_task": "done", "keyframe_task": "failed"} {
		var got string
		if err := db.QueryRow(`SELECT status FROM `+table+` WHERE media_id=?`, mediaID).Scan(&got); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if got != want {
			t.Fatalf("%s status=%q want %q", table, got, want)
		}
	}
}

func TestEnqueuer_RejectsInvalidInputsAndRollsBack(t *testing.T) {
	db, _ := openQueueTestDB(t)
	mediaID, scanID := seedEnqueueMedia(t, db, "video", 1, 1)
	cfg := &config.Config{Subtitle: config.SubtitleProcessingConfig{AutoOnScan: boolPtr(true)}, ATrack: config.ATrackConfig{AutoOnScan: boolPtr(true)}, EncryptedAssets: config.EncryptedAssetsConfig{Enabled: boolPtr(true)}}
	tests := []struct {
		name    string
		e       *Enqueuer
		mediaID int64
		scan    *int64
		hint    string
		cancel  bool
	}{
		{"nil database", NewEnqueuer(nil, cfg, nil), mediaID, &scanID, "video", false},
		{"nil config", NewEnqueuer(db, nil, nil), mediaID, &scanID, "video", false},
		{"missing media", NewEnqueuer(db, cfg, nil), 1 << 60, &scanID, "video", false},
		{"mismatched hint", NewEnqueuer(db, cfg, nil), mediaID, &scanID, "image", false},
		{"cancelled context", NewEnqueuer(db, cfg, nil), mediaID, &scanID, "video", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := tc.e.EnqueueMedia(ctx, tc.mediaID, tc.scan, tc.hint)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v want context canceled", err)
			}
		})
	}
	badScan := int64(1 << 60)
	_, err := NewEnqueuer(db, cfg, nil).EnqueueMedia(context.Background(), mediaID, &badScan, "video")
	var invalidScan ClassifiedError
	if !errors.As(err, &invalidScan) || invalidScan.Kind != FailurePermanent {
		t.Fatalf("invalid scan error=%v want permanent", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial transaction left %d rows", count)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_keyframe_enqueue BEFORE INSERT ON post_ingest_task WHEN NEW.task_type='keyframe' BEGIN SELECT RAISE(FAIL,'reject keyframe'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = NewEnqueuer(db, cfg, nil).EnqueueMedia(context.Background(), mediaID, &scanID, "video")
	var constraint ClassifiedError
	if !errors.As(err, &constraint) || constraint.Kind != FailurePermanent {
		t.Fatalf("constraint error=%v want permanent", err)
	}
	if got := failureKind(err); got != FailurePermanent {
		t.Fatalf("failureKind=%v want permanent", got)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM post_ingest_task WHERE media_id=?`, mediaID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback left %d partial rows", count)
	}

	var ce ClassifiedError
	_, err = NewEnqueuer(db, cfg, nil).EnqueueMedia(context.Background(), mediaID, &scanID, " image ")
	if !errors.As(err, &ce) || ce.Kind != FailurePermanent || !strings.Contains(err.Error(), "file type") {
		t.Fatalf("mismatch error=%v", err)
	}
}
