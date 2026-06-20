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

CREATE TABLE IF NOT EXISTS series (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    title TEXT NOT NULL,
    title_norm TEXT NOT NULL,
    year INTEGER,
    tmdb_id TEXT,
    tvdb_id TEXT,
    poster TEXT,
    folder_paths TEXT DEFAULT '[]',
    meta_json TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id)
);
CREATE INDEX IF NOT EXISTS idx_series_library ON series(library_id);
CREATE INDEX IF NOT EXISTS idx_series_title_norm ON series(library_id, title_norm);
CREATE INDEX IF NOT EXISTS idx_series_tmdb ON series(library_id, tmdb_id);
CREATE INDEX IF NOT EXISTS idx_series_tvdb ON series(library_id, tvdb_id);

CREATE TABLE IF NOT EXISTS season (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tv_id INTEGER,
    season_num INTEGER,
    name TEXT,
    poster TEXT,
    FOREIGN KEY (tv_id) REFERENCES series(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_season_series_num ON season(tv_id, season_num);

CREATE TABLE IF NOT EXISTS episode (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    season_id INTEGER,
    episode_num INTEGER,
    title TEXT,
    duration INTEGER,
    file_path TEXT,
    FOREIGN KEY (season_id) REFERENCES season(id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_episode_season_num ON episode(season_id, episode_num);

CREATE TABLE IF NOT EXISTS episode_media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    episode_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL UNIQUE,
    sort_order INTEGER DEFAULT 0,
    FOREIGN KEY (episode_id) REFERENCES episode(id),
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_episode_media_episode ON episode_media(episode_id);

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

CREATE TABLE IF NOT EXISTS playlist (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    poster_url TEXT DEFAULT '',
    background_url TEXT DEFAULT '',
    logo_url TEXT DEFAULT '',
    square_art_url TEXT DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES user(id)
);
CREATE INDEX IF NOT EXISTS idx_playlist_user ON playlist(user_id);

CREATE TABLE IF NOT EXISTS playlist_item (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    playlist_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    sort_order INTEGER DEFAULT 0,
    added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(playlist_id, media_id),
    FOREIGN KEY (playlist_id) REFERENCES playlist(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_playlist_item_playlist ON playlist_item(playlist_id);
CREATE INDEX IF NOT EXISTS idx_playlist_item_media ON playlist_item(media_id);

CREATE TABLE IF NOT EXISTS favorite_folder (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES user(id)
);
CREATE INDEX IF NOT EXISTS idx_favorite_folder_user ON favorite_folder(user_id);

CREATE TABLE IF NOT EXISTS favorite_folder_item (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    folder_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    sort_order INTEGER DEFAULT 0,
    added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(folder_id, media_id),
    FOREIGN KEY (folder_id) REFERENCES favorite_folder(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_favorite_folder_item_folder ON favorite_folder_item(folder_id);
CREATE INDEX IF NOT EXISTS idx_favorite_folder_item_media ON favorite_folder_item(media_id);

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
    fail_count INTEGER DEFAULT 0,
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

CREATE TABLE IF NOT EXISTS lyric_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'pending',
    message TEXT,
    vtt_path TEXT,
    lrc_path TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_lyric_task_status ON lyric_task(status, updated_at);

CREATE TABLE IF NOT EXISTS photo_classify_task (
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
CREATE INDEX IF NOT EXISTS idx_photo_classify_task_status ON photo_classify_task(status, updated_at);

CREATE TABLE IF NOT EXISTS photo_location_task (
    media_id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_photo_location_task_status ON photo_location_task(library_id, status, updated_at);

CREATE TABLE IF NOT EXISTS photo_person (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    cover_face_id INTEGER,
    face_count INTEGER NOT NULL DEFAULT 0,
    media_count INTEGER NOT NULL DEFAULT 0,
    embedding BLOB,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_photo_person_library ON photo_person(library_id);

CREATE TABLE IF NOT EXISTS photo_face (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    library_id INTEGER NOT NULL,
    person_id INTEGER,
    bbox_x REAL NOT NULL DEFAULT 0,
    bbox_y REAL NOT NULL DEFAULT 0,
    bbox_w REAL NOT NULL DEFAULT 0,
    bbox_h REAL NOT NULL DEFAULT 0,
    embedding BLOB,
    quality REAL,
    match_score REAL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id),
    FOREIGN KEY (person_id) REFERENCES photo_person(id)
);
CREATE INDEX IF NOT EXISTS idx_photo_face_media ON photo_face(media_id);
CREATE INDEX IF NOT EXISTS idx_photo_face_person ON photo_face(library_id, person_id);
CREATE INDEX IF NOT EXISTS idx_photo_face_person_media ON photo_face(person_id, media_id);

CREATE TABLE IF NOT EXISTS photo_face_task (
    media_id INTEGER PRIMARY KEY,
    library_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id)
);
CREATE INDEX IF NOT EXISTS idx_photo_face_task_status ON photo_face_task(library_id, status, updated_at);

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

CREATE TABLE IF NOT EXISTS system_options (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    options_json TEXT NOT NULL DEFAULT '{}',
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
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
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN encrypted_assets_enabled INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN encrypted_assets_cleanup_plaintext INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN encrypted_assets_dir_mode TEXT DEFAULT 'library'`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN encrypted_assets_custom_dir TEXT DEFAULT ''`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS media_encrypted_assets (
			media_id INTEGER PRIMARY KEY,
			enc_path TEXT NOT NULL,
			wrapped_dek TEXT NOT NULL,
			iv TEXT NOT NULL,
			plain_path TEXT,
			status TEXT NOT NULL DEFAULT 'encrypted',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_media_encrypted_status ON media_encrypted_assets(status)`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS media_derived_assets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			artifact_kind TEXT NOT NULL,
			logical_name TEXT NOT NULL,
			enc_path TEXT NOT NULL,
			wrapped_dek TEXT NOT NULL,
			iv TEXT NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
			UNIQUE(media_id, artifact_kind, logical_name)
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_derived_media ON media_derived_assets(media_id)`)
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
	// Playlist image columns (added later)
	_, _ = db.Exec(`ALTER TABLE playlist ADD COLUMN poster_url TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE playlist ADD COLUMN background_url TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE playlist ADD COLUMN logo_url TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE playlist ADD COLUMN square_art_url TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN avatar_url TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN ui_locale TEXT DEFAULT 'zh'`)
	_, _ = db.Exec(`ALTER TABLE user ADD COLUMN player_prefs_json TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE scrape_task ADD COLUMN fail_count INTEGER DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE photo_person ADD COLUMN media_count INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`
		UPDATE photo_person
		SET media_count = (
			SELECT COUNT(DISTINCT media_id) FROM photo_face WHERE photo_face.person_id = photo_person.id
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_photo_face_person_media ON photo_face(person_id, media_id)`)
	// TV series / episode linking (added for hierarchical TV library scan).
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS series (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			library_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			title_norm TEXT NOT NULL,
			year INTEGER,
			tmdb_id TEXT,
			tvdb_id TEXT,
			poster TEXT,
			folder_paths TEXT DEFAULT '[]',
			meta_json TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (library_id) REFERENCES library(id)
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_series_library ON series(library_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_series_title_norm ON series(library_id, title_norm)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_series_tmdb ON series(library_id, tmdb_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_series_tvdb ON series(library_id, tvdb_id)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_season_series_num ON season(tv_id, season_num)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_episode_season_num ON episode(season_id, episode_num)`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS episode_media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			episode_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL UNIQUE,
			sort_order INTEGER DEFAULT 0,
			FOREIGN KEY (episode_id) REFERENCES episode(id),
			FOREIGN KEY (media_id) REFERENCES media(id)
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_episode_media_episode ON episode_media(episode_id)`)
	// Music library: artists, albums, tracks (linked to media rows).
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS music_artist (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			library_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			name_norm TEXT NOT NULL,
			artwork_path TEXT,
			meta_json TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (library_id) REFERENCES library(id)
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_artist_library ON music_artist(library_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_artist_name_norm ON music_artist(library_id, name_norm)`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS music_album (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			library_id INTEGER NOT NULL,
			title TEXT NOT NULL,
			title_norm TEXT NOT NULL,
			album_artist_id INTEGER,
			year INTEGER,
			genre TEXT,
			artwork_path TEXT,
			is_compilation INTEGER DEFAULT 0,
			is_unknown INTEGER DEFAULT 0,
			rating INTEGER DEFAULT 0,
			is_favorite INTEGER DEFAULT 0,
			folder_paths TEXT DEFAULT '[]',
			meta_json TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (library_id) REFERENCES library(id),
			FOREIGN KEY (album_artist_id) REFERENCES music_artist(id)
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_album_library ON music_album(library_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_album_title_norm ON music_album(library_id, title_norm)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_album_artist ON music_album(album_artist_id)`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS music_track (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			album_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL UNIQUE,
			track_number INTEGER DEFAULT 0,
			disc_number INTEGER DEFAULT 1,
			title TEXT NOT NULL,
			artist_display TEXT DEFAULT '',
			sort_order INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (album_id) REFERENCES music_album(id),
			FOREIGN KEY (media_id) REFERENCES media(id)
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_track_album ON music_track(album_id, sort_order)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_music_track_media ON music_track(media_id)`)
	_, _ = db.Exec(`INSERT OR IGNORE INTO scrape_config (id) VALUES (1)`)
	_, _ = db.Exec(`INSERT OR IGNORE INTO system_options (id, options_json) VALUES (1, '{}')`)
	// Document library support
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN scan_exclude_patterns TEXT DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE library ADD COLUMN scan_recursive INTEGER DEFAULT 1`)
	_, _ = db.Exec(`UPDATE library_node SET parent_path = '' WHERE parent_path IS NULL`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS read_progress (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL,
			position TEXT NOT NULL DEFAULT '',
			percent REAL DEFAULT 0,
			update_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, media_id),
			FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE,
			FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_read_progress_user ON read_progress(user_id, update_at DESC)`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS document_tag (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			tag TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(media_id, tag),
			FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_document_tag_tag ON document_tag(tag)`)
	_, _ = db.Exec(`
		CREATE TABLE IF NOT EXISTS scan_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scan_task_id INTEGER,
			library_id INTEGER NOT NULL,
			file_path TEXT NOT NULL,
			action TEXT NOT NULL,
			message TEXT DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (library_id) REFERENCES library(id) ON DELETE CASCADE
		)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_scan_log_task ON scan_log(scan_task_id)`)
	// Seed default AI provider configs.
	seedAIProviders(db)
	// Remove duplicate scheduled tasks (legacy seed inserted on every restart).
	if _, err := DedupeScheduledTasks(db); err != nil {
		return nil, err
	}
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_task_type_name ON scheduled_task(task_type, name)`)
	// Clean up stale transcode tasks that failed due to transient issues (path not found, context canceled).
	cleanupStaleTranscodeTasks(db)
	recoverStalePhotoTasks(db)
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

// DedupeScheduledTasks keeps the oldest row per (task_type, name) and deletes duplicates.
func DedupeScheduledTasks(db *sql.DB) (int64, error) {
	if db == nil {
		return 0, nil
	}
	res, err := db.Exec(`
		DELETE FROM scheduled_task
		WHERE id NOT IN (
			SELECT MIN(id) FROM scheduled_task GROUP BY task_type, name
		)
	`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
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

// recoverStalePhotoTasks resets orphaned "running" rows left by process restarts
// so workers resume and progress bars can clear.
func recoverStalePhotoTasks(db *sql.DB) {
	if db == nil {
		return
	}
	for _, table := range []string{"photo_face_task", "photo_location_task", "photo_classify_task"} {
		_, _ = db.Exec(`
			UPDATE ` + table + `
			SET status = 'pending', started_at = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE status = 'running'`)
	}
}
