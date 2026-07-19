CREATE TABLE entities (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('personal', 'business')),
    name TEXT NOT NULL CHECK (name <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (kind, name)
) STRICT;

CREATE TABLE provider_items (
    id INTEGER PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider <> ''),
    item_id TEXT NOT NULL CHECK (item_id <> ''),
    institution TEXT NOT NULL DEFAULT '',
    access_token_enc BLOB NOT NULL CHECK (length(access_token_enc) > 0),
    status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'login_required', 'error')),
    sync_cursor TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (provider, item_id)
) STRICT;

CREATE TABLE accounts (
    id INTEGER PRIMARY KEY,
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE RESTRICT,
    provider_item_id INTEGER REFERENCES provider_items(id) ON DELETE RESTRICT,
    type TEXT NOT NULL CHECK (type IN ('checking', 'savings', 'credit_card', 'loan', 'investment', 'asset')),
    name TEXT NOT NULL CHECK (name <> ''),
    institution TEXT NOT NULL DEFAULT '',
    mask TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL CHECK (provider <> ''),
    provider_account_id TEXT NOT NULL CHECK (provider_account_id <> ''),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency <> ''),
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (provider, provider_account_id),
    UNIQUE (id, entity_id)
) STRICT;

CREATE INDEX accounts_entity_idx ON accounts (entity_id, is_active);
CREATE INDEX accounts_provider_item_idx ON accounts (provider_item_id);

CREATE TABLE categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (name <> ''),
    parent_id INTEGER REFERENCES categories(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('income', 'expense', 'transfer', 'ignore')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (parent_id IS NULL OR parent_id <> id)
) STRICT;

CREATE UNIQUE INDEX categories_root_name_uq ON categories (name) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX categories_child_name_uq ON categories (parent_id, name) WHERE parent_id IS NOT NULL;

CREATE TABLE category_mappings (
    id INTEGER PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider <> ''),
    source_category TEXT NOT NULL CHECK (source_category <> ''),
    category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (provider, source_category)
) STRICT;

CREATE TABLE entity_rules (
    id INTEGER PRIMARY KEY,
    priority INTEGER NOT NULL CHECK (priority >= 0),
    account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    category_id INTEGER REFERENCES categories(id) ON DELETE CASCADE,
    merchant_pattern TEXT,
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (account_id IS NOT NULL OR category_id IS NOT NULL OR merchant_pattern IS NOT NULL),
    CHECK (merchant_pattern IS NULL OR merchant_pattern <> ''),
    UNIQUE (priority)
) STRICT;

CREATE TABLE recurring_items (
    id INTEGER PRIMARY KEY,
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (name <> ''),
    kind TEXT NOT NULL CHECK (kind IN ('subscription', 'bill', 'income')),
    cadence TEXT NOT NULL CHECK (cadence <> ''),
    expected_cents INTEGER NOT NULL,
    next_expected_date TEXT,
    drift_pct REAL NOT NULL DEFAULT 0,
    source TEXT NOT NULL CHECK (source IN ('detected', 'manual')),
    is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (
        next_expected_date IS NULL OR (
            length(next_expected_date) = 10 AND
            next_expected_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
        )
    )
) STRICT;

CREATE INDEX recurring_items_entity_idx ON recurring_items (entity_id, kind, is_active);

CREATE TABLE transactions (
    id INTEGER PRIMARY KEY,
    account_id INTEGER NOT NULL,
    entity_id INTEGER NOT NULL,
    date TEXT NOT NULL CHECK (
        length(date) = 10 AND
        date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
    ),
    amount_cents INTEGER NOT NULL,
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency <> ''),
    merchant_raw TEXT NOT NULL DEFAULT '',
    merchant_norm TEXT NOT NULL DEFAULT '',
    category_id INTEGER REFERENCES categories(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'posted')),
    tags TEXT NOT NULL DEFAULT '[]',
    notes TEXT NOT NULL DEFAULT '',
    recurring_id INTEGER REFERENCES recurring_items(id) ON DELETE SET NULL,
    is_transfer INTEGER NOT NULL DEFAULT 0 CHECK (is_transfer IN (0, 1)),
    excluded INTEGER NOT NULL DEFAULT 0 CHECK (excluded IN (0, 1)),
    dedup_hash TEXT NOT NULL CHECK (dedup_hash <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (account_id, entity_id) REFERENCES accounts(id, entity_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX transactions_entity_date_idx ON transactions (entity_id, date DESC);
CREATE INDEX transactions_account_date_idx ON transactions (account_id, date DESC);
CREATE INDEX transactions_category_date_idx ON transactions (category_id, date DESC);
CREATE INDEX transactions_dedup_idx ON transactions (account_id, dedup_hash);
CREATE INDEX transactions_fuzzy_pending_idx
    ON transactions (account_id, amount_cents, merchant_norm, date);

CREATE TABLE txn_provider_refs (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider <> ''),
    provider_txn_id TEXT NOT NULL CHECK (provider_txn_id <> ''),
    pending_txn_id TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (pending_txn_id IS NULL OR pending_txn_id <> ''),
    UNIQUE (provider, provider_txn_id)
) STRICT;

CREATE INDEX txn_provider_refs_transaction_idx ON txn_provider_refs (transaction_id);
CREATE INDEX txn_provider_refs_pending_idx
    ON txn_provider_refs (provider, pending_txn_id)
    WHERE pending_txn_id IS NOT NULL;

CREATE TABLE credit_terms (
    account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    limit_cents INTEGER,
    apr REAL,
    statement_day INTEGER CHECK (statement_day BETWEEN 1 AND 31),
    due_day INTEGER CHECK (due_day BETWEEN 1 AND 31),
    min_payment_cents INTEGER,
    last_statement_cents INTEGER,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE TABLE loan_terms (
    account_id INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    apr REAL,
    min_payment_cents INTEGER,
    origination_cents INTEGER,
    maturity_date TEXT,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (
        maturity_date IS NULL OR (
            length(maturity_date) = 10 AND
            maturity_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
        )
    )
) STRICT;

CREATE TABLE balance_snapshots (
    id INTEGER PRIMARY KEY,
    account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    date TEXT NOT NULL CHECK (
        length(date) = 10 AND
        date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
    ),
    current_cents INTEGER NOT NULL,
    available_cents INTEGER,
    limit_cents INTEGER,
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency <> ''),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (account_id, date)
) STRICT;

CREATE INDEX balance_snapshots_date_idx ON balance_snapshots (date DESC);

CREATE TABLE net_worth_snapshots (
    id INTEGER PRIMARY KEY,
    entity_id INTEGER REFERENCES entities(id) ON DELETE CASCADE,
    date TEXT NOT NULL CHECK (
        length(date) = 10 AND
        date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'
    ),
    assets_cents INTEGER NOT NULL,
    liabilities_cents INTEGER NOT NULL,
    net_cents INTEGER NOT NULL,
    checking_cents INTEGER NOT NULL DEFAULT 0,
    savings_cents INTEGER NOT NULL DEFAULT 0,
    credit_card_cents INTEGER NOT NULL DEFAULT 0,
    loan_cents INTEGER NOT NULL DEFAULT 0,
    investment_cents INTEGER NOT NULL DEFAULT 0,
    asset_cents INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE UNIQUE INDEX net_worth_snapshots_entity_date_uq
    ON net_worth_snapshots (entity_id, date)
    WHERE entity_id IS NOT NULL;
CREATE UNIQUE INDEX net_worth_snapshots_combined_date_uq
    ON net_worth_snapshots (date)
    WHERE entity_id IS NULL;

CREATE TABLE budgets (
    id INTEGER PRIMARY KEY,
    entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    month TEXT NOT NULL CHECK (
        length(month) = 7 AND
        month GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]'
    ),
    target_cents INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (entity_id, category_id, month)
) STRICT;

CREATE TABLE import_runs (
    id INTEGER PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider <> ''),
    provider_item_id INTEGER REFERENCES provider_items(id) ON DELETE SET NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    cursor_before TEXT NOT NULL DEFAULT '',
    cursor_after TEXT NOT NULL DEFAULT '',
    accounts_seen INTEGER NOT NULL DEFAULT 0 CHECK (accounts_seen >= 0),
    transactions_added INTEGER NOT NULL DEFAULT 0 CHECK (transactions_added >= 0),
    transactions_modified INTEGER NOT NULL DEFAULT 0 CHECK (transactions_modified >= 0),
    transactions_removed INTEGER NOT NULL DEFAULT 0 CHECK (transactions_removed >= 0),
    error_detail TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    completed_at TEXT
) STRICT;

CREATE INDEX import_runs_provider_started_idx ON import_runs (provider, started_at DESC);

INSERT INTO categories (id, name, kind) VALUES
    (1,  'Income',                    'income'),
    (2,  'Transfers In',              'transfer'),
    (3,  'Transfers Out',             'transfer'),
    (4,  'Loan Payments',             'transfer'),
    (5,  'Bank Fees',                 'expense'),
    (6,  'Entertainment',             'expense'),
    (7,  'Food and Drink',            'expense'),
    (8,  'General Merchandise',       'expense'),
    (9,  'Home Improvement',          'expense'),
    (10, 'Medical',                   'expense'),
    (11, 'Personal Care',             'expense'),
    (12, 'General Services',          'expense'),
    (13, 'Government and Non-Profit', 'expense'),
    (14, 'Transportation',            'expense'),
    (15, 'Travel',                    'expense'),
    (16, 'Rent and Utilities',        'expense');

INSERT INTO category_mappings (provider, source_category, category_id) VALUES
    ('plaid', 'INCOME',                    1),
    ('plaid', 'TRANSFER_IN',               2),
    ('plaid', 'TRANSFER_OUT',              3),
    ('plaid', 'LOAN_PAYMENTS',             4),
    ('plaid', 'BANK_FEES',                 5),
    ('plaid', 'ENTERTAINMENT',             6),
    ('plaid', 'FOOD_AND_DRINK',            7),
    ('plaid', 'GENERAL_MERCHANDISE',       8),
    ('plaid', 'HOME_IMPROVEMENT',          9),
    ('plaid', 'MEDICAL',                  10),
    ('plaid', 'PERSONAL_CARE',            11),
    ('plaid', 'GENERAL_SERVICES',         12),
    ('plaid', 'GOVERNMENT_AND_NON_PROFIT',13),
    ('plaid', 'TRANSPORTATION',           14),
    ('plaid', 'TRAVEL',                   15),
    ('plaid', 'RENT_AND_UTILITIES',       16);
