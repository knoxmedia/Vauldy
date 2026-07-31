package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"

	"knox-media/internal/relationshipmigration"
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
    publication_state TEXT NOT NULL DEFAULT 'published' CHECK (publication_state IN ('processing','published','degraded','failed','cancelled')),
    published_at TIMESTAMP,
    publication_error TEXT NOT NULL DEFAULT '',
    ingest_generation INTEGER NOT NULL DEFAULT 0 CHECK (ingest_generation >= 0),
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
    failed_count INTEGER DEFAULT 0,
    error_message TEXT,
    cancelled INTEGER DEFAULT 0,
    started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id)
);

CREATE TABLE IF NOT EXISTS scan_lease (
    library_id INTEGER PRIMARY KEY,
    scan_task_id INTEGER NOT NULL,
    owner_id TEXT NOT NULL,
    lease_until TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (library_id) REFERENCES library(id) ON DELETE CASCADE,
    FOREIGN KEY (scan_task_id) REFERENCES scan_task(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_scan_lease_until ON scan_lease(lease_until);

CREATE TABLE IF NOT EXISTS scan_finalize_recovery (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL,
    library_id INTEGER NOT NULL,
    owner_id TEXT NOT NULL,
    desired_status TEXT NOT NULL CHECK (desired_status IN ('done','failed','cancelled')),
    error_message TEXT,
    cancelled INTEGER NOT NULL DEFAULT 0 CHECK (cancelled IN (0,1)),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    claim_owner TEXT,
    claim_until TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(task_id,owner_id),
    FOREIGN KEY (task_id) REFERENCES scan_task(id) ON DELETE CASCADE,
    FOREIGN KEY (library_id) REFERENCES library(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_scan_finalize_recovery_available ON scan_finalize_recovery(next_available_at,id);

CREATE TABLE IF NOT EXISTS media_ingest_run (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    scan_task_id INTEGER,
    reason TEXT NOT NULL CHECK (reason IN ('scan','repair','manual_retry')),
    status TEXT NOT NULL CHECK (status IN ('processing','published','degraded','failed','cancelled')),
    preserve_visibility INTEGER NOT NULL DEFAULT 0 CHECK (preserve_visibility IN (0,1)),
    config_snapshot_json TEXT NOT NULL CHECK (json_valid(config_snapshot_json)),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (scan_task_id) REFERENCES scan_task(id) ON DELETE SET NULL,
    UNIQUE(media_id,generation),
    UNIQUE(id,media_id,generation)
);
CREATE INDEX IF NOT EXISTS idx_media_ingest_run_status_updated ON media_ingest_run(status,updated_at);
CREATE INDEX IF NOT EXISTS idx_media_ingest_run_scan_status ON media_ingest_run(scan_task_id,status);

CREATE TABLE IF NOT EXISTS media_ingest_step (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL,
    media_id INTEGER NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    step_type TEXT NOT NULL CHECK (step_type IN ('poster','scrape','preview','keyframe','subtitle','atrack','encrypt','prepare')),
    required INTEGER NOT NULL CHECK (required IN (0,1)),
    status TEXT NOT NULL CHECK (status IN ('waiting','running','done','skipped','failed','cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (media_id,generation) REFERENCES media_ingest_run(media_id,generation),
    FOREIGN KEY (run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
    UNIQUE(run_id,step_type),
    UNIQUE(id,media_id,generation)
);

CREATE TABLE IF NOT EXISTS post_ingest_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    scan_task_id INTEGER,
    ingest_run_id INTEGER,
    ingest_step_id INTEGER,
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    task_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'waiting',
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    available_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_until TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP,
    FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
    FOREIGN KEY (scan_task_id) REFERENCES scan_task(id) ON DELETE SET NULL,
    FOREIGN KEY (ingest_run_id) REFERENCES media_ingest_run(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_step_id) REFERENCES media_ingest_step(id) ON DELETE CASCADE,
    FOREIGN KEY (ingest_run_id,media_id,generation) REFERENCES media_ingest_run(id,media_id,generation),
    FOREIGN KEY (ingest_step_id,media_id,generation) REFERENCES media_ingest_step(id,media_id,generation),
    UNIQUE(media_id,generation,task_type),
    CHECK (task_type IN ('poster','preview','keyframe','subtitle','atrack','encrypt')),
    CHECK (status IN ('waiting','running','done','failed','cancelled'))
);
CREATE INDEX IF NOT EXISTS idx_post_ingest_claim ON post_ingest_task(status,available_at,lease_until,created_at);
CREATE INDEX IF NOT EXISTS idx_post_ingest_scan ON post_ingest_task(scan_task_id,status);

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

CREATE TABLE IF NOT EXISTS photo_face_thumb_repair_state (
    name TEXT PRIMARY KEY,
    phase TEXT NOT NULL DEFAULT 'covers',
    last_person_id INTEGER NOT NULL DEFAULT 0,
    last_face_id INTEGER NOT NULL DEFAULT 0,
    completed_at TIMESTAMP,
    next_audit_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS photo_face_thumb_repair_failure (
    face_id INTEGER PRIMARY KEY,
    person_id INTEGER,
    attempts INTEGER NOT NULL DEFAULT 1,
    next_retry_at TIMESTAMP NOT NULL,
    last_error TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (face_id) REFERENCES photo_face(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_photo_face_thumb_repair_failure_due ON photo_face_thumb_repair_failure(next_retry_at, face_id);

CREATE TABLE IF NOT EXISTS media_file_cleanup_task (
    path TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, last_error TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_media_file_cleanup_task_due ON media_file_cleanup_task(status,next_retry_at,path);

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

// Migration is a single idempotent schema migration step. Community
// migrations live inline in OpenSQLite; enterprise migrations are registered
// via RegisterEnterpriseMigration so the community build excludes them.
type Migration struct {
	ID string
	Up func(db *sql.DB) error
}

// enterpriseMigrations is appended to by commercial init() functions
// (e.g. internal/store/migrations_pretranscode.go). It stays empty in the
// community build, keeping commercial tables out of the community schema.
var enterpriseMigrations []Migration

// RegisterEnterpriseMigration appends a commercial migration. Called from
// init() in commercial-only files; a no-op in the community build.
func RegisterEnterpriseMigration(m Migration) {
	enterpriseMigrations = append(enterpriseMigrations, m)
}

func appendSQLitePragmas(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	if strings.HasSuffix(path, "?") || strings.HasSuffix(path, "&") {
		separator = ""
	}
	return path + separator + "_pragma=busy_timeout(30000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
}

func isMemorySQLitePath(path string) bool {
	raw := strings.TrimSpace(path)
	if strings.EqualFold(raw, ":memory:") {
		return true
	}
	if !strings.HasPrefix(strings.ToLower(raw), "file:") {
		return false
	}
	if strings.EqualFold(strings.SplitN(raw, "?", 2)[0], "file::memory:") {
		return true
	}
	parts := strings.SplitN(raw, "?", 2)
	if len(parts) != 2 {
		return false
	}
	values, err := url.ParseQuery(parts[1])
	if err != nil {
		return false
	}
	for key, vals := range values {
		if strings.EqualFold(key, "mode") && len(vals) > 0 && strings.EqualFold(strings.TrimSpace(vals[0]), "memory") {
			return true
		}
	}
	return false
}

func withStartupBusyRetry(ctx context.Context, fn func() error) error {
	return WithBusyRetry(ctx, nil, fn)
}

func startupExecContext(ctx context.Context, db *sql.DB, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := withStartupBusyRetry(ctx, func() error { var err error; result, err = db.ExecContext(ctx, query, args...); return err })
	return result, err
}

func ensureColumnContext(ctx context.Context, db *sql.DB, table, column, definition string) error {
	var exists int
	query := fmt.Sprintf("SELECT COUNT(*) FROM pragma_table_info(%q) WHERE name=?", table)
	if err := withStartupBusyRetry(ctx, func() error { return db.QueryRowContext(ctx, query, column).Scan(&exists) }); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
	_, err := startupExecContext(ctx, db, stmt)
	if err != nil {
		var nowExists int
		if checkErr := db.QueryRowContext(ctx, query, column).Scan(&nowExists); checkErr == nil && nowExists > 0 {
			return nil
		}
	}
	return err
}

func OpenSQLite(path string) (*sql.DB, error) { return OpenSQLiteContext(context.Background(), path) }

func OpenSQLiteContext(ctx context.Context, path string) (opened *sql.DB, returnErr error) {
	defer func() {
		if returnErr != nil && ctx.Err() != nil {
			if opened != nil {
				_ = opened.Close()
			}
			opened = nil
			returnErr = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bootstrapDSN := appendSQLitePragmas(path)
	if !isMemorySQLitePath(path) {
		bootstrapDSN = strings.Replace(bootstrapDSN, "busy_timeout(30000)", "busy_timeout(100)", 1)
	}
	db, err := sql.Open("sqlite", bootstrapDSN)
	if err != nil {
		return nil, err
	}
	// A plain :memory: database is private to each physical connection.
	if isMemorySQLitePath(path) {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		// WAL allows concurrent readers while bounding lock contention.
		// Encrypt / post-ingest workers + API share this pool; 8 saturated easily
		// under USB SQLite write load and surfaces as context deadline exceeded.
		db.SetMaxOpenConns(24)
		db.SetMaxIdleConns(8)
	}
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// WAL greatly reduces "database is locked" under concurrent API + scanner writes.
	if err := WithBusyRetry(ctx, nil, func() error { _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); return err }); err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("pragma journal_mode: %w", err)
	}

	if _, err := startupExecContext(ctx, db, schema); err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := withStartupBusyRetry(ctx, func() error { return migrateIngestPublication(ctx, db) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ingest publication migration: %w", err)
	}
	if err := ensurePlaybackCompletionSchema(ctx, db); err != nil {
		_ = db.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("playback completion migration: %w", err)
	}
	_, _ = startupExecContext(ctx, db, `ALTER TABLE transcode_task ADD COLUMN error_message TEXT`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN enabled INTEGER DEFAULT 1`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN realtime_monitor INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN metadata_providers TEXT DEFAULT 'tmdb,omdb'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN image_providers TEXT DEFAULT 'tmdb,omdb,embedded,screen_grabber'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN metadata_refresh_policy TEXT DEFAULT 'never'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN preview_extract INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN drm_enabled INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN encryption_mode TEXT DEFAULT 'drm'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN cleanup_local_source_after_package INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN jit_prepare_on_ingest INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN encrypted_assets_enabled INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN encrypted_assets_cleanup_plaintext INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN encrypted_assets_dir_mode TEXT DEFAULT 'library'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN encrypted_assets_custom_dir TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_media_encrypted_status ON media_encrypted_assets(status)`)
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_derived_media ON media_derived_assets(media_id)`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE media ADD COLUMN file_mtime INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE scheduled_task ADD COLUMN category TEXT NOT NULL DEFAULT 'media'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE play_progress ADD COLUMN play_start_at TIMESTAMP`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE play_progress ADD COLUMN play_end_at TIMESTAMP`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE play_progress ADD COLUMN completed INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE play_progress ADD COLUMN play_count INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE scan_task ADD COLUMN total_count INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN can_manage INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN can_play INTEGER DEFAULT 1`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN can_download INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN can_access_features INTEGER DEFAULT 1`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN library_scope TEXT DEFAULT 'all'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN parental_enabled INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN parental_max_rating TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN parental_pin_hash TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN allowed_time_start TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN allowed_time_end TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN parental_access_plan_json TEXT DEFAULT '[]'`)
	// Playlist image columns (added later)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE playlist ADD COLUMN poster_url TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE playlist ADD COLUMN background_url TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE playlist ADD COLUMN logo_url TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE playlist ADD COLUMN square_art_url TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN avatar_url TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN ui_locale TEXT DEFAULT 'zh'`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE user ADD COLUMN player_prefs_json TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE scrape_task ADD COLUMN fail_count INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE photo_person ADD COLUMN media_count INTEGER NOT NULL DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `
		UPDATE photo_person
		SET media_count = (
			SELECT COUNT(DISTINCT media_id) FROM photo_face WHERE photo_face.person_id = photo_person.id
		)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_photo_face_person_media ON photo_face(person_id, media_id)`)
	if err := ensureColumnContext(ctx, db, "photo_face_thumb_repair_state", "completed_at", "TIMESTAMP"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate photo face repair completed_at: %w", err)
	}
	if err := ensureColumnContext(ctx, db, "photo_face_thumb_repair_state", "next_audit_at", "TIMESTAMP"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate photo face repair next_audit_at: %w", err)
	}
	// TV series / episode linking (added for hierarchical TV library scan).
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_series_library ON series(library_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_series_title_norm ON series(library_id, title_norm)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_series_tmdb ON series(library_id, tmdb_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_series_tvdb ON series(library_id, tvdb_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_season_series_num ON season(tv_id, season_num)`)
	_, _ = startupExecContext(ctx, db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_episode_season_num ON episode(season_id, episode_num)`)
	_, _ = startupExecContext(ctx, db, `
		CREATE TABLE IF NOT EXISTS episode_media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			episode_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL UNIQUE,
			sort_order INTEGER DEFAULT 0,
			FOREIGN KEY (episode_id) REFERENCES episode(id),
			FOREIGN KEY (media_id) REFERENCES media(id)
		)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_episode_media_episode ON episode_media(episode_id)`)
	// Music library: artists, albums, tracks (linked to media rows).
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_artist_library ON music_artist(library_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_artist_name_norm ON music_artist(library_id, name_norm)`)
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_album_library ON music_album(library_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_album_title_norm ON music_album(library_id, title_norm)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_album_artist ON music_album(album_artist_id)`)
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_track_album ON music_track(album_id, sort_order)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_music_track_media ON music_track(media_id)`)
	_, _ = startupExecContext(ctx, db, `INSERT OR IGNORE INTO scrape_config (id) VALUES (1)`)
	_, _ = startupExecContext(ctx, db, `INSERT OR IGNORE INTO system_options (id, options_json) VALUES (1, '{}')`)
	// Document library support
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN scan_exclude_patterns TEXT DEFAULT ''`)
	_, _ = startupExecContext(ctx, db, `ALTER TABLE library ADD COLUMN scan_recursive INTEGER DEFAULT 1`)
	_, _ = startupExecContext(ctx, db, `UPDATE library_node SET parent_path = '' WHERE parent_path IS NULL`)
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_read_progress_user ON read_progress(user_id, update_at DESC)`)
	_, _ = startupExecContext(ctx, db, `
		CREATE TABLE IF NOT EXISTS document_tag (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			tag TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(media_id, tag),
			FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE
		)`)
	_, _ = startupExecContext(ctx, db, `DELETE FROM document_tag WHERE id NOT IN (SELECT MIN(id) FROM document_tag GROUP BY media_id, tag COLLATE NOCASE)`)
	_, _ = startupExecContext(ctx, db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_document_tag_media_tag_nocase ON document_tag(media_id, tag COLLATE NOCASE)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_document_tag_tag ON document_tag(tag COLLATE NOCASE)`)
	_, _ = startupExecContext(ctx, db, `
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
	_, _ = startupExecContext(ctx, db, `ALTER TABLE scan_task ADD COLUMN failed_count INTEGER DEFAULT 0`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_scan_log_task ON scan_log(scan_task_id)`)
	// Cast/crew persons (film library).
	_, _ = startupExecContext(ctx, db, `
		CREATE TABLE IF NOT EXISTS cast_person (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			name_norm TEXT NOT NULL,
			english_name TEXT DEFAULT '',
			gender INTEGER DEFAULT 0,
			birth_date TEXT DEFAULT '',
			birth_place TEXT DEFAULT '',
			nationality TEXT DEFAULT '',
			occupation_json TEXT DEFAULT '[]',
			biography TEXT DEFAULT '',
			avatar_url TEXT DEFAULT '',
			aliases TEXT DEFAULT '',
			scraped INTEGER DEFAULT 0,
			scraped_at TIMESTAMP,
			tmdb_id TEXT DEFAULT '',
			imdb_id TEXT DEFAULT '',
			douban_id TEXT DEFAULT '',
			field_locks_json TEXT DEFAULT '{}',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			deleted_at TIMESTAMP
		)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_cast_person_name_norm ON cast_person(name_norm)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_cast_person_tmdb ON cast_person(tmdb_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_cast_person_deleted ON cast_person(deleted_at)`)
	_, _ = startupExecContext(ctx, db, `
		CREATE TABLE IF NOT EXISTS media_person (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_id INTEGER NOT NULL,
			person_id INTEGER NOT NULL,
			occupation TEXT NOT NULL,
			character_name TEXT DEFAULT '',
			role_type TEXT DEFAULT '',
			sort_order INTEGER DEFAULT 9999,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (media_id) REFERENCES media(id) ON DELETE CASCADE,
			FOREIGN KEY (person_id) REFERENCES cast_person(id),
			UNIQUE(media_id, person_id, occupation)
		)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_media_person_media ON media_person(media_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_media_person_person ON media_person(person_id)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_media_person_person_occ ON media_person(person_id, occupation)`)
	_, _ = startupExecContext(ctx, db, `
		CREATE TABLE IF NOT EXISTS person_scrape_task (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			person_id INTEGER,
			source TEXT DEFAULT 'tmdb',
			status TEXT DEFAULT 'pending',
			query TEXT DEFAULT '',
			external_id TEXT DEFAULT '',
			result_json TEXT DEFAULT '',
			error_message TEXT DEFAULT '',
			started_at TIMESTAMP,
			completed_at TIMESTAMP,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (person_id) REFERENCES cast_person(id)
		)`)
	_, _ = startupExecContext(ctx, db, `CREATE INDEX IF NOT EXISTS idx_person_scrape_task_status ON person_scrape_task(status, created_at)`)
	// Seed default AI provider configs.
	seedAIProviders(db)
	// Remove duplicate scheduled tasks (legacy seed inserted on every restart).
	if _, err := DedupeScheduledTasks(db); err != nil {
		return nil, err
	}
	_, _ = startupExecContext(ctx, db, `CREATE UNIQUE INDEX IF NOT EXISTS idx_scheduled_task_type_name ON scheduled_task(task_type, name)`)
	// Clean up stale transcode tasks that failed due to transient issues (path not found, context canceled).
	cleanupStaleTranscodeTasks(db)
	recoverStalePhotoTasks(db)
	if _, err := startupExecContext(ctx, db, `PRAGMA busy_timeout=30000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma busy_timeout: %w", err)
	}
	if err := verifySQLitePragmas(ctx, db, isMemorySQLitePath(path)); err != nil {
		_ = db.Close()
		return nil, err
	}
	// This migration is deliberately synchronous: callers never receive a DB handle
	// until normalized media sort values and their indexes satisfy the read invariant.
	if err := withStartupBusyRetry(ctx, func() error { return MigrateMediaSortColumns(ctx, db) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("media sort migration: %w", err)
	}
	if err := withStartupBusyRetry(ctx, func() error { return relationshipmigration.MigrateMediaRelationships(ctx, db) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("media relationship migration: %w", err)
	}
	// Apply enterprise migrations registered via RegisterEnterpriseMigration.
	// In the community build this slice is empty; commercial init() functions
	// append migrations for pretranscode/license tables before main runs.
	for _, m := range enterpriseMigrations {
		if err := ctx.Err(); err != nil {
			_ = db.Close()
			return nil, err
		}
		if err := m.Up(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("enterprise migration %s: %w", m.ID, err)
		}
	}
	if len(enterpriseMigrations) > 0 {
		if err := withStartupBusyRetry(ctx, func() error { return migrateIngestPublication(ctx, db) }); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("enterprise publication migration: %w", err)
		}
	}
	if isMemorySQLitePath(path) {
		if _, err := startupExecContext(ctx, db, `PRAGMA busy_timeout=30000`); err != nil {
			_ = db.Close()
			return nil, err
		}
		if err := verifySQLitePragmas(ctx, db, true); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	db, err = sql.Open("sqlite", appendSQLitePragmas(path))
	if err != nil {
		return nil, err
	}
	if isMemorySQLitePath(path) {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
	}
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifySQLitePragmas(ctx, db, isMemorySQLitePath(path)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func verifySQLitePragmas(ctx context.Context, db *sql.DB, allowMemoryJournal bool) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("verify sqlite pragmas connection: %w", err)
	}
	defer conn.Close()

	var journalMode string
	var busyTimeout, foreignKeys, synchronous int
	if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return fmt.Errorf("verify sqlite pragmas journal_mode: %w", err)
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("verify sqlite pragmas busy_timeout: %w", err)
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("verify sqlite pragmas foreign_keys: %w", err)
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return fmt.Errorf("verify sqlite pragmas synchronous: %w", err)
	}
	expectedJournal := "wal"
	if allowMemoryJournal {
		expectedJournal = "memory"
	}
	if journalMode != expectedJournal || busyTimeout != 30000 || foreignKeys != 1 || synchronous != 1 {
		return fmt.Errorf("sqlite pragma verification failed: journal_mode=%q want %s, busy_timeout=%d want 30000, foreign_keys=%d want 1, synchronous=%d want 1", journalMode, expectedJournal, busyTimeout, foreignKeys, synchronous)
	}
	return nil
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
		{id: "tongyi", name: "Tongyi Qianwen", apiURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", model: "qwen-plus"},
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
