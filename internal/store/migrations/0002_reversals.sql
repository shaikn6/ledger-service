-- Transfer reversals.
--
-- A reversal is itself a normal transfer that moves the same amount back in the
-- opposite direction. It carries reverses_transfer_id so the pair is linkable,
-- and the original transfer's status flips to 'reversed'. A transfer can be
-- reversed at most once; the partial unique index below enforces that even
-- under concurrency.

ALTER TABLE transfers
    ADD COLUMN reverses_transfer_id UUID NULL REFERENCES transfers (id);

ALTER TABLE transfers
    DROP CONSTRAINT IF EXISTS transfers_status_check;

ALTER TABLE transfers
    ADD CONSTRAINT transfers_status_check
    CHECK (status IN ('posted', 'reversed'));

CREATE UNIQUE INDEX transfers_one_reversal_per_original
    ON transfers (reverses_transfer_id)
    WHERE reverses_transfer_id IS NOT NULL;
