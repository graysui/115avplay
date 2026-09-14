-- Sakuraplayer v2 SQLite Database Schema Export
-- Reference for movie metadata, magnet hashes, 115 bindings, and playback sessions

CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	);

CREATE TABLE sqlite_sequence(name,seq);

CREATE TABLE schema_meta (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

CREATE TABLE admin_user (
    id TEXT PRIMARY KEY NOT NULL,
    singleton_key INTEGER NOT NULL DEFAULT 1 CHECK (singleton_key = 1),
    username TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 64),
    password_hash TEXT NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    session_epoch INTEGER NOT NULL DEFAULT 0 CHECK (session_epoch >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE refresh_session (
    id TEXT PRIMARY KEY NOT NULL,
    admin_id TEXT NOT NULL REFERENCES admin_user (id) ON DELETE CASCADE,
    token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
    client_instance_id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    last_used_at TEXT
);

CREATE TABLE encrypted_setting (
    key TEXT PRIMARY KEY NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
    public_value TEXT,
    key_id TEXT CHECK (key_id IS NULL OR length(key_id) BETWEEN 1 AND 64),
    nonce BLOB CHECK (nonce IS NULL OR length(nonce) = 12),
    ciphertext BLOB CHECK (ciphertext IS NULL OR length(ciphertext) >= 16),
    version INTEGER NOT NULL CHECK (version >= 1),
    updated_at TEXT NOT NULL,
    CHECK (
        (public_value IS NOT NULL AND key_id IS NULL AND nonce IS NULL AND ciphertext IS NULL)
        OR
        (public_value IS NULL AND key_id IS NOT NULL AND nonce IS NOT NULL AND ciphertext IS NOT NULL)
    )
);

CREATE TABLE cloud115_binding (
    id TEXT PRIMARY KEY NOT NULL,
    singleton_key INTEGER NOT NULL DEFAULT 1 CHECK (singleton_key = 1),
    account_key TEXT NOT NULL CHECK (length(account_key) BETWEEN 1 AND 128),
    display_name TEXT,
    cookie_setting_key TEXT NOT NULL DEFAULT 'cloud115.cookie'
        REFERENCES encrypted_setting (key) ON DELETE RESTRICT
        CHECK (cookie_setting_key = 'cloud115.cookie'),
    login_app TEXT NOT NULL DEFAULT 'alipaymini' CHECK (login_app = 'alipaymini'),
    cache_root_cid TEXT NOT NULL CHECK (length(cache_root_cid) BETWEEN 1 AND 64),
    status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'unavailable', 'detached')),
    credential_version INTEGER NOT NULL CHECK (credential_version >= 1),
    last_verified_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE movie (
    id TEXT PRIMARY KEY NOT NULL,
    normalized_number TEXT NOT NULL CHECK (length(normalized_number) BETWEEN 1 AND 128),
    raw_numbers TEXT NOT NULL,
    javdb_id TEXT CHECK (javdb_id IS NULL OR length(javdb_id) BETWEEN 1 AND 128),
    title_original TEXT,
    title_zh TEXT,
    release_date TEXT,
    maker TEXT,
    series TEXT,
    director TEXT,
    score TEXT,
    catalog_state TEXT NOT NULL CHECK (
        catalog_state IN ('raw_only', 'metadata_queued', 'metadata_running', 'core_ready')
    ),
    metadata_updated_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
, description_original TEXT, description_zh TEXT);

CREATE TABLE resource_source (
    id TEXT PRIMARY KEY NOT NULL,
    website TEXT NOT NULL CHECK (website IN ('sehuatang', 'x1080x')),
    external_post_id INTEGER NOT NULL,
    movie_id TEXT REFERENCES movie (id) ON DELETE RESTRICT,
    raw_number TEXT,
    normalized_number TEXT,
    title TEXT NOT NULL,
    publish_date TEXT,
    section TEXT NOT NULL CHECK (
        section IN ('亚洲有码', '亚洲无码', '中文字幕', '4K原版', '素人有码', 'FC2')
    ),
    category TEXT,
    resource_size_mb INTEGER CHECK (resource_size_mb IS NULL OR resource_size_mb >= 0),
    magnet_key_id TEXT CHECK (magnet_key_id IS NULL OR length(magnet_key_id) BETWEEN 1 AND 64),
    magnet_nonce BLOB CHECK (magnet_nonce IS NULL OR length(magnet_nonce) = 12),
    magnet_ciphertext BLOB CHECK (magnet_ciphertext IS NULL OR length(magnet_ciphertext) >= 16),
    identification_status TEXT NOT NULL CHECK (
        identification_status IN ('identified', 'pending', 'manual', 'rejected')
    ),
    imported_at TEXT NOT NULL, detail_url TEXT, preview_urls TEXT NOT NULL DEFAULT '[]', source_created_at TEXT, source_updated_at TEXT,
    CHECK (
        (magnet_key_id IS NULL AND magnet_nonce IS NULL AND magnet_ciphertext IS NULL)
        OR
        (magnet_key_id IS NOT NULL AND magnet_nonce IS NOT NULL AND magnet_ciphertext IS NOT NULL)
    ),
    CHECK (
        (identification_status = 'pending' AND movie_id IS NULL AND normalized_number IS NULL)
        OR
        (identification_status IN ('identified', 'manual') AND movie_id IS NOT NULL AND normalized_number IS NOT NULL)
        OR
        (identification_status = 'rejected')
    ),
    CHECK (
        identification_status <> 'rejected'
        OR (magnet_key_id IS NULL AND magnet_nonce IS NULL AND magnet_ciphertext IS NULL)
    )
);

CREATE TABLE actor (
    id TEXT PRIMARY KEY NOT NULL,
    javdb_id TEXT NOT NULL UNIQUE,
    name_ja TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
, name_zh TEXT, bio_original TEXT, bio_zh TEXT, bio_zh_source TEXT CHECK (bio_zh_source IS NULL OR bio_zh_source IN ('actor_mapping', 'ai')), gender TEXT NOT NULL DEFAULT 'unknown' CHECK (gender IN ('female', 'male', 'unknown')));

CREATE TABLE movie_actor (
    movie_id TEXT NOT NULL REFERENCES movie (id) ON DELETE CASCADE,
    actor_id TEXT NOT NULL REFERENCES actor (id) ON DELETE CASCADE,
    PRIMARY KEY (movie_id, actor_id)
);

CREATE TABLE tag (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

CREATE TABLE movie_tag (
    movie_id TEXT NOT NULL REFERENCES movie (id) ON DELETE CASCADE,
    tag_id TEXT NOT NULL REFERENCES tag (id) ON DELETE CASCADE,
    PRIMARY KEY (movie_id, tag_id)
);

CREATE TABLE metadata_job (
    id TEXT PRIMARY KEY NOT NULL,
    movie_id TEXT NOT NULL REFERENCES movie (id) ON DELETE CASCADE,
    normalized_number TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    reason TEXT NOT NULL DEFAULT 'initial',
    failure_code TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT
, priority INTEGER NOT NULL DEFAULT 100 CHECK (priority BETWEEN 1 AND 1000), archived_at TEXT, archive_requested_at TEXT);

CREATE TABLE movie_favorite (
    movie_id TEXT PRIMARY KEY NOT NULL REFERENCES movie (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
);

CREATE TABLE actor_favorite (
    actor_id TEXT PRIMARY KEY NOT NULL REFERENCES actor (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE movie_fts USING fts5(
    movie_id UNINDEXED,
    number,
    title,
    tokenize = 'unicode61'
);

CREATE TABLE 'movie_fts_data'(id INTEGER PRIMARY KEY, block BLOB);

CREATE TABLE 'movie_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;

CREATE TABLE 'movie_fts_content'(id INTEGER PRIMARY KEY, c0, c1, c2);

CREATE TABLE 'movie_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);

CREATE TABLE 'movie_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;

CREATE TABLE cache_job (
    id TEXT PRIMARY KEY NOT NULL,
    movie_id TEXT NOT NULL REFERENCES movie (id) ON DELETE RESTRICT,
    source_id TEXT NOT NULL REFERENCES resource_source (id) ON DELETE RESTRICT,
    binding_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN (
            'queued', 'submitting', 'offlining', 'submit_uncertain', 'resolving',
            'awaiting_selection', 'ready', 'cancelling', 'cleaning',
            'cleanup_failed', 'failed', 'cleaned', 'detached'
        )
    ),
    task_dir_name TEXT NOT NULL CHECK (length(task_dir_name) = 10),
    task_cid TEXT,
    info_hash TEXT,
    remote_percent REAL NOT NULL DEFAULT 0 CHECK (remote_percent >= 0 AND remote_percent <= 100),
    error_code TEXT,
    ready_at TEXT,
    last_accessed_at TEXT,
    expires_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE cache_play_request (
    idempotency_key TEXT PRIMARY KEY NOT NULL,
    movie_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    cache_job_id TEXT NOT NULL REFERENCES cache_job (id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
);

CREATE TABLE remote_media (
    id TEXT PRIMARY KEY NOT NULL,
    cache_job_id TEXT NOT NULL REFERENCES cache_job (id) ON DELETE CASCADE,
    file_id TEXT NOT NULL,
    pickcode TEXT NOT NULL,
    name TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sequence_no INTEGER NOT NULL,
    selected INTEGER NOT NULL DEFAULT 0 CHECK (selected IN (0, 1)),
    is_valid INTEGER NOT NULL DEFAULT 1 CHECK (is_valid IN (0, 1))
);

CREATE TABLE remote_subtitle (
    id TEXT PRIMARY KEY NOT NULL,
    cache_job_id TEXT NOT NULL REFERENCES cache_job (id) ON DELETE CASCADE,
    media_id TEXT REFERENCES remote_media (id) ON DELETE SET NULL,
    file_id TEXT NOT NULL,
    pickcode TEXT NOT NULL,
    name TEXT NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('srt', 'ass', 'ssa', 'vtt')),
    selected_by_default INTEGER NOT NULL DEFAULT 0 CHECK (selected_by_default IN (0, 1))
);

CREATE TABLE playback_session (
    id TEXT PRIMARY KEY NOT NULL,
    cache_job_id TEXT NOT NULL REFERENCES cache_job (id) ON DELETE CASCADE,
    media_id TEXT NOT NULL REFERENCES remote_media (id) ON DELETE CASCADE,
    client_instance_id TEXT NOT NULL,
    platform TEXT NOT NULL CHECK (platform IN ('web', 'windows', 'harmonyos')),
    mode TEXT NOT NULL CHECK (mode IN ('original', 'compatibility')),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE movie_progress (
    movie_id TEXT PRIMARY KEY NOT NULL REFERENCES movie (id) ON DELETE CASCADE,
    position_seconds REAL NOT NULL CHECK (position_seconds >= 0),
    duration_seconds REAL CHECK (duration_seconds IS NULL OR duration_seconds > 0),
    completed INTEGER NOT NULL CHECK (completed IN (0, 1)),
    version INTEGER NOT NULL CHECK (version >= 1),
    updated_at TEXT NOT NULL
);

CREATE TABLE domain_event (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    type TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE actor_alias (
    actor_id TEXT NOT NULL REFERENCES actor (id) ON DELETE CASCADE,
    alias TEXT NOT NULL,
    normalized_alias TEXT NOT NULL,
    authority TEXT NOT NULL CHECK (authority IN ('javdb', 'actor_mapping')),
    PRIMARY KEY (actor_id, normalized_alias)
);

CREATE TABLE catalog_image (
    id TEXT PRIMARY KEY NOT NULL,
    owner_type TEXT NOT NULL CHECK (owner_type IN ('movie', 'actor')),
    owner_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('cover', 'plot', 'profile', 'placeholder')),
    position INTEGER NOT NULL CHECK (position >= 0),
    source_url TEXT,
    relative_path TEXT NOT NULL CHECK (
        relative_path <> '' AND relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%..%'
    ),
    sha256 TEXT CHECK (sha256 IS NULL OR (length(sha256) = 64 AND lower(sha256) = sha256)),
    status TEXT NOT NULL CHECK (status IN ('ready', 'placeholder', 'retry_pending')),
    created_at TEXT NOT NULL,
    UNIQUE (owner_type, owner_id, kind, position)
);

CREATE TABLE metadata_queue_control (
    singleton_key INTEGER PRIMARY KEY CHECK (singleton_key = 1),
    paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
    updated_at TEXT NOT NULL
);

CREATE TABLE mgdb_sync_run (
    id TEXT PRIMARY KEY NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('incremental_30d', 'full_reconcile')),
    repository TEXT NOT NULL,
    release_id TEXT NOT NULL,
    release_tag TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    imported_count INTEGER NOT NULL DEFAULT 0 CHECK (imported_count >= 0),
    pending_count INTEGER NOT NULL DEFAULT 0 CHECK (pending_count >= 0),
    rejected_count INTEGER NOT NULL DEFAULT 0 CHECK (rejected_count >= 0),
    queued_count INTEGER NOT NULL DEFAULT 0 CHECK (queued_count >= 0),
    failure_code TEXT,
    started_at TEXT NOT NULL,
    completed_at TEXT, new_movie_count INTEGER NOT NULL DEFAULT 0 CHECK (new_movie_count >= 0), source_created_count INTEGER NOT NULL DEFAULT 0 CHECK (source_created_count >= 0), source_updated_count INTEGER NOT NULL DEFAULT 0 CHECK (source_updated_count >= 0), skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0), failure_stage TEXT,
    UNIQUE (repository, release_id, mode)
);

CREATE TABLE mgdb_sync_request (
    id TEXT PRIMARY KEY NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('incremental_30d', 'full_reconcile')),
    scheduled_for TEXT NOT NULL,
    slot TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    failure_code TEXT,
    sync_run_id TEXT REFERENCES mgdb_sync_run (id) ON DELETE RESTRICT,
    UNIQUE (mode, slot)
);

CREATE TABLE mgdb_sync_asset (
    id TEXT PRIMARY KEY NOT NULL,
    sync_run_id TEXT NOT NULL REFERENCES mgdb_sync_run (id) ON DELETE CASCADE,
    asset_name TEXT NOT NULL,
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    byte_size INTEGER NOT NULL CHECK (byte_size > 0),
    row_count INTEGER NOT NULL DEFAULT 0 CHECK (row_count >= 0),
    status TEXT NOT NULL CHECK (status IN ('verified', 'decrypted', 'imported', 'failed')),
    manifest TEXT NOT NULL, stage TEXT NOT NULL DEFAULT 'pending', row_cursor INTEGER NOT NULL DEFAULT 0 CHECK (row_cursor >= 0), failure_code TEXT,
    UNIQUE (sync_run_id, asset_name)
);

CREATE TABLE metadata_enrichment (
    movie_id TEXT NOT NULL REFERENCES movie (id) ON DELETE CASCADE,
    stage TEXT NOT NULL CHECK (stage IN ('dmm', 'actor_mapping', 'gfriends', 'translation')),
    status TEXT NOT NULL CHECK (status IN ('succeeded', 'not_found', 'warning')),
    error_code TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (movie_id, stage),
    CHECK (
        (status = 'warning' AND error_code IS NOT NULL)
        OR (status <> 'warning' AND error_code IS NULL)
    )
);

CREATE TABLE provider_snapshot_request (
    id TEXT PRIMARY KEY NOT NULL,
    slot TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    failure_code TEXT,
    scheduled_for TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT
);

CREATE TABLE provider_snapshot (
    id TEXT PRIMARY KEY NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('actor_mapping', 'gfriends')),
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64 AND lower(sha256) = sha256),
    byte_size INTEGER NOT NULL CHECK (byte_size > 0),
    relative_path TEXT NOT NULL CHECK (
        relative_path <> '' AND relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%..%'
    ),
    status TEXT NOT NULL CHECK (status IN ('current', 'superseded')),
    fetched_at TEXT NOT NULL,
    activated_at TEXT NOT NULL,
    UNIQUE (source, sha256)
);

CREATE TABLE gfriends_actor_asset (
    id TEXT PRIMARY KEY NOT NULL,
    actor_id TEXT NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    snapshot_id TEXT NOT NULL REFERENCES provider_snapshot(id) ON DELETE RESTRICT,
    asset_kind TEXT NOT NULL CHECK (asset_kind IN ('profile', 'gallery')),
    position INTEGER NOT NULL CHECK (position >= 0),
    url TEXT NOT NULL,
    match_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (actor_id, position),
    UNIQUE (actor_id, url)
);

CREATE TABLE translation_record (
    id TEXT PRIMARY KEY NOT NULL,
    owner_type TEXT NOT NULL CHECK (owner_type IN ('movie_title', 'movie_description')),
    owner_id TEXT NOT NULL REFERENCES movie(id) ON DELETE CASCADE,
    source_text TEXT NOT NULL CHECK (length(source_text) BETWEEN 1 AND 32000),
    source_hash TEXT NOT NULL CHECK (
        length(source_hash) = 64 AND lower(source_hash) = source_hash
        AND source_hash NOT GLOB '*[^0-9a-f]*'
    ),
    translated_text TEXT CHECK (translated_text IS NULL OR length(translated_text) BETWEEN 1 AND 32000),
    model TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 255),
    prompt_version TEXT NOT NULL CHECK (length(prompt_version) BETWEEN 1 AND 64),
    status TEXT NOT NULL CHECK (status IN ('reserved', 'dispatched', 'completed', 'rejected', 'unknown')),
    claim_token TEXT,
    claim_expires_at TEXT,
    dispatch_started_at TEXT,
    failure_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (owner_type, owner_id, source_hash, model, prompt_version),
    CHECK (
        (status = 'reserved' AND translated_text IS NULL AND claim_token IS NOT NULL
            AND claim_expires_at IS NOT NULL AND dispatch_started_at IS NULL AND failure_code IS NULL)
        OR (status = 'dispatched' AND translated_text IS NULL AND claim_token IS NOT NULL
            AND claim_expires_at IS NULL AND dispatch_started_at IS NOT NULL AND failure_code IS NULL)
        OR (status = 'completed' AND translated_text IS NOT NULL AND claim_token IS NULL
            AND claim_expires_at IS NULL AND dispatch_started_at IS NOT NULL AND failure_code IS NULL)
        OR (status IN ('rejected', 'unknown') AND translated_text IS NULL AND claim_token IS NULL
            AND claim_expires_at IS NULL AND dispatch_started_at IS NOT NULL AND failure_code IS NOT NULL)
    )
);

CREATE TABLE ranking_sync_request (
    id TEXT PRIMARY KEY NOT NULL,
    board TEXT NOT NULL CHECK (board IN ('daily', 'weekly', 'monthly', 'top250')),
    year INTEGER NOT NULL DEFAULT 0 CHECK (year = 0 OR year BETWEEN 2008 AND 2200),
    slot TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    scheduled_for TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    failure_code TEXT,
    snapshot_id TEXT REFERENCES ranking_snapshot (id) ON DELETE RESTRICT,
    UNIQUE (board, year, slot),
    CHECK (
        (status = 'queued' AND started_at IS NULL AND completed_at IS NULL AND failure_code IS NULL AND snapshot_id IS NULL)
        OR (status = 'running' AND started_at IS NOT NULL AND completed_at IS NULL AND failure_code IS NULL AND snapshot_id IS NULL)
        OR (status = 'completed' AND started_at IS NOT NULL AND completed_at IS NOT NULL AND failure_code IS NULL AND snapshot_id IS NOT NULL)
        OR (status = 'failed' AND started_at IS NOT NULL AND completed_at IS NOT NULL AND failure_code IS NOT NULL AND snapshot_id IS NULL)
    )
);

CREATE TABLE ranking_snapshot (
    id TEXT PRIMARY KEY NOT NULL,
    board TEXT NOT NULL CHECK (board IN ('daily', 'weekly', 'monthly', 'top250')),
    year INTEGER NOT NULL DEFAULT 0 CHECK (year = 0 OR year BETWEEN 2008 AND 2200),
    status TEXT NOT NULL CHECK (status IN ('building', 'current', 'superseded')),
    synced_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE ranking_entry (
    snapshot_id TEXT NOT NULL REFERENCES ranking_snapshot (id) ON DELETE CASCADE,
    rank INTEGER NOT NULL CHECK (rank >= 1),
    normalized_number TEXT NOT NULL CHECK (length(normalized_number) BETWEEN 1 AND 128),
    PRIMARY KEY (snapshot_id, rank),
    UNIQUE (snapshot_id, normalized_number)
);

CREATE TABLE mgdb_sync_batch (
    id TEXT PRIMARY KEY NOT NULL,
    sync_run_id TEXT NOT NULL REFERENCES mgdb_sync_run (id) ON DELETE CASCADE,
    asset_name TEXT NOT NULL,
    row_start INTEGER NOT NULL CHECK (row_start >= 0),
    row_end INTEGER NOT NULL CHECK (row_end > row_start),
    imported_count INTEGER NOT NULL DEFAULT 0 CHECK (imported_count >= 0),
    new_movie_count INTEGER NOT NULL DEFAULT 0 CHECK (new_movie_count >= 0),
    source_created_count INTEGER NOT NULL DEFAULT 0 CHECK (source_created_count >= 0),
    source_updated_count INTEGER NOT NULL DEFAULT 0 CHECK (source_updated_count >= 0),
    pending_count INTEGER NOT NULL DEFAULT 0 CHECK (pending_count >= 0),
    rejected_count INTEGER NOT NULL DEFAULT 0 CHECK (rejected_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    created_at TEXT NOT NULL,
    UNIQUE (sync_run_id, asset_name, row_start, row_end)
);

CREATE TABLE mgdb_sync_seed (
    sync_run_id TEXT PRIMARY KEY NOT NULL REFERENCES mgdb_sync_run (id) ON DELETE CASCADE,
    queued_count INTEGER NOT NULL CHECK (queued_count >= 0),
    created_at TEXT NOT NULL
);

CREATE TABLE legacy_repair_fact (
    id TEXT PRIMARY KEY NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('legacy_movie', 'legacy_source', 'legacy_metadata_job', 'mgdb_run', 'mgdb_initial_job')),
    legacy_id TEXT NOT NULL,
    movie_id TEXT,
    normalized_number TEXT,
    original_state TEXT,
    original_status TEXT,
    failure_code TEXT,
    observed_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (kind, legacy_id)
);

