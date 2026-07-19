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

func TestNormalizeMerchantIsConservative(t *testing.T) {
	got := NormalizeMerchant("  Grocery   MART #12 ")
	want := "grocery mart #12"
	if got != want {
		t.Fatalf("NormalizeMerchant() = %q, want %q", got, want)
	}
}
