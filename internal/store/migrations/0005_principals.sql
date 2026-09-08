-- Principals = devices/credentials. NULL token_hash => password auth (tier-all).
CREATE TABLE principals (
    id             BIGSERIAL PRIMARY KEY,
    user_id        BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tier           principal_tier NOT NULL DEFAULT 'primary',
    label          TEXT NOT NULL,
    token_hash     TEXT UNIQUE,
    sync_epoch     BIGINT NOT NULL DEFAULT 0,
    last_synced_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_principals_user   ON principals(user_id);
CREATE INDEX idx_principals_token  ON principals(token_hash)
    WHERE token_hash IS NOT NULL;

-- Per-principal opt-outs of otherwise-visible books.
CREATE TABLE principal_book_overrides (
    principal_id BIGINT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    book_id      BIGINT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    enabled      BOOLEAN NOT NULL,
    PRIMARY KEY (principal_id, book_id)
);
