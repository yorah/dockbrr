package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrSessionNotFound is returned by Sessions.Get for an absent or expired token.
var ErrSessionNotFound = errors.New("session not found")

// Session is a persisted login session. TokenHash is sha256(hex) of the opaque
// cookie token; the raw token is never stored. CSRFToken is the double-submit
// token bound to this session.
type Session struct {
	TokenHash string
	UserID    int64
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Sessions is the repository for the sessions table.
type Sessions struct{ db *sql.DB }

func NewSessions(db *DB) *Sessions { return &Sessions{db: db.DB} }

// Create persists a session. expiresAt is stored in UTC.
func (s *Sessions) Create(tokenHash string, userID int64, csrf string, expiresAt time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, csrf, expiresAt.UTC(),
	)
	return err
}

// Get returns the unexpired session for tokenHash. Expiry is checked
// authoritatively in Go (robust to timestamp encoding); an expired row is
// treated as absent and best-effort deleted.
func (s *Sessions) Get(tokenHash string, now time.Time) (Session, error) {
	var sess Session
	err := s.db.QueryRow(
		`SELECT token_hash, user_id, csrf_token, created_at, expires_at
		   FROM sessions WHERE token_hash=?`,
		tokenHash,
	).Scan(&sess.TokenHash, &sess.UserID, &sess.CSRFToken, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if !now.UTC().Before(sess.ExpiresAt) {
		_ = s.Delete(tokenHash)
		return Session{}, ErrSessionNotFound
	}
	return sess, nil
}

// Delete removes a single session (logout).
func (s *Sessions) Delete(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// DeleteByUser revokes every session for a user (e.g. on password change).
func (s *Sessions) DeleteByUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// DeleteExpired garbage-collects expired sessions; returns rows removed.
func (s *Sessions) DeleteExpired(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
