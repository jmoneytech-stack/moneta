package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"strings"

	"github.com/jmoneytech-stack/moneta/internal/canon"
)

// NormalizeMerchant returns the stable merchant form used for matching. It is
// intentionally conservative so normalization does not merge distinct names.
func NormalizeMerchant(merchant string) string {
	return strings.Join(strings.Fields(strings.ToLower(merchant)), " ")
}

// DedupHash returns the exact-match hash for a provider transaction. Status is
// deliberately absent because it is mutable during pending-to-posted changes.
func DedupHash(transaction canon.Transaction) string {
	hash := sha256.New()
	writeHashField(hash, transaction.AccountRef)
	writeHashField(hash, string(transaction.Date))

	var amount [8]byte
	binary.BigEndian.PutUint64(amount[:], uint64(transaction.AmountCents))
	_, _ = hash.Write(amount[:])

	writeHashField(hash, NormalizeMerchant(transaction.MerchantRaw))
	return hex.EncodeToString(hash.Sum(nil))
}

func writeHashField(hash io.Writer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}
