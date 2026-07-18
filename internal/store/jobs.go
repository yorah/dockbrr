package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrJobNotFound is returned by Jobs.Get when no row matches.
var ErrJobNotFound = errors.New("job not found")

// Job is a persisted unit of work for the Job Engine.
type Job struct {
	ID          int64
	Type        string // check|apply|rollback|sync|start|stop|restart|remove
	ProjectID   *int64
	ServiceID   *int64
	Status      string // queued|running|success|failed|canceled
	Scope       string // service|project
	RequestedBy string // user|scheduler
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	ExitCode    *int
	Error       string
}

// Jobs is the repository for the jobs table.
type Jobs struct{ db *sql.DB }

func NewJobs(db *DB) *Jobs { return &Jobs{db: db.DB} }

const jobColumns = `id, type, project_id, service_id, status, scope,
	requested_by, created_at, started_at, finished_at, exit_code, error`

// Enqueue inserts a queued job and returns its id. Empty Status/Scope/RequestedBy
// default to queued/service/user.
func (j *Jobs) Enqueue(job Job) (int64, error) {
	status := job.Status
	if status == "" {
		status = "queued"
	}
	scope := job.Scope
	if scope == "" {
		scope = "service"
	}
	by := job.RequestedBy
	if by == "" {
		by = "user"
	}
	var id int64
	err := j.db.QueryRow(
		`INSERT INTO jobs (type, project_id, service_id, status, scope, requested_by)
		 VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		job.Type, nullI64(job.ProjectID), nullI64(job.ServiceID), status, scope, by,
	).Scan(&id)
	return id, err
}

// ClaimNext atomically flips the oldest queued job to running and returns it.
// ok=false when the queue is empty. The single-writer connection
// (SetMaxOpenConns(1)) plus the atomic UPDATE...RETURNING guarantees no job is
// claimed twice under concurrency.
func (j *Jobs) ClaimNext() (Job, bool, error) {
	row := j.db.QueryRow(
		`UPDATE jobs SET status='running', started_at=CURRENT_TIMESTAMP
		   WHERE id = (SELECT id FROM jobs WHERE status='queued' ORDER BY id LIMIT 1)
		 RETURNING ` + jobColumns,
	)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

// Finish records a terminal job result.
func (j *Jobs) Finish(id int64, status string, exitCode *int, errMsg string) error {
	var code sql.NullInt64
	if exitCode != nil {
		code = sql.NullInt64{Int64: int64(*exitCode), Valid: true}
	}
	_, err := j.db.Exec(
		`UPDATE jobs SET status=?, finished_at=CURRENT_TIMESTAMP, exit_code=?, error=?
		   WHERE id=?`,
		status, code, errMsg, id,
	)
	return err
}

// ResumeRunning re-queues every job left running (e.g. after a crash) so the
// worker pool re-claims it on boot. Returns rows affected.
func (j *Jobs) ResumeRunning() (int64, error) {
	res, err := j.db.Exec(`UPDATE jobs SET status='queued', started_at=NULL WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Get returns the job by id, or ErrJobNotFound.
func (j *Jobs) Get(id int64) (Job, error) {
	row := j.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id=?`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	return job, err
}

// JobListRow is a Job plus the display names the Jobs history screen shows.
// LEFT JOINed, so a job whose service/project has since been deleted comes
// back with an empty name rather than dropping off the list.
type JobListRow struct {
	Job
	ProjectName string
	ServiceName string
}

// List returns the most recent jobs, newest first, with project/service names
// resolved. limit is clamped to (0, 500]; an out-of-range value falls back to 50.
func (j *Jobs) List(limit int) ([]JobListRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := j.db.Query(`
		SELECT j.id, j.type, j.project_id, j.service_id, j.status, j.scope,
		       j.requested_by, j.created_at, j.started_at, j.finished_at, j.exit_code, j.error,
		       COALESCE(p.name, ''), COALESCE(s.name, '')
		  FROM jobs j
		  LEFT JOIN projects p ON p.id = j.project_id
		  LEFT JOIN services s ON s.id = j.service_id
		 ORDER BY j.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []JobListRow
	for rows.Next() {
		var row JobListRow
		job, err := scanJob(rows, &row.ProjectName, &row.ServiceName)
		if err != nil {
			return nil, err
		}
		row.Job = job
		out = append(out, row)
	}
	return out, rows.Err()
}

// finishedStatuses is the terminal set both delete paths purge. Queued and
// running jobs are never removed: the worker still owns them.
const finishedStatuses = `status IN ('success','failed','canceled')`

// DeleteFinished removes every terminal job and, via ON DELETE CASCADE, its
// logs. Snapshots and service events keep their rows (their job_id is set to
// NULL), so rollback (which resolves the latest snapshot by service) is
// unaffected. Returns the number of jobs deleted.
func (j *Jobs) DeleteFinished() (int64, error) {
	res, err := j.db.Exec(`DELETE FROM jobs WHERE ` + finishedStatuses)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteFinishedBefore removes terminal jobs created before t. It matches on
// created_at rather than finished_at: created_at is NOT NULL, so a job that
// reached a terminal status without a finished_at still ages out.
func (j *Jobs) DeleteFinishedBefore(t time.Time) (int64, error) {
	res, err := j.db.Exec(
		`DELETE FROM jobs WHERE `+finishedStatuses+` AND created_at < ?`,
		t.UTC().Format(sqliteTimeLayout),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// sqliteTimeLayout is how SQLite's CURRENT_TIMESTAMP renders created_at, so a
// Go time must be formatted this way to compare lexically against it.
const sqliteTimeLayout = "2006-01-02 15:04:05"

// rowScanner abstracts *sql.Row and *sql.Rows so scanJob services both a
// single QueryRow result (Get/ClaimNext) and List's per-row iteration.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJob scans a jobColumns row into a Job. extra receives any additional
// columns selected after jobColumns (List's joined name columns).
func scanJob(row rowScanner, extra ...any) (Job, error) {
	var (
		job        Job
		projID     sql.NullInt64
		svcID      sql.NullInt64
		startedAt  sql.NullTime
		finishedAt sql.NullTime
		exitCode   sql.NullInt64
	)
	dest := []any{
		&job.ID, &job.Type, &projID, &svcID, &job.Status, &job.Scope,
		&job.RequestedBy, &job.CreatedAt, &startedAt, &finishedAt, &exitCode, &job.Error,
	}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return Job{}, err
	}
	if projID.Valid {
		v := projID.Int64
		job.ProjectID = &v
	}
	if svcID.Valid {
		v := svcID.Int64
		job.ServiceID = &v
	}
	if startedAt.Valid {
		t := startedAt.Time
		job.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		job.FinishedAt = &t
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		job.ExitCode = &v
	}
	return job, nil
}

// nullI64 maps a *int64 to a NULL-able driver value.
func nullI64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// JobLog is one streamed output line for a job.
type JobLog struct {
	ID     int64
	JobID  int64
	Stream string // stdout|stderr|system
	Line   string
	TS     time.Time
}

// JobLogs is the repository for the job_logs table.
type JobLogs struct{ db *sql.DB }

func NewJobLogs(db *DB) *JobLogs { return &JobLogs{db: db.DB} }

func (l *JobLogs) Append(jobID int64, stream, line string) error {
	if stream == "" {
		stream = "stdout"
	}
	_, err := l.db.Exec(
		`INSERT INTO job_logs (job_id, stream, line) VALUES (?, ?, ?)`,
		jobID, stream, line,
	)
	return err
}

// ListByJob returns a job's logs oldest-first (by monotonic id).
func (l *JobLogs) ListByJob(jobID int64) ([]JobLog, error) {
	rows, err := l.db.Query(
		`SELECT id, job_id, ts, stream, line FROM job_logs WHERE job_id=? ORDER BY id ASC`,
		jobID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []JobLog
	for rows.Next() {
		var lg JobLog
		if err := rows.Scan(&lg.ID, &lg.JobID, &lg.TS, &lg.Stream, &lg.Line); err != nil {
			return nil, err
		}
		out = append(out, lg)
	}
	return out, rows.Err()
}
