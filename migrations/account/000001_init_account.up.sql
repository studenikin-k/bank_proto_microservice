CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE accounts (
                          id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                          user_id UUID NOT NULL,
                          balance DECIMAL(15,2) NOT NULL DEFAULT 100.00,
                          status TEXT NOT NULL DEFAULT 'active',
                          created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_accounts_user_id ON accounts(user_id);

-- Системный счёт банка для сбора комиссий
INSERT INTO accounts (id, user_id, balance, status)
VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000000', 0.00, 'active')
    ON CONFLICT (id) DO NOTHING;