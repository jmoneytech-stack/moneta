package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

var (
	// ErrCursorChanged indicates that another sync advanced the Item cursor
	// before this batch could commit.
	ErrCursorChanged = errors.New("provider item sync cursor changed")
	// ErrProviderItemNotFound indicates that the target Item does not exist.
	ErrProviderItemNotFound = errors.New("provider item not found")
	// errAccountNotConnected marks the per-record case of a record
	// referencing an account that is not connected to this provider item.
	errAccountNotConnected = errors.New("account is not connected to this provider item")
)

// skipRecord signals that one record failed a validation invariant and must
// be skipped and recorded, not treated as a batch-fatal error. Infrastructure
// errors (DB failures, affected-rows mismatches, cursor conflicts) are
// returned as ordinary errors and still roll back the whole batch.
type skipRecord struct {
	rec canon.SkippedRecord
}

func (e skipRecord) Error() string { return "skip: " + e.rec.Detail }

// SyncTarget identifies the provider Item and default entity receiving a
// batch. Existing account entity assignments are preserved.
type SyncTarget struct {
	ProviderItemID  int64
	DefaultEntityID int64
	ExpectedCursor  string
}

// Ingestor atomically writes normalized provider batches.
type Ingestor struct {
	db *sql.DB
}

// NewIngestor returns an ingestion service backed by db.
func NewIngestor(db *sql.DB) *Ingestor {
	return &Ingestor{db: db}
}

// IngestResult summarizes one applied sync batch.
type IngestResult struct {
	// Skipped lists batch records dropped during ingest with stable,
	// machine-readable reasons. It is empty when nothing was skipped.
	Skipped []canon.SkippedRecord
}

// ApplySync writes a complete provider batch and advances its cursor in the
// same transaction. A row that fails a per-record validation invariant (an
// account with no name, a pending transaction carrying a pending id, a
// record referencing an account this Item never connected, a liability for
// an account type with no terms table) is skipped and reported in the
// result, so one bad record cannot wedge the Item cursor. Any infrastructure
// failure still rolls back the full batch.
func (i *Ingestor) ApplySync(
	ctx context.Context,
	target SyncTarget,
	batch *canon.SyncBatch,
) (*IngestResult, error) {
	if i == nil || i.db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if target.ProviderItemID <= 0 {
		return nil, fmt.Errorf("provider item id must be positive")
	}
	if target.DefaultEntityID <= 0 {
		return nil, fmt.Errorf("default entity id must be positive")
	}
	if batch == nil {
		return nil, fmt.Errorf("sync batch is required")
	}

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin sync transaction: %w", err)
	}
	defer tx.Rollback()

	writer, err := newSyncWriter(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	for index := range batch.Accounts {
		if err := writer.upsertAccount(ctx, batch.Accounts[index]); err != nil {
			if writer.recordSkip(err) {
				continue
			}
			return nil, fmt.Errorf("apply account %d: %w", index, err)
		}
	}
	for index := range batch.Added {
		if err := writer.upsertTransaction(ctx, batch.Added[index]); err != nil {
			if writer.recordSkip(err) {
				continue
			}
			return nil, fmt.Errorf("apply added transaction %d: %w", index, err)
		}
	}
	for index := range batch.Modified {
		if err := writer.upsertTransaction(ctx, batch.Modified[index]); err != nil {
			if writer.recordSkip(err) {
				continue
			}
			return nil, fmt.Errorf("apply modified transaction %d: %w", index, err)
		}
	}
	for index := range batch.Balances {
		if err := writer.upsertBalance(ctx, batch.Balances[index]); err != nil {
			if writer.recordSkip(err) {
				continue
			}
			return nil, fmt.Errorf("apply balance %d: %w", index, err)
		}
	}
	for index := range batch.Liabilities {
		if err := writer.upsertLiability(ctx, batch.Liabilities[index]); err != nil {
			if writer.recordSkip(err) {
				continue
			}
			return nil, fmt.Errorf("apply liability %d: %w", index, err)
		}
	}
	// Removals run after additions so a posted transaction can claim its
	// pending row before Plaid's removal of the pending provider ID is applied.
	for index, providerTransactionID := range batch.Removed {
		if err := writer.removeTransaction(ctx, providerTransactionID); err != nil {
			return nil, fmt.Errorf("apply removed transaction %d: %w", index, err)
		}
	}

	if err := writer.finish(ctx, batch); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit sync transaction: %w", err)
	}

	return &IngestResult{Skipped: writer.skipped}, nil
}

type syncWriter struct {
	tx              *sql.Tx
	provider        string
	providerItemID  int64
	defaultEntityID int64
	expectedCursor  string
	accounts        map[string]accountRecord
	categories      map[string]categoryRecord
	skipped         []canon.SkippedRecord
}

type accountRecord struct {
	id       int64
	entityID int64
	typeName canon.AccountType
}

type categoryRecord struct {
	id         int64
	found      bool
	isTransfer bool
}

func newSyncWriter(ctx context.Context, tx *sql.Tx, target SyncTarget) (*syncWriter, error) {
	var provider, cursor string
	err := tx.QueryRowContext(ctx, `
		SELECT provider, sync_cursor
		FROM provider_items
		WHERE id = ?
	`, target.ProviderItemID).Scan(&provider, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProviderItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load provider item: %w", err)
	}
	if cursor != target.ExpectedCursor {
		return nil, fmt.Errorf(
			"%w: expected %q, found %q",
			ErrCursorChanged,
			target.ExpectedCursor,
			cursor,
		)
	}

	var entityExists bool
	if err := tx.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM entities WHERE id = ?)",
		target.DefaultEntityID,
	).Scan(&entityExists); err != nil {
		return nil, fmt.Errorf("validate default entity: %w", err)
	}
	if !entityExists {
		return nil, fmt.Errorf("default entity %d does not exist", target.DefaultEntityID)
	}

	return &syncWriter{
		tx:              tx,
		provider:        provider,
		providerItemID:  target.ProviderItemID,
		defaultEntityID: target.DefaultEntityID,
		expectedCursor:  target.ExpectedCursor,
		accounts:        make(map[string]accountRecord),
		categories:      make(map[string]categoryRecord),
	}, nil
}

// recordSkip reports whether err is a per-record validation skip and, if
// so, records it. Any other error is left for the caller to treat as
// batch-fatal.
func (w *syncWriter) recordSkip(err error) bool {
	var skip skipRecord
	if errors.As(err, &skip) {
		w.skipped = append(w.skipped, skip.rec)
		return true
	}
	return false
}

func (w *syncWriter) upsertAccount(ctx context.Context, account canon.Account) error {
	if account.ProviderAccountID == "" {
		return fmt.Errorf("provider account id is required")
	}
	if account.Name == "" {
		return skipRecord{rec: canon.SkippedRecord{
			Kind:   canon.RecordKindAccount,
			ID:     account.ProviderAccountID,
			Reason: canon.SkipMalformedRecord,
			Detail: "missing account name",
		}}
	}
	if !validAccountType(account.Type) {
		return fmt.Errorf("unsupported account type %q", account.Type)
	}
	currency, err := canonicalCurrency(account.Currency)
	if err != nil {
		return err
	}

	result, err := w.tx.ExecContext(ctx, `
		INSERT INTO accounts (
			entity_id, provider_item_id, type, name, institution, mask,
			provider, provider_account_id, currency, is_active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT (provider, provider_account_id) DO UPDATE SET
			provider_item_id = excluded.provider_item_id,
			type = excluded.type,
			name = excluded.name,
			institution = CASE
				WHEN excluded.institution <> '' THEN excluded.institution
				ELSE accounts.institution
			END,
			mask = excluded.mask,
			currency = excluded.currency,
			is_active = 1,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE accounts.provider_item_id = excluded.provider_item_id
	`,
		w.defaultEntityID,
		w.providerItemID,
		account.Type,
		account.Name,
		account.Institution,
		account.Mask,
		w.provider,
		account.ProviderAccountID,
		currency,
	)
	if err != nil {
		return fmt.Errorf("upsert account %q: %w", account.ProviderAccountID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read account upsert result: %w", err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf(
			"account %q belongs to another provider item",
			account.ProviderAccountID,
		)
	}

	record, err := w.loadAccount(ctx, account.ProviderAccountID)
	if err != nil {
		return err
	}
	w.accounts[account.ProviderAccountID] = record
	return nil
}

func (w *syncWriter) upsertTransaction(ctx context.Context, transaction canon.Transaction) error {
	if err := validateDate(transaction.Date); err != nil {
		return err
	}
	if transaction.Status != canon.TxnStatusPending && transaction.Status != canon.TxnStatusPosted {
		return fmt.Errorf("unsupported transaction status %q", transaction.Status)
	}
	if transaction.PendingTxnID != "" && transaction.ProviderTxnID == "" {
		return fmt.Errorf("pending transaction id requires a provider transaction id")
	}
	if transaction.PendingTxnID != "" && transaction.Status != canon.TxnStatusPosted {
		return skipRecord{rec: canon.SkippedRecord{
			Kind:   canon.RecordKindTransaction,
			ID:     transaction.ProviderTxnID,
			Reason: canon.SkipMalformedRecord,
			Detail: "pending row carries pending id",
		}}
	}
	currency, err := canonicalCurrency(transaction.Currency)
	if err != nil {
		return err
	}
	account, err := w.account(ctx, transaction.AccountRef)
	if errors.Is(err, errAccountNotConnected) {
		return skipRecord{rec: canon.SkippedRecord{
			Kind:   canon.RecordKindTransaction,
			ID:     transaction.ProviderTxnID,
			Reason: canon.SkipAccountSkipped,
			Detail: transaction.AccountRef,
		}}
	}
	if err != nil {
		return err
	}
	category, err := w.category(ctx, transaction.SourceCategory)
	if err != nil {
		return err
	}

	merchantNormalized := NormalizeMerchant(transaction.MerchantRaw)
	dedupHash := DedupHash(transaction)
	transactionID, found, err := w.findTransaction(ctx, transaction, account.id, dedupHash)
	if err != nil {
		return err
	}
	if found {
		if err := w.updateTransaction(
			ctx,
			transactionID,
			account,
			transaction,
			currency,
			merchantNormalized,
			category,
			dedupHash,
		); err != nil {
			return err
		}
	} else {
		transactionID, err = w.insertTransaction(
			ctx,
			account,
			transaction,
			currency,
			merchantNormalized,
			category,
			dedupHash,
		)
		if err != nil {
			return err
		}
	}

	if transaction.ProviderTxnID != "" {
		if err := w.upsertProviderReference(ctx, transactionID, transaction); err != nil {
			return err
		}
	}

	return nil
}

func (w *syncWriter) findTransaction(
	ctx context.Context,
	transaction canon.Transaction,
	accountID int64,
	dedupHash string,
) (int64, bool, error) {
	if transaction.ProviderTxnID != "" {
		transactionID, found, err := w.findProviderReference(ctx, transaction.ProviderTxnID)
		if err != nil || found {
			return transactionID, found, err
		}
	}
	if transaction.PendingTxnID != "" {
		transactionID, found, err := w.findProviderReference(ctx, transaction.PendingTxnID)
		if err != nil || found {
			return transactionID, found, err
		}
	}

	var transactionID int64
	if transaction.ProviderTxnID == "" {
		err := w.tx.QueryRowContext(ctx, `
			SELECT id
			FROM transactions
			WHERE account_id = ? AND dedup_hash = ?
			ORDER BY id
			LIMIT 1
		`, accountID, dedupHash).Scan(&transactionID)
		if err == nil {
			return transactionID, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("find exact transaction match: %w", err)
		}
	}

	if transaction.Status != canon.TxnStatusPosted || transaction.ProviderTxnID != "" {
		return 0, false, nil
	}
	err := w.tx.QueryRowContext(ctx, `
		SELECT id
		FROM transactions
		WHERE account_id = ?
		  AND amount_cents = ?
		  AND merchant_norm = ?
		  AND status = 'pending'
		  AND date BETWEEN date(?, '-3 days') AND date(?, '+3 days')
		ORDER BY abs(julianday(date) - julianday(?)), id
		LIMIT 1
	`,
		accountID,
		transaction.AmountCents,
		NormalizeMerchant(transaction.MerchantRaw),
		transaction.Date,
		transaction.Date,
		transaction.Date,
	).Scan(&transactionID)
	if err == nil {
		return transactionID, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return 0, false, fmt.Errorf("find fuzzy pending transaction match: %w", err)
}

func (w *syncWriter) findProviderReference(
	ctx context.Context,
	providerTransactionID string,
) (int64, bool, error) {
	var transactionID int64
	err := w.tx.QueryRowContext(ctx, `
		SELECT transaction_id
		FROM txn_provider_refs
		WHERE provider = ? AND provider_txn_id = ?
	`, w.provider, providerTransactionID).Scan(&transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find provider transaction reference: %w", err)
	}
	return transactionID, true, nil
}

func (w *syncWriter) insertTransaction(
	ctx context.Context,
	account accountRecord,
	transaction canon.Transaction,
	currency string,
	merchantNormalized string,
	category categoryRecord,
	dedupHash string,
) (int64, error) {
	result, err := w.tx.ExecContext(ctx, `
		INSERT INTO transactions (
			account_id, entity_id, date, amount_cents, currency,
			merchant_raw, merchant_norm, category_id, status,
			is_transfer, excluded, dedup_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		account.id,
		account.entityID,
		transaction.Date,
		transaction.AmountCents,
		currency,
		transaction.MerchantRaw,
		merchantNormalized,
		nullableCategoryID(category),
		transaction.Status,
		boolInteger(category.isTransfer),
		boolInteger(category.isTransfer),
		dedupHash,
	)
	if err != nil {
		return 0, fmt.Errorf("insert transaction: %w", err)
	}
	transactionID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read inserted transaction id: %w", err)
	}
	return transactionID, nil
}

func (w *syncWriter) updateTransaction(
	ctx context.Context,
	transactionID int64,
	account accountRecord,
	transaction canon.Transaction,
	currency string,
	merchantNormalized string,
	category categoryRecord,
	dedupHash string,
) error {
	result, err := w.tx.ExecContext(ctx, `
		UPDATE transactions
		SET account_id = ?,
			entity_id = ?,
			date = ?,
			amount_cents = ?,
			currency = ?,
			merchant_raw = ?,
			merchant_norm = ?,
			category_id = ?,
			status = ?,
			is_transfer = ?,
			excluded = ?,
			dedup_hash = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ?
	`,
		account.id,
		account.entityID,
		transaction.Date,
		transaction.AmountCents,
		currency,
		transaction.MerchantRaw,
		merchantNormalized,
		nullableCategoryID(category),
		transaction.Status,
		boolInteger(category.isTransfer),
		boolInteger(category.isTransfer),
		dedupHash,
		transactionID,
	)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read transaction update result: %w", err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("transaction %d no longer exists", transactionID)
	}
	return nil
}

func (w *syncWriter) upsertProviderReference(
	ctx context.Context,
	transactionID int64,
	transaction canon.Transaction,
) error {
	_, err := w.tx.ExecContext(ctx, `
		INSERT INTO txn_provider_refs (
			transaction_id, provider, provider_txn_id, pending_txn_id
		) VALUES (?, ?, ?, ?)
		ON CONFLICT (provider, provider_txn_id) DO UPDATE SET
			transaction_id = excluded.transaction_id,
			pending_txn_id = excluded.pending_txn_id,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
	`,
		transactionID,
		w.provider,
		transaction.ProviderTxnID,
		nullableString(transaction.PendingTxnID),
	)
	if err != nil {
		return fmt.Errorf("upsert provider transaction reference: %w", err)
	}

	if transaction.PendingTxnID != "" && transaction.PendingTxnID != transaction.ProviderTxnID {
		if _, err := w.tx.ExecContext(ctx, `
			DELETE FROM txn_provider_refs
			WHERE provider = ? AND provider_txn_id = ?
		`, w.provider, transaction.PendingTxnID); err != nil {
			return fmt.Errorf("remove replaced pending reference: %w", err)
		}
	}
	return nil
}

func (w *syncWriter) removeTransaction(
	ctx context.Context,
	providerTransactionID string,
) error {
	if providerTransactionID == "" {
		return fmt.Errorf("removed provider transaction id is required")
	}
	transactionID, found, err := w.findProviderReference(ctx, providerTransactionID)
	if err != nil || !found {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, `
		DELETE FROM txn_provider_refs
		WHERE provider = ? AND provider_txn_id = ?
	`, w.provider, providerTransactionID); err != nil {
		return fmt.Errorf("remove provider transaction reference: %w", err)
	}

	var references int
	if err := w.tx.QueryRowContext(ctx, `
		SELECT count(*)
		FROM txn_provider_refs
		WHERE transaction_id = ?
	`, transactionID).Scan(&references); err != nil {
		return fmt.Errorf("count remaining transaction references: %w", err)
	}
	if references == 0 {
		if _, err := w.tx.ExecContext(ctx,
			"DELETE FROM transactions WHERE id = ?",
			transactionID,
		); err != nil {
			return fmt.Errorf("remove canonical transaction: %w", err)
		}
	}
	return nil
}

func (w *syncWriter) upsertBalance(ctx context.Context, balance canon.Balance) error {
	if err := validateDate(balance.Date); err != nil {
		return err
	}
	account, err := w.account(ctx, balance.AccountRef)
	if errors.Is(err, errAccountNotConnected) {
		return skipRecord{rec: canon.SkippedRecord{
			Kind:   canon.RecordKindBalance,
			ID:     balance.AccountRef,
			Reason: canon.SkipAccountSkipped,
			Detail: balance.AccountRef,
		}}
	}
	if err != nil {
		return err
	}
	_, err = w.tx.ExecContext(ctx, `
		INSERT INTO balance_snapshots (
			account_id, date, current_cents, available_cents, limit_cents, currency
		) VALUES (?, ?, ?, ?, ?, 'USD')
		ON CONFLICT (account_id, date) DO UPDATE SET
			current_cents = excluded.current_cents,
			available_cents = excluded.available_cents,
			limit_cents = excluded.limit_cents,
			currency = excluded.currency
	`,
		account.id,
		balance.Date,
		balance.CurrentCents,
		nullableCents(balance.AvailableCents),
		nullableCents(balance.LimitCents),
	)
	if err != nil {
		return fmt.Errorf("upsert balance snapshot: %w", err)
	}
	return nil
}

func (w *syncWriter) upsertLiability(ctx context.Context, liability canon.Liability) error {
	if math.IsNaN(liability.APR) || math.IsInf(liability.APR, 0) {
		return fmt.Errorf("liability APR must be finite")
	}
	statementDay, err := nullableDay(liability.StatementDay)
	if err != nil {
		return fmt.Errorf("statement day: %w", err)
	}
	dueDay, err := nullableDay(liability.DueDay)
	if err != nil {
		return fmt.Errorf("due day: %w", err)
	}
	account, err := w.account(ctx, liability.AccountRef)
	if errors.Is(err, errAccountNotConnected) {
		return skipRecord{rec: canon.SkippedRecord{
			Kind:   canon.RecordKindLiability,
			ID:     liability.AccountRef,
			Reason: canon.SkipAccountSkipped,
			Detail: liability.AccountRef,
		}}
	}
	if err != nil {
		return err
	}

	switch account.typeName {
	case canon.AccountTypeCreditCard:
		if _, err := w.tx.ExecContext(ctx, `
			INSERT INTO credit_terms (
				account_id, limit_cents, apr, statement_day, due_day,
				min_payment_cents, last_statement_cents
			) VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account_id) DO UPDATE SET
				limit_cents = excluded.limit_cents,
				apr = excluded.apr,
				statement_day = excluded.statement_day,
				due_day = excluded.due_day,
				min_payment_cents = excluded.min_payment_cents,
				last_statement_cents = excluded.last_statement_cents,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		`,
			account.id,
			nullableCents(liability.LimitCents),
			liability.APR,
			statementDay,
			dueDay,
			nullableCents(liability.MinPaymentCents),
			nullableCents(liability.LastStatementCents),
		); err != nil {
			return fmt.Errorf("upsert credit terms: %w", err)
		}
		if _, err := w.tx.ExecContext(ctx,
			"DELETE FROM loan_terms WHERE account_id = ?",
			account.id,
		); err != nil {
			return fmt.Errorf("remove obsolete loan terms: %w", err)
		}
	case canon.AccountTypeLoan:
		if _, err := w.tx.ExecContext(ctx, `
			INSERT INTO loan_terms (account_id, apr, min_payment_cents)
			VALUES (?, ?, ?)
			ON CONFLICT (account_id) DO UPDATE SET
				apr = excluded.apr,
				min_payment_cents = excluded.min_payment_cents,
				updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		`, account.id, liability.APR, nullableCents(liability.MinPaymentCents)); err != nil {
			return fmt.Errorf("upsert loan terms: %w", err)
		}
		if _, err := w.tx.ExecContext(ctx,
			"DELETE FROM credit_terms WHERE account_id = ?",
			account.id,
		); err != nil {
			return fmt.Errorf("remove obsolete credit terms: %w", err)
		}
	default:
		// A liability for an account type with no terms table is row-local
		// poison: skip it so the batch and cursor can still advance. The
		// account may have carried terms under a previous type, so clear
		// both tables just like the typed branches clear each other's.
		if _, err := w.tx.ExecContext(ctx,
			"DELETE FROM credit_terms WHERE account_id = ?",
			account.id,
		); err != nil {
			return fmt.Errorf("remove obsolete credit terms: %w", err)
		}
		if _, err := w.tx.ExecContext(ctx,
			"DELETE FROM loan_terms WHERE account_id = ?",
			account.id,
		); err != nil {
			return fmt.Errorf("remove obsolete loan terms: %w", err)
		}
		w.skipped = append(w.skipped, canon.SkippedRecord{
			Kind:   canon.RecordKindLiability,
			ID:     liability.AccountRef,
			Reason: canon.SkipUnsupportedAccountType,
			Detail: string(account.typeName),
		})
	}

	return nil
}

func (w *syncWriter) account(ctx context.Context, providerAccountID string) (accountRecord, error) {
	if providerAccountID == "" {
		return accountRecord{}, fmt.Errorf("account reference is required")
	}
	if account, ok := w.accounts[providerAccountID]; ok {
		return account, nil
	}
	account, err := w.loadAccount(ctx, providerAccountID)
	if err != nil {
		return accountRecord{}, err
	}
	w.accounts[providerAccountID] = account
	return account, nil
}

func (w *syncWriter) loadAccount(ctx context.Context, providerAccountID string) (accountRecord, error) {
	var account accountRecord
	err := w.tx.QueryRowContext(ctx, `
		SELECT id, entity_id, type
		FROM accounts
		WHERE provider = ?
		  AND provider_account_id = ?
		  AND provider_item_id = ?
	`, w.provider, providerAccountID, w.providerItemID).Scan(
		&account.id,
		&account.entityID,
		&account.typeName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return accountRecord{}, fmt.Errorf("%w: %q", errAccountNotConnected, providerAccountID)
	}
	if err != nil {
		return accountRecord{}, fmt.Errorf("load account %q: %w", providerAccountID, err)
	}
	return account, nil
}

func (w *syncWriter) category(
	ctx context.Context,
	sourceCategory string,
) (categoryRecord, error) {
	if sourceCategory == "" {
		return categoryRecord{}, nil
	}
	if category, ok := w.categories[sourceCategory]; ok {
		return category, nil
	}

	var category categoryRecord
	var kind string
	err := w.tx.QueryRowContext(ctx, `
		SELECT categories.id, categories.kind
		FROM category_mappings
		JOIN categories ON categories.id = category_mappings.category_id
		WHERE category_mappings.provider = ?
		  AND category_mappings.source_category = ?
	`, w.provider, sourceCategory).Scan(&category.id, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		w.categories[sourceCategory] = category
		return category, nil
	}
	if err != nil {
		return categoryRecord{}, fmt.Errorf("map source category %q: %w", sourceCategory, err)
	}
	category.found = true
	category.isTransfer = kind == "transfer"
	w.categories[sourceCategory] = category
	return category, nil
}

func (w *syncWriter) finish(ctx context.Context, batch *canon.SyncBatch) error {
	result, err := w.tx.ExecContext(ctx, `
		UPDATE provider_items
		SET sync_cursor = ?,
			status = 'ok',
			last_synced_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE id = ? AND sync_cursor = ?
	`, batch.NextCursor, w.providerItemID, w.expectedCursor)
	if err != nil {
		return fmt.Errorf("advance provider item cursor: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read cursor update result: %w", err)
	}
	if rowsAffected != 1 {
		return ErrCursorChanged
	}

	// The skipped count is the durable audit trail for row-local poison:
	// provider-side skips from the batch plus ingest-side skips recorded
	// while applying it. Only the count is persisted, never record payloads.
	if _, err := w.tx.ExecContext(ctx, `
		INSERT INTO import_runs (
			provider, provider_item_id, status, cursor_before, cursor_after,
			accounts_seen, transactions_added, transactions_modified,
			transactions_removed, skipped, completed_at
		) VALUES (?, ?, 'succeeded', ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
	`,
		w.provider,
		w.providerItemID,
		w.expectedCursor,
		batch.NextCursor,
		len(batch.Accounts),
		len(batch.Added),
		len(batch.Modified),
		len(batch.Removed),
		len(batch.Skipped)+len(w.skipped),
	); err != nil {
		return fmt.Errorf("record successful import run: %w", err)
	}
	return nil
}

func validateDate(date canon.Date) error {
	value := string(date)
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("date %q must use valid YYYY-MM-DD form", value)
	}
	return nil
}

func canonicalCurrency(currency string) (string, error) {
	if currency == "" {
		return "USD", nil
	}
	currency = strings.ToUpper(currency)
	if currency != "USD" {
		return "", fmt.Errorf("currency %q is unsupported; Phase 1 supports USD only", currency)
	}
	return currency, nil
}

func validAccountType(accountType canon.AccountType) bool {
	switch accountType {
	case canon.AccountTypeChecking,
		canon.AccountTypeSavings,
		canon.AccountTypeCreditCard,
		canon.AccountTypeLoan,
		canon.AccountTypeInvestment,
		canon.AccountTypeAsset:
		return true
	default:
		return false
	}
}

func nullableCents(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableCategoryID(category categoryRecord) any {
	if !category.found {
		return nil
	}
	return category.id
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableDay(day int) (any, error) {
	if day == 0 {
		return nil, nil
	}
	if day < 1 || day > 31 {
		return nil, fmt.Errorf("must be between 1 and 31")
	}
	return day, nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
