CREATE TABLE IF NOT EXISTS athletes (
    athlete_id    BIGINT PRIMARY KEY,
    refresh_token TEXT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
