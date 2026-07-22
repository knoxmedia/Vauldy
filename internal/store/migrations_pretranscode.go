// Package store migrations_pretranscode.go registers the commercial-only
// pretranscode schema via init(). The community build excludes this file,
// so transcode_preset / pretranscode_* tables never appear in Vauldy.
package store

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

func init() {
	RegisterEnterpriseMigration(Migration{
		ID: "0001_create_pretranscode_tables",
		Up: func(db *sql.DB) error {
			// transcode_task gets a task_type discriminator column so the
			// existing community table can host both batch and pretranscode
			// rows without a structural fork. ALTER ... ADD COLUMN is
			// idempotent via the guard on column existence.
			addColumnIfMissing(db, "transcode_task", "task_type TEXT NOT NULL DEFAULT 'batch'")
			addColumnIfMissing(db, "transcode_task", "started_at TIMESTAMP")
			addColumnIfMissing(db, "transcode_task", "completed_at TIMESTAMP")
			addColumnIfMissing(db, "transcode_task", "preset_id INTEGER")
			addColumnIfMissing(db, "transcode_task", "ingest_run_id INTEGER")
			addColumnIfMissing(db, "transcode_task", "ingest_step_id INTEGER")
			addColumnIfMissing(db, "transcode_task", "generation INTEGER")

			stmts := []string{
				`CREATE TABLE IF NOT EXISTS transcode_preset (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					name TEXT NOT NULL,
					description TEXT,
					output_format TEXT NOT NULL,
					encryption_mode TEXT DEFAULT 'none',
					video_codec TEXT NOT NULL,
					video_preset TEXT,
					video_crf INTEGER,
					video_maxrate TEXT,
					video_bufsize TEXT,
					video_profile TEXT,
					video_gop INTEGER,
					video_pix_fmt TEXT,
					audio_codec TEXT NOT NULL,
					audio_bitrate TEXT NOT NULL,
					audio_channels INTEGER,
					audio_sample_rate INTEGER,
					hw_fallback INTEGER DEFAULT 1,
					is_builtin INTEGER DEFAULT 0,
					is_enabled INTEGER DEFAULT 1,
					sort_order INTEGER DEFAULT 0,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				)`,
				`CREATE TABLE IF NOT EXISTS preset_rendition (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					preset_id INTEGER NOT NULL,
					name TEXT NOT NULL,
					height INTEGER NOT NULL,
					width INTEGER,
					video_bitrate TEXT NOT NULL,
					audio_bitrate TEXT,
					video_rate TEXT NOT NULL DEFAULT '',
					audio_rate TEXT NOT NULL DEFAULT '',
					bandwidth INTEGER,
					sort_order INTEGER DEFAULT 0,
					FOREIGN KEY (preset_id) REFERENCES transcode_preset(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_preset_rendition_preset ON preset_rendition(preset_id, sort_order)`,
				`CREATE TABLE IF NOT EXISTS pretranscode_task_meta (
					task_id INTEGER PRIMARY KEY,
					preset_id INTEGER NOT NULL,
					output_format TEXT NOT NULL,
					encryption_mode TEXT DEFAULT 'none',
					priority TEXT DEFAULT 'normal',
					output_path TEXT,
					ingest_jobs_snapshot_json TEXT,
					FOREIGN KEY (task_id) REFERENCES transcode_task(id) ON DELETE CASCADE,
					FOREIGN KEY (preset_id) REFERENCES transcode_preset(id)
				)`,
				`CREATE TABLE IF NOT EXISTS pretranscode_rendition_job (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					task_id INTEGER NOT NULL,
					rendition_id INTEGER,
					rendition_name TEXT NOT NULL DEFAULT '',
					status TEXT DEFAULT 'waiting',
					progress INTEGER DEFAULT 0,
					output_path TEXT,
					error_message TEXT,
					encoder_used TEXT,
					started_at TIMESTAMP,
					completed_at TIMESTAMP,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					available_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (task_id) REFERENCES transcode_task(id) ON DELETE CASCADE,
					FOREIGN KEY (rendition_id) REFERENCES preset_rendition(id) ON DELETE SET NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_pretranscode_job_status ON pretranscode_rendition_job(status, created_at)`,
				`CREATE INDEX IF NOT EXISTS idx_pretranscode_job_task ON pretranscode_rendition_job(task_id)`,
				`CREATE TABLE IF NOT EXISTS pretranscode_webhook (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					name TEXT NOT NULL,
					url TEXT NOT NULL,
					events TEXT NOT NULL,
					headers TEXT,
					secret TEXT,
					is_enabled INTEGER DEFAULT 1,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
				)`,
				`CREATE TABLE IF NOT EXISTS pretranscode_webhook_log (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					webhook_id INTEGER NOT NULL,
					event TEXT NOT NULL,
					payload TEXT,
					response_code INTEGER,
					response_body TEXT,
					error TEXT,
					retry_count INTEGER DEFAULT 0,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					FOREIGN KEY (webhook_id) REFERENCES pretranscode_webhook(id) ON DELETE CASCADE
				)`,
				`CREATE INDEX IF NOT EXISTS idx_pretranscode_webhook_log ON pretranscode_webhook_log(webhook_id, created_at)`,
			}
			for _, s := range stmts {
				if _, err := db.Exec(s); err != nil {
					return fmt.Errorf("pretranscode migration: %w", err)
				}
			}
			// Ensure all columns exist (for databases created before these columns were added).
			// CREATE TABLE IF NOT EXISTS is a no-op when the table already exists, so
			// we must explicitly add any columns introduced after the initial creation.
			addColumnIfMissing(db, "pretranscode_rendition_job", "rendition_id INTEGER NOT NULL DEFAULT 0")
			addColumnIfMissing(db, "pretranscode_rendition_job", "rendition_name TEXT NOT NULL DEFAULT ''")
			addColumnIfMissing(db, "pretranscode_rendition_job", "status TEXT DEFAULT 'waiting'")
			addColumnIfMissing(db, "pretranscode_rendition_job", "progress INTEGER DEFAULT 0")
			addColumnIfMissing(db, "pretranscode_rendition_job", "output_path TEXT")
			addColumnIfMissing(db, "pretranscode_rendition_job", "error_message TEXT")
			addColumnIfMissing(db, "pretranscode_rendition_job", "encoder_used TEXT")
			addColumnIfMissing(db, "pretranscode_rendition_job", "started_at TIMESTAMP")
			addColumnIfMissing(db, "pretranscode_rendition_job", "completed_at TIMESTAMP")
			addColumnIfMissing(db, "pretranscode_rendition_job", "created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP")
			if err := ensurePretranscodeRenditionAvailability(db); err != nil {
				return err
			}
			addColumnIfMissing(db, "pretranscode_rendition_job", "lease_owner TEXT")
			addColumnIfMissing(db, "pretranscode_rendition_job", "lease_until TIMESTAMP")
			addColumnIfMissing(db, "pretranscode_rendition_job", "config_snapshot_json TEXT")
			// Ensure pretranscode_task_meta columns exist
			addColumnIfMissing(db, "pretranscode_task_meta", "output_format TEXT NOT NULL DEFAULT 'hls'")
			addColumnIfMissing(db, "pretranscode_task_meta", "encryption_mode TEXT DEFAULT 'none'")
			addColumnIfMissing(db, "pretranscode_task_meta", "priority TEXT DEFAULT 'normal'")
			addColumnIfMissing(db, "pretranscode_task_meta", "output_path TEXT")
			addColumnIfMissing(db, "pretranscode_task_meta", "ingest_jobs_snapshot_json TEXT")
			return seedBuiltinPresets(db)
		},
	})
	RegisterEnterpriseMigration(Migration{
		ID: "0002_preset_rendition_bitrate_columns",
		Up: migratePresetRenditionBitrateColumns,
	})
	RegisterEnterpriseMigration(Migration{
		ID: "0003_webhook_columns",
		Up: func(db *sql.DB) error {
			// Ensure pretranscode_webhook has all columns expected by the
			// current code.  Tables created by an earlier schema (or a
			// community fork) may be missing newer columns.
			addColumnIfMissing(db, "pretranscode_webhook", "name TEXT NOT NULL DEFAULT ''")
			addColumnIfMissing(db, "pretranscode_webhook", "url TEXT NOT NULL DEFAULT ''")
			addColumnIfMissing(db, "pretranscode_webhook", "events TEXT NOT NULL DEFAULT '[]'")
			addColumnIfMissing(db, "pretranscode_webhook", "headers TEXT")
			addColumnIfMissing(db, "pretranscode_webhook", "secret TEXT")
			addColumnIfMissing(db, "pretranscode_webhook", "is_enabled INTEGER DEFAULT 1")
			addColumnIfMissing(db, "pretranscode_webhook", "created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP")
			addColumnIfMissing(db, "pretranscode_webhook", "updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP")
			return nil
		},
	})
	RegisterEnterpriseMigration(Migration{
		ID: "0004_pretranscode_rendition_job_fk",
		Up: migratePretranscodeRenditionJobFK,
	})
	RegisterEnterpriseMigration(Migration{
		ID: "0005_transcode_preset_audio_codec",
		Up: migrateTranscodePresetAudioCodec,
	})
	RegisterEnterpriseMigration(Migration{
		ID: "0006_transcode_preset_output_dir",
		Up: migrateTranscodePresetOutputDir,
	})
}

// migratePresetRenditionBitrateColumns aligns legacy preset_rendition schemas
// that used video_rate/audio_rate with the current video_bitrate/audio_bitrate
// column names expected by the pretranscode service.
func migratePresetRenditionBitrateColumns(db *sql.DB) error {
	addColumnIfMissing(db, "preset_rendition", "video_bitrate TEXT")
	addColumnIfMissing(db, "preset_rendition", "audio_bitrate TEXT")
	addColumnIfMissing(db, "preset_rendition", "video_rate TEXT NOT NULL DEFAULT ''")
	addColumnIfMissing(db, "preset_rendition", "audio_rate TEXT NOT NULL DEFAULT ''")

	var hasVideoRate int
	_ = db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('preset_rendition') WHERE name='video_rate'`).Scan(&hasVideoRate)
	if hasVideoRate > 0 {
		// Copy old 鈫?new (legacy tables that had video_rate but not video_bitrate).
		if _, err := db.Exec(`UPDATE preset_rendition
			SET video_bitrate = video_rate
			WHERE COALESCE(video_bitrate,'') = '' AND COALESCE(video_rate,'') != ''`); err != nil {
			return fmt.Errorf("copy video_rate -> video_bitrate: %w", err)
		}
		// Copy new 鈫?old (tables created by migration 0001 that have video_bitrate but empty video_rate).
		if _, err := db.Exec(`UPDATE preset_rendition
			SET video_rate = video_bitrate
			WHERE COALESCE(video_rate,'') = '' AND COALESCE(video_bitrate,'') != ''`); err != nil {
			return fmt.Errorf("copy video_bitrate -> video_rate: %w", err)
		}
	}
	var hasAudioRate int
	_ = db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('preset_rendition') WHERE name='audio_rate'`).Scan(&hasAudioRate)
	if hasAudioRate > 0 {
		// Copy old 鈫?new.
		if _, err := db.Exec(`UPDATE preset_rendition
			SET audio_bitrate = audio_rate
			WHERE COALESCE(audio_bitrate,'') = '' AND COALESCE(audio_rate,'') != ''`); err != nil {
			return fmt.Errorf("copy audio_rate -> audio_bitrate: %w", err)
		}
		// Copy new 鈫?old.
		if _, err := db.Exec(`UPDATE preset_rendition
			SET audio_rate = COALESCE(audio_bitrate, '')
			WHERE COALESCE(audio_rate,'') = '' AND COALESCE(audio_bitrate,'') != ''`); err != nil {
			return fmt.Errorf("copy audio_bitrate -> audio_rate: %w", err)
		}
	}
	_, _ = db.Exec(`UPDATE preset_rendition SET video_bitrate = '2800k' WHERE COALESCE(video_bitrate,'') = ''`)
	return nil
}

// migratePretranscodeRenditionJobFK rebuilds pretranscode_rendition_job when an
// older schema still references the legacy pretranscode_task table instead of
// transcode_task. CREATE TABLE IF NOT EXISTS cannot fix an existing wrong FK.
func migratePretranscodeRenditionJobFK(db *sql.DB) error {
	var createSQL sql.NullString
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='pretranscode_rendition_job'`).Scan(&createSQL)
	if err == sql.ErrNoRows || !createSQL.Valid {
		return nil
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(createSQL.String), ""))
	if strings.Contains(normalized, "foreignkey(task_id)referencestranscode_task(id)ondeletecascade") && strings.Contains(normalized, "foreignkey(rendition_id)referencespreset_rendition(id)ondeletesetnull") && strings.Contains(normalized, "config_snapshot_json") {
		return nil
	}

	log.Printf("pretranscode migration: rebuilding pretranscode_rendition_job constraints")
	// Standalone callers may invoke this migration against a pre-0001 fixture.
	if err := ensurePretranscodeRenditionAvailability(db); err != nil {
		return err
	}
	addColumnIfMissing(db, "pretranscode_rendition_job", "lease_owner TEXT")
	addColumnIfMissing(db, "pretranscode_rendition_job", "lease_until TIMESTAMP")
	addColumnIfMissing(db, "pretranscode_rendition_job", "config_snapshot_json TEXT")

	stmts := []string{
		`CREATE TABLE pretranscode_rendition_job_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL,
			rendition_id INTEGER,
			rendition_name TEXT NOT NULL DEFAULT '',
			status TEXT DEFAULT 'waiting',
			progress INTEGER DEFAULT 0,
			output_path TEXT,
			error_message TEXT,
			encoder_used TEXT,
			started_at TIMESTAMP,
			completed_at TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			available_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			lease_owner TEXT,
			lease_until TIMESTAMP,
			config_snapshot_json TEXT,
			FOREIGN KEY (task_id) REFERENCES transcode_task(id) ON DELETE CASCADE,
			FOREIGN KEY (rendition_id) REFERENCES preset_rendition(id) ON DELETE SET NULL
		)`,
		`INSERT INTO pretranscode_rendition_job_new
			(id, task_id, rendition_id, rendition_name, status, progress, output_path, error_message, encoder_used, started_at, completed_at, created_at, available_at, lease_owner, lease_until, config_snapshot_json)
		 SELECT old.id, old.task_id, old.rendition_id, COALESCE(old.rendition_name,''), old.status, old.progress,
		        old.output_path, old.error_message, old.encoder_used, old.started_at, old.completed_at, old.created_at, COALESCE(old.available_at,CURRENT_TIMESTAMP), old.lease_owner, old.lease_until, old.config_snapshot_json
		   FROM pretranscode_rendition_job old
		  WHERE EXISTS (SELECT 1 FROM transcode_task t WHERE t.id = old.task_id)`,
		`DROP TABLE pretranscode_rendition_job`,
		`ALTER TABLE pretranscode_rendition_job_new RENAME TO pretranscode_rendition_job`,
		`CREATE INDEX IF NOT EXISTS idx_pretranscode_job_status ON pretranscode_rendition_job(status, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_pretranscode_job_task ON pretranscode_rendition_job(task_id)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("rebuild pretranscode_rendition_job: %w", err)
		}
	}
	return nil
}

// migrateTranscodePresetAudioCodec back-fills audio_codec on presets created
// before the column existed or seeded with an empty value.
func migrateTranscodePresetAudioCodec(db *sql.DB) error {
	_, err := db.Exec(`UPDATE transcode_preset SET audio_codec = 'aac' WHERE COALESCE(audio_codec, '') = ''`)
	return err
}

func migrateTranscodePresetOutputDir(db *sql.DB) error {
	addColumnIfMissing(db, "transcode_preset", "output_dir_mode TEXT NOT NULL DEFAULT 'source'")
	addColumnIfMissing(db, "transcode_preset", "output_dir_custom TEXT DEFAULT ''")
	_, err := db.Exec(`UPDATE transcode_preset SET output_dir_mode = 'source' WHERE COALESCE(output_dir_mode, '') = ''`)
	return err
}

// addColumnIfMissing runs ALTER TABLE ADD COLUMN ignoring the "duplicate
// column" error that fires on subsequent boots. SQLite has no IF NOT EXISTS
// clause for ADD COLUMN, so we swallow the specific error string.
func addColumnIfMissing(db *sql.DB, table, def string) {
	_, _ = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, def))
}

func ensurePretranscodeRenditionAvailability(db *sql.DB) error {
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pretranscode_rendition_job') WHERE name='available_at'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect pretranscode_rendition_job.available_at: %w", err)
	}
	if exists == 0 {
		// SQLite rejects ALTER TABLE with non-constant CURRENT_TIMESTAMP defaults
		// when the table already contains rows. New tables retain the schema default.
		if _, err := db.Exec(`ALTER TABLE pretranscode_rendition_job ADD COLUMN available_at TIMESTAMP`); err != nil {
			return fmt.Errorf("add pretranscode_rendition_job.available_at: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE pretranscode_rendition_job SET available_at=CURRENT_TIMESTAMP WHERE available_at IS NULL`); err != nil {
		return fmt.Errorf("backfill pretranscode_rendition_job.available_at: %w", err)
	}
	return nil
}

// seedBuiltinPresets inserts the seven SRS 3.1.7 builtin templates marked
// is_builtin=1 (not deletable). Idempotent: skips when rows already exist.
func seedBuiltinPresets(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transcode_preset WHERE is_builtin = 1`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	type rendition struct {
		name, vbr string
		h, w      int
		bw        int
	}
	type preset struct {
		name, desc, format, enc, codec, vp string
		crf                                int
		ab                                 string
		renditions                         []rendition
	}
	presets := []preset{
		{"HLS-标准", "开箱即用的 HLS 自适应码率", "hls", "none", "libx264", "veryfast", 23, "128k",
			[]rendition{{"360p", "850k", 360, 640, 1000000}, {"480p", "1400k", 480, 854, 1700000}, {"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}}},
		{"HLS-高质量", "高码率 HLS 适配大屏", "hls", "none", "libx264", "medium", 20, "192k",
			[]rendition{{"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}, {"1440p", "9000k", 1440, 2560, 12000000}, {"2160p", "18000k", 2160, 3840, 24000000}}},
		{"HLS-AES128", "HLS AES-128 基本加密", "hls", "aes128", "libx264", "veryfast", 23, "128k", []rendition{{"360p", "850k", 360, 640, 1000000}, {"480p", "1400k", 480, 854, 1700000}, {"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}}},
		{"HLS-PowerDRM", "HLS PowerDRM 私有加密", "hls", "powerdrm", "libx264", "veryfast", 23, "128k", []rendition{{"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}}},
		{"DASH-自适应", "DASH 自适应码率", "dash", "none", "libx264", "veryfast", 23, "128k", []rendition{{"360p", "850k", 360, 640, 1000000}, {"480p", "1400k", 480, 854, 1700000}, {"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}}},
		{"DASH-DRM", "DASH CENC DRM 加密", "dash", "drm", "libx264", "veryfast", 23, "128k", []rendition{{"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}}},
		{"MP4-兼容", "MP4 兼容输出", "mp4", "none", "libx264", "veryfast", 23, "128k", []rendition{{"720p", "2800k", 720, 1280, 3300000}, {"1080p", "5000k", 1080, 1920, 5800000}}},
	}
	for i, p := range presets {
		res, err := db.Exec(`INSERT INTO transcode_preset
			(name, description, output_format, encryption_mode, video_codec, video_preset, video_crf,
			 audio_codec, audio_bitrate, audio_channels, audio_sample_rate, hw_fallback, is_builtin, is_enabled, sort_order)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 2, 48000, 1, 1, 1, ?)`,
			p.name, p.desc, p.format, p.enc, p.codec, p.vp, p.crf, "aac", p.ab, i)
		if err != nil {
			return err
		}
		pid, _ := res.LastInsertId()
		for j, r := range p.renditions {
			_, err := db.Exec(`INSERT INTO preset_rendition
				(preset_id, name, height, width, video_bitrate, audio_bitrate, bandwidth, sort_order, video_rate, audio_rate)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				pid, r.name, r.h, r.w, r.vbr, p.ab, r.bw, j, r.vbr, p.ab)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
