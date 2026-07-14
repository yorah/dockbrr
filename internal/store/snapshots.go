package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrSnapshotNotFound is returned by Snapshots.GetLatestForService when the
// service has no snapshot.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// Snapshot is the pre-mutation state captured before an apply. It is the
// rollback source: no mutation ever runs without one of these first.
type Snapshot struct {
	ID                   int64
	ServiceID            int64
	JobID                *int64
	PrevRepo             string
	PrevDigest           string
	PrevImageID          string
	PrevContainerInspect string // JSON
	ComposeFileHash      string
	ComposeBlob          *string
	CreatedAt            time.Time
}

// Snapshots is the repository for the state_snapshots table.
type Snapshots struct{ db *sql.DB }

func NewSnapshots(db *DB) *Snapshots { return &Snapshots{db: db.DB} }

// Insert writes a pre-mutation snapshot and returns its id.
func (s *Snapshots) Insert(sn Snapshot) (int64, error) {
	inspect := sn.PrevContainerInspect
	if inspect == "" {
		inspect = "{}"
	}
	var blob sql.NullString
	if sn.ComposeBlob != nil {
		blob = sql.NullString{String: *sn.ComposeBlob, Valid: true}
	}
	var id int64
	err := s.db.QueryRow(
		`INSERT INTO state_snapshots
		   (service_id, job_id, prev_repo, prev_digest, prev_image_id,
		    prev_container_inspect, compose_file_hash, compose_blob)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		sn.ServiceID, nullI64(sn.JobID), sn.PrevRepo, sn.PrevDigest, sn.PrevImageID,
		inspect, sn.ComposeFileHash, blob,
	).Scan(&id)
	return id, err
}

// GetLatestForService returns the newest snapshot for a service (by id), or
// ErrSnapshotNotFound.
func (s *Snapshots) GetLatestForService(serviceID int64) (Snapshot, error) {
	var (
		sn     Snapshot
		jobID  sql.NullInt64
		blob   sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT id, service_id, job_id, prev_repo, prev_digest, prev_image_id,
		        prev_container_inspect, compose_file_hash, compose_blob, created_at
		   FROM state_snapshots WHERE service_id=? ORDER BY id DESC LIMIT 1`,
		serviceID,
	).Scan(
		&sn.ID, &sn.ServiceID, &jobID, &sn.PrevRepo, &sn.PrevDigest, &sn.PrevImageID,
		&sn.PrevContainerInspect, &sn.ComposeFileHash, &blob, &sn.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, ErrSnapshotNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	if jobID.Valid {
		v := jobID.Int64
		sn.JobID = &v
	}
	if blob.Valid {
		v := blob.String
		sn.ComposeBlob = &v
	}
	return sn, nil
}
