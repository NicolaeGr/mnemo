-- Contacts (soft-deleted; purged later by the jobs loop). search_meta shape is
-- frozen by the vcard module; the fn/tel_norm indexes are tuned to it.
CREATE TABLE contacts (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename    TEXT NOT NULL
                CONSTRAINT chk_contacts_filename
                CHECK (filename ~ '^[A-Za-z0-9][A-Za-z0-9._-]*\.vcf$'),
    uid         TEXT NOT NULL,
    vcard_text  TEXT NOT NULL,
    search_meta JSONB NOT NULL DEFAULT '{}',
    etag        TEXT NOT NULL,
    modified_by BIGINT REFERENCES users(id),
    deleted_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, filename)
);
CREATE UNIQUE INDEX idx_contacts_live_uid
    ON contacts(user_id, uid) WHERE deleted_at IS NULL;
CREATE INDEX idx_contacts_user     ON contacts(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_contacts_fn_trgm  ON contacts USING gin ((search_meta->>'fn') gin_trgm_ops);
CREATE INDEX idx_contacts_tel_gin  ON contacts USING gin ((search_meta->'tel_norm') jsonb_path_ops);
