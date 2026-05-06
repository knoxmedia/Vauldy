package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS library (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    path TEXT NOT NULL,
    auto_scan INTEGER DEFAULT 1,
    enabled INTEGER DEFAULT 1,
    realtime_monitor INTEGER DEFAULT 0,
    metadata_providers TEXT DEFAULT 'tmdb,omdb',
    image_providers TEXT DEFAULT 'tmdb,omdb,embedded,screen_grabber',
    metadata_refresh_policy TEXT DEFAULT 'never',
    preview_extract INTEGER DEFAULT 0,
    encryption_mode TEXT DEFAULT 'drm',
    scraper TEXT DEFAULT 'tmdb',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS library_folder (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    path TEXT NOT NULL,
    sort_order INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id)
);

CREATE TABLE IF NOT EXISTS media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER,
    file_id TEXT UNIQUE,
    title TEXT,
    original_title TEXT,
    file_path TEXT,
    file_mtime INTEGER DEFAULT 0,
    file_type TEXT,
    duration INTEGER,
    width INTEGER,
    height INTEGER,
    bitrate INTEGER,
    md5 TEXT,
    format TEXT,
    meta_json TEXT,
    status TEXT DEFAULT 'active',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id)
);

CREATE TABLE IF NOT EXISTS library_node (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    parent_path TEXT,
    node_path TEXT NOT NULL,
    node_name TEXT NOT NULL,
    node_type TEXT NOT NULL,
    media_id INTEGER,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id),
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE IF NOT EXISTS season (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tv_id INTEGER,
    season_num INTEGER,
    name TEXT,
    poster TEXT
);

CREATE TABLE IF NOT EXISTS episode (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    season_id INTEGER,
    episode_num INTEGER,
    title TEXT,
    duration INTEGER,
    file_path TEXT,
    FOREIGN KEY (season_id) REFERENCES season(id)
);

CREATE TABLE IF NOT EXISTS transcode_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id TEXT,
    quality TEXT,
    status TEXT DEFAULT 'waiting',
    progress INTEGER DEFAULT 0,
    error_message TEXT,
    output_path TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS package_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    pipeline_type TEXT NOT NULL,
    status TEXT DEFAULT 'waiting',
    progress INTEGER DEFAULT 0,
    output_path TEXT,
    drm_status TEXT,
    source_cleanup_status TEXT DEFAULT 'pending',
    error_message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE IF NOT EXISTS drm_asset (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    kid TEXT NOT NULL,
    key_ref TEXT NOT NULL,
    manifest_path TEXT NOT NULL,
    license_policy_json TEXT DEFAULT '{}',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE IF NOT EXISTS drm_license_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER,
    drm_type TEXT NOT NULL,
    result TEXT NOT NULL,
    reason TEXT,
    client_ip TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE IF NOT EXISTS drm_key_material (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    mode TEXT NOT NULL,
    kid TEXT NOT NULL,
    key_hex TEXT NOT NULL,
    iv_hex TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE IF NOT EXISTS preview_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    status TEXT DEFAULT 'waiting',
    interval_sec INTEGER DEFAULT 10,
    thumb_count INTEGER DEFAULT 0,
    thumb_width INTEGER DEFAULT 240,
    thumb_height INTEGER DEFAULT 135,
    sprite_path TEXT,
    vtt_path TEXT,
    error_message TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);

CREATE TABLE IF NOT EXISTS scan_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    status TEXT DEFAULT 'running',
    source TEXT DEFAULT 'manual',
    processed_count INTEGER DEFAULT 0,
    total_count INTEGER DEFAULT 0,
    added_count INTEGER DEFAULT 0,
    error_message TEXT,
    cancelled INTEGER DEFAULT 0,
    started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id)
);

CREATE TABLE IF NOT EXISTS user (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT UNIQUE,
    password TEXT,
    role TEXT DEFAULT 'user',
    can_manage INTEGER DEFAULT 0,
    can_play INTEGER DEFAULT 1,
    can_download INTEGER DEFAULT 0,
    can_access_features INTEGER DEFAULT 1,
    library_scope TEXT DEFAULT 'all',
    parental_enabled INTEGER DEFAULT 0,
    parental_max_rating TEXT DEFAULT '',
    parental_pin_hash TEXT DEFAULT '',
    allowed_time_start TEXT DEFAULT '',
    allowed_time_end TEXT DEFAULT '',
    parental_access_plan_json TEXT DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS user_library_permission (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    library_id INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, library_id),
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE,
    FOREIGN KEY (library_id) REFERENCES library(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS user_library_folder_permission (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    library_id INTEGER NOT NULL,
    folder_path TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, library_id, folder_path),
    FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE,
    FOREIGN KEY (library_id) REFERENCES library(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS play_progress (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    file_id TEXT,
    position INTEGER,
    play_start_at TIMESTAMP,
    play_end_at TIMESTAMP,
    completed INTEGER DEFAULT 0,
    play_count INTEGER DEFAULT 0,
    update_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES user(id)
);

CREATE TABLE IF NOT EXISTS activity_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    username TEXT,
    action TEXT NOT NULL,
    media_id INTEGER,
    message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scheduled_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'media',
    task_type TEXT NOT NULL,
    interval_min INTEGER NOT NULL DEFAULT 60,
    payload_json TEXT DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_run_at TIMESTAMP,
    last_status TEXT,
    last_message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_media_library ON media(library_id);
CREATE INDEX IF NOT EXISTS idx_media_file_id ON media(file_id);
CREATE INDEX IF NOT EXISTS idx_library_folder_library ON library_folder(library_id, sort_order);
CREATE UNIQUE INDEX IF NOT EXISTS idx_library_node_unique ON library_node(library_id, node_path);
CREATE INDEX IF NOT EXISTS idx_library_node_library_parent ON library_node(library_id, parent_path);
CREATE INDEX IF NOT EXISTS idx_progress_user_file ON play_progress(user_id, file_id);
CREATE INDEX IF NOT EXISTS idx_activity_log_created_at ON activity_log(created_at);
CREATE INDEX IF NOT EXISTS idx_preview_task_status ON preview_task(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_scheduled_task_enabled ON scheduled_task(enabled, updated_at);
CREATE INDEX IF NOT EXISTS idx_scan_task_library ON scan_task(library_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_scan_task_status ON scan_task(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_package_task_media ON package_task(media_id, created_at);
CREATE INDEX IF NOT EXISTS idx_package_task_status ON package_task(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_drm_license_audit_media ON drm_license_audit(media_id, created_at);
CREATE INDEX IF NOT EXISTS idx_drm_key_material_media ON drm_key_material(media_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_user_library_permission_user ON user_library_permission(user_id, library_id);
CREATE INDEX IF NOT EXISTS idx_user_library_folder_permission_user ON user_library_folder_permission(user_id, library_id);

CREATE TABLE IF NOT EXISTS favorite (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, media_id),
    FOREIGN KEY (user_id) REFERENCES user(id),
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_favorite_user ON favorite(user_id);
CREATE INDEX IF NOT EXISTS idx_favorite_media ON favorite(media_id);

CREATE TABLE IF NOT EXISTS scrape_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    enabled INTEGER DEFAULT 1,
    providers TEXT DEFAULT 'tmdb,omdb,douban,tvdb,bangumi,fanart,ai',
    api_keys_json TEXT DEFAULT '{}',
    image_sources TEXT DEFAULT 'tmdb,omdb,screen_grabber,embedded',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ai_provider_config (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    api_url TEXT DEFAULT '',
    api_key TEXT DEFAULT '',
    model TEXT DEFAULT '',
    enabled INTEGER DEFAULT 0,
    request_count INTEGER DEFAULT 0,
    token_count INTEGER DEFAULT 0,
    last_used_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scrape_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    task_type TEXT DEFAULT 'media',
    source TEXT DEFAULT 'auto',
    query TEXT,
    year INTEGER,
    status TEXT DEFAULT 'waiting',
    progress INTEGER DEFAULT 0,
    message TEXT,
    created_by INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_scrape_task_status ON scrape_task(status, created_at);
CREATE INDEX IF NOT EXISTS idx_scrape_task_media ON scrape_task(media_id, created_at);

CREATE TABLE IF NOT EXISTS scrape_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    source TEXT,
    query TEXT,
    status TEXT,
    message TEXT,
    result_json TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_scrape_history_media ON scrape_history(media_id, created_at);

CREATE TABLE IF NOT EXISTS media_subtitle (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    dedupe_key TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    stream_index INTEGER,
    codec_name TEXT,
    lang TEXT,
    lang_source TEXT,
    label TEXT,
    source_path TEXT,
    vtt_path TEXT NOT NULL,
    status TEXT DEFAULT 'ready',
    error_message TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id),
    UNIQUE(media_id, dedupe_key)
);
CREATE INDEX IF NOT EXISTS idx_media_subtitle_media ON media_subtitle(media_id, status);

CREATE TABLE IF NOT EXISTS subtitle_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'pending',
    message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_subtitle_task_status ON subtitle_task(status, updated_at);

CREATE TABLE IF NOT EXISTS atrack_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'waiting',
    output_dir TEXT,
    error_message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_atrack_task_status ON atrack_task(status, updated_at);

CREATE TABLE IF NOT EXISTS keyframe_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'waiting',
    output_dir TEXT,
    keyframe_count INTEGER DEFAULT 0,
    error_message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_keyframe_task_status ON keyframe_task(status, updated_at);

CREATE TABLE IF NOT EXISTS api_client (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT,
    client_id TEXT NOT NULL UNIQUE,
    secret_hash TEXT NOT NULL,
    revoked INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_api_client_client_id ON api_client(client_id);
`

func OpenSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	// SQLite: avoid unlimited concurrent connections fighting for the DB lock; WAL allows concurrent readers.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// WAL greatly reduces "database is locked" under concurrent API + scanner writes.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma journal_mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma synchronous: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	_, _ = db.Exec(`ALTER TABLE transcode_task ADD COLUMN error_message TEXT`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN enabled INTEGER DEFAULT 1`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN realtime_monitor INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN metadata_providers TEXT DEFAULT 'tmdb,omdb'`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN image_providers TEXT DEFAULT 'tmdb,omdb,embedded,screen_grabber'`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN metadata_refresh_policy TEXT DEFAULT 'never'`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN preview_extract INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN drm_enabled INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN encryption_mode TEXT DEFAULT 'drm'`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN cleanup_local_source_after_package INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN jit_prepare_on_ingest INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE media ADD COLUMN file_mtime INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE scheduled_task ADD COLUMN category TEXT NOT NULL DEFAULT 'media'`)
	_, _ = db.Exec(`ALTER TABLE play_progress ADD COLUMN play_start_at TIMESTAMP`)
	_, _ = db.Exec(`ALTER TABLE play_progress ADD COLUMN play_end_at TIMESTAMP`)
	_, _ = db.Exec(`ALTER TABLE play_progress ADD COLUMN completed INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE play_progress ADD COLUMN play_count INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE scan_task ADD COLUMN total_count INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN can_manage INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN can_play INTEGER DEFAULT 1`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN can_download INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN can_access_features INTEGER DEFAULT 1`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN library_scope TEXT DEFAULT 'all'`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN parental_enabled INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN parental_max_rating TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN parental_pin_hash TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN allowed_time_start TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN allowed_time_end TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN parental_access_plan_json TEXT DEFAULT '[]'`)
	_, _ = db.Exec(`INSERT OR IGNORE INTO scrape_config (id) VALUES (1)`)
	// Seed default AI provider configs.
	seedAIProviders(db)
	// Seed default scheduled tasks so scrape/subtitle/cleanup run automatically.
	seedScheduledTasks(db)
	// Clean up stale transcode tasks that failed due to transient issues (path not found, context canceled).
	cleanupStaleTranscodeTasks(db)
	return db, nil
}

// seedAIProviders inserts default AI provider configs (OpenAI, DeepSeek, Tongyi, Ollama)
// if they don't already exist.
func seedAIProviders(db *sql.DB) {
	if db == nil {
		return
	}
	for _, p := range []struct{ id, name, apiURL, model string }{
		{id: "openai", name: "OpenAI", apiURL: "https://api.openai.com/v1", model: "gpt-4o"},
		{id: "deepseek", name: "DeepSeek", apiURL: "https://api.deepseek.com/v1", model: "deepseek-chat"},
		{id: "tongyi", name: "通义千问", apiURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", model: "qwen-plus"},
		{id: "ollama", name: "Ollama", apiURL: "http://localhost:11434", model: ""},
	} {
		_, _ = db.Exec(
			`INSERT OR IGNORE INTO ai_provider_config (id, name, api_url, model) VALUES (?, ?, ?, ?)`,
			p.id, p.name, p.apiURL, p.model,
		)
	}
}

// seedScheduledTasks ensures default maintenance tasks exist so scrape, subtitle
// processing, and transcode cleanup run automatically every 30 seconds (via
// StartScheduleLoop). Uses INSERT OR IGNORE so existing tasks are preserved.
func seedScheduledTasks(db *sql.DB) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO scheduled_task (name, category, task_type, interval_min, payload_json, enabled)
		VALUES ('自动刮削', 'media', 'scrape_run', 2, '{"limit":10}', 1)
	`)
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO scheduled_task (name, category, task_type, interval_min, payload_json, enabled)
		VALUES ('自动字幕处理', 'media', 'subtitle_process', 5, '{"limit":10}', 1)
	`)
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO scheduled_task (name, category, task_type, interval_min, payload_json, enabled)
		VALUES ('自动音轨提取', 'media', 'atrack_process', 5, '{"limit":10}', 1)
	`)
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO scheduled_task (name, category, task_type, interval_min, payload_json, enabled)
		VALUES ('自动关键帧提取', 'media', 'keyframe_process', 5, '{"limit":10}', 1)
	`)
}

// cleanupStaleTranscodeTasks removes transcode_task rows that failed due to
// transient infrastructure issues (ffmpeg path not found, context canceled by
// process restart) so they don't clutter the task list permanently.
func cleanupStaleTranscodeTasks(db *sql.DB) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`
		DELETE FROM transcode_task
		WHERE status = 'failed'
		  AND (error_message LIKE '%The system cannot find the path specified%'
		       OR error_message LIKE '%context canceled%')
	`)
	_, _ = db.Exec(`
		DELETE FROM package_task
		WHERE status = 'failed'
		  AND error_message LIKE '%The system cannot find the path specified%'
	`)
}
