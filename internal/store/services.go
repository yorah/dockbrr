package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Service is a single service within a project (one row per compose service
// or per standalone container).
type Service struct {
	ID                int64
	ProjectID         int64
	Name              string
	ContainerIDs      []string // persisted as a JSON array
	ImageRef          string
	CurrentDigest     string
	CurrentImageID    string
	ImageVersion      string // running image's org.opencontainers.image.version label ("" if unset)
	Pinned            bool
	Drifted           bool
	State             string
	GoneSince         *time.Time // set on transition into "gone", cleared when seen present again
	Healthcheck       bool
	AutoUpdateEnabled *bool // nullable per-service override
	UpdatedAt         time.Time
}

// Services is the repository for the services table.
type Services struct{ db *sql.DB }

func NewServices(db *DB) *Services { return &Services{db: db.DB} }

// Upsert inserts s, or on (project_id, name) conflict updates only the
// discovery-owned columns, preserving auto_update_enabled. Returns the id.
func (s *Services) Upsert(sv Service) (int64, error) {
	cids := sv.ContainerIDs
	if cids == nil {
		cids = []string{}
	}
	cidsJSON, err := json.Marshal(cids)
	if err != nil {
		return 0, err
	}
	var auto sql.NullBool
	if sv.AutoUpdateEnabled != nil {
		auto = sql.NullBool{Bool: *sv.AutoUpdateEnabled, Valid: true}
	}
	var id int64
	err = s.db.QueryRow(
		`INSERT INTO services
		   (project_id, name, container_ids, image_ref, current_digest,
		    current_image_id, image_version, pinned, drifted, state, gone_since, healthcheck, auto_update_enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
		 ON CONFLICT(project_id, name) DO UPDATE SET
		   container_ids    = excluded.container_ids,
		   image_ref        = excluded.image_ref,
		   current_digest   = excluded.current_digest,
		   current_image_id = excluded.current_image_id,
		   image_version    = excluded.image_version,
		   pinned           = excluded.pinned,
		   drifted          = excluded.drifted,
		   state            = excluded.state,
		   gone_since       = NULL,
		   healthcheck      = excluded.healthcheck,
		   updated_at       = CURRENT_TIMESTAMP
		 RETURNING id`,
		sv.ProjectID, sv.Name, string(cidsJSON), sv.ImageRef, sv.CurrentDigest,
		sv.CurrentImageID, sv.ImageVersion, sv.Pinned, sv.Drifted, sv.State, sv.Healthcheck, auto,
	).Scan(&id)
	return id, err
}

func (s *Services) ListByProject(projectID int64) ([]Service, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, container_ids, image_ref, current_digest,
		        current_image_id, image_version, pinned, drifted, state, gone_since, healthcheck, auto_update_enabled, updated_at
		   FROM services WHERE project_id=? ORDER BY name`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Service
	for rows.Next() {
		var (
			sv        Service
			cidsJSON  string
			auto      sql.NullBool
			goneSince sql.NullTime
		)
		if err := rows.Scan(
			&sv.ID, &sv.ProjectID, &sv.Name, &cidsJSON, &sv.ImageRef, &sv.CurrentDigest,
			&sv.CurrentImageID, &sv.ImageVersion, &sv.Pinned, &sv.Drifted, &sv.State, &goneSince, &sv.Healthcheck, &auto, &sv.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cidsJSON), &sv.ContainerIDs); err != nil {
			return nil, err
		}
		if auto.Valid {
			b := auto.Bool
			sv.AutoUpdateEnabled = &b
		}
		if goneSince.Valid {
			t := goneSince.Time
			sv.GoneSince = &t
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// MarkGone marks a service whose container is no longer present. gone_since
// is set only on the transition into gone (COALESCE preserves the original
// timestamp across repeat calls while the service stays gone).
func (s *Services) MarkGone(id int64) error {
	_, err := s.db.Exec(
		`UPDATE services SET state='gone',
		   gone_since=COALESCE(gone_since, CURRENT_TIMESTAMP),
		   updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`, id)
	return err
}

// ErrServiceNotFound is returned by Services.Get when no row matches.
var ErrServiceNotFound = errors.New("service not found")

// IsStoppedState reports whether a service state permits removal (any
// non-running, non-transitional state).
func IsStoppedState(state string) bool {
	switch state {
	case "exited", "dead", "created":
		return true
	default:
		return false
	}
}

// Get returns the service by id, or ErrServiceNotFound.
func (s *Services) Get(id int64) (Service, error) {
	var (
		sv        Service
		cidsJSON  string
		auto      sql.NullBool
		goneSince sql.NullTime
	)
	err := s.db.QueryRow(
		`SELECT id, project_id, name, container_ids, image_ref, current_digest,
		        current_image_id, image_version, pinned, drifted, state, gone_since, healthcheck, auto_update_enabled, updated_at
		   FROM services WHERE id=?`,
		id,
	).Scan(
		&sv.ID, &sv.ProjectID, &sv.Name, &cidsJSON, &sv.ImageRef, &sv.CurrentDigest,
		&sv.CurrentImageID, &sv.ImageVersion, &sv.Pinned, &sv.Drifted, &sv.State, &goneSince, &sv.Healthcheck, &auto, &sv.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, ErrServiceNotFound
	}
	if err != nil {
		return Service{}, err
	}
	if err := json.Unmarshal([]byte(cidsJSON), &sv.ContainerIDs); err != nil {
		return Service{}, err
	}
	if auto.Valid {
		b := auto.Bool
		sv.AutoUpdateEnabled = &b
	}
	if goneSince.Valid {
		t := goneSince.Time
		sv.GoneSince = &t
	}
	return sv, nil
}

// SetAutoUpdate sets (or clears, with nil) a service's per-service override.
func (s *Services) SetAutoUpdate(id int64, enabled *bool) error {
	var v sql.NullBool
	if enabled != nil {
		v = sql.NullBool{Bool: *enabled, Valid: true}
	}
	_, err := s.db.Exec(
		`UPDATE services SET auto_update_enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		v, id,
	)
	return err
}

// UpdateRuntime refreshes the recreated container ids and running digest after a
// successful apply (compose up recreates containers with new ids). It touches
// only runtime columns, never auto_update_enabled or pinned.
func (s *Services) UpdateRuntime(id int64, containerIDs []string, currentDigest string) error {
	if containerIDs == nil {
		containerIDs = []string{}
	}
	j, err := json.Marshal(containerIDs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE services SET container_ids=?, current_digest=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		string(j), currentDigest, id,
	)
	return err
}

// UpdateImageRef persists a service's tracked image reference (e.g. after a
// cross-tag apply moved it to a newer tag). Discovery would re-derive it on the
// next reconcile; this makes the dashboard reflect it immediately.
func (s *Services) UpdateImageRef(id int64, imageRef string) error {
	_, err := s.db.Exec(`UPDATE services SET image_ref=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, imageRef, id)
	return err
}

// List returns every service (used by the scheduler's CheckAll pass).
func (s *Services) List() ([]Service, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, container_ids, image_ref, current_digest,
		        current_image_id, image_version, pinned, drifted, state, gone_since, healthcheck, auto_update_enabled, updated_at
		   FROM services ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Service
	for rows.Next() {
		var (
			sv        Service
			cidsJSON  string
			auto      sql.NullBool
			goneSince sql.NullTime
		)
		if err := rows.Scan(
			&sv.ID, &sv.ProjectID, &sv.Name, &cidsJSON, &sv.ImageRef, &sv.CurrentDigest,
			&sv.CurrentImageID, &sv.ImageVersion, &sv.Pinned, &sv.Drifted, &sv.State, &goneSince, &sv.Healthcheck, &auto, &sv.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cidsJSON), &sv.ContainerIDs); err != nil {
			return nil, err
		}
		if auto.Valid {
			b := auto.Bool
			sv.AutoUpdateEnabled = &b
		}
		if goneSince.Valid {
			t := goneSince.Time
			sv.GoneSince = &t
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// Delete removes a service row. FK cascade removes its updates, snapshots,
// and events (ON DELETE CASCADE in the schema).
func (s *Services) Delete(id int64) error {
	_, err := s.db.Exec(`DELETE FROM services WHERE id=?`, id)
	return err
}
