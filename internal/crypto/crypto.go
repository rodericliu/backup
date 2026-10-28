// Package crypto encrypts MySQL credentials at rest and generates and hashes
// tokens and passwords (PRD U-1, U-6, U-8).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// KeySize is the size of the AES-256 master key.
const KeySize = 32

// sealVersion prefixes every ciphertext so that the format can evolve.
const sealVersion = 1

// ErrDecrypt is returned when a ciphertext cannot be authenticated.
var ErrDecrypt = errors.New("decrypt: message authentication failed")

// GenerateKeyFile writes a new random master key, base64 encoded, to path
// with mode 0600. It fails if the file exists.
func GenerateKeyFile(path string) error {
	key := make([]byte, KeySize)
	rand.Read(key)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create master key: %w", err)
	}
	_, err = f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
	return errors.Join(err, f.Close())
}

// LoadKeyFile reads a base64 encoded master key. The file must not be
// readable by group or others.
func LoadKeyFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("master key %s: mode %v is accessible by group or others; chmod 600", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("master key %s: %w", path, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key %s: %d bytes, want %d", path, len(key), KeySize)
	}
	return key, nil
}

// Sealer encrypts and authenticates small secrets with AES-256-GCM.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a Sealer using a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext. aad binds the ciphertext to its context (for
// example the project ID) so that it cannot be moved to another record.
// The result is version || nonce || ciphertext.
func (s *Sealer) Seal(plaintext, aad []byte) []byte {
	ns := s.aead.NonceSize()
	out := make([]byte, 1+ns, 1+ns+len(plaintext)+s.aead.Overhead())
	out[0] = sealVersion
	rand.Read(out[1:])
	return s.aead.Seal(out, out[1:], plaintext, aad)
}

// Open decrypts a value produced by Seal with the same aad.
func (s *Sealer) Open(sealed, aad []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(sealed) < 1+ns+s.aead.Overhead() || sealed[0] != sealVersion {
		return nil, ErrDecrypt
	}
	pt, err := s.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// Token prefixes identify the kind of a token in logs and configuration.
const (
	PrefixAgent    = "bat_" // long-lived agent token
	PrefixRegister = "brt_" // one-time registration token
	PrefixSession  = "bss_" // browser session
)

// NewToken returns a random token with 256 bits of entropy.
func NewToken(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the hex SHA-256 of a token, which is what the database
// stores (PRD U-8). Tokens are random, so a fast hash is sufficient.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// EqualHash compares two token hashes in constant time.
func EqualHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// PasswordCost is the bcrypt cost of password hashes.
const PasswordCost = 12

// MinPasswordLength is the minimum length of user passwords.
const MinPasswordLength = 10

// HashPassword returns a bcrypt hash of password.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches a bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// RandomPassword returns a random password for bootstrap accounts.
func RandomPassword() string {
	return rand.Text()
}
