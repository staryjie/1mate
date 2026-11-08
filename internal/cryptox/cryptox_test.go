package cryptox

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := box.Encrypt("super-secret", "credentials:abc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "super-secret") {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := box.Decrypt(ct, "credentials:abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != "super-secret" {
		t.Fatalf("got %q", got)
	}
}

func TestDecryptRejectsWrongAAD(t *testing.T) {
	key := make([]byte, KeySize)
	box, _ := NewBox(key)
	ct, err := box.Encrypt("value", "credentials:one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := box.Decrypt(ct, "credentials:two"); err == nil {
		t.Fatal("expected failure when the binding record changes")
	}
}

func TestParseKey(t *testing.T) {
	raw := "0123456789abcdef0123456789abcdef"
	key, err := ParseKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != KeySize {
		t.Fatalf("len=%d", len(key))
	}
	if _, err := ParseKey("too-short"); err == nil {
		t.Fatal("expected error")
	}
}
