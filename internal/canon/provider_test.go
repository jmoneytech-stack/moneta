package canon

import "testing"

func TestCapabilityHas(t *testing.T) {
	capabilities := CapAccounts | CapTransactions | CapBalances

	if !capabilities.Has(CapAccounts | CapTransactions) {
		t.Fatal("expected capability set to include accounts and transactions")
	}
	if capabilities.Has(CapLiabilities) {
		t.Fatal("did not expect capability set to include liabilities")
	}
}
