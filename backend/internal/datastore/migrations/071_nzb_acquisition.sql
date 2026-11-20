-- +goose Up
CREATE TABLE nzb_download_attempts (
    title_key TEXT NOT NULL,
    provider TEXT NOT NULL,
    attempted_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX nzb_download_attempts_title_time ON nzb_download_attempts(title_key, attempted_at);
CREATE INDEX nzb_download_attempts_time ON nzb_download_attempts(attempted_at);

CREATE TABLE indexer_cooldowns (
    provider TEXT PRIMARY KEY,
    until_at TIMESTAMPTZ NOT NULL,
    failures INTEGER NOT NULL
);

-- +goose Down
DROP TABLE indexer_cooldowns;
DROP TABLE nzb_download_attempts;
