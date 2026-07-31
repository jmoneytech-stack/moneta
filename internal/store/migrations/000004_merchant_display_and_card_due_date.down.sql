ALTER TABLE credit_terms
    DROP COLUMN next_payment_due_date;

ALTER TABLE transactions
    DROP COLUMN merchant_display;
