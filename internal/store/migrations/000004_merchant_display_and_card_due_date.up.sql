ALTER TABLE transactions
    ADD COLUMN merchant_display TEXT NOT NULL DEFAULT '';

ALTER TABLE credit_terms
    ADD COLUMN next_payment_due_date TEXT CHECK (
        next_payment_due_date IS NULL OR (
            length(next_payment_due_date) = 10 AND
            next_payment_due_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
        )
    );
