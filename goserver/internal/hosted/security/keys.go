// Package security provides the purpose-separated cryptographic primitives
// used by the hosted service.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
)

const keySize = 32

var (
	// ErrInvalidInput reports malformed public input without reflecting it.
	ErrInvalidInput = errors.New("security: invalid input")
	// ErrAuthentication reports that authenticated decryption failed.
	ErrAuthentication = errors.New("security: authentication failed")
	// ErrUnknownKeyVersion reports a ciphertext created with an unavailable key.
	ErrUnknownKeyVersion = errors.New("security: unknown key version")
	// ErrRandomUnavailable reports failure of the operating-system CSPRNG.
	ErrRandomUnavailable = errors.New("security: random source unavailable")
)

// Keyring holds separate AES-256-GCM and HMAC-SHA-256 keys. Key material is
// copied at construction so callers cannot mutate it after validation.
type Keyring struct {
	version     byte
	aeadKey     [keySize]byte
	hmacKey     [keySize]byte
	initialized bool
}

// NewKeyring validates and copies the active version's AEAD and HMAC keys.
func NewKeyring(version byte, aeadKey, hmacKey []byte) (Keyring, error) {
	if len(aeadKey) != keySize || len(hmacKey) != keySize || subtle.ConstantTimeCompare(aeadKey, hmacKey) == 1 {
		return Keyring{}, ErrInvalidInput
	}

	keys := Keyring{version: version, initialized: true}
	copy(keys.aeadKey[:], aeadKey)
	copy(keys.hmacKey[:], hmacKey)
	return keys, nil
}

// Seal encrypts plaintext with a fresh nonce and binds it to purpose. The
// returned wire format starts with the one-byte key version.
func (keys Keyring) Seal(purpose string, plaintext []byte) ([]byte, error) {
	if !validPurpose(purpose) {
		return nil, ErrInvalidInput
	}
	aead, err := keys.aead()
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, ErrRandomUnavailable
	}

	result := make([]byte, 1, 1+len(nonce)+len(plaintext)+aead.Overhead())
	result[0] = keys.version
	result = append(result, nonce...)
	result = aead.Seal(result, nonce, plaintext, aeadDomain(keys.version, purpose))
	return result, nil
}

// Open authenticates and decrypts ciphertext for purpose.
func (keys Keyring) Open(purpose string, ciphertext []byte) ([]byte, error) {
	if !keys.initialized || !validPurpose(purpose) || len(ciphertext) == 0 {
		return nil, ErrInvalidInput
	}
	if ciphertext[0] != keys.version {
		return nil, ErrUnknownKeyVersion
	}

	aead, err := keys.aead()
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < 1+aead.NonceSize()+aead.Overhead() {
		return nil, ErrInvalidInput
	}
	nonce := ciphertext[1 : 1+aead.NonceSize()]
	sealed := ciphertext[1+aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, aeadDomain(ciphertext[0], purpose))
	if err != nil {
		return nil, ErrAuthentication
	}
	return plaintext, nil
}

// Lookup returns a deterministic, keyed, purpose-separated equality digest.
func (keys Keyring) Lookup(purpose string, value []byte) ([]byte, error) {
	if !keys.initialized || !validPurpose(purpose) {
		return nil, ErrInvalidInput
	}
	mac := hmac.New(sha256.New, keys.hmacKey[:])
	_, _ = mac.Write(purposeDomain("lookup", purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(value)
	return mac.Sum(nil), nil
}

// HashToken returns an unkeyed SHA-256 digest for a high-entropy token. The
// purpose domain prevents token classes from silently sharing one hash space.
func (keys Keyring) HashToken(purpose string, token []byte) ([]byte, error) {
	if !keys.initialized || !validPurpose(purpose) {
		return nil, ErrInvalidInput
	}
	digest := sha256.New()
	_, _ = digest.Write(purposeDomain("token", purpose))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(token)
	return digest.Sum(nil), nil
}

// NewToken returns 32 bytes from crypto/rand encoded as unpadded base64url.
func (keys Keyring) NewToken() (string, error) {
	if !keys.initialized {
		return "", ErrInvalidInput
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", ErrRandomUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (keys Keyring) aead() (cipher.AEAD, error) {
	if !keys.initialized {
		return nil, ErrInvalidInput
	}
	block, err := aes.NewCipher(keys.aeadKey[:])
	if err != nil {
		return nil, ErrInvalidInput
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return aead, nil
}

func validPurpose(purpose string) bool {
	if len(purpose) == 0 || len(purpose) > 64 {
		return false
	}
	for _, character := range purpose {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func purposeDomain(operation, purpose string) []byte {
	return []byte("gift-panel/hosted/" + operation + "/v1\x00" + purpose)
}

func aeadDomain(version byte, purpose string) []byte {
	domain := purposeDomain("aead", purpose)
	return append(domain, 0, version)
}
