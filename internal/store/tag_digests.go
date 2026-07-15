package store

import (
	"database/sql"
	"errors"
)

// TagDigests caches immutable tag->digest resolutions. It backs the detector's
// floating-tag reverse version-naming scan so the same exact-semver tags are not
// re-HEADed every detect cycle. Unlike image_remote_state it has NO TTL: a
// published release tag's served digest is permanent, so a hit never expires.
type TagDigests struct{ db *sql.DB }

func NewTagDigests(db *DB) *TagDigests { return &TagDigests{db: db.DB} }

// Get returns the cached digest for (repo, tag). ok is false when no row exists.
func (t *TagDigests) Get(repo, tag string) (digest string, ok bool, err error) {
	err = t.db.QueryRow(
		`SELECT digest FROM tag_digest_cache WHERE repo=? AND tag=?`, repo, tag,
	).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return digest, true, nil
}

// Put records digest for (repo, tag). An empty digest is ignored (never cache a
// non-answer). Re-recording is idempotent since the mapping is immutable.
func (t *TagDigests) Put(repo, tag, digest string) error {
	if digest == "" {
		return nil
	}
	_, err := t.db.Exec(
		`INSERT INTO tag_digest_cache (repo, tag, digest)
		 VALUES (?, ?, ?)
		 ON CONFLICT(repo, tag) DO UPDATE SET digest=excluded.digest`,
		repo, tag, digest,
	)
	return err
}
