package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Project is a discovered or manually-registered compose project (or a
// standalone container surfaced as a one-service project).
type Project struct {
	ID                int64
	HostID            int64
	Kind              string // compose|standalone
	Name              string
	WorkingDir        string
	ConfigFiles       []string // persisted as a JSON array
	Source            string   // discovered|manual
	AutoUpdateEnabled bool
	UpdatePolicy      string // raw JSON
	Unmanaged         bool   // compose files missing/unreadable; apply is refused (design §7)
	AutoNamed         bool   // standalone container name was Docker-assigned (adjective_surname); UI groups these as "Loose"
	LastSyncedAt      *time.Time
	CreatedAt         time.Time
}

// Projects is the repository for the projects table.
type Projects struct{ db *sql.DB }

func NewProjects(db *DB) *Projects { return &Projects{db: db.DB} }

// Upsert inserts pr, or on (host_id, name) conflict updates only the
// discovery-owned columns (kind, working_dir, config_files, last_synced_at),
// preserving source, auto_update_enabled, and update_policy. Returns the id.
func (p *Projects) Upsert(pr Project) (int64, error) {
	cf := pr.ConfigFiles
	if cf == nil {
		cf = []string{}
	}
	cfJSON, err := json.Marshal(cf)
	if err != nil {
		return 0, err
	}
	source := pr.Source
	if source == "" {
		source = "discovered"
	}
	var id int64
	err = p.db.QueryRow(
		`INSERT INTO projects (host_id, kind, name, working_dir, config_files, source, auto_update_enabled, last_synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host_id, name) DO UPDATE SET
		   kind           = excluded.kind,
		   working_dir    = excluded.working_dir,
		   config_files   = excluded.config_files,
		   last_synced_at = excluded.last_synced_at
		 RETURNING id`,
		pr.HostID, pr.Kind, pr.Name, pr.WorkingDir, string(cfJSON), source, pr.AutoUpdateEnabled, pr.LastSyncedAt,
	).Scan(&id)
	return id, err
}

func (p *Projects) List() ([]Project, error) {
	rows, err := p.db.Query(
		`SELECT id, host_id, kind, name, working_dir, config_files, source,
		        auto_update_enabled, update_policy, unmanaged, auto_named, last_synced_at, created_at
		   FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		var (
			pr        Project
			cfJSON    string
			lastSync  sql.NullTime
			autoUpd   int
			unmanaged int
			autoNamed int
		)
		if err := rows.Scan(
			&pr.ID, &pr.HostID, &pr.Kind, &pr.Name, &pr.WorkingDir, &cfJSON,
			&pr.Source, &autoUpd, &pr.UpdatePolicy, &unmanaged, &autoNamed, &lastSync, &pr.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cfJSON), &pr.ConfigFiles); err != nil {
			return nil, err
		}
		pr.AutoUpdateEnabled = autoUpd != 0
		pr.Unmanaged = unmanaged != 0
		pr.AutoNamed = autoNamed != 0
		if lastSync.Valid {
			t := lastSync.Time
			pr.LastSyncedAt = &t
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// ErrProjectNotFound is returned by Projects.Get when no row matches.
var ErrProjectNotFound = errors.New("project not found")

// Get returns the project by id, or ErrProjectNotFound.
func (p *Projects) Get(id int64) (Project, error) {
	var (
		pr        Project
		cfJSON    string
		lastSync  sql.NullTime
		autoUpd   int
		unmanaged int
		autoNamed int
	)
	err := p.db.QueryRow(
		`SELECT id, host_id, kind, name, working_dir, config_files, source,
		        auto_update_enabled, update_policy, unmanaged, auto_named, last_synced_at, created_at
		   FROM projects WHERE id=?`,
		id,
	).Scan(
		&pr.ID, &pr.HostID, &pr.Kind, &pr.Name, &pr.WorkingDir, &cfJSON,
		&pr.Source, &autoUpd, &pr.UpdatePolicy, &unmanaged, &autoNamed, &lastSync, &pr.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrProjectNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if err := json.Unmarshal([]byte(cfJSON), &pr.ConfigFiles); err != nil {
		return Project{}, err
	}
	pr.AutoUpdateEnabled = autoUpd != 0
	pr.Unmanaged = unmanaged != 0
	pr.AutoNamed = autoNamed != 0
	if lastSync.Valid {
		t := lastSync.Time
		pr.LastSyncedAt = &t
	}
	return pr, nil
}

// SetAutoUpdate flips a project's auto-update flag.
func (p *Projects) SetAutoUpdate(id int64, enabled bool) error {
	_, err := p.db.Exec(`UPDATE projects SET auto_update_enabled=? WHERE id=?`, enabled, id)
	return err
}

// Delete removes a project row (its services cascade).
func (p *Projects) Delete(id int64) error {
	_, err := p.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

// SetUnmanaged flags a project whose compose files are missing/unreadable.
// Apply is refused for unmanaged projects (design §7).
func (p *Projects) SetUnmanaged(id int64, v bool) error {
	_, err := p.db.Exec(`UPDATE projects SET unmanaged=? WHERE id=?`, v, id)
	return err
}

// SetAutoNamed flags a project whose standalone container name was assigned by
// Docker (adjective_surname). Drives the "Loose" grouping in the UI. Idempotent
// and recomputed on every reconcile.
func (p *Projects) SetAutoNamed(id int64, v bool) error {
	_, err := p.db.Exec(`UPDATE projects SET auto_named=? WHERE id=?`, v, id)
	return err
}

// EffectiveAutoUpdate is the auto-apply gate: the project flag must be on, the
// service must not veto it, and the service must not be GENUINELY digest-pinned.
// A pin counts as genuine only when the running digest matches the compose
// file (not Drifted): a user who pinned a digest in their file is left alone,
// but a service dockbrr fallback-pinned at runtime (its file still tracks a
// tag -> Drifted) stays auto-updatable so it keeps following its tag.
// A nil service override INHERITS the project setting; an explicit false vetoes.
// Default-safe: projects default OFF, so a fresh install never auto-applies.
func EffectiveAutoUpdate(p Project, s Service) bool {
	if !p.AutoUpdateEnabled || (s.Pinned && !s.Drifted) {
		return false
	}
	return s.AutoUpdateEnabled == nil || *s.AutoUpdateEnabled
}
