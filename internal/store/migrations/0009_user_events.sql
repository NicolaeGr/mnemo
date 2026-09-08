-- Event outbox for cross-user effects (phase 1 uses force_resync only).
CREATE TABLE user_events (
    id             BIGSERIAL PRIMARY KEY,
    target_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN (
                       'force_resync',
                       'share_book_granted', 'share_book_revoked', 'share_book_updated',
                       'contact_share_granted', 'contact_share_revoked', 'contact_share_updated',
                       'group_membership_changed'
                   )),
    payload        JSONB NOT NULL,
    initiated_by   BIGINT REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at  TIMESTAMPTZ
);
CREATE INDEX idx_user_events_pending ON user_events(id) WHERE dispatched_at IS NULL;
CREATE INDEX idx_user_events_target  ON user_events(target_user_id, id);
