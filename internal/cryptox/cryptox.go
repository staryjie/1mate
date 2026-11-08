package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const KeySize = 32

// Box encrypts small secrets with AES-256-GCM. The additional authenticated
// data binds a ciphertext to the row it belongs to, so a value copied to another
// record fails to decrypt instead of leaking silently.
type Box struct {
	aead cipher.AEAD
}

func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// ParseKey accepts base64 (raw or std) or a 32 byte raw string.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty master key")
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	if len(s) == KeySize {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("master key must be 32 bytes or base64-encoded 32 bytes")
}

// LoadOrCreateKey returns the master key from the environment when present,
// otherwise from path, generating a new 0600 file on first start.
func LoadOrCreateKey(envKey, path string) (key []byte, created bool, err error) {
	if strings.TrimSpace(envKey) != "" {
		key, err = ParseKey(envKey)
		return key, false, err
	}
	if b, rerr := os.ReadFile(path); rerr == nil {
		key, err = ParseKey(string(b))
		return key, false, err
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return nil, false, rerr
	}
	key = make([]byte, KeySize)
	if _, err = rand.Read(key); err != nil {
		return nil, false, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	if err = os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func (b *Box) Encrypt(plaintext, aad string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (b *Box) Decrypt(encoded, aad string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode secret: %w", err)
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("secret too short")
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(aad))
	if err != nil {
		return "", errors.New("cannot decrypt secret: wrong master key or corrupted data")
	}
	return string(plain), nil
}

// RandomToken returns a URL-safe random token of n bytes of entropy.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
