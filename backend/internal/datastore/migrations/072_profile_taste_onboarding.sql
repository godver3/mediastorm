-- +goose Up
CREATE TABLE profile_taste_onboarding (
    profile_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('completed', 'skipped')),
    source TEXT NOT NULL CHECK (source IN ('', 'manual', 'history')),
    interests JSONB NOT NULL DEFAULT '[]',
    updated_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE profile_taste_onboarding;
