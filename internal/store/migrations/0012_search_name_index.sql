-- Name and org search: search_meta now carries an "n" key (the structured name
-- joined), and search matches fn/n/org, so index them like fn already is.
CREATE INDEX idx_contacts_n_trgm   ON contacts USING gin ((search_meta->>'n') gin_trgm_ops);
CREATE INDEX idx_contacts_org_trgm ON contacts USING gin ((search_meta->>'org') gin_trgm_ops);
