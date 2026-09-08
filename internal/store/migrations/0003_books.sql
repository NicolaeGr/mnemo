-- Books = tags, never CardDAV collections. One per-user "all" book is_system.
CREATE TABLE books (
    id            BIGSERIAL PRIMARY KEY,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slug          TEXT NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$'),
    display_name  TEXT NOT NULL,
    description   TEXT,
    sort_order    INT NOT NULL DEFAULT 100,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    is_system     BOOLEAN NOT NULL DEFAULT false,
    synced_tiers  principal_tier[] NOT NULL DEFAULT ARRAY['primary','secondary']::principal_tier[],
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_user_id, slug),
    CONSTRAINT chk_system_slug CHECK (NOT is_system OR slug = 'all')
);
CREATE INDEX idx_books_owner ON books(owner_user_id);
CREATE UNIQUE INDEX idx_books_system_per_owner
    ON books(owner_user_id) WHERE is_system;
