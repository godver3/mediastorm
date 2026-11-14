-- +goose Up
CREATE TABLE profile_title_ratings (
    profile_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title_key TEXT NOT NULL,
    aliases TEXT[] NOT NULL,
    title JSONB NOT NULL,
    rating TEXT NOT NULL CHECK (rating IN ('like', 'love', 'dislike')),
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (profile_id, title_key)
);
CREATE INDEX idx_profile_title_ratings_aliases ON profile_title_ratings USING GIN(aliases);

-- +goose Down
DROP TABLE profile_title_ratings;
