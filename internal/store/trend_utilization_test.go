package store

import (
	"context"
	"database/sql"
	"testing"
)

func insertUtilizationSnapshot(
	t *testing.T,
	db *sql.DB,
	accountID int64,
	date string,
	currentCents int64,
	limit any,
) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO balance_snapshots (
			account_id, date, current_cents, limit_cents
		) VALUES (?, ?, ?, ?)
	`, accountID, date, currentCents, limit); err != nil {
		t.Fatalf("insert utilization snapshot: %v", err)
	}
}

func TestReadTrendUtilizationCarriesForwardAndHandlesCreditBalanceAsZero(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	cardID := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "card-1")
	loanID := insertAccountFull(t, db, entityID, "Loan Example", "loan", "loan-1")
	insertUtilizationSnapshot(t, db, cardID, "2026-07-01", 340000, int64(1000000))
	insertUtilizationSnapshot(t, db, cardID, "2026-07-03", -5000, int64(1000000))
	insertUtilizationSnapshot(t, db, loanID, "2026-07-01", 900000, int64(1000000))

	report, err := ReadTrendUtilization(ctx, db, TrendUtilizationFilter{
		From: "2026-07-01",
		To:   "2026-07-04",
	})
	if err != nil {
		t.Fatalf("ReadTrendUtilization() error: %v", err)
	}
	if report.From != "2026-07-01" || report.To != "2026-07-04" ||
		report.Days != 4 || report.Accounts != 1 || report.MissingLimitDays != 0 {
		t.Fatalf("summary = %+v, want four days / one card / no missing-limit days", report)
	}
	want := []TrendUtilizationPoint{
		{Date: "2026-07-01", DebtCents: 340000, LimitCents: 1000000, Accounts: 1, HasUtilization: true},
		{Date: "2026-07-02", DebtCents: 340000, LimitCents: 1000000, Accounts: 1, HasUtilization: true},
		{Date: "2026-07-03", DebtCents: 0, LimitCents: 1000000, Accounts: 1, HasUtilization: true},
		{Date: "2026-07-04", DebtCents: 0, LimitCents: 1000000, Accounts: 1, HasUtilization: true},
	}
	if len(report.Points) != len(want) {
		t.Fatalf("points = %d, want %d: %+v", len(report.Points), len(want), report.Points)
	}
	for index := range want {
		if report.Points[index] != want[index] {
			t.Errorf("points[%d] = %+v, want %+v", index, report.Points[index], want[index])
		}
	}
}

func TestReadTrendUtilizationAggregatesCardsAndFiltersAccountsLiterally(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	underscoreID := insertAccountFull(t, db, entityID, "Card_One", "credit_card", "card-underscore")
	otherID := insertAccountFull(t, db, entityID, "CardXOne", "credit_card", "card-other")
	insertUtilizationSnapshot(t, db, underscoreID, "2026-07-01", 10000, int64(100000))
	insertUtilizationSnapshot(t, db, otherID, "2026-07-01", 40000, int64(200000))

	report, err := ReadTrendUtilization(ctx, db, TrendUtilizationFilter{
		From: "2026-07-01",
		To:   "2026-07-01",
	})
	if err != nil {
		t.Fatalf("ReadTrendUtilization() error: %v", err)
	}
	if report.Accounts != 2 || len(report.Points) != 1 {
		t.Fatalf("report = %+v, want two cards and one point", report)
	}
	point := report.Points[0]
	if point.DebtCents != 50000 || point.LimitCents != 300000 || point.Accounts != 2 {
		t.Errorf("portfolio point = %+v, want 50000/300000 across two cards", point)
	}

	filtered, err := ReadTrendUtilization(ctx, db, TrendUtilizationFilter{
		From:    "2026-07-01",
		To:      "2026-07-01",
		Account: "_",
	})
	if err != nil {
		t.Fatalf("ReadTrendUtilization(account) error: %v", err)
	}
	if filtered.Accounts != 1 || len(filtered.Points) != 1 ||
		filtered.Points[0].DebtCents != 10000 || filtered.Points[0].LimitCents != 100000 {
		t.Errorf("literal account-filter report = %+v", filtered)
	}
}

func TestReadTrendUtilizationNullAndZeroLimitsStayUndefined(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	nullLimitID := insertAccountFull(t, db, entityID, "Missing Limit Card", "credit_card", "card-null")
	zeroLimitID := insertAccountFull(t, db, entityID, "Legacy Zero Card", "credit_card", "card-zero")
	insertUtilizationSnapshot(t, db, nullLimitID, "2026-07-01", 25000, nil)
	insertUtilizationSnapshot(t, db, zeroLimitID, "2026-07-01", 15000, int64(0))

	report, err := ReadTrendUtilization(ctx, db, TrendUtilizationFilter{
		From: "2026-07-01",
		To:   "2026-07-02",
	})
	if err != nil {
		t.Fatalf("ReadTrendUtilization() error: %v", err)
	}
	if report.Accounts != 2 || report.MissingLimitDays != 2 || len(report.Points) != 2 {
		t.Fatalf("summary = %+v, want two cards and two missing-limit days", report)
	}
	for _, point := range report.Points {
		if point.HasUtilization || point.DebtCents != 0 || point.LimitCents != 0 || point.Accounts != 0 {
			t.Errorf("undefined point = %+v, want null utilization inputs", point)
		}
	}
}

func TestReadTrendUtilizationFallsBackOnlyForNullSnapshotLimit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	cardID := insertAccountFull(t, db, entityID, "Fallback Card", "credit_card", "card-fallback")
	if _, err := db.Exec(`
		INSERT INTO credit_terms (account_id, limit_cents) VALUES (?, 500000)
	`, cardID); err != nil {
		t.Fatalf("insert credit terms: %v", err)
	}
	insertUtilizationSnapshot(t, db, cardID, "2026-07-01", 100000, nil)
	insertUtilizationSnapshot(t, db, cardID, "2026-07-02", 200000, int64(0))

	report, err := ReadTrendUtilization(ctx, db, TrendUtilizationFilter{
		From: "2026-07-01",
		To:   "2026-07-02",
	})
	if err != nil {
		t.Fatalf("ReadTrendUtilization() error: %v", err)
	}
	if report.MissingLimitDays != 1 || len(report.Points) != 2 {
		t.Fatalf("report = %+v, want one missing-limit day", report)
	}
	first := report.Points[0]
	if !first.HasUtilization || first.DebtCents != 100000 ||
		first.LimitCents != 500000 || first.Accounts != 1 {
		t.Errorf("fallback point = %+v, want 100000/500000", first)
	}
	second := report.Points[1]
	if second.HasUtilization || second.DebtCents != 0 ||
		second.LimitCents != 0 || second.Accounts != 0 {
		t.Errorf("stored-zero point = %+v, want undefined without fallback", second)
	}
}

func TestReadTrendUtilizationEmptyAndValidatesFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	valid := TrendUtilizationFilter{From: "2026-07-01", To: "2026-07-02"}
	report, err := ReadTrendUtilization(ctx, db, valid)
	if err != nil {
		t.Fatalf("ReadTrendUtilization(empty) error: %v", err)
	}
	if report.Days != 2 || report.Accounts != 0 || report.MissingLimitDays != 0 ||
		len(report.Points) != 2 {
		t.Errorf("empty report = %+v", report)
	}
	for _, point := range report.Points {
		if point.HasUtilization || point.DebtCents != 0 ||
			point.LimitCents != 0 || point.Accounts != 0 {
			t.Errorf("empty point = %+v", point)
		}
	}

	for _, filter := range []TrendUtilizationFilter{
		{},
		{From: "bad", To: "2026-07-02"},
		{From: "2026-07-02", To: "2026-07-01"},
	} {
		if _, err := ReadTrendUtilization(ctx, db, filter); err == nil {
			t.Errorf("ReadTrendUtilization(%+v) succeeded, want error", filter)
		}
	}
	if _, err := ReadTrendUtilization(ctx, nil, valid); err == nil {
		t.Error("ReadTrendUtilization(nil db) succeeded, want error")
	}
}
