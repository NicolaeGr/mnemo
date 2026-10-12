-- Dispatcher claim lease. Claiming an event stamps leased_until in the same
-- statement that selects it (FOR UPDATE SKIP LOCKED + UPDATE), so with more than
-- one dispatcher worker no event can be claimed twice. The lease expires on its
-- own if a worker dies mid-apply; retry bookkeeping clears it explicitly.
ALTER TABLE user_events
    ADD COLUMN leased_until TIMESTAMPTZ;
