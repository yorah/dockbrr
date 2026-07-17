package store

import (
	"database/sql"
	"errors"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/secret"
)

// Credentials is the table-backed registry.CredentialStore: anonymous-first, so
// a missing or undecryptable credential yields ok=false (anonymous fallback),
// never a hard error.
var _ registry.CredentialStore = (*Credentials)(nil)

// Credential is a stored registry credential (secret intentionally omitted).
type Credential struct {
	ID           int64
	RegistryHost string
	Username     string
	CreatedAt    time.Time
}

// Credentials is the repository for the registry_credentials table. Secrets are
// sealed with the AES-256-GCM Sealer before storage.
type Credentials struct {
	db     *sql.DB
	sealer *secret.Sealer
}

func NewCredentials(db *DB, sealer *secret.Sealer) *Credentials {
	return &Credentials{db: db.DB, sealer: sealer}
}

// Upsert seals secretPlaintext and inserts/updates the row for host.
func (c *Credentials) Upsert(host, username, secretPlaintext string) (int64, error) {
	enc, err := c.sealer.Seal([]byte(secretPlaintext))
	if err != nil {
		return 0, err
	}
	var id int64
	err = c.db.QueryRow(
		`INSERT INTO registry_credentials (registry_host, username, secret)
		 VALUES (?, ?, ?)
		 ON CONFLICT(registry_host) DO UPDATE SET
		   username = excluded.username,
		   secret   = excluded.secret
		 RETURNING id`,
		host, username, enc,
	).Scan(&id)
	return id, err
}

// List returns the stored credentials without their secrets.
func (c *Credentials) List() ([]Credential, error) {
	rows, err := c.db.Query(
		`SELECT id, registry_host, username, created_at
		   FROM registry_credentials ORDER BY registry_host`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Credential
	for rows.Next() {
		var cr Credential
		if err := rows.Scan(&cr.ID, &cr.RegistryHost, &cr.Username, &cr.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// Delete removes the credential for host (no-op if absent).
func (c *Credentials) Delete(host string) error {
	_, err := c.db.Exec(`DELETE FROM registry_credentials WHERE registry_host=?`, host)
	return err
}

// Lookup implements registry.CredentialStore. A miss or decryption failure
// yields ok=false so the resolver stays anonymous. It never errors or panics.
func (c *Credentials) Lookup(registryHost string) (username, password string, ok bool) {
	var user, enc string
	err := c.db.QueryRow(
		`SELECT username, secret FROM registry_credentials WHERE registry_host=?`,
		registryHost,
	).Scan(&user, &enc)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			logger.Errorf("store: credential lookup %q: %v", registryHost, err)
		}
		return "", "", false
	}
	pw, err := c.sealer.Open(enc)
	if err != nil {
		logger.Errorf("store: credential decrypt %q: %v", registryHost, err)
		return "", "", false
	}
	return user, string(pw), true
}
