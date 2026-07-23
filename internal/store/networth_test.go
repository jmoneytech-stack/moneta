package store

import (
	"context"
	"testing"
	"time"
)

func seedNetworthStoreDB(t *testing.T) func(NetworthFilter) NetworthReport {
	t.Helper()
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")

	checking := insertAccountFull(t, db, entityID, "Everyday Checking", "checking", "acct-1")
	insertAccountFull(t, db, entityID, "Rainy Day", "savings", "acct-2")
	investment := insertAccountFull(t, db, entityID, "Investment Example", "investment", "acct-3")
	asset := insertAccountFull(t, db, entityID, "Asset Example", "asset", "acct-4")
	credit := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "acct-5")
	loan := insertAccountFull(t, db, entityID, "Loan Example", "loan", "acct-6")

	insertBalanceSnapshot(t, db, checking, "2026-07-10", 100000)
	insertBalanceSnapshot(t, db, checking, "2026-07-20", 120000)
	insertBalanceSnapshot(t, db, investment, "2026-07-12", 450000)
	insertBalanceSnapshot(t, db, investment, "2026-07-21", 500000)
	insertBalanceSnapshot(t, db, asset, "2026-07-16", 250000)
	insertBalanceSnapshot(t, db, credit, "2026-07-15", 300000)
	insertBalanceSnapshot(t, db, credit, "2026-07-22", 340000)
	// Liability balances use positive-when-owed canonical storage.
	insertBalanceSnapshot(t, db, loan, "2026-07-10", 100000)

	read := func(filter NetworthFilter) NetworthReport {
		t.Helper()
		report, err := ReadNetworth(ctx, db, filter)
		if err != nil {
			t.Fatalf("ReadNetworth(%+v) error: %v", filter, err)
		}
		return report
	}
	return read
}

func TestReadNetworthLatestBalancePerAccount(t *testing.T) {
	read := seedNetworthStoreDB(t)
	report := read(NetworthFilter{})

	if report.AsOf != "2026-07-22" {
		t.Errorf("AsOf = %q, want 2026-07-22", report.AsOf)
	}
	if report.Accounts != 6 || report.MissingBalance != 1 {
		t.Errorf("account counts = %d / %d missing, want 6 / 1", report.Accounts, report.MissingBalance)
	}
	if report.AssetsCents != 870000 || report.LiabilitiesCents != 440000 || report.NetworthCents != 430000 {
		t.Errorf("money totals = assets %d, liabilities %d, networth %d",
			report.AssetsCents, report.LiabilitiesCents, report.NetworthCents)
	}

	wantTypes := []NetworthTypeSummary{
		{Type: "checking", Count: 1, BalancedCount: 1, BalanceCents: 120000},
		{Type: "savings", Count: 1, BalancedCount: 0, BalanceCents: 0},
		{Type: "investment", Count: 1, BalancedCount: 1, BalanceCents: 500000},
		{Type: "asset", Count: 1, BalancedCount: 1, BalanceCents: 250000},
		{Type: "credit_card", Count: 1, BalancedCount: 1, BalanceCents: 340000},
		{Type: "loan", Count: 1, BalancedCount: 1, BalanceCents: 100000},
	}
	if len(report.ByType) != len(wantTypes) {
		t.Fatalf("ByType has %d rows, want %d: %+v", len(report.ByType), len(wantTypes), report.ByType)
	}
	for i, want := range wantTypes {
		if report.ByType[i] != want {
			t.Errorf("ByType[%d] = %+v, want %+v", i, report.ByType[i], want)
		}
	}
}

func TestReadNetworthAsOfUsesLatestEligibleBalance(t *testing.T) {
	read := seedNetworthStoreDB(t)
	report := read(NetworthFilter{AsOf: "2026-07-15"})

	if report.AsOf != "2026-07-15" {
		t.Errorf("AsOf = %q, want requested cutoff", report.AsOf)
	}
	if report.Accounts != 6 || report.MissingBalance != 2 {
		t.Errorf("account counts = %d / %d missing, want 6 / 2", report.Accounts, report.MissingBalance)
	}
	if report.AssetsCents != 550000 || report.LiabilitiesCents != 400000 || report.NetworthCents != 150000 {
		t.Errorf("as-of totals = assets %d, liabilities %d, networth %d",
			report.AssetsCents, report.LiabilitiesCents, report.NetworthCents)
	}
}

func TestReadNetworthBeforeAllSnapshots(t *testing.T) {
	read := seedNetworthStoreDB(t)
	report := read(NetworthFilter{AsOf: "2026-07-01"})

	if report.AsOf != "2026-07-01" || report.Accounts != 6 || report.MissingBalance != 6 {
		t.Errorf("report = %+v, want requested date with every balance missing", report)
	}
	if report.AssetsCents != 0 || report.LiabilitiesCents != 0 || report.NetworthCents != 0 {
		t.Errorf("money totals should be zero when no snapshots are eligible: %+v", report)
	}
	for _, group := range report.ByType {
		if group.BalancedCount != 0 || group.BalanceCents != 0 {
			t.Errorf("type group should have no money contribution: %+v", group)
		}
	}
}

func TestNetworthCreditBalanceCountsAsAsset(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	checking := insertAccountFull(t, db, entityID, "Checking Example", "checking", "acct-1")
	card := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "acct-2")
	insertBalanceSnapshot(t, db, checking, "2026-07-22", 100000)
	insertBalanceSnapshot(t, db, card, "2026-07-22", -5000)

	report, err := ReadNetworth(ctx, db, NetworthFilter{})
	if err != nil {
		t.Fatalf("ReadNetworth() error: %v", err)
	}
	if report.AssetsCents != 100000 || report.LiabilitiesCents != -5000 ||
		report.NetworthCents != 105000 {
		t.Errorf("money totals = assets %d, liabilities %d, networth %d; want 100000, -5000, 105000",
			report.AssetsCents, report.LiabilitiesCents, report.NetworthCents)
	}
}

func TestNetworthLoanStillCountsAsDebt(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	loan := insertAccountFull(t, db, entityID, "Loan Example", "loan", "acct-1")
	insertBalanceSnapshot(t, db, loan, "2026-07-22", 5000)

	report, err := ReadNetworth(ctx, db, NetworthFilter{})
	if err != nil {
		t.Fatalf("ReadNetworth() error: %v", err)
	}
	if report.LiabilitiesCents != 5000 || report.NetworthCents != -5000 {
		t.Errorf("liabilities/networth = %d/%d, want 5000/-5000",
			report.LiabilitiesCents, report.NetworthCents)
	}
}

func TestReadNetworthCanBeNegative(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	checking := insertAccountFull(t, db, entityID, "Checking Example", "checking", "acct-1")
	credit := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "acct-2")
	insertBalanceSnapshot(t, db, checking, "2026-07-22", 100000)
	insertBalanceSnapshot(t, db, credit, "2026-07-22", 500000)

	report, err := ReadNetworth(ctx, db, NetworthFilter{})
	if err != nil {
		t.Fatalf("ReadNetworth() error: %v", err)
	}
	if report.NetworthCents != -400000 {
		t.Errorf("NetworthCents = %d, want -400000", report.NetworthCents)
	}
}

func TestNetworthHistoryCarriesForwardAcrossGaps(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	checking := insertAccountFull(t, db, entityID, "Checking Example", "checking", "acct-1")
	insertBalanceSnapshot(t, db, checking, "2026-07-01", 100000)
	insertBalanceSnapshot(t, db, checking, "2026-07-05", 200000)

	report, err := ReadNetworthHistory(ctx, db, NetworthHistoryFilter{
		From: "2026-07-01",
		To:   "2026-07-05",
	})
	if err != nil {
		t.Fatalf("ReadNetworthHistory() error: %v", err)
	}
	if report.From != "2026-07-01" || report.To != "2026-07-05" || len(report.Points) != 5 {
		t.Fatalf("history bounds/length = %s/%s/%d, want 2026-07-01/2026-07-05/5",
			report.From, report.To, len(report.Points))
	}
	want := []int64{100000, 100000, 100000, 100000, 200000}
	for index, wantCents := range want {
		point := report.Points[index]
		if point.AssetsCents != wantCents || point.LiabilitiesCents != 0 ||
			point.NetworthCents != wantCents {
			t.Errorf("point %d = %+v, want assets/networth %d", index, point, wantCents)
		}
	}
}

func TestNetworthHistoryRespectsSign(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	entityID := insertEntity(t, db, "personal", "Personal")
	checking := insertAccountFull(t, db, entityID, "Checking Example", "checking", "acct-1")
	card := insertAccountFull(t, db, entityID, "Credit Example", "credit_card", "acct-2")
	insertBalanceSnapshot(t, db, checking, "2026-07-01", 100000)
	insertBalanceSnapshot(t, db, card, "2026-07-01", 20000)
	insertBalanceSnapshot(t, db, card, "2026-07-03", -5000)

	report, err := ReadNetworthHistory(ctx, db, NetworthHistoryFilter{
		From: "2026-07-01",
		To:   "2026-07-03",
	})
	if err != nil {
		t.Fatalf("ReadNetworthHistory() error: %v", err)
	}
	if len(report.Points) != 3 {
		t.Fatalf("history has %d points, want 3", len(report.Points))
	}
	beforeCredit := report.Points[1]
	if beforeCredit.LiabilitiesCents != 20000 || beforeCredit.NetworthCents != 80000 {
		t.Errorf("before credit = %+v, want liabilities 20000 / networth 80000", beforeCredit)
	}
	credit := report.Points[2]
	if credit.LiabilitiesCents != -5000 || credit.NetworthCents != 105000 {
		t.Errorf("credit point = %+v, want liabilities -5000 / networth 105000", credit)
	}
}

func TestResolveNetworthHistoryWindow(t *testing.T) {
	now := time.Date(2026, time.July, 22, 23, 30, 0, 0, time.FixedZone("local", -7*60*60))
	window, err := ResolveNetworthHistoryWindow("90d", now)
	if err != nil {
		t.Fatalf("ResolveNetworthHistoryWindow() error: %v", err)
	}
	if window.From != "2026-04-24" || window.To != "2026-07-22" {
		t.Errorf("window = %+v, want 2026-04-24 through 2026-07-22", window)
	}
	for _, value := range []string{"", "0d", "-1d", "7h", "1.5d", "3661d"} {
		if _, err := ResolveNetworthHistoryWindow(value, now); err == nil {
			t.Errorf("ResolveNetworthHistoryWindow(%q) succeeded, want error", value)
		}
	}
}

func TestReadNetworthHistoryEmptyAndValidatesFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	report, err := ReadNetworthHistory(ctx, db, NetworthHistoryFilter{
		From: "2026-07-21",
		To:   "2026-07-22",
	})
	if err != nil {
		t.Fatalf("ReadNetworthHistory() error: %v", err)
	}
	if report.Days != 2 || len(report.Points) != 2 || report.HasBalances {
		t.Errorf("empty history = %+v, want two zero points without balances", report)
	}
	for _, point := range report.Points {
		if point.AssetsCents != 0 || point.LiabilitiesCents != 0 || point.NetworthCents != 0 {
			t.Errorf("empty history point = %+v, want zero money", point)
		}
	}

	filters := []NetworthHistoryFilter{
		{},
		{From: "2026-07-01"},
		{From: "bad", To: "2026-07-22"},
		{From: "2026-07-22", To: "2026-07-01"},
	}
	for _, filter := range filters {
		if _, err := ReadNetworthHistory(ctx, db, filter); err == nil {
			t.Errorf("ReadNetworthHistory(%+v) succeeded, want error", filter)
		}
	}
	if _, err := ReadNetworthHistory(ctx, nil, NetworthHistoryFilter{
		From: "2026-07-21",
		To:   "2026-07-22",
	}); err == nil {
		t.Error("ReadNetworthHistory(nil db) succeeded, want error")
	}
}

func TestReadNetworthEmptyAndValidatesFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	report, err := ReadNetworth(ctx, db, NetworthFilter{})
	if err != nil {
		t.Fatalf("ReadNetworth() error: %v", err)
	}
	if report.AsOf != "" || report.Accounts != 0 || report.MissingBalance != 0 ||
		report.AssetsCents != 0 || report.LiabilitiesCents != 0 || report.NetworthCents != 0 ||
		len(report.ByType) != 0 {
		t.Errorf("empty report = %+v", report)
	}

	report, err = ReadNetworth(ctx, db, NetworthFilter{AsOf: "2026-07-22"})
	if err != nil {
		t.Fatalf("ReadNetworth(as-of) error: %v", err)
	}
	if report.AsOf != "2026-07-22" || report.Accounts != 0 {
		t.Errorf("empty as-of report = %+v", report)
	}

	for _, asOf := range []string{"bad", "2026-02-30", "2026-7-01"} {
		if _, err := ReadNetworth(ctx, db, NetworthFilter{AsOf: asOf}); err == nil {
			t.Errorf("ReadNetworth(as-of %q) succeeded, want error", asOf)
		}
	}
	if _, err := ReadNetworth(ctx, nil, NetworthFilter{}); err == nil {
		t.Error("ReadNetworth(nil db) succeeded, want error")
	}
}
