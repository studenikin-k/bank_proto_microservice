CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Системный пользователь — владелец системного счёта банка.
INSERT INTO users (id, name, password_hash)
VALUES ('00000000-0000-0000-0000-000000000000', 'SYSTEM', 'none')
ON CONFLICT (id) DO NOTHING;
