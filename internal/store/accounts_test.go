package store

import (
	"context"
	"database/sql"
	"testing"
)

func insertAccountFull(
	t *testing.T,
	db *sql.DB,
	entityID int64,
	name string,
	accountType string,
	providerAccountID string,
) int64 {
	t.Helper()

	result, err := db.Exec(`
		INSERT INTO accounts (
			entity_id, type, name, institution, provider, provider_account_id
		) VALUES (?, ?, ?, 'Test Institution', 'plaid', ?)
	`, entityID, accountType, name, providerAccountID)
	if err != nil {
		t.Fatalf("insert account %q: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read account id: %v", err)
	}
	return id
}

func insertBalanceSnapshot(t *testing.T, db *sql.DB, accountID int64, date string, cents int64) {
	t.Helper()

	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (account_id, date, current_cents)
		VALUES (?, ?, ?)
	`, accountID, date, cents); err != nil {
		t.Fatalf("insert balance snapshot: %v", err)
	}
}

func TestListAccountSummariesEmpty(t *testing.T) {
	db := openTestDB(t)

	accounts, err := ListAccountSummaries(context.Background(), db, "")
	if err != nil {
		t.Fatalf("ListAccountSummaries() error: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("ListAccountSummaries() = %d rows, want 0", len(accounts))
	}
}

func TestListAccountSummariesLatestBalanceAndOrder(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")

	checking := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")
	card := insertAccountFull(t, db, entityID, "Travel Card", "credit_card", "acct-2")
	savings := insertAccountFull(t, db, entityID, "Rainy Day", "savings", "acct-3")

	insertBalanceSnapshot(t, db, checking, "2026-07-19", 100000)
	insertBalanceSnapshot(t, db, checking, "2026-07-20", 99500)
	insertBalanceSnapshot(t, db, card, "2026-07-20", 4218)

	if _, err := db.Exec("UPDATE accounts SET is_active = 0 WHERE id = ?", savings); err != nil {
		t.Fatalf("deactivate savings: %v", err)
	}

	accounts, err := ListAccountSummaries(ctx, db, "")
	if err != nil {
		t.Fatalf("ListAccountSummaries() error: %v", err)
	}
	if len(accounts) != 3 {
		t.Fatalf("ListAccountSummaries() = %d rows, want 3", len(accounts))
	}

	wantOrder := []string{"Everyday Checking", "Rainy Day", "Travel Card"}
	for i, want := range wantOrder {
		if accounts[i].Name != want {
			t.Errorf("accounts[%d].Name = %q, want %q", i, accounts[i].Name, want)
		}
	}

	// Latest snapshot wins.
	if accounts[0].BalanceCents == nil || *accounts[0].BalanceCents != 99500 {
		t.Errorf("checking balance = %v, want 99500", accounts[0].BalanceCents)
	}
	if accounts[0].BalanceDate != "2026-07-20" {
		t.Errorf("checking balance date = %q, want 2026-07-20", accounts[0].BalanceDate)
	}
	if accounts[0].Institution != "Test Institution" || accounts[0].EntityName != "Personal" {
		t.Errorf("checking context = %q / %q", accounts[0].Institution, accounts[0].EntityName)
	}

	// No snapshot: nil balance, empty date, inactive flag preserved.
	if accounts[1].BalanceCents != nil || accounts[1].BalanceDate != "" {
		t.Errorf("savings balance = %v / %q, want nil / empty",
			accounts[1].BalanceCents, accounts[1].BalanceDate)
	}
	if accounts[1].Active {
		t.Error("savings should be inactive")
	}
	if !accounts[0].Active || !accounts[2].Active {
		t.Error("checking and card should be active")
	}
}

func TestListAccountSummariesTypeFilter(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	entityID := insertEntity(t, db, "personal", "Personal")

	insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")
	insertAccountFull(t, db, entityID, "Travel Card", "credit_card", "acct-2")

	accounts, err := ListAccountSummaries(ctx, db, "credit_card")
	if err != nil {
		t.Fatalf("ListAccountSummaries() error: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Name != "Travel Card" {
		t.Errorf("ListAccountSummaries(credit_card) = %+v, want only Travel Card", accounts)
	}
}

func TestListAccountSummariesRequiresDB(t *testing.T) {
	if _, err := ListAccountSummaries(context.Background(), nil, ""); err == nil {
		t.Fatal("ListAccountSummaries(nil) succeeded, want an error")
	}
}
