-- Retry bookkeeping for the dispatcher outbox: a failed event backs off and,
-- past max attempts, dead-letters so the poller stops claiming it.
ALTER TABLE user_events
    ADD COLUMN attempts       INT NOT NULL DEFAULT 0,
    ADD COLUMN dead_letter    BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();
