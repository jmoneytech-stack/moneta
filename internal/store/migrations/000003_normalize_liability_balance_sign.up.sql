-- Plaid reports credit-card and loan current balances using Moneta's
-- canonical liability convention: positive means owed and negative means the
-- institution owes the user. Existing negative liability snapshots are
-- therefore genuine credits and must be preserved. No data update is needed.
SELECT 1;
