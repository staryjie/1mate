package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redactor removes known secret values and obvious secret-looking assignments
// from text before it reaches a log file, API response or SSE stream.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

var (
	keyValuePattern = regexp.MustCompile(`(?i)(ENCRYPTION_KEY|DATABASE_PASS|POSTGRES_PASSWORD|GRAFANA_PASSWD|GF_SECURITY_ADMIN_PASSWORD|MQTT_PASSWORD|TM_DB_PASS|ONEMATE_MASTER_KEY|SUDO_PASS(?:WORD)?)(\s*[=:]\s*)("[^"]*"|'[^']*'|\S+)`)
	privateKeyPat   = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
	bearerPattern   = regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)\S+`)
)

func New() *Redactor { return &Redactor{} }

func (r *Redactor) Add(secret string) {
	secret = strings.TrimSpace(secret)
	if len(secret) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if s == secret {
			return
		}
	}
	r.secrets = append(r.secrets, secret)
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

func (r *Redactor) Apply(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	secrets := r.secrets
	r.mu.RUnlock()
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	s = keyValuePattern.ReplaceAllStringFunc(s, func(match string) string {
		sub := keyValuePattern.FindStringSubmatch(match)
		return sub[1] + sub[2] + "***"
	})
	s = privateKeyPat.ReplaceAllString(s, "***private key***")
	s = bearerPattern.ReplaceAllString(s, "${1}***")
	return s
}
