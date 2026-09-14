-- MediaVault 目标 Schema；设计 DDL，不是可直接作用于旧库的升级脚本。
-- 时间统一为 UTC RFC3339 文本（秒精度），比较前保证同格式。
PRAGMA foreign_keys = ON;

CREATE TABLE schema_meta (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

CREATE TABLE offline_movies (
    code TEXT PRIMARY KEY NOT NULL,
    title TEXT,
    official_title TEXT,
    category TEXT NOT NULL DEFAULT '未知',
    publish_date TEXT,
    release_date TEXT,
    first_seen_at TEXT NOT NULL,
    preview_images TEXT,
    source_websites TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(source_websites)),
    title_zh TEXT,
    description_zh TEXT,
    cover_url TEXT,
    poster_url TEXT,
    actors TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(actors)),
    tags TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(tags)),
    maker TEXT,
    director TEXT,
    score REAL CHECK(score IS NULL OR score BETWEEN 0 AND 5),
    runtime_ticks INTEGER CHECK(runtime_ticks IS NULL OR runtime_ticks > 0),
    is_enriched INTEGER NOT NULL DEFAULT 0 CHECK(is_enriched IN (0,1,3)),
    scrape_policy TEXT NOT NULL DEFAULT 'auto' CHECK(scrape_policy IN ('auto','exempt','paused')),
    policy_reason TEXT,
    scrape_status TEXT NOT NULL DEFAULT 'idle' CHECK(scrape_status IN ('idle','success','partial','not_found','transient')),
    scrape_failed_reason TEXT,
    not_found_count INTEGER NOT NULL DEFAULT 0 CHECK(not_found_count >= 0),
    last_not_found_day TEXT,
    partial_attempts INTEGER NOT NULL DEFAULT 0 CHECK(partial_attempts >= 0),
    next_scrape_at TEXT,
    last_scraped_at TEXT,
    metadata_sources TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(metadata_sources)),
    manual_fields TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(manual_fields)),
    legacy_metadata TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(legacy_metadata)),
    deleted_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_movies_category ON offline_movies(category,code);
CREATE INDEX idx_movies_created ON offline_movies(created_at DESC,code);
CREATE INDEX idx_movies_release ON offline_movies(release_date DESC,code);
CREATE INDEX idx_movies_scrape ON offline_movies(scrape_policy,next_scrape_at,is_enriched);

CREATE TABLE offline_magnets (
    info_hash TEXT PRIMARY KEY NOT NULL,
    movie_code TEXT NOT NULL REFERENCES offline_movies(code) ON DELETE CASCADE,
    resource_kind TEXT NOT NULL CHECK(resource_kind IN ('btih','ed2k','existing')),
    content_hash TEXT,
    magnet_url TEXT NOT NULL,
    title TEXT,
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes >= 0),
    section TEXT,
    quality_label TEXT NOT NULL DEFAULT 'unknown',
    website TEXT,
    publish_date TEXT,
    has_chinese_sub INTEGER NOT NULL DEFAULT 0 CHECK(has_chinese_sub IN (0,1)),
    is_cracked INTEGER NOT NULL DEFAULT 0 CHECK(is_cracked IN (0,1)),
    is_4k INTEGER NOT NULL DEFAULT 0 CHECK(is_4k IN (0,1)),
    is_censored INTEGER NOT NULL DEFAULT 0 CHECK(is_censored IN (0,1)),
    priority_score INTEGER GENERATED ALWAYS AS
      (8*has_chinese_sub + 4*is_cracked + 2*is_4k + is_censored) STORED,
    is_preferred INTEGER NOT NULL DEFAULT 0 CHECK(is_preferred IN (0,1)),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    metadata_sources TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(metadata_sources)),
    manual_fields TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(manual_fields)),
    legacy_runtime TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(legacy_runtime)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_resources_movie ON offline_magnets(movie_code,enabled,priority_score DESC,size_bytes DESC);
CREATE UNIQUE INDEX idx_resources_preferred ON offline_magnets(movie_code) WHERE is_preferred=1;

CREATE TABLE cloud_bindings (
    id TEXT PRIMARY KEY NOT NULL,
    provider TEXT NOT NULL DEFAULT '115' CHECK(provider='115'),
    provider_user_id TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    secret_setting_key TEXT NOT NULL,
    config_revision INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_binding_active ON cloud_bindings(provider) WHERE enabled=1;

CREATE TABLE jobs (
    id TEXT PRIMARY KEY NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('auth','transfer','cleanup','scan','scrape','search','sync30d','import_full','rankings','delete_movie')),
    dedupe_key TEXT NOT NULL,
    resource_key TEXT REFERENCES offline_magnets(info_hash) ON DELETE SET NULL,
    binding_id TEXT REFERENCES cloud_bindings(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK(state IN ('queued','running','reconcile','retry_wait','succeeded','failed','cancelled')),
    generation INTEGER NOT NULL DEFAULT 1,
    lease_owner TEXT,
    lease_until TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_run_at TEXT,
    deadline_at TEXT,
    remote_id TEXT,
    owned_root_id TEXT,
    params_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(params_json)),
    result_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(result_json)),
    last_error TEXT,
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_jobs_active ON jobs(kind,dedupe_key)
 WHERE state IN ('queued','running','reconcile','retry_wait');
CREATE INDEX idx_jobs_due ON jobs(state,next_run_at,lease_until);

CREATE TABLE cloud_assets (
    id TEXT PRIMARY KEY NOT NULL,
    binding_id TEXT NOT NULL REFERENCES cloud_bindings(id) ON DELETE RESTRICT,
    resource_key TEXT REFERENCES offline_magnets(info_hash) ON DELETE SET NULL,
    owning_job_id TEXT REFERENCES jobs(id) ON DELETE RESTRICT,
    source_type TEXT NOT NULL CHECK(source_type IN ('permanent','temporary')),
    state TEXT NOT NULL CHECK(state IN ('ready','missing','pending_delete','deleting','deleted','quarantined')),
    generation INTEGER NOT NULL DEFAULT 1,
    file_id TEXT,
    pick_code TEXT,
    file_name TEXT,
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes >= 0),
    container TEXT,
    runtime_ticks INTEGER CHECK(runtime_ticks IS NULL OR runtime_ticks>0),
    media_streams TEXT CHECK(media_streams IS NULL OR json_valid(media_streams)),
    parent_id TEXT,
    owned_root_id TEXT,
    root_snapshot TEXT,
    manifest_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(manifest_json)),
    ready_at TEXT,
    expires_at TEXT,
    deleted_at TEXT,
    last_seen_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK(source_type!='permanent' OR expires_at IS NULL),
    CHECK(state!='ready' OR (length(trim(file_id))>0 AND file_id IS NOT NULL
                         AND length(trim(pick_code))>0 AND pick_code IS NOT NULL)),
    CHECK(source_type!='temporary' OR state!='ready'
       OR (owning_job_id IS NOT NULL AND owned_root_id IS NOT NULL
           AND root_snapshot IS NOT NULL AND ready_at IS NOT NULL AND expires_at IS NOT NULL AND expires_at>ready_at))
);
CREATE INDEX idx_assets_resource ON cloud_assets(resource_key,binding_id,state,expires_at);
CREATE INDEX idx_assets_cleanup ON cloud_assets(source_type,state,expires_at);
CREATE UNIQUE INDEX idx_assets_live_file ON cloud_assets(binding_id,file_id)
 WHERE file_id IS NOT NULL AND state IN ('ready','pending_delete','deleting');

CREATE TABLE ingest_assets (
    id TEXT PRIMARY KEY NOT NULL,
    run_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE RESTRICT,
    release_id TEXT NOT NULL,
    source TEXT NOT NULL,
    asset_name TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    coverage_start TEXT,
    coverage_end TEXT,
    state TEXT NOT NULL CHECK(state IN ('downloaded','processing','completed','failed')),
    last_committed_row INTEGER NOT NULL DEFAULT 0,
    counts_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(counts_json)),
    last_error TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE(release_id,source,sha256)
);

CREATE TABLE scan_runs (
    id TEXT PRIMARY KEY NOT NULL,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE RESTRICT,
    binding_id TEXT NOT NULL REFERENCES cloud_bindings(id) ON DELETE RESTRICT,
    root_id TEXT NOT NULL,
    mode TEXT NOT NULL CHECK(mode IN ('full','incremental')),
    state TEXT NOT NULL CHECK(state IN ('running','completed','failed')),
    started_at TEXT NOT NULL,
    completed_at TEXT,
    last_error TEXT
);
CREATE INDEX idx_scan_root ON scan_runs(binding_id,root_id,started_at DESC);
CREATE TABLE scan_seen (
    scan_id TEXT NOT NULL REFERENCES scan_runs(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL,
    resource_key TEXT REFERENCES offline_magnets(info_hash) ON DELETE SET NULL,
    PRIMARY KEY(scan_id,file_id)
);

CREATE TABLE users (
    id TEXT PRIMARY KEY NOT NULL,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_admin INTEGER NOT NULL DEFAULT 0 CHECK(is_admin IN (0,1)),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK(must_change_password IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    audience TEXT NOT NULL CHECK(audience IN ('admin','emby')),
    device_id TEXT NOT NULL,
    device_name TEXT,
    client_name TEXT,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_auth_user ON auth_sessions(user_id,expires_at);

CREATE TABLE play_sessions (
    id TEXT PRIMARY KEY NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    movie_code TEXT NOT NULL REFERENCES offline_movies(code) ON DELETE CASCADE,
    resource_key TEXT REFERENCES offline_magnets(info_hash) ON DELETE SET NULL,
    asset_id TEXT REFERENCES cloud_assets(id) ON DELETE SET NULL,
    device_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('negotiating','active','stopped')),
    position_ticks INTEGER NOT NULL DEFAULT 0 CHECK(position_ticks>=0),
    duration_ticks INTEGER CHECK(duration_ticks IS NULL OR duration_ticks>0),
    last_sequence INTEGER,
    is_paused INTEGER NOT NULL DEFAULT 0 CHECK(is_paused IN (0,1)),
    counted_complete INTEGER NOT NULL DEFAULT 0 CHECK(counted_complete IN (0,1)),
    lease_until TEXT,
    started_at TEXT,
    closed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_play_asset_lease ON play_sessions(asset_id,state,lease_until);
CREATE TABLE user_progress (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    movie_code TEXT NOT NULL REFERENCES offline_movies(code) ON DELETE CASCADE,
    active_session_id TEXT REFERENCES play_sessions(id) ON DELETE SET NULL,
    position_ticks INTEGER NOT NULL DEFAULT 0 CHECK(position_ticks>=0),
    duration_ticks INTEGER,
    played INTEGER NOT NULL DEFAULT 0 CHECK(played IN (0,1)),
    favorite INTEGER NOT NULL DEFAULT 0 CHECK(favorite IN (0,1)),
    play_count INTEGER NOT NULL DEFAULT 0 CHECK(play_count>=0),
    last_played_at TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(user_id,movie_code)
);
CREATE INDEX idx_progress_resume ON user_progress(user_id,played,last_played_at DESC);

CREATE TABLE libraries (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    predicate TEXT NOT NULL CHECK(predicate IN ('chinese_sub','censored','uncensored','4k','fc2','domestic')),
    sort_order INTEGER NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    cover_url TEXT
);
INSERT INTO libraries(id,name,predicate,sort_order) VALUES
 ('lib_chinese_sub','中文字幕','chinese_sub',1),
 ('lib_censored','亚洲有码','censored',2),
 ('lib_uncensored','亚洲无码','uncensored',3),
 ('lib_4k','4K原版','4k',4),
 ('lib_fc2','FC2/素人','fc2',5),
 ('lib_domestic','国产','domestic',6);

CREATE TABLE system_settings (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT,
    is_secret INTEGER NOT NULL DEFAULT 0 CHECK(is_secret IN (0,1)),
    revision INTEGER NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL
);
CREATE TABLE schedules (
    id TEXT PRIMARY KEY NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('sync30d','rankings','scan')),
    timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    rule_json TEXT NOT NULL CHECK(json_valid(rule_json)),
    params_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(params_json)),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
    last_slot TEXT,
    next_run_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE TABLE alerts (
    key TEXT PRIMARY KEY NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('active','resolved')),
    message TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    last_delivered_at TEXT,
    next_delivery_at TEXT,
    delivery_attempts INTEGER NOT NULL DEFAULT 0,
    delivery_error TEXT
);
