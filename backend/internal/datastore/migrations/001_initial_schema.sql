-- +goose Up
-- +goose StatementBegin

-- Accounts: user accounts that can own multiple profiles
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    is_master BOOLEAN NOT NULL DEFAULT false,
    max_streams INTEGER NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Users: profiles belonging to an account
CREATE TABLE users (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    color TEXT NOT NULL DEFAULT '',
    icon_url TEXT NOT NULL DEFAULT '',
    pin_hash TEXT NOT NULL DEFAULT '',
    trakt_account_id TEXT NOT NULL DEFAULT '',
    plex_account_id TEXT NOT NULL DEFAULT '',
    is_kids_profile BOOLEAN NOT NULL DEFAULT false,
    kids_mode TEXT NOT NULL DEFAULT '',
    kids_max_rating TEXT NOT NULL DEFAULT '',
    kids_max_movie_rating TEXT NOT NULL DEFAULT '',
    kids_max_tv_rating TEXT NOT NULL DEFAULT '',
    kids_allowed_lists JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_users_account_id ON users(account_id);

-- Sessions: authenticated sessions for accounts
CREATE TABLE sessions (
    token TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    is_master BOOLEAN NOT NULL DEFAULT false,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_agent TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_account_id ON sessions(account_id);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);

-- Invitations: one-time use invitation links for account creation
CREATE TABLE invitations (
    id TEXT PRIMARY KEY,
    token TEXT UNIQUE NOT NULL,
    created_by TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    account_expires_in_hours INTEGER NOT NULL DEFAULT 0,
    used_at TIMESTAMPTZ,
    used_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Clients: registered devices
CREATE TABLE clients (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    device_type TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    app_version TEXT NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    filter_enabled BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX idx_clients_user_id ON clients(user_id);

-- Client settings: per-device filter/display/network overrides (stored as JSONB blob)
CREATE TABLE client_settings (
    client_id TEXT PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    settings JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- User settings: per-profile preferences (stored as JSONB blob — deeply nested struct)
CREATE TABLE user_settings (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    settings JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Watchlist: saved media items per user
CREATE TABLE watchlist (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    overview TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    poster_url TEXT NOT NULL DEFAULT '',
    backdrop_url TEXT NOT NULL DEFAULT '',
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    external_ids JSONB NOT NULL DEFAULT '{}',
    genres JSONB NOT NULL DEFAULT '[]',
    runtime_minutes INTEGER NOT NULL DEFAULT 0,
    sync_source TEXT NOT NULL DEFAULT '',
    synced_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, item_key)
);

-- Custom lists: user-created collections
CREATE TABLE custom_lists (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_custom_lists_user_id ON custom_lists(user_id);

-- Custom list items: items within a custom list (reuses WatchlistItem shape)
CREATE TABLE custom_list_items (
    list_id TEXT NOT NULL REFERENCES custom_lists(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    overview TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    poster_url TEXT NOT NULL DEFAULT '',
    backdrop_url TEXT NOT NULL DEFAULT '',
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    external_ids JSONB NOT NULL DEFAULT '{}',
    genres JSONB NOT NULL DEFAULT '[]',
    runtime_minutes INTEGER NOT NULL DEFAULT 0,
    sync_source TEXT NOT NULL DEFAULT '',
    synced_at TIMESTAMPTZ,
    PRIMARY KEY (list_id, item_key)
);

-- Watch history: all watched items
CREATE TABLE watch_history (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    watched BOOLEAN NOT NULL DEFAULT true,
    watched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    external_ids JSONB NOT NULL DEFAULT '{}',
    season_number INTEGER NOT NULL DEFAULT 0,
    episode_number INTEGER NOT NULL DEFAULT 0,
    series_id TEXT NOT NULL DEFAULT '',
    series_name TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (user_id, item_key)
);
CREATE INDEX idx_watch_history_user_id ON watch_history(user_id);

-- Playback progress: resume positions
CREATE TABLE playback_progress (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_key TEXT NOT NULL,
    media_type TEXT NOT NULL,
    item_id TEXT NOT NULL,
    position DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration DOUBLE PRECISION NOT NULL DEFAULT 0,
    percent_watched DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_paused BOOLEAN NOT NULL DEFAULT false,
    external_ids JSONB NOT NULL DEFAULT '{}',
    season_number INTEGER NOT NULL DEFAULT 0,
    episode_number INTEGER NOT NULL DEFAULT 0,
    series_id TEXT NOT NULL DEFAULT '',
    series_name TEXT NOT NULL DEFAULT '',
    episode_name TEXT NOT NULL DEFAULT '',
    movie_name TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    hidden_from_continue_watching BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (user_id, item_key)
);
CREATE INDEX idx_playback_progress_user_id ON playback_progress(user_id);

-- Content preferences: per-content audio/subtitle choices
CREATE TABLE content_preferences (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content_id TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    audio_language TEXT NOT NULL DEFAULT '',
    subtitle_language TEXT NOT NULL DEFAULT '',
    subtitle_mode TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, content_id)
);

-- Prequeue: pre-queued content with search/resolve state (stored as JSONB — complex nested struct)
CREATE TABLE prequeue (
    id TEXT PRIMARY KEY,
    title_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    data JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_prequeue_title_user ON prequeue(title_id, user_id);
CREATE INDEX idx_prequeue_status ON prequeue(status);

-- Prewarm: pre-warmed continue watching entries (stored as JSONB — complex struct)
CREATE TABLE prewarm (
    id TEXT PRIMARY KEY,
    title_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    data JSONB NOT NULL DEFAULT '{}',
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_prewarm_user_id ON prewarm(user_id);

-- Import queue (migrated from queue.db)
CREATE TABLE import_queue (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    nzb_path TEXT NOT NULL,
    relative_path TEXT,
    title TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    priority INTEGER NOT NULL DEFAULT 0,
    batch_id TEXT,
    error_message TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    max_retries INTEGER NOT NULL DEFAULT 3,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX idx_import_queue_status ON import_queue(status);
CREATE INDEX idx_import_queue_batch_id ON import_queue(batch_id);

-- Queue stats
CREATE TABLE queue_stats (
    id INTEGER PRIMARY KEY DEFAULT 1,
    total_processed BIGINT NOT NULL DEFAULT 0,
    total_failed BIGINT NOT NULL DEFAULT 0,
    total_retried BIGINT NOT NULL DEFAULT 0,
    last_processed_at TIMESTAMPTZ,
    CHECK (id = 1)
);

-- File health tracking
CREATE TABLE file_health (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    file_path TEXT UNIQUE NOT NULL,
    file_name TEXT NOT NULL DEFAULT '',
    file_size BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'unknown',
    last_check_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_check_at TIMESTAMPTZ,
    check_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    repair_retry_count INTEGER NOT NULL DEFAULT 0,
    max_repair_retries INTEGER NOT NULL DEFAULT 3,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Media files tracking
CREATE TABLE media_files (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    file_id TEXT UNIQUE,
    file_path TEXT NOT NULL,
    media_type TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS media_files;
DROP TABLE IF EXISTS file_health;
DROP TABLE IF EXISTS queue_stats;
DROP TABLE IF EXISTS import_queue;
DROP TABLE IF EXISTS prewarm;
DROP TABLE IF EXISTS prequeue;
DROP TABLE IF EXISTS content_preferences;
DROP TABLE IF EXISTS playback_progress;
DROP TABLE IF EXISTS watch_history;
DROP TABLE IF EXISTS custom_list_items;
DROP TABLE IF EXISTS custom_lists;
DROP TABLE IF EXISTS watchlist;
DROP TABLE IF EXISTS user_settings;
DROP TABLE IF EXISTS client_settings;
DROP TABLE IF EXISTS clients;
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS accounts;
-- +goose StatementEnd
