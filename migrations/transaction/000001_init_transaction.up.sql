-- Переводы и платежи. Все денежные суммы — BIGINT в копейках.
-- Жизненный цикл записи (сага): pending -> completed | failed.
CREATE TABLE transactions (
    id              UUID PRIMARY KEY,            -- он же transfer_id в Account Service
    user_id         UUID NOT NULL,               -- инициатор операции
    idempotency_key TEXT NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('transfer', 'payment')),
    from_account_id TEXT NOT NULL,
    to_account_id   TEXT NOT NULL,
    amount          BIGINT NOT NULL CHECK (amount > 0),
    fee_percent     SMALLINT NOT NULL CHECK (fee_percent IN (1, 3)),
    fee_amount      BIGINT NOT NULL CHECK (fee_amount >= 0),
    total_debit     BIGINT NOT NULL,
    fee_account_id  TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed')),
    failure_reason  TEXT,
    failure_message TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT transactions_idempotency_key UNIQUE (user_id, idempotency_key),
    CONSTRAINT transactions_total_debit CHECK (total_debit = amount + fee_amount)
);

CREATE INDEX idx_tx_from ON transactions (from_account_id, created_at DESC);
CREATE INDEX idx_tx_to ON transactions (to_account_id, created_at DESC);
-- Recovery-воркер ищет зависшие pending-записи.
CREATE INDEX idx_tx_pending ON transactions (created_at) WHERE status = 'pending';
