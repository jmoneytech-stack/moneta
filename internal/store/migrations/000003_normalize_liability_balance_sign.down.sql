-- The up migration intentionally changes no data because Plaid's raw
-- liability sign already matches Moneta's canonical convention. There is
-- nothing to reverse, and genuine negative credit balances remain unchanged.
SELECT 1;
