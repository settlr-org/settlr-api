-- +goose Up
-- Personal expense tracker + NPR defaults
ALTER TABLE users ALTER COLUMN default_currency SET DEFAULT 'NPR';
ALTER TABLE groups ALTER COLUMN currency SET DEFAULT 'NPR';

-- Backfill existing USD to NPR where user never changed
UPDATE users SET default_currency='NPR' WHERE default_currency='USD';
UPDATE groups SET currency='NPR' WHERE currency='USD';

CREATE TABLE personal_expenses (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    description   TEXT NOT NULL,
    amount        BIGINT NOT NULL CHECK (amount > 0),
    currency      CHAR(3) NOT NULL DEFAULT 'NPR',
    category_id   UUID REFERENCES categories(id) ON DELETE SET NULL,
    notes         TEXT NOT NULL DEFAULT '',
    expense_date  DATE NOT NULL DEFAULT CURRENT_DATE,
    -- optional conversion to user's default_currency
    base_currency CHAR(3),
    exchange_rate NUMERIC(12,6),
    base_amount   BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_personal_expenses_user_date ON personal_expenses (user_id, expense_date DESC);
CREATE INDEX idx_personal_expenses_category ON personal_expenses (category_id);
CREATE INDEX idx_personal_expenses_user_currency ON personal_expenses (user_id, currency);

-- +goose Down
DROP TABLE IF EXISTS personal_expenses;
ALTER TABLE users ALTER COLUMN default_currency SET DEFAULT 'USD';
ALTER TABLE groups ALTER COLUMN currency SET DEFAULT 'USD';
