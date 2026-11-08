package redact

import (
	"strings"
	"testing"
)

func TestKnownSecretValues(t *testing.T) {
	r := New()
	r.Add("db-password-value")
	if got := r.Apply(`TM_DB_PASS="db-password-value"`); strings.Contains(got, "db-password-value") {
		t.Fatalf("known secret leaked: %s", got)
	}
	if got := r.Apply("random line db-password-value here"); strings.Contains(got, "db-password-value") {
		t.Fatalf("known secret leaked mid-line: %s", got)
	}
}

func TestKeyValuePatterns(t *testing.T) {
	r := New()
	cases := []string{
		"ENCRYPTION_KEY=0123456789abcdef",
		"POSTGRES_PASSWORD=supersecret",
		"GF_SECURITY_ADMIN_PASSWORD=grafana",
		"Authorization: Bearer abcdef123456",
	}
	for _, c := range cases {
		got := r.Apply(c)
		if !strings.Contains(got, "***") {
			t.Fatalf("%q not redacted: %q", c, got)
		}
	}
}

func TestPrivateKeyBlock(t *testing.T) {
	r := New()
	in := "before\n-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\nBBBB\n-----END OPENSSH PRIVATE KEY-----\nafter"
	got := r.Apply(in)
	if strings.Contains(got, "AAAA") || !strings.Contains(got, "***private key***") {
		t.Fatalf("private key not redacted: %q", got)
	}
}

func TestShortSecretsIgnored(t *testing.T) {
	r := New()
	r.Add("ab")
	if got := r.Apply("ab ab"); got != "ab ab" {
		t.Fatalf("short values must not be treated as secrets: %q", got)
	}
}
