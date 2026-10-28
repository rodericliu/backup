package crypto

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	if err := GenerateKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if err := GenerateKeyFile(path); err == nil {
		t.Error("overwrote existing key file")
	}
	key, err := LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != KeySize {
		t.Errorf("key size = %d", len(key))
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyFile(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable key: err = %v", err)
	}

	short := filepath.Join(t.TempDir(), "short.key")
	if err := os.WriteFile(short, []byte("c2hvcnQ=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyFile(short); err == nil {
		t.Error("short key accepted")
	}
}

func TestSealer(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("s3cret")
	a := s.Seal(pt, []byte("project:1"))
	b := s.Seal(pt, []byte("project:1"))
	if bytes.Equal(a, b) {
		t.Error("ciphertexts of the same plaintext are equal; nonce reused")
	}
	if bytes.Contains(a, pt) {
		t.Error("ciphertext contains plaintext")
	}
	got, err := s.Open(a, []byte("project:1"))
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := s.Open(a, []byte("project:2")); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong aad: err = %v", err)
	}
	tampered := bytes.Clone(a)
	tampered[len(tampered)-1] ^= 1
	if _, err := s.Open(tampered, []byte("project:1")); !errors.Is(err, ErrDecrypt) {
		t.Errorf("tampered: err = %v", err)
	}
	if _, err := s.Open(a[:5], nil); !errors.Is(err, ErrDecrypt) {
		t.Errorf("truncated: err = %v", err)
	}
	other, _ := NewSealer(bytes.Repeat([]byte{8}, KeySize))
	if _, err := other.Open(a, []byte("project:1")); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong key: err = %v", err)
	}
	if _, err := NewSealer(key[:16]); err == nil {
		t.Error("AES-128 key accepted")
	}
}

func TestTokens(t *testing.T) {
	a, b := NewToken(PrefixAgent), NewToken(PrefixAgent)
	if a == b || !strings.HasPrefix(a, PrefixAgent) || len(a) != len(PrefixAgent)+43 {
		t.Errorf("tokens %q %q", a, b)
	}
	h := HashToken(a)
	if len(h) != 64 || h != HashToken(a) || h == HashToken(b) {
		t.Errorf("hash %q", h)
	}
	if !EqualHash(h, HashToken(a)) || EqualHash(h, HashToken(b)) {
		t.Error("EqualHash")
	}
}

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2a$12$") {
		t.Errorf("hash %q is not bcrypt cost 12", h)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong horse") {
		t.Error("CheckPassword")
	}
	if p := RandomPassword(); len(p) < MinPasswordLength {
		t.Errorf("random password %q too short", p)
	}
}
