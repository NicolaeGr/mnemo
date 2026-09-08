-- Typed 1:1 settings. Default book is wired to the system book at signup.
CREATE TABLE user_settings (
    user_id         BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    default_book_id BIGINT REFERENCES books(id) ON DELETE SET NULL,
    misc            JSONB NOT NULL DEFAULT '{}',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
