package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"1mate/internal/ids"
	"1mate/internal/model"
	"1mate/internal/store"
)

const SessionCookie = "1mate_session"

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrSetupRequired = errors.New("setup required")
)

type Manager struct {
	store        *store.Store
	ttl          time.Duration
	idle         time.Duration
	secureCookie bool

	mu       sync.Mutex
	attempts map[string]*attempt
}

type attempt struct {
	count int
	first time.Time
}

func NewManager(st *store.Store, ttl, idle time.Duration, secureCookie bool) *Manager {
	return &Manager{store: st, ttl: ttl, idle: idle, secureCookie: secureCookie, attempts: map[string]*attempt{}}
}

// SetupRequired reports whether the very first admin still has to be created.
func (m *Manager) SetupRequired() bool {
	n, err := m.store.CountUsers()
	return err == nil && n == 0
}

// CreateFirstUser bootstraps the platform. It fails once a user exists.
func (m *Manager) CreateFirstUser(username, password string) (*model.User, error) {
	if !m.SetupRequired() {
		return nil, ErrSetupRequired
	}
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &model.User{ID: ids.New(), Username: strings.ToLower(username), PasswordHash: hash}
	if err := m.store.CreateUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// Login verifies credentials and issues a session token.
func (m *Manager) Login(ctx context.Context, username, password, ip, ua string) (*model.Session, string, error) {
	if !m.allow(ip) {
		return nil, "", errors.New("too many failed attempts; wait a minute and try again")
	}
	u, err := m.store.GetUserByUsername(strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		// Spend the same work as a real verification to avoid a timing oracle.
		_ = VerifyPassword("$argon2id$v=19$m=65536,t=2,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		m.fail(ip)
		return nil, "", ErrUnauthorized
	}
	if !VerifyPassword(u.PasswordHash, password) {
		m.fail(ip)
		return nil, "", ErrUnauthorized
	}
	m.reset(ip)
	token, err := generateSessionToken()
	if err != nil {
		return nil, "", err
	}
	csrf, err := generateSessionToken()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	sess := &model.Session{
		ID:         ids.New(),
		UserID:     u.ID,
		TokenHash:  HashToken(token),
		CSRFToken:  csrf,
		IP:         ip,
		UserAgent:  truncateUA(ua),
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(m.ttl),
	}
	if err := m.store.CreateSession(sess); err != nil {
		return nil, "", err
	}
	return sess, token, nil
}

// Authenticate resolves the session from a request cookie, enforcing idle and
// absolute expiry.
func (m *Manager) Authenticate(r *http.Request) (*model.Session, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, ErrUnauthorized
	}
	sess, err := m.store.GetSessionByTokenHash(HashToken(cookie.Value))
	if err != nil {
		return nil, ErrUnauthorized
	}
	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) {
		_ = m.store.DeleteSession(sess.ID)
		return nil, ErrUnauthorized
	}
	if now.Sub(sess.LastSeenAt) > m.idle {
		_ = m.store.DeleteSession(sess.ID)
		return nil, ErrUnauthorized
	}
	// Throttle the write: a session touched within a minute is fresh enough.
	if now.Sub(sess.LastSeenAt) > time.Minute {
		_ = m.store.TouchSession(sess.ID, now)
		sess.LastSeenAt = now
	}
	return sess, nil
}

func (m *Manager) Logout(sess *model.Session) {
	if sess != nil {
		_ = m.store.DeleteSession(sess.ID)
	}
}

// SetCookie writes the session cookie with hardening attributes.
func (m *Manager) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(m.ttl.Seconds()),
	})
}

func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// Reap clears expired sessions; the server calls it periodically.
func (m *Manager) Reap() (int64, error) { return m.store.DeleteExpiredSessions(time.Now().UTC()) }

func (m *Manager) allow(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.attempts[ip]
	if !ok {
		return true
	}
	if time.Since(a.first) > time.Minute {
		delete(m.attempts, ip)
		return true
	}
	return a.count < 5
}

func (m *Manager) fail(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.attempts[ip]
	if !ok || time.Since(a.first) > time.Minute {
		m.attempts[ip] = &attempt{count: 1, first: time.Now()}
		return
	}
	a.count++
	if len(m.attempts) > 10000 {
		for k, v := range m.attempts {
			if time.Since(v.first) > time.Minute {
				delete(m.attempts, k)
			}
		}
	}
}

func (m *Manager) reset(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attempts, ip)
}

func truncateUA(s string) string {
	if len(s) > 255 {
		return s[:255]
	}
	return s
}
