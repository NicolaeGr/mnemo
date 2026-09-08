-- Shared extensions and the principal tier enum.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TYPE principal_tier AS ENUM ('primary', 'secondary', 'archived');
