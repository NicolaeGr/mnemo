-- Users: login + password. username doubles as the CardDAV URL component.
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      CITEXT NOT NULL UNIQUE
                  CONSTRAINT chk_username_shape
                  CHECK (username ~ '^[a-z0-9][a-z0-9-]{1,63}$'),
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
