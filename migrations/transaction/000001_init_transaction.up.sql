CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE transactions (
                              id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
                              type TEXT NOT NULL CHECK (type IN ('transfer', 'payment')),
                              from_account_id UUID NOT NULL,
                              to_account_id UUID NOT NULL,
                              amount DECIMAL(15,2) NOT NULL,
                              fee_percent SMALLINT NOT NULL CHECK (fee_percent IN (1,3)),
                              fee_amount DECIMAL(15,2) NOT NULL,
                              total_debit DECIMAL(15,2) NOT NULL,
                              fee_account_id UUID NOT NULL,
                              status TEXT NOT NULL DEFAULT 'pending',
                              created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_tx_from ON transactions(from_account_id);
CREATE INDEX idx_tx_to ON transactions(to_account_id);
CREATE INDEX idx_tx_created ON transactions(created_at);