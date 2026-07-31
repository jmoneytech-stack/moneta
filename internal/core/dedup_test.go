package core

import (
	"testing"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

func TestDedupHashExcludesMutableStatus(t *testing.T) {
	pending := canon.Transaction{
		AccountRef:  "account-1",
		Date:        "2026-07-01",
		AmountCents: -435,
		MerchantRaw: "  Coffee   Shop ",
		Status:      canon.TxnStatusPending,
	}
	posted := pending
	posted.Status = canon.TxnStatusPosted
	posted.ProviderTxnID = "posted-id"
	posted.PendingTxnID = "pending-id"

	if DedupHash(pending) != DedupHash(posted) {
		t.Fatal("dedup hash changed when only mutable provider fields changed")
	}

	shifted := posted
	shifted.Date = "2026-07-03"
	if DedupHash(pending) == DedupHash(shifted) {
		t.Fatal("dedup hash did not change when the transaction date changed")
	}
}

func TestDedupHashIgnoresMerchantDisplay(t *testing.T) {
	base := canon.Transaction{
		AccountRef:      "account-1",
		Date:            "2026-07-01",
		AmountCents:     -435,
		MerchantRaw:     "TST* COFFEE",
		MerchantDisplay: "Coffee Shop",
		Status:          canon.TxnStatusPosted,
	}
	emptyDisplay := base
	emptyDisplay.MerchantDisplay = ""
	if DedupHash(base) != DedupHash(emptyDisplay) {
		t.Fatal("dedup hash changed when merchant display was cleared")
	}
	renamed := base
	renamed.MerchantDisplay = "Coffee Shop Downtown"
	if DedupHash(base) != DedupHash(renamed) {
		t.Fatal("dedup hash changed when only merchant display changed")
	}
	rawChanged := base
	rawChanged.MerchantRaw = "TST* COFFEE 2"
	if DedupHash(base) == DedupHash(rawChanged) {
		t.Fatal("dedup hash did not change when merchant raw changed")
	}
}

func TestNormalizeMerchantIsConservative(t *testing.T) {
	got := NormalizeMerchant("  Grocery   MART #12 ")
	want := "grocery mart #12"
	if got != want {
		t.Fatalf("NormalizeMerchant() = %q, want %q", got, want)
	}
}
