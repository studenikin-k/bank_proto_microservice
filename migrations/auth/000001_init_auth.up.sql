CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE users (
                       id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                       name TEXT UNIQUE NOT NULL,
                       password_hash TEXT NOT NULL,
                       created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_users_name ON users(name);

-- Системный пользователь
INSERT INTO users (id, name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000000', 'SYSTEM', 'none')
    ON CONFLICT (id) DO NOTHING;