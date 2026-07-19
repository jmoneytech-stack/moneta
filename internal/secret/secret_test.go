package secret

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFromEnvironment(t *testing.T) {
	key := testKey(1)
	t.Setenv(EnvironmentKey, base64.StdEncoding.EncodeToString(key))

	cipher, err := FromEnvironment()
	if err != nil {
		t.Fatalf("FromEnvironment() error: %v", err)
	}
	if cipher == nil {
		t.Fatal("FromEnvironment() returned a nil cipher")
	}
}

func TestFromEnvironmentRequiresKey(t *testing.T) {
	t.Setenv(EnvironmentKey, "")

	_, err := FromEnvironment()
	if !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("FromEnvironment() error = %v, want ErrKeyMissing", err)
	}
}

func TestFromBase64KeyAcceptsPaddedAndUnpaddedStandardEncoding(t *testing.T) {
	key := testKey(2)
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(key),
		base64.RawStdEncoding.EncodeToString(key),
	} {
		if _, err := FromBase64Key(encoded); err != nil {
			t.Errorf("FromBase64Key() rejected valid encoding: %v", err)
		}
	}
}

func TestFromBase64KeyRejectsMalformedOrWrongSizedKeys(t *testing.T) {
	tests := map[string]string{
		"malformed":        "not-base64!",
		"leading space":    " " + base64.StdEncoding.EncodeToString(testKey(3)),
		"embedded newline": base64.StdEncoding.EncodeToString(testKey(3))[:10] + "\nrest",
		"31 bytes":         base64.StdEncoding.EncodeToString(make([]byte, 31)),
		"33 bytes":         base64.StdEncoding.EncodeToString(make([]byte, 33)),
	}

	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := FromBase64Key(encoded)
			if !errors.Is(err, ErrKeyInvalid) {
				t.Fatalf("FromBase64Key() error = %v, want ErrKeyInvalid", err)
			}
		})
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	cipher := mustCipher(t, testKey(4))
	plaintext := []byte("access-sandbox-fake-token")

	envelope, err := cipher.Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal() error: %v", err)
	}
	if bytes.Contains(envelope, plaintext) {
		t.Fatal("encrypted envelope contains plaintext token")
	}

	opened, err := cipher.Open(envelope)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q, want original plaintext", opened)
	}
}

func TestSealUsesUniqueNonce(t *testing.T) {
	cipher := mustCipher(t, testKey(5))
	plaintext := []byte("same-fake-token")

	first, err := cipher.Seal(plaintext)
	if err != nil {
		t.Fatalf("first Seal() error: %v", err)
	}
	second, err := cipher.Seal(plaintext)
	if err != nil {
		t.Fatalf("second Seal() error: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same plaintext produced the same envelope")
	}

	nonceEnd := 1 + cipher.aead.NonceSize()
	if bytes.Equal(first[1:nonceEnd], second[1:nonceEnd]) {
		t.Fatal("two seals reused the same nonce")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	cipher := mustCipher(t, testKey(6))
	envelope, err := cipher.Seal([]byte("fake-token"))
	if err != nil {
		t.Fatalf("Seal() error: %v", err)
	}

	for name, index := range map[string]int{
		"version":    0,
		"nonce":      1,
		"ciphertext": len(envelope) - 1,
	} {
		t.Run(name, func(t *testing.T) {
			tampered := append([]byte(nil), envelope...)
			tampered[index] ^= 0x01
			_, err := cipher.Open(tampered)
			if !errors.Is(err, ErrCiphertextInvalid) {
				t.Fatalf("Open() error = %v, want ErrCiphertextInvalid", err)
			}
		})
	}
}

func TestOpenRejectsWrongKeyAndMalformedEnvelope(t *testing.T) {
	first := mustCipher(t, testKey(7))
	second := mustCipher(t, testKey(8))
	envelope, err := first.Seal([]byte("fake-token"))
	if err != nil {
		t.Fatalf("Seal() error: %v", err)
	}

	for name, candidate := range map[string][]byte{
		"wrong key": envelope,
		"empty":     nil,
		"truncated": envelope[:len(envelope)-1],
	} {
		t.Run(name, func(t *testing.T) {
			cipher := second
			if name != "wrong key" {
				cipher = first
			}
			_, err := cipher.Open(candidate)
			if !errors.Is(err, ErrCiphertextInvalid) {
				t.Fatalf("Open() error = %v, want ErrCiphertextInvalid", err)
			}
		})
	}
}

func TestErrorsDoNotExposeKeyOrPlaintext(t *testing.T) {
	plaintext := "access-sandbox-fake-sensitive-token"
	encodedKey := base64.StdEncoding.EncodeToString(testKey(9))

	_, keyErr := FromBase64Key(encodedKey + "!")
	if keyErr == nil {
		t.Fatal("FromBase64Key() accepted malformed key")
	}
	if strings.Contains(keyErr.Error(), encodedKey) {
		t.Fatal("key parsing error contains configured key")
	}

	cipher := mustCipher(t, testKey(9))
	envelope, err := cipher.Seal([]byte(plaintext))
	if err != nil {
		t.Fatalf("Seal() error: %v", err)
	}
	envelope[len(envelope)-1] ^= 0x01
	_, openErr := cipher.Open(envelope)
	if openErr == nil {
		t.Fatal("Open() accepted tampered envelope")
	}
	if strings.Contains(openErr.Error(), plaintext) {
		t.Fatal("decryption error contains plaintext token")
	}
}

func TestSealReportsRandomSourceFailure(t *testing.T) {
	cipher, err := newCipher(testKey(10), failingReader{})
	if err != nil {
		t.Fatalf("newCipher() error: %v", err)
	}

	_, err = cipher.Seal([]byte("fake-token"))
	if err == nil || !strings.Contains(err.Error(), "generate secret nonce") {
		t.Fatalf("Seal() error = %v, want nonce generation error", err)
	}
}

func TestZeroValueCipherReturnsError(t *testing.T) {
	var cipher Cipher
	if _, err := cipher.Seal([]byte("fake-token")); !errors.Is(err, ErrCipherNotInitialized) {
		t.Fatalf("Seal() error = %v, want ErrCipherNotInitialized", err)
	}
	if _, err := cipher.Open(nil); !errors.Is(err, ErrCipherNotInitialized) {
		t.Fatalf("Open() error = %v, want ErrCipherNotInitialized", err)
	}
}

func mustCipher(t *testing.T, key []byte) *Cipher {
	t.Helper()

	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher() error: %v", err)
	}
	return cipher
}

func testKey(seed byte) []byte {
	key := make([]byte, keySize)
	for index := range key {
		key[index] = seed + byte(index)
	}
	return key
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("test random source failed")
}
