package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	// EnvironmentKey is the environment variable containing the base64-encoded
	// 32-byte encryption key.
	EnvironmentKey = "MONETA_ENCRYPTION_KEY"

	keySize       = 32
	envelopeV1    = byte(1)
	envelopeLabel = "moneta:secret:v1"
)

var (
	// ErrKeyMissing indicates that MONETA_ENCRYPTION_KEY is unset or empty.
	ErrKeyMissing = errors.New("encryption key is required")
	// ErrKeyInvalid indicates that the configured key is not a base64-encoded
	// 32-byte AES-256 key.
	ErrKeyInvalid = errors.New("encryption key must be a base64-encoded 32-byte value")
	// ErrCiphertextInvalid deliberately combines malformed and authentication
	// failures so callers do not receive sensitive cryptographic details.
	ErrCiphertextInvalid = errors.New("encrypted secret is invalid")
	// ErrCipherNotInitialized indicates use of an uninitialized Cipher.
	ErrCipherNotInitialized = errors.New("secret cipher is not initialized")
)

// Cipher seals and opens versioned AES-256-GCM envelopes.
type Cipher struct {
	aead   cipher.AEAD
	random io.Reader
}

// FromEnvironment constructs a Cipher from MONETA_ENCRYPTION_KEY.
func FromEnvironment() (*Cipher, error) {
	value, ok := os.LookupEnv(EnvironmentKey)
	if !ok || value == "" {
		return nil, ErrKeyMissing
	}
	return FromBase64Key(value)
}

// FromBase64Key constructs a Cipher from a padded or unpadded standard-base64
// AES-256 key. Whitespace is rejected rather than silently normalized.
func FromBase64Key(encoded string) (*Cipher, error) {
	if encoded == "" {
		return nil, ErrKeyMissing
	}
	if strings.IndexFunc(encoded, unicode.IsSpace) >= 0 {
		return nil, ErrKeyInvalid
	}

	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.Strict().DecodeString(encoded)
	}
	if err != nil || len(key) != keySize {
		return nil, ErrKeyInvalid
	}
	defer clear(key)
	return NewCipher(key)
}

// NewCipher constructs a Cipher from exactly 32 bytes of key material.
func NewCipher(key []byte) (*Cipher, error) {
	return newCipher(key, rand.Reader)
}

func newCipher(key []byte, random io.Reader) (*Cipher, error) {
	if len(key) != keySize {
		return nil, ErrKeyInvalid
	}
	if random == nil {
		return nil, fmt.Errorf("secure random source is required")
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-256: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize AES-GCM: %w", err)
	}
	return &Cipher{aead: aead, random: random}, nil
}

// Seal encrypts plaintext into a versioned binary envelope suitable for a
// SQLite BLOB. A new cryptographically random nonce is generated every time.
func (c *Cipher) Seal(plaintext []byte) ([]byte, error) {
	if c == nil || c.aead == nil || c.random == nil {
		return nil, ErrCipherNotInitialized
	}

	nonceSize := c.aead.NonceSize()
	prefixSize := 1 + nonceSize
	envelope := make(
		[]byte,
		prefixSize,
		prefixSize+len(plaintext)+c.aead.Overhead(),
	)
	envelope[0] = envelopeV1
	nonce := envelope[1:prefixSize]
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return nil, fmt.Errorf("generate secret nonce: %w", err)
	}

	return c.aead.Seal(envelope, nonce, plaintext, []byte(envelopeLabel)), nil
}

// Open authenticates and decrypts a versioned binary envelope. Every malformed
// or unauthenticated envelope returns the same non-sensitive error.
func (c *Cipher) Open(envelope []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, ErrCipherNotInitialized
	}

	nonceSize := c.aead.NonceSize()
	minimumSize := 1 + nonceSize + c.aead.Overhead()
	if len(envelope) < minimumSize || envelope[0] != envelopeV1 {
		return nil, ErrCiphertextInvalid
	}

	nonce := envelope[1 : 1+nonceSize]
	ciphertext := envelope[1+nonceSize:]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(envelopeLabel))
	if err != nil {
		return nil, ErrCiphertextInvalid
	}
	return plaintext, nil
}
