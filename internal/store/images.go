package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrImageNotFound is returned by Images.GetByDigest when no row matches.
var ErrImageNotFound = errors.New("image not found")

// ErrRemoteStateNotFound is returned by RemoteStates.Get when no row matches.
var ErrRemoteStateNotFound = errors.New("remote state not found")

// Image is an observed image identity (repo:tag@digest plus OCI metadata).
type Image struct {
	ID        int64
	Repo      string
	Tag       string
	Digest    string
	MediaType string
	OS        string
	Arch      string
	Size      int64
	BuiltAt   *time.Time
	Labels    string // raw JSON object
	SourceURL string
	Revision  string
	// ResolvedVersion is the release version reverse-looked from this image's
	// digest for a floating tag (latest) that carries no semver. Empty until
	// detection resolves it; owned by SetResolvedVersion, never by Upsert.
	ResolvedVersion string
	// VersionResolved is true once detection has attempted to name this image's
	// floating-tag version (a match, a label fallback, or a conclusive no-match).
	// It gates re-scanning so an unnameable digest is not re-HEADed every cycle.
	VersionResolved bool
}

// Images is the repository for the images table.
type Images struct{ db *sql.DB }

func NewImages(db *DB) *Images { return &Images{db: db.DB} }

// Upsert inserts img, or on (repo, digest) conflict refreshes the mutable
// metadata columns and last_seen, preserving first_seen. Returns the id.
func (i *Images) Upsert(img Image) (int64, error) {
	labels := img.Labels
	if labels == "" {
		labels = "{}"
	}
	var id int64
	err := i.db.QueryRow(
		`INSERT INTO images
		   (repo, tag, digest, media_type, os, arch, size, built_at,
		    labels, source_url, revision, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(repo, digest) DO UPDATE SET
		   tag        = excluded.tag,
		   media_type = excluded.media_type,
		   os         = excluded.os,
		   arch       = excluded.arch,
		   size       = excluded.size,
		   built_at   = excluded.built_at,
		   labels     = excluded.labels,
		   source_url = excluded.source_url,
		   revision   = excluded.revision,
		   last_seen  = CURRENT_TIMESTAMP
		 RETURNING id`,
		img.Repo, img.Tag, img.Digest, img.MediaType, img.OS, img.Arch,
		img.Size, img.BuiltAt, labels, img.SourceURL, img.Revision,
	).Scan(&id)
	return id, err
}

func (i *Images) GetByDigest(repo, digest string) (Image, error) {
	var (
		img     Image
		builtAt sql.NullTime
	)
	err := i.db.QueryRow(
		`SELECT id, repo, tag, digest, media_type, os, arch, size, built_at,
		        labels, source_url, revision, resolved_version, version_resolved
		   FROM images WHERE repo=? AND digest=?`,
		repo, digest,
	).Scan(
		&img.ID, &img.Repo, &img.Tag, &img.Digest, &img.MediaType, &img.OS,
		&img.Arch, &img.Size, &builtAt, &img.Labels, &img.SourceURL, &img.Revision,
		&img.ResolvedVersion, &img.VersionResolved,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, ErrImageNotFound
	}
	if err != nil {
		return Image{}, err
	}
	if builtAt.Valid {
		t := builtAt.Time
		img.BuiltAt = &t
	}
	return img, nil
}

// SetResolvedVersion records the reverse-looked release version for (repo,
// digest) and marks the image as version-resolved (preventing re-scan next
// cycle). An empty version is a valid cache entry (negative cache = conclusive
// no-match). A no-op when no image row matches (best effort; the row is written
// by Upsert first). Owns the resolved_version and version_resolved columns so
// Upsert can leave them untouched and never clobber a resolved value on a
// metadata refresh.
func (i *Images) SetResolvedVersion(repo, digest, version string) error {
	_, err := i.db.Exec(
		`UPDATE images SET resolved_version=?, version_resolved=1 WHERE repo=? AND digest=?`,
		version, repo, digest,
	)
	return err
}

// RemoteState caches the last remote-resolution outcome for a (repo, tag).
type RemoteState struct {
	Repo           string
	Tag            string
	RemoteDigest   string
	ResolvedAt     *time.Time
	ManifestLabels string // raw JSON object
	Status         string // ok|rate_limited|error|not_found|local
}

// RemoteStates is the repository for the image_remote_state table.
type RemoteStates struct{ db *sql.DB }

func NewRemoteStates(db *DB) *RemoteStates { return &RemoteStates{db: db.DB} }

// Upsert inserts or fully replaces the remote-state row for (repo, tag).
// An empty Status is promoted to "ok"; callers should pass an explicit status
// from the enum ok|rate_limited|error|not_found|local.
func (r *RemoteStates) Upsert(s RemoteState) error {
	labels := s.ManifestLabels
	if labels == "" {
		labels = "{}"
	}
	status := s.Status
	if status == "" {
		status = "ok"
	}
	_, err := r.db.Exec(
		`INSERT INTO image_remote_state
		   (repo, tag, remote_digest, resolved_at, manifest_labels, status)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo, tag) DO UPDATE SET
		   remote_digest   = excluded.remote_digest,
		   resolved_at     = excluded.resolved_at,
		   manifest_labels = excluded.manifest_labels,
		   status          = excluded.status`,
		s.Repo, s.Tag, s.RemoteDigest, s.ResolvedAt, labels, status,
	)
	return err
}

// Invalidate drops the cached remote state for (repo, tag) so the next detect
// does a full network resolve + semver scan instead of the digest-only
// short-circuit. Called when a service's running image changed (recreate).
func (r *RemoteStates) Invalidate(repo, tag string) error {
	_, err := r.db.Exec(`DELETE FROM image_remote_state WHERE repo=? AND tag=?`, repo, tag)
	return err
}

func (r *RemoteStates) Get(repo, tag string) (RemoteState, error) {
	var (
		s          RemoteState
		resolvedAt sql.NullTime
	)
	err := r.db.QueryRow(
		`SELECT repo, tag, remote_digest, resolved_at, manifest_labels, status
		   FROM image_remote_state WHERE repo=? AND tag=?`,
		repo, tag,
	).Scan(&s.Repo, &s.Tag, &s.RemoteDigest, &resolvedAt, &s.ManifestLabels, &s.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteState{}, ErrRemoteStateNotFound
	}
	if err != nil {
		return RemoteState{}, err
	}
	if resolvedAt.Valid {
		t := resolvedAt.Time
		s.ResolvedAt = &t
	}
	return s, nil
}
