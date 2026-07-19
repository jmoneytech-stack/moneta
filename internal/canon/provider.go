package canon

import "context"

// Capability identifies an operation supported by a provider.
type Capability uint8

const (
	CapAccounts Capability = 1 << iota
	CapTransactions
	CapBalances
	CapLiabilities
	CapWrite
)

// Has reports whether the capability set contains capability.
func (c Capability) Has(capability Capability) bool {
	return c&capability == capability
}

// Provider is the swappable ingestion boundary. Implementations translate
// external data only; the core owns cursors, deduplication, mapping, and writes.
type Provider interface {
	Name() string
	Capabilities() Capability
	Connections(ctx context.Context) ([]ConnectionStatus, error)
	Sync(ctx context.Context, cursor string) (*SyncBatch, error)
}
