package store

import (
	"database/sql"
	"errors"
	"time"
)

// ChangelogRepos caches the image->GitHub-repo resolution (including negative
// results) so the changelog GitHubSource does not re-query GitHub on every new
// update detection. Keyed by the image repo string as written in the service's
// image ref.
type ChangelogRepos struct{ db *sql.DB }

func NewChangelogRepos(db *DB) *ChangelogRepos { return &ChangelogRepos{db: db.DB} }

// Get returns the cached resolution for an image repo. found reports whether a
// row exists AND is within ttl; positive reports owner != "" (a real repo, vs a
// cached negative). A stale row (older than ttl) reports found=false so the
// caller re-resolves.
func (r *ChangelogRepos) Get(repo string, ttl time.Duration) (owner, name string, positive, found bool, err error) {
	var resolvedAt int64
	err = r.db.QueryRow(
		`SELECT owner, name, resolved_at FROM changelog_repo_cache WHERE image_repo=?`,
		repo,
	).Scan(&owner, &name, &resolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, false, nil
	}
	if err != nil {
		return "", "", false, false, err
	}
	if time.Since(time.Unix(resolvedAt, 0)) > ttl {
		return "", "", false, false, nil
	}
	return owner, name, owner != "", true, nil
}

// Put upserts a resolution. An empty owner records a negative result (no GitHub
// repo). resolved_at is stamped to now.
func (r *ChangelogRepos) Put(repo, owner, name string) error {
	_, err := r.db.Exec(
		`INSERT INTO changelog_repo_cache (image_repo, owner, name, resolved_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(image_repo) DO UPDATE SET
		   owner       = excluded.owner,
		   name        = excluded.name,
		   resolved_at = excluded.resolved_at`,
		repo, owner, name, time.Now().Unix(),
	)
	return err
}
