package store

import (
	"database/sql"
	"strings"
	"time"

	"1mate/internal/model"
)

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(u *model.User) error {
	_, err := s.db.Exec(
		`INSERT INTO users(id, username, password_hash, created_at, updated_at) VALUES(?,?,?,?,?)`,
		u.ID, u.Username, u.PasswordHash, now(), now())
	return err
}

func (s *Store) GetUserByUsername(username string) (*model.User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username = ?`,
		strings.ToLower(username)))
}

func (s *Store) GetUserByID(id string) (*model.User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, created_at, updated_at FROM users WHERE id = ?`, id))
}

func (s *Store) scanUser(row *sql.Row) (*model.User, error) {
	var u model.User
	var created, updated string
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &created, &updated); err != nil {
		return nil, mapErr(err)
	}
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return &u, nil
}

func (s *Store) UpdateUserPassword(id, hash string) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, now(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateSession(sess *model.Session) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions(id, user_id, token_hash, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		sess.ID, sess.UserID, sess.TokenHash, sess.CSRFToken, sess.IP, sess.UserAgent,
		sess.CreatedAt.UTC().Format(time.RFC3339Nano), sess.LastSeenAt.UTC().Format(time.RFC3339Nano),
		sess.ExpiresAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetSessionByTokenHash(hash string) (*model.Session, error) {
	var sess model.Session
	var created, seen, expires string
	err := s.db.QueryRow(
		`SELECT id, user_id, token_hash, csrf_token, ip, user_agent, created_at, last_seen_at, expires_at
		 FROM sessions WHERE token_hash = ?`, hash).
		Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.CSRFToken, &sess.IP, &sess.UserAgent,
			&created, &seen, &expires)
	if err != nil {
		return nil, mapErr(err)
	}
	sess.CreatedAt = parseTime(created)
	sess.LastSeenAt = parseTime(seen)
	sess.ExpiresAt = parseTime(expires)
	return &sess, nil
}

func (s *Store) TouchSession(id string, lastSeen time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`,
		lastSeen.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteExpiredSessions(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) InsertAudit(a *model.AuditLog) error {
	_, err := s.db.Exec(
		`INSERT INTO audit_logs(id, actor, action, target_type, target_id, detail_json, ip, created_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		a.ID, a.Actor, a.Action, a.TargetType, a.TargetID, a.DetailJSON, a.IP, a.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListAudit(limit int, before string) ([]model.AuditLog, error) {
	q := `SELECT id, actor, action, target_type, target_id, detail_json, ip, created_at FROM audit_logs`
	args := []any{}
	if before != "" {
		q += ` WHERE created_at < ?`
		args = append(args, before)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditLog{}
	for rows.Next() {
		var a model.AuditLog
		var created string
		if err := rows.Scan(&a.ID, &a.Actor, &a.Action, &a.TargetType, &a.TargetID, &a.DetailJSON, &a.IP, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetSetting(key, def string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	return v, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
