-- Double-entry ledger schema.
--
-- Design notes:
--   * Money is stored as BIGINT minor units. Never floating point.
--   * accounts.balance is a cached running total, only ever mutated inside the
--     same transaction that inserts the matching postings, so it can never
--     drift from SUM(postings).
--   * transfers.idempotency_key is UNIQUE: a retried request with the same key
--     resolves to the original transfer instead of moving money twice.
--   * postings are append-only. There is no UPDATE or DELETE path for them.

CREATE TABLE accounts (
    id              UUID PRIMARY KEY,
    name            TEXT        NOT NULL,
    currency        CHAR(3)     NOT NULL,
    balance         BIGINT      NOT NULL DEFAULT 0,
    allow_overdraft BOOLEAN     NOT NULL DEFAULT FALSE,
    version         BIGINT      NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_currency_upper CHECK (currency = upper(currency)),
    CONSTRAINT accounts_balance_nonneg CHECK (allow_overdraft OR balance >= 0)
);

CREATE TABLE transfers (
    id               UUID PRIMARY KEY,
    idempotency_key  TEXT        NOT NULL,
    debit_account_id  UUID       NOT NULL REFERENCES accounts (id),
    credit_account_id UUID       NOT NULL REFERENCES accounts (id),
    amount           BIGINT      NOT NULL,
    currency         CHAR(3)     NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'posted',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT transfers_amount_positive CHECK (amount > 0),
    CONSTRAINT transfers_distinct_accounts CHECK (debit_account_id <> credit_account_id)
);

CREATE UNIQUE INDEX transfers_idempotency_key_uniq ON transfers (idempotency_key);

CREATE TABLE postings (
    id            BIGSERIAL PRIMARY KEY,
    transfer_id   UUID        NOT NULL REFERENCES transfers (id),
    account_id    UUID        NOT NULL REFERENCES accounts (id),
    direction     TEXT        NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount        BIGINT      NOT NULL CHECK (amount > 0),
    balance_after BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX postings_account_id_id_idx ON postings (account_id, id DESC);
CREATE INDEX postings_transfer_id_idx ON postings (transfer_id);
