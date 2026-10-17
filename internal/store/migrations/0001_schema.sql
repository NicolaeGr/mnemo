-- Consolidated schema. Replaces the incremental migrations 0001 through 0014.
-- Books are tags, never CardDAV collections. Contacts may be untagged.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TYPE principal_tier AS ENUM ('primary', 'secondary', 'archived');

CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      CITEXT NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9-]{1,63}$'),
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE books (
    id            BIGSERIAL PRIMARY KEY,
    owner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slug          TEXT NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$'),
    display_name  TEXT NOT NULL,
    description   TEXT,
    sort_order    INT NOT NULL DEFAULT 100,
    is_active     BOOLEAN NOT NULL DEFAULT true,
    is_approved   BOOLEAN NOT NULL DEFAULT true, -- FALSE when auto-created from a DAV client's CATEGORIES
    synced_tiers  principal_tier[] NOT NULL DEFAULT ARRAY['primary','secondary']::principal_tier[],
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_user_id, slug)
);
CREATE INDEX idx_books_owner ON books(owner_user_id);

CREATE TABLE user_settings (
    user_id         BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    default_book_id BIGINT REFERENCES books(id) ON DELETE SET NULL,
    misc            JSONB NOT NULL DEFAULT '{}', -- UI prefs (accent color, columns, etc.)
    network         JSONB NOT NULL DEFAULT '[]', -- Array of user IDs you've interacted with
    notif_prefs     JSONB NOT NULL DEFAULT '{"site": true, "mail": false}',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
CREATE INDEX idx_principals_user ON principals(user_id);
CREATE INDEX idx_principals_token ON principals(token_hash) WHERE token_hash IS NOT NULL;

CREATE TABLE principal_book_overrides (
    principal_id BIGINT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    book_id      BIGINT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    enabled      BOOLEAN NOT NULL,
    PRIMARY KEY (principal_id, book_id)
);

CREATE TABLE contact_shares (
    id                  BIGSERIAL PRIMARY KEY,
    shared_by_user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    shared_with_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected', 'decoupled')),
    sync_mode           TEXT NOT NULL DEFAULT 'static' CHECK (sync_mode IN ('static', 'synced')),
    payload             JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    responded_at        TIMESTAMPTZ
);
CREATE INDEX idx_contact_shares_target ON contact_shares(shared_with_user_id);

CREATE TABLE contacts (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename              TEXT NOT NULL CHECK (filename ~ '^[A-Za-z0-9][A-Za-z0-9._-]*\.vcf$'),
    uid                   TEXT NOT NULL,
    vcard_text            TEXT NOT NULL,
    search_meta           JSONB NOT NULL DEFAULT '{}',
    etag                  TEXT NOT NULL,
    modified_by           BIGINT REFERENCES users(id),
    shared_by_user_id     BIGINT REFERENCES users(id) ON DELETE SET NULL, -- provenance for static shares
    deleted_at            TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, filename)
);
CREATE UNIQUE INDEX idx_contacts_live_uid ON contacts(user_id, uid) WHERE deleted_at IS NULL;
CREATE INDEX idx_contacts_user ON contacts(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_contacts_fn_trgm ON contacts USING gin ((search_meta->>'fn') gin_trgm_ops);
CREATE INDEX idx_contacts_n_trgm ON contacts USING gin ((search_meta->>'n') gin_trgm_ops);
CREATE INDEX idx_contacts_org_trgm ON contacts USING gin ((search_meta->>'org') gin_trgm_ops);

CREATE TABLE contact_books (
    contact_id BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    book_id    BIGINT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    tagged_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (contact_id, book_id)
);

CREATE TABLE contact_changes (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename    TEXT NOT NULL,
    change_type TEXT NOT NULL CHECK (change_type IN ('put','delete')),
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_contact_changes_user ON contact_changes(user_id, id);

CREATE TABLE user_events (
    id             BIGSERIAL PRIMARY KEY,
    target_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN (
                       'force_resync',
                       'share_book_granted', 'contact_share_granted',
                       'group_invite', 'network_request'
                   )),
    payload        JSONB NOT NULL,
    initiated_by   BIGINT REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at  TIMESTAMPTZ,
    attempts       INT NOT NULL DEFAULT 0,
    dead_letter    BOOLEAN NOT NULL DEFAULT FALSE,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_until   TIMESTAMPTZ
);
CREATE INDEX idx_user_events_pending ON user_events(id) WHERE dispatched_at IS NULL;

CREATE TABLE groups (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    created_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE group_members (
    group_id  BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      TEXT NOT NULL DEFAULT 'editor' CHECK (role IN ('owner', 'editor')),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE book_shares (
    id           BIGSERIAL PRIMARY KEY,
    book_id      BIGINT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    grantee_kind TEXT NOT NULL CHECK (grantee_kind IN ('user','group')),
    grantee_id   BIGINT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked')),
    invited_by   BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE invitations (
    id              BIGSERIAL PRIMARY KEY,
    token           TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL CHECK (kind IN ('contact_share', 'group_invite', 'network_request')),
    resource_id     BIGINT NOT NULL,
    inviter_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invitee_user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Resolve the circular reference to contact_shares.
ALTER TABLE contacts
    ADD COLUMN linked_source_share_id BIGINT REFERENCES contact_shares(id) ON DELETE SET NULL;
