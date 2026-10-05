-- Счета. Все денежные суммы — BIGINT в копейках.
CREATE TABLE accounts (
    id              TEXT PRIMARY KEY,           -- "13" + 12 случайных цифр; системный счёт — UUID
    user_id         UUID NOT NULL,
    balance         BIGINT NOT NULL CHECK (balance >= 0),
    opening_balance BIGINT NOT NULL,            -- сумма, выданная при открытии счёта (нужна для сверки)
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_accounts_user_id ON accounts (user_id);

-- Системный счёт банка: на него фоновая задача переносит собранные комиссии.
INSERT INTO accounts (id, user_id, balance, opening_balance, status)
VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000000', 0, 0, 'active')
ON CONFLICT (id) DO NOTHING;

-- Журнал шага саги на стороне Account Service (ключ идемпотентности — transfer_id).
-- У каждого transfer_id ровно один окончательный исход:
--   applied — перевод применён; повторный ApplyTransfer с тем же id ничего не делает;
--   aborted — перевод отклонён (reason) или отменён recovery-воркером; применить его уже нельзя.
-- Комиссия копится здесь (только INSERT) и периодически переносится на системный счёт,
-- поэтому переводы не конкурируют за одну «горячую» строку системного счёта.
CREATE TABLE applied_transfers (
    transfer_id     UUID PRIMARY KEY,
    status          TEXT NOT NULL CHECK (status IN ('applied', 'aborted')),
    reason          TEXT,                       -- причина для status = 'aborted'
    from_account_id TEXT,
    to_account_id   TEXT,
    amount          BIGINT NOT NULL DEFAULT 0,
    fee             BIGINT NOT NULL DEFAULT 0,
    fee_swept       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_applied_transfers_unswept_fee ON applied_transfers (created_at)
    WHERE status = 'applied' AND fee > 0 AND NOT fee_swept;
