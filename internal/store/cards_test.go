package store

import (
	"context"
	"testing"
)

func TestReadCardsExcludesLoansAndOtherAccountTypes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")

	card := insertAccountFull(t, db, entityID, "Travel Card", "credit_card", "card-1")
	noLimit := insertAccountFull(t, db, entityID, "Store Card", "credit_card", "card-2")
	loan := insertAccountFull(t, db, entityID, "Auto Loan", "loan", "loan-1")
	checking := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "checking-1")

	insertBalanceSnapshot(t, db, card, "2026-07-20", 300000)
	insertBalanceSnapshot(t, db, card, "2026-07-22", 340000)
	insertBalanceSnapshot(t, db, noLimit, "2026-07-22", 15000)
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 500000)
	insertBalanceSnapshot(t, db, checking, "2026-07-22", 100000)
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15), (?, NULL, 27.49, 3)
	`, card, noLimit); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO loan_terms (account_id, apr) VALUES (?, 5.5)
	`, loan); err != nil {
		t.Fatalf("insert loan terms: %v", err)
	}

	report, err := ReadCards(ctx, db)
	if err != nil {
		t.Fatalf("ReadCards() error: %v", err)
	}
	if report.Count != 2 || report.MissingBalance != 0 || report.TotalDebtCents != 355000 {
		t.Errorf("summary = count %d, missing %d, total %d, want 2, 0, 355000",
			report.Count, report.MissingBalance, report.TotalDebtCents)
	}
	if len(report.Debts) != 2 {
		t.Fatalf("Debts has %d rows, want 2", len(report.Debts))
	}
	for _, row := range report.Debts {
		if row.Type != "credit_card" {
			t.Errorf("row %q has type %q, want credit_card", row.Name, row.Type)
		}
		if row.Name == "Auto Loan" || row.Name == "Everyday Checking" {
			t.Errorf("non-card account %q appears in cards", row.Name)
		}
	}

	// Rows keep the debts ordering (name, id) and its terms mapping.
	storeCard := report.Debts[0]
	if storeCard.Name != "Store Card" ||
		storeCard.BalanceCents == nil || *storeCard.BalanceCents != 15000 ||
		storeCard.LimitCents != nil ||
		storeCard.APRBasisPoints == nil || *storeCard.APRBasisPoints != 2749 ||
		storeCard.DueDay == nil || *storeCard.DueDay != 3 {
		t.Errorf("NULL-limit card row = %+v", storeCard)
	}
	travel := report.Debts[1]
	if travel.Name != "Travel Card" || travel.BalanceCents == nil || *travel.BalanceCents != 340000 ||
		travel.LimitCents == nil || *travel.LimitCents != 1000000 ||
		travel.APRBasisPoints == nil || *travel.APRBasisPoints != 2299 ||
		travel.DueDay == nil || *travel.DueDay != 15 {
		t.Errorf("card row = %+v", travel)
	}
}

func TestReadCardsCountsMissingBalancesAndKeepsCreditSign(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	credit := insertAccountFull(t, db, entityID, "Overpaid Card", "credit_card", "card-1")
	insertAccountFull(t, db, entityID, "Pending Card", "credit_card", "card-2")
	insertBalanceSnapshot(t, db, credit, "2026-07-22", -5000)

	report, err := ReadCards(ctx, db)
	if err != nil {
		t.Fatalf("ReadCards() error: %v", err)
	}
	if report.Count != 2 || report.MissingBalance != 1 || report.TotalDebtCents != -5000 {
		t.Errorf("summary = count %d, missing %d, total %d, want 2, 1, -5000",
			report.Count, report.MissingBalance, report.TotalDebtCents)
	}
	if report.Debts[0].BalanceCents == nil || *report.Debts[0].BalanceCents != -5000 {
		t.Errorf("credit balance row = %+v, want honest -5000", report.Debts[0])
	}
	if report.Debts[1].BalanceCents != nil {
		t.Errorf("card without a snapshot = %+v, want nil balance", report.Debts[1])
	}
}

func TestReadCardsEmptyAndRequiresDatabase(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	loan := insertAccountFull(t, db, entityID, "Auto Loan", "loan", "loan-1")
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 500000)

	report, err := ReadCards(ctx, db)
	if err != nil {
		t.Fatalf("ReadCards() error: %v", err)
	}
	if report.Count != 0 || report.TotalDebtCents != 0 ||
		report.MissingBalance != 0 || len(report.Debts) != 0 {
		t.Errorf("loan-only report = %+v, want empty card report", report)
	}
	if _, err := ReadCards(ctx, nil); err == nil {
		t.Error("ReadCards(nil) succeeded")
	}
}
