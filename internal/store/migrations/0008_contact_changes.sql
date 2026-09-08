-- Per-user change stream (sync-collection material). Keyed by user because the
-- only CardDAV-visible collection is the per-user union.
CREATE TABLE contact_changes (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename    TEXT NOT NULL,
    change_type TEXT NOT NULL CHECK (change_type IN ('put','delete')),
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_contact_changes_user ON contact_changes(user_id, id);
