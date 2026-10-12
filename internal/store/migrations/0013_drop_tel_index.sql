-- Phone search matches normalized numbers element by element in search.go
-- (jsonb_array_elements_text). The jsonb_path_ops GIN index cannot serve that
-- query because it indexed search_meta->'tel_norm' while the query reads
-- ->>'tel_norm'. Drop it. Phone search becomes a scan until a dedicated
-- normalized-phone structure needs an index.
DROP INDEX IF EXISTS idx_contacts_tel_gin;
