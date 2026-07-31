package plaid

import (
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

func TestNormalizeTransactionsMerchantDisplayPreference(t *testing.T) {
	base := rawTransaction{
		ID:        "txn-1",
		AccountID: "checking-1",
		Date:      "2026-07-01",
		Amount:    4.35,
		Currency:  "USD",
	}
	tests := []struct {
		name         string
		merchantName string
		displayName  string
		original     string
		wantRaw      string
		wantDisplay  string
	}{
		{
			"enriched merchant name is display only, raw keeps original",
			"Coffee Shop", "COFFEE SHOP #123", "TST* COFFEE",
			"TST* COFFEE", "Coffee Shop",
		},
		{
			"name fills display when merchant name is empty",
			"", "COFFEE SHOP #123", "TST* COFFEE",
			"TST* COFFEE", "COFFEE SHOP #123",
		},
		{
			"original fills display when merchant name and name are empty",
			"", "", "TST* COFFEE",
			"TST* COFFEE", "TST* COFFEE",
		},
		{
			"name fills raw when original is empty",
			"Coffee Shop", "COFFEE SHOP #123", "",
			"COFFEE SHOP #123", "Coffee Shop",
		},
		{
			"merchant name alone never fills raw",
			"Coffee Shop", "", "",
			"", "Coffee Shop",
		},
		{
			"all empty stays empty",
			"", "", "",
			"", "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := base
			raw.MerchantName = test.merchantName
			raw.Name = test.displayName
			raw.OriginalDescription = test.original
			transactions, skipped := normalizeTransactions([]rawTransaction{raw})
			if len(skipped) != 0 || len(transactions) != 1 {
				t.Fatalf("normalizeTransactions() = %d transactions / %#v skipped",
					len(transactions), skipped)
			}
			got := transactions[0]
			if got.MerchantRaw != test.wantRaw || got.MerchantDisplay != test.wantDisplay {
				t.Errorf("merchant raw/display = %q/%q, want %q/%q",
					got.MerchantRaw, got.MerchantDisplay, test.wantRaw, test.wantDisplay)
			}
		})
	}
}

func TestNormalizeLiabilitiesMergesDueDatePairAsOfAware(t *testing.T) {
	asOf := canon.Date("2026-07-30")
	tests := []struct {
		name     string
		dates    []string
		wantDate canon.Date
		wantDay  int
	}{
		{"earliest future beats older past", []string{"2025-01-15", "2026-08-20"}, "2026-08-20", 20},
		{"latest past wins when nothing is future", []string{"2025-01-15", "2025-03-10"}, "2025-03-10", 10},
		{"earliest of two futures wins", []string{"2026-09-05", "2026-08-20"}, "2026-08-20", 20},
		{"date equal to asOf counts as future", []string{"2026-08-20", "2026-07-30"}, "2026-07-30", 30},
		{"dated record beats empty record", []string{"", "2026-08-20"}, "2026-08-20", 20},
		{"empty pair stays empty", []string{"", ""}, "", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := rawLiabilities{}
			for index, date := range test.dates {
				// Alternate arrays so the merge path across liability kinds is
				// exercised, not only single-record passthrough.
				if index%2 == 0 {
					raw.Credit = append(raw.Credit, rawCreditLiability{
						AccountID:          "card-1",
						NextPaymentDueDate: date,
					})
				} else {
					raw.Student = append(raw.Student, rawStudentLiability{
						AccountID:          "card-1",
						NextPaymentDueDate: date,
					})
				}
			}
			liabilities, skipped := normalizeLiabilities(raw, map[string]rawAccount{}, asOf)
			if len(skipped) != 0 || len(liabilities) != 1 {
				t.Fatalf("normalizeLiabilities() = %d liabilities / %#v skipped",
					len(liabilities), skipped)
			}
			got := liabilities[0]
			if got.NextPaymentDueDate != test.wantDate || got.DueDay != test.wantDay {
				t.Errorf("due date pair = %q/%d, want %q/%d",
					got.NextPaymentDueDate, got.DueDay, test.wantDate, test.wantDay)
			}
		})
	}
}

func TestMergeLiabilityDueDateKeepsLegacyDayFallbackWithoutFullDates(t *testing.T) {
	got := mergeLiabilityDueDate(
		canon.Liability{DueDay: 20},
		canon.Liability{DueDay: 15},
		canon.Date("2026-07-30"),
	)
	if got.NextPaymentDueDate != "" || got.DueDay != 15 {
		t.Errorf("due date fallback = %q/%d, want empty/15", got.NextPaymentDueDate, got.DueDay)
	}
}

func TestNormalizeLiabilitiesInvalidDueDateStillSkips(t *testing.T) {
	liabilities, skipped := normalizeLiabilities(rawLiabilities{
		Credit: []rawCreditLiability{{
			AccountID:          "card-1",
			NextPaymentDueDate: "07/28/2026",
		}},
	}, map[string]rawAccount{}, canon.Date("2026-07-30"))
	if len(liabilities) != 0 || len(skipped) != 1 {
		t.Fatalf("normalizeLiabilities() = %d liabilities / %#v skipped, want 0/1",
			len(liabilities), skipped)
	}
	if skipped[0].Detail != "invalid due date" {
		t.Errorf("skip detail = %q, want invalid due date", skipped[0].Detail)
	}
}
