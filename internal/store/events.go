package store

import (
	"database/sql"
	"time"
)

// Event is a service-history entry (detected/apply_started/succeeded/...).
type Event struct {
	ID            int64
	ServiceID     int64
	Kind          string
	RefJobID      *int64
	FromDigest    string
	ToDigest      string
	Message       string
	CreatedAt     time.Time
	ChangelogURL  string
	ChangelogText string
}

// Events is the repository for the service_events table.
type Events struct{ db *sql.DB }

func NewEvents(db *DB) *Events { return &Events{db: db.DB} }

func (e *Events) Insert(ev Event) (int64, error) {
	var refJob sql.NullInt64
	if ev.RefJobID != nil {
		refJob = sql.NullInt64{Int64: *ev.RefJobID, Valid: true}
	}
	res, err := e.db.Exec(
		`INSERT INTO service_events
		   (service_id, kind, ref_job_id, from_digest, to_digest, message)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.ServiceID, ev.Kind, refJob, ev.FromDigest, ev.ToDigest, ev.Message,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListByService returns the service's history events, newest first. Each
// event is LEFT JOINed against updates on (service_id, to_digest). The
// UNIQUE constraint on that pair makes the join 1:1, so the changelog cached
// on the matching update (if any) travels with the event even after the
// update itself has been applied. Events with no matching update (e.g. a
// digest that was never resolved to an update row) still return, with empty
// changelog fields.
func (e *Events) ListByService(serviceID int64) ([]Event, error) {
	rows, err := e.db.Query(
		`SELECT e.id, e.service_id, e.kind, e.ref_job_id, e.from_digest, e.to_digest,
		        e.message, e.created_at,
		        COALESCE(u.changelog_url,''), COALESCE(u.changelog_text,'')
		   FROM service_events e
		   LEFT JOIN updates u
		     ON u.service_id = e.service_id AND u.to_digest = e.to_digest
		  WHERE e.service_id=? ORDER BY e.id DESC`,
		serviceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			ev     Event
			refJob sql.NullInt64
		)
		if err := rows.Scan(
			&ev.ID, &ev.ServiceID, &ev.Kind, &refJob,
			&ev.FromDigest, &ev.ToDigest, &ev.Message, &ev.CreatedAt,
			&ev.ChangelogURL, &ev.ChangelogText,
		); err != nil {
			return nil, err
		}
		if refJob.Valid {
			v := refJob.Int64
			ev.RefJobID = &v
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
