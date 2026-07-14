package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrUserNotFound is returned by Users.GetByUsername when no row matches.
var ErrUserNotFound = errors.New("user not found")

// User is the single admin (v1); the table allows growth.
type User struct {
	ID           int64
	Username     string
	PasswordHash string // argon2id PHC string
	CreatedAt    time.Time
}

// Users is the repository for the users table.
type Users struct{ db *sql.DB }

func NewUsers(db *DB) *Users { return &Users{db: db.DB} }

// Count returns the number of users (0 => setup required).
func (u *Users) Count() (int, error) {
	var n int
	err := u.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Create inserts a user and returns its id. A duplicate username violates
// UNIQUE(username) and returns an error.
func (u *Users) Create(username, passwordHash string) (int64, error) {
	var id int64
	err := u.db.QueryRow(
		`INSERT INTO users (username, password_hash) VALUES (?, ?) RETURNING id`,
		username, passwordHash,
	).Scan(&id)
	return id, err
}

// GetByUsername returns the user, or ErrUserNotFound.
func (u *Users) GetByUsername(username string) (User, error) {
	var usr User
	err := u.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE username=?`,
		username,
	).Scan(&usr.ID, &usr.Username, &usr.PasswordHash, &usr.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return usr, err
}

// GetByID returns the user, or ErrUserNotFound.
func (u *Users) GetByID(id int64) (User, error) {
	var usr User
	err := u.db.QueryRow(
		`SELECT id, username, password_hash, created_at FROM users WHERE id=?`,
		id,
	).Scan(&usr.ID, &usr.Username, &usr.PasswordHash, &usr.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return usr, err
}

// UpdatePasswordHash replaces the stored argon2id hash for a user.
func (u *Users) UpdatePasswordHash(id int64, hash string) error {
	_, err := u.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}
