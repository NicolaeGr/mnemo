package db

const Schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS server_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    is_active INTEGER NOT NULL DEFAULT 1,         
    is_temporary_password INTEGER NOT NULL DEFAULT 0, 
    password_reset_token TEXT DEFAULT NULL,
    password_reset_expires_at DATETIME DEFAULT NULL,
    default_address_book_id TEXT DEFAULT NULL REFERENCES address_books(id) ON DELETE SET NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_invites (
    token TEXT PRIMARY KEY,                        
    created_by TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    used_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS address_books (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    display_name TEXT NOT NULL,
    UNIQUE(owner_id, slug)
);

CREATE TABLE IF NOT EXISTS address_book_permissions (
    address_book_id TEXT REFERENCES address_books(id) ON DELETE CASCADE,
    user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
    access_level TEXT NOT NULL, -- 'read' or 'read-write'
    PRIMARY KEY (address_book_id, user_id)
);

CREATE TABLE IF NOT EXISTS address_book_subscriptions (
    user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
    address_book_id TEXT REFERENCES address_books(id) ON DELETE CASCADE,
    sync_enabled INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (user_id, address_book_id)
);

CREATE TABLE IF NOT EXISTS user_device_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_name TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    last_used_at DATETIME DEFAULT NULL,
    last_seen_ip TEXT DEFAULT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS contacts (
    id TEXT PRIMARY KEY,
    address_book_id TEXT REFERENCES address_books(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    vcard_text TEXT NOT NULL,
    etag TEXT NOT NULL,
    last_modified DATETIME DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL
);

CREATE INDEX IF NOT EXISTS idx_address_books_owner ON address_books(owner_id);
CREATE INDEX IF NOT EXISTS idx_permissions_user ON address_book_permissions(user_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user ON address_book_subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_device_tokens_user ON user_device_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_contacts_address_book ON contacts(address_book_id);
CREATE INDEX IF NOT EXISTS idx_contacts_search ON contacts(address_book_id, display_name) WHERE deleted_at IS NULL;
`

const SeedDefaultSettings = `
INSERT OR IGNORE INTO server_settings (key, value) VALUES 
('log_level', '"info"'),
('trust_proxy_headers', 'false'),
('password_reset_timeout_minutes', '15'),
('ip_allow_list', '[]');
`
