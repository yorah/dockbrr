// Package store owns the SQLite database: connection setup, schema
// migrations, and (in later phases) typed repositories. Single writer.
package store

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB so repositories can hang methods off it.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at path, enables WAL +
// foreign keys, constrains to a single writer, and applies migrations.
//
// Pragmas are embedded in the DSN so every connection the pool opens, including
// driver reconnects on ErrBadConn, inherits busy_timeout, foreign_keys, and
// journal_mode without a separate per-connection PRAGMA round-trip.
func Open(path string) (*DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // single-writer invariant
	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &DB{sqlDB}, nil
}
