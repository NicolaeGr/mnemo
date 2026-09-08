-- Phase 2 tables: created now, untouched by phase 1 code. See STRATEGY Part 12.
CREATE TYPE share_status AS ENUM ('pending','accepted','declined','revoked');
CREATE TYPE contact_share_status AS ENUM ('pending','linked','decoupled','declined','revoked');

CREATE TABLE groups (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    created_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE group_members (
    group_id  BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    is_admin  BOOLEAN NOT NULL DEFAULT false,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);
CREATE TABLE book_shares (
    id           BIGSERIAL PRIMARY KEY,
    book_id      BIGINT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    grantee_kind TEXT NOT NULL CHECK (grantee_kind IN ('user','group')),
    grantee_id   BIGINT NOT NULL,
    status       share_status NOT NULL DEFAULT 'pending',
    invited_by   BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    responded_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (book_id, grantee_kind, grantee_id)
);
CREATE TABLE contact_shares (
    id                   BIGSERIAL PRIMARY KEY,
    source_contact_id    BIGINT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    recipient_contact_id BIGINT REFERENCES contacts(id) ON DELETE SET NULL,
    shared_by_user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    shared_with_user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status               contact_share_status NOT NULL DEFAULT 'pending',
    field_whitelist      JSONB NOT NULL DEFAULT '["FN","TEL"]',
    field_overrides      JSONB NOT NULL DEFAULT '{}',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    responded_at         TIMESTAMPTZ
);
CREATE INDEX idx_contact_shares_source    ON contact_shares(source_contact_id);
CREATE INDEX idx_contact_shares_recipient ON contact_shares(shared_with_user_id);
