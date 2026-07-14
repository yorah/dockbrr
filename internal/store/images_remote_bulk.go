package store

import "database/sql"

// All returns every cached remote-resolution state keyed by [repo, tag].
// One query; used by the projects endpoint to annotate services without
// an N+1 per row.
func (r *RemoteStates) All() (map[[2]string]RemoteState, error) {
	rows, err := r.db.Query(
		`SELECT repo, tag, remote_digest, resolved_at, manifest_labels, status
		   FROM image_remote_state`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[[2]string]RemoteState)
	for rows.Next() {
		var (
			s          RemoteState
			resolvedAt sql.NullTime
		)
		if err := rows.Scan(&s.Repo, &s.Tag, &s.RemoteDigest, &resolvedAt, &s.ManifestLabels, &s.Status); err != nil {
			return nil, err
		}
		if resolvedAt.Valid {
			t := resolvedAt.Time
			s.ResolvedAt = &t
		}
		out[[2]string{s.Repo, s.Tag}] = s
	}
	return out, rows.Err()
}
