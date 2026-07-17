package store

import (
	"database/sql"
	"errors"
	"time"
)

// Update is a detected available (or dismissed/applied/...) image update for a
// service.
type Update struct {
	ID              int64
	ServiceID       int64
	FromDigest      string
	ToDigest        string
	FromVersion     string
	ToVersion       string
	Tag             string
	Severity        string // major|minor|patch|digest-only
	ChangelogURL    string
	ChangelogText   string
	ChangelogStatus string // "" (resolved/absent) | "rate_limited"
	Status          string // available|dismissed|applied|failed|superseded|rolled_back
	DetectedAt      time.Time
	// AppliedAt is set by MarkApplied when an update is applied. Nil for rows
	// that predate the applied_at column (migration 0007) or that have never
	// been applied. Callers that need "when was this applied" must treat nil
	// as unknown, not as "never applied" (status is the source of truth for
	// that). The converse also holds: non-nil only records that an apply once
	// happened, NOT that the update is applied now. RecordDrift re-opens an
	// applied row to 'available' and MarkRolledBack flips it to 'rolled_back',
	// both leaving applied_at populated. Status stays authoritative.
	AppliedAt *time.Time
}

// Updates is the repository for the updates table.
type Updates struct{ db *sql.DB }

func NewUpdates(db *DB) *Updates { return &Updates{db: db.DB} }

// Upsert inserts up, or on (service_id, to_digest) conflict refreshes the
// mutable columns (preserving detected_at). Returns the id.
//
// On conflict the existing row's status is preserved (a dismissed/applied
// update is not resurrected to available); all other mutable columns are
// refreshed.
func (u *Updates) Upsert(up Update) (int64, error) {
	severity := up.Severity
	if severity == "" {
		severity = "digest-only"
	}
	status := up.Status
	if status == "" {
		status = "available"
	}
	var id int64
	err := u.db.QueryRow(
		`INSERT INTO updates
		   (service_id, from_digest, to_digest, from_version, to_version,
		    tag, severity, changelog_url, changelog_text, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(service_id, to_digest) DO UPDATE SET
		   from_digest    = excluded.from_digest,
		   from_version   = excluded.from_version,
		   to_version     = excluded.to_version,
		   tag            = excluded.tag,
		   severity       = excluded.severity
		   -- changelog_url / changelog_text preserved (written only via SetChangelog);
		   -- status / detected_at also preserved on conflict
		 RETURNING id`,
		up.ServiceID, up.FromDigest, up.ToDigest, up.FromVersion, up.ToVersion,
		up.Tag, severity, up.ChangelogURL, up.ChangelogText, status,
	).Scan(&id)
	return id, err
}

func (u *Updates) ListOpen() ([]Update, error) {
	rows, err := u.db.Query(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates WHERE status='available' ORDER BY detected_at DESC, id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var up Update
		var appliedAt sql.NullTime
		if err := rows.Scan(
			&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
			&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL,
			&up.ChangelogText, &up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
		); err != nil {
			return nil, err
		}
		if appliedAt.Valid {
			t := appliedAt.Time
			up.AppliedAt = &t
		}
		out = append(out, up)
	}
	return out, rows.Err()
}

// ListVisible returns the updates the dashboard surfaces: available
// (actionable) plus dismissed (acknowledged by the user, but kept visible
// with a Dismissed badge so they stay restorable) plus rolled_back (a
// user-initiated rollback, kept visible so the history is not lost).
// Callers meaning strictly "actionable" should use ListOpen instead.
func (u *Updates) ListVisible() ([]Update, error) {
	rows, err := u.db.Query(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates WHERE status IN ('available','dismissed','rolled_back') ORDER BY detected_at DESC, id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var up Update
		var appliedAt sql.NullTime
		if err := rows.Scan(
			&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
			&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL,
			&up.ChangelogText, &up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
		); err != nil {
			return nil, err
		}
		if appliedAt.Valid {
			t := appliedAt.Time
			up.AppliedAt = &t
		}
		out = append(out, up)
	}
	return out, rows.Err()
}

// SupersedePriorOpen marks every still-available update for serviceID whose
// to_digest differs from keepToDigest as superseded. Returns rows affected.
func (u *Updates) SupersedePriorOpen(serviceID int64, keepToDigest string) (int64, error) {
	res, err := u.db.Exec(
		`UPDATE updates SET status='superseded'
		   WHERE service_id=? AND status='available' AND to_digest<>?`,
		serviceID, keepToDigest,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SupersedeAllOpen marks every still-available update for serviceID as
// superseded. Called when detection finds the service up to date: the running
// image matches the tracked tag's remote, so no available update is actionable.
// This closes a row left open when the container reached its target outside
// RecordDrift/MarkApplied, e.g. dockbrr updating its OWN container (the recreate
// kills the process before MarkApplied runs) or an image updated outside
// dockbrr. dismissed and rolled_back rows (both user intent) are left untouched.
// Returns rows affected.
func (u *Updates) SupersedeAllOpen(serviceID int64) (int64, error) {
	res, err := u.db.Exec(
		`UPDATE updates SET status='superseded'
		   WHERE service_id=? AND status='available'`,
		serviceID,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecordDrift upserts the (service_id, to_digest) update and supersedes the
// service's other still-open updates, all in ONE transaction, and reports
// whether the update row was newly created. On an existing row it refreshes the
// mutable descriptor columns (from_digest/from_version/to_version/tag/severity);
// detected_at and the cached changelog are preserved. A blank incoming
// from_version/to_version (or digest-only severity) does NOT overwrite a
// non-blank existing value, so the cheap digest-only cache-hit cycle can't wipe
// the names a network cycle resolved. Status is
// also preserved, except that 'failed' (transient apply failure), 'superseded'
// (a newer target that has since flapped back to this digest), and 'applied'
// (the service has diverged from its applied target again, e.g. a recreate,
// so the drift is pending once more) are flipped to 'available' so a
// still-current drift is never hidden, while 'dismissed' and 'rolled_back'
// (both user intent) are deliberately kept unchanged. This makes re-detection
// idempotent: a repeated digest returns isNew=false so the caller suppresses a
// duplicate `detected` event.
func (u *Updates) RecordDrift(up Update) (id int64, isNew bool, err error) {
	severity := up.Severity
	if severity == "" {
		severity = "digest-only"
	}
	status := up.Status
	if status == "" {
		status = "available"
	}
	tx, err := u.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var existing int64
	e := tx.QueryRow(
		`SELECT id FROM updates WHERE service_id=? AND to_digest=?`,
		up.ServiceID, up.ToDigest,
	).Scan(&existing)
	switch {
	case e == nil:
		// Refresh descriptor columns. Additionally, re-detecting this exact digest
		// means the drift is current again, so a prior FAILED apply (transient),
		// SUPERSEDED state (a newer target that has since flapped back), or
		// APPLIED state (the service diverged from its applied target again,
		// e.g. a recreate, so the drift is pending once more) is re-opened,
		// otherwise the drift stays invisible on the dashboard. The tail supersede
		// below then demotes any sibling that is no longer current, leaving exactly
		// one available row. dismissed and rolled_back (both user intent) are
		// deliberately preserved.
		// Version columns and severity are preserved when the incoming values are
		// blank/digest-only: a cache-hit detect cycle re-detects the same to_digest
		// via the cheap digest-only fast path, which carries no version info. Since
		// a version is a pure function of the (keyed) to_digest, a blank incoming
		// means "not computed this cycle", never "changed to blank", so wiping a
		// name the network cycle resolved (e.g. a floating "latest" reverse-named to
		// v1.13.0 -> v1.14.1) would be wrong. A non-blank incoming still overwrites.
		if _, err = tx.Exec(
			`UPDATE updates SET from_digest=?,
			        from_version = CASE WHEN ?='' THEN from_version ELSE ? END,
			        to_version   = CASE WHEN ?='' THEN to_version   ELSE ? END,
			        tag=?,
			        severity = CASE WHEN ?='digest-only' AND severity IN ('major','minor','patch') THEN severity ELSE ? END,
			        status = CASE WHEN status IN ('failed','superseded','applied') THEN 'available' ELSE status END
			  WHERE id=?`,
			up.FromDigest,
			up.FromVersion, up.FromVersion,
			up.ToVersion, up.ToVersion,
			up.Tag,
			severity, severity,
			existing,
		); err != nil {
			return 0, false, err
		}
		id, isNew = existing, false
	case errors.Is(e, sql.ErrNoRows):
		if err = tx.QueryRow(
			`INSERT INTO updates
			   (service_id, from_digest, to_digest, from_version, to_version,
			    tag, severity, changelog_url, changelog_text, status)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			up.ServiceID, up.FromDigest, up.ToDigest, up.FromVersion, up.ToVersion,
			up.Tag, severity, up.ChangelogURL, up.ChangelogText, status,
		).Scan(&id); err != nil {
			return 0, false, err
		}
		isNew = true
	default:
		err = e
		return 0, false, err
	}

	if _, err = tx.Exec(
		`UPDATE updates SET status='superseded'
		   WHERE service_id=? AND status='available' AND to_digest<>?`,
		up.ServiceID, up.ToDigest,
	); err != nil {
		return 0, false, err
	}
	err = tx.Commit()
	return id, isNew, err
}

// MarkRolledBack flips a service's APPLIED update at toDigest to rolled_back
// (a user-initiated rollback). Gated on status='applied' so it only demotes
// the row the rollback actually reverted; rolled_back is preserved by
// RecordDrift and excluded from the auto-apply path.
func (u *Updates) MarkRolledBack(serviceID int64, toDigest string) error {
	_, err := u.db.Exec(
		`UPDATE updates SET status='rolled_back'
		   WHERE service_id=? AND to_digest=? AND status='applied'`,
		serviceID, toDigest)
	return err
}

// SetChangelog persists the resolved changelog url + text on the update row and
// clears changelog_status (a successful resolve supersedes a prior rate-limit).
func (u *Updates) SetChangelog(updateID int64, url, text string) error {
	_, err := u.db.Exec(
		`UPDATE updates SET changelog_url=?, changelog_text=?, changelog_status='' WHERE id=?`,
		url, text, updateID,
	)
	return err
}

// SetChangelogStatus records a non-content changelog outcome ("rate_limited")
// on the update row, leaving changelog_url/text untouched. Used when the
// resolve chain produced no content because GitHub throttled it.
func (u *Updates) SetChangelogStatus(updateID int64, status string) error {
	_, err := u.db.Exec(
		`UPDATE updates SET changelog_status=? WHERE id=?`,
		status, updateID,
	)
	return err
}

// ErrNoOpenUpdate is returned by GetLatestOpenByService when the service has no
// available update.
var ErrNoOpenUpdate = errors.New("no open update for service")

// GetLatestOpenByService returns the newest available update for a service, or
// ErrNoOpenUpdate.
func (u *Updates) GetLatestOpenByService(serviceID int64) (Update, error) {
	var up Update
	var appliedAt sql.NullTime
	err := u.db.QueryRow(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates WHERE service_id=? AND status='available'
		 ORDER BY detected_at DESC, id DESC LIMIT 1`,
		serviceID,
	).Scan(
		&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
		&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL, &up.ChangelogText,
		&up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Update{}, ErrNoOpenUpdate
	}
	if appliedAt.Valid {
		t := appliedAt.Time
		up.AppliedAt = &t
	}
	return up, err
}

// ListLastAppliedByService returns, for each service that has ever had an
// update applied, that service's most recently APPLIED update, changelog
// columns included. Ordering prefers applied_at (when the apply actually ran,
// via MarkApplied); rows applied before migration 0007 introduced that column
// have a NULL applied_at and fall back to detected_at, so legacy data keeps
// its old (approximate) ordering instead of erroring or vanishing. It is the
// read path behind the dashboard's "last applied changelog": an applied
// update leaves ListVisible, but its cached changelog stays on the row
// (SetStatus/MarkApplied never clear it), so the dashboard can keep showing
// it once the pending update is gone.
//
// Only status='applied' rows are considered; a row RecordDrift has flipped back
// to 'available' is therefore (correctly) no longer the last applied one. It is
// pending again and the dashboard renders it via the normal updates list.
func (u *Updates) ListLastAppliedByService() ([]Update, error) {
	rows, err := u.db.Query(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates
		  WHERE status='applied'
		    AND id = (SELECT id FROM updates u2
		               WHERE u2.service_id = updates.service_id AND u2.status='applied'
		               ORDER BY COALESCE(u2.applied_at, u2.detected_at) DESC, u2.id DESC LIMIT 1)
		  ORDER BY service_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Update
	for rows.Next() {
		var up Update
		var appliedAt sql.NullTime
		if err := rows.Scan(
			&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
			&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL,
			&up.ChangelogText, &up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
		); err != nil {
			return nil, err
		}
		if appliedAt.Valid {
			t := appliedAt.Time
			up.AppliedAt = &t
		}
		out = append(out, up)
	}
	return out, rows.Err()
}

// SetStatus updates only the status column of an update (never touches the
// cached changelog columns, cf. the Phase-4 conflict-clobber fix).
func (u *Updates) SetStatus(updateID int64, status string) error {
	_, err := u.db.Exec(`UPDATE updates SET status=? WHERE id=?`, status, updateID)
	return err
}

// MarkApplied flips an update to status='applied' and stamps applied_at with
// the current time, in one statement. This is the only way applied_at is
// ever set, so use it (never SetStatus) wherever an apply succeeds, so that
// ListLastAppliedByService can order by actual apply time instead of falling
// back to detected_at.
func (u *Updates) MarkApplied(updateID int64) error {
	_, err := u.db.Exec(
		`UPDATE updates SET status='applied', applied_at=CURRENT_TIMESTAMP WHERE id=?`,
		updateID,
	)
	return err
}

// ErrUpdateNotFound is returned by Updates.Get when no row matches.
var ErrUpdateNotFound = errors.New("update not found")

// Get returns the update by id, or ErrUpdateNotFound.
func (u *Updates) Get(id int64) (Update, error) {
	var up Update
	var appliedAt sql.NullTime
	err := u.db.QueryRow(
		`SELECT id, service_id, from_digest, to_digest, from_version, to_version,
		        tag, severity, changelog_url, changelog_text, changelog_status, status, detected_at, applied_at
		   FROM updates WHERE id=?`,
		id,
	).Scan(
		&up.ID, &up.ServiceID, &up.FromDigest, &up.ToDigest, &up.FromVersion,
		&up.ToVersion, &up.Tag, &up.Severity, &up.ChangelogURL, &up.ChangelogText,
		&up.ChangelogStatus, &up.Status, &up.DetectedAt, &appliedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Update{}, ErrUpdateNotFound
	}
	if appliedAt.Valid {
		t := appliedAt.Time
		up.AppliedAt = &t
	}
	return up, err
}
