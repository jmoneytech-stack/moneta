package store

import (
	"context"
	"testing"
)

func TestReadDebtsLatestBalancesAndTerms(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")

	card := insertAccountFull(t, db, entityID, "Travel Card", "credit_card", "card-1")
	loan := insertAccountFull(t, db, entityID, "Auto Loan", "loan", "loan-1")
	missing := insertAccountFull(t, db, entityID, "Credit Missing", "credit_card", "card-2")
	checking := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "checking-1")

	insertBalanceSnapshot(t, db, card, "2026-07-20", 300000)
	insertBalanceSnapshot(t, db, card, "2026-07-22", 340000)
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 500000)
	insertBalanceSnapshot(t, db, checking, "2026-07-22", 100000)
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents, apr, due_day)
		VALUES (?, 1000000, 22.99, 15), (?, 0, NULL, NULL)
	`, card, missing); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO loan_terms (account_id, apr, min_payment_cents)
		VALUES (?, 5.5, 25000)
	`, loan); err != nil {
		t.Fatalf("insert loan terms: %v", err)
	}

	report, err := ReadDebts(ctx, db)
	if err != nil {
		t.Fatalf("ReadDebts() error: %v", err)
	}
	if report.Count != 3 || report.MissingBalance != 1 || report.TotalDebtCents != 840000 {
		t.Errorf("summary = count %d, missing %d, total %d, want 3, 1, 840000",
			report.Count, report.MissingBalance, report.TotalDebtCents)
	}
	if len(report.Debts) != 3 {
		t.Fatalf("Debts has %d rows, want 3", len(report.Debts))
	}

	auto := report.Debts[0]
	if auto.Name != "Auto Loan" || auto.Type != "loan" ||
		auto.BalanceCents == nil || *auto.BalanceCents != 500000 ||
		auto.LimitCents != nil || auto.APRBasisPoints == nil || *auto.APRBasisPoints != 550 ||
		auto.DueDay != nil {
		t.Errorf("loan row = %+v", auto)
	}
	missingCard := report.Debts[1]
	if missingCard.Name != "Credit Missing" || missingCard.BalanceCents != nil ||
		missingCard.LimitCents == nil || *missingCard.LimitCents != 0 ||
		missingCard.APRBasisPoints != nil || missingCard.DueDay != nil {
		t.Errorf("missing card row = %+v", missingCard)
	}
	travel := report.Debts[2]
	if travel.Name != "Travel Card" || travel.BalanceCents == nil || *travel.BalanceCents != 340000 ||
		travel.LimitCents == nil || *travel.LimitCents != 1000000 ||
		travel.APRBasisPoints == nil || *travel.APRBasisPoints != 2299 ||
		travel.DueDay == nil || *travel.DueDay != 15 {
		t.Errorf("card row = %+v", travel)
	}
}

func TestDebtsCreditBalanceReportedNegative(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	card := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "card-1")
	insertBalanceSnapshot(t, db, card, "2026-07-22", -5000)

	report, err := ReadDebts(ctx, db)
	if err != nil {
		t.Fatalf("ReadDebts() error: %v", err)
	}
	if report.TotalDebtCents != -5000 || len(report.Debts) != 1 ||
		report.Debts[0].BalanceCents == nil || *report.Debts[0].BalanceCents != -5000 {
		t.Errorf("report = %+v, want one -5000 credit balance", report)
	}
}

func TestDebtsLoanStillPositive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	loan := insertAccountFull(t, db, entityID, "Loan Example", "loan", "loan-1")
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 5000)

	report, err := ReadDebts(ctx, db)
	if err != nil {
		t.Fatalf("ReadDebts() error: %v", err)
	}
	if report.TotalDebtCents != 5000 || len(report.Debts) != 1 ||
		report.Debts[0].BalanceCents == nil || *report.Debts[0].BalanceCents != 5000 {
		t.Errorf("report = %+v, want one 5000 loan balance", report)
	}
}

func TestReadDebtsEmptyAndRequiresDatabase(t *testing.T) {
	db := openTestDB(t)
	report, err := ReadDebts(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadDebts() error: %v", err)
	}
	if report.Count != 0 || report.TotalDebtCents != 0 || report.MissingBalance != 0 || len(report.Debts) != 0 {
		t.Errorf("empty report = %+v", report)
	}
	if _, err := ReadDebts(context.Background(), nil); err == nil {
		t.Error("ReadDebts(nil) succeeded")
	}
}

func TestAPRPercentToBasisPoints(t *testing.T) {
	tests := []struct {
		percent float64
		want    int64
	}{
		{22.99, 2299},
		{5.5, 550},
		{0, 0},
		{22.994, 2299},
		{22.996, 2300},
	}
	for _, test := range tests {
		got, err := aprPercentToBasisPoints(test.percent)
		if err != nil {
			t.Fatalf("aprPercentToBasisPoints(%v) error: %v", test.percent, err)
		}
		if got != test.want {
			t.Errorf("aprPercentToBasisPoints(%v) = %d, want %d", test.percent, got, test.want)
		}
	}
}
