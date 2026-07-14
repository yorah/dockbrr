package store

import (
	"database/sql"
	"errors"

	"dockbrr/internal/secret"
)

// ErrSettingNotFound is returned by Settings.Get when the key is absent.
var ErrSettingNotFound = errors.New("setting not found")

// Settings is the typed, UI-editable key/value config store. Values written
// via SetSecret are encrypted at rest with the Sealer.
type Settings struct {
	db     *sql.DB
	sealer *secret.Sealer
}

func NewSettings(db *DB, sealer *secret.Sealer) *Settings {
	return &Settings{db: db.DB, sealer: sealer}
}

func (s *Settings) Get(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSettingNotFound
	}
	return v, err
}

func (s *Settings) Set(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (s *Settings) SetSecret(key, plaintext string) error {
	enc, err := s.sealer.Seal([]byte(plaintext))
	if err != nil {
		return err
	}
	return s.Set(key, enc)
}

func (s *Settings) GetSecret(key string) (string, error) {
	enc, err := s.Get(key)
	if err != nil {
		return "", err
	}
	b, err := s.sealer.Open(enc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// GetBoolDefault returns the boolean value of key, or def when the key is
// absent or not a valid bool. Used for feature flags that must have a safe
// default before the user ever visits Settings.
func (s *Settings) GetBoolDefault(key string, def bool) bool {
	v, err := s.Get(key)
	if err != nil {
		return def
	}
	switch v {
	case "true":
		return true
	case "false":
		return false
	default:
		return def
	}
}
