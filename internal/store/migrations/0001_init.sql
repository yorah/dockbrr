CREATE TABLE hosts (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL,
  type        TEXT NOT NULL DEFAULT 'local',   -- local|tcp|ssh|agent
  socket_addr TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE projects (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  host_id             INTEGER NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  kind                TEXT NOT NULL DEFAULT 'compose',     -- compose|standalone
  name                TEXT NOT NULL,
  working_dir         TEXT NOT NULL DEFAULT '',
  config_files        TEXT NOT NULL DEFAULT '[]',          -- json array
  source              TEXT NOT NULL DEFAULT 'discovered',  -- discovered|manual
  auto_update_enabled INTEGER NOT NULL DEFAULT 0,
  update_policy       TEXT NOT NULL DEFAULT '{}',          -- json
  last_synced_at      TIMESTAMP,
  created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(host_id, name)
);

CREATE TABLE services (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id          INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name                TEXT NOT NULL,
  container_ids       TEXT NOT NULL DEFAULT '[]',  -- json array
  image_ref           TEXT NOT NULL DEFAULT '',
  current_digest      TEXT NOT NULL DEFAULT '',
  current_image_id    TEXT NOT NULL DEFAULT '',
  pinned              INTEGER NOT NULL DEFAULT 0,
  state               TEXT NOT NULL DEFAULT '',
  healthcheck         INTEGER NOT NULL DEFAULT 0,
  auto_update_enabled INTEGER,                     -- nullable override
  updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(project_id, name)
);

CREATE TABLE images (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  repo        TEXT NOT NULL,
  tag         TEXT NOT NULL DEFAULT '',
  digest      TEXT NOT NULL,
  media_type  TEXT NOT NULL DEFAULT '',
  os          TEXT NOT NULL DEFAULT '',
  arch        TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL DEFAULT 0,
  built_at    TIMESTAMP,
  labels      TEXT NOT NULL DEFAULT '{}',
  source_url  TEXT NOT NULL DEFAULT '',
  revision    TEXT NOT NULL DEFAULT '',
  first_seen  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_seen   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(repo, digest)
);

CREATE TABLE image_remote_state (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  repo            TEXT NOT NULL,
  tag             TEXT NOT NULL,
  remote_digest   TEXT NOT NULL DEFAULT '',
  resolved_at     TIMESTAMP,
  manifest_labels TEXT NOT NULL DEFAULT '{}',
  status          TEXT NOT NULL DEFAULT 'ok',  -- ok|rate_limited|error
  UNIQUE(repo, tag)
);

CREATE TABLE updates (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  service_id     INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  from_digest    TEXT NOT NULL DEFAULT '',
  to_digest      TEXT NOT NULL,
  from_version   TEXT NOT NULL DEFAULT '',
  to_version     TEXT NOT NULL DEFAULT '',
  tag            TEXT NOT NULL DEFAULT '',
  severity       TEXT NOT NULL DEFAULT 'digest-only', -- major|minor|patch|digest-only
  changelog_url  TEXT NOT NULL DEFAULT '',
  changelog_text TEXT NOT NULL DEFAULT '',
  detected_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  status         TEXT NOT NULL DEFAULT 'available',   -- available|dismissed|applied|failed|superseded
  UNIQUE(service_id, to_digest)
);

CREATE TABLE jobs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  type         TEXT NOT NULL,                  -- check|apply|rollback|sync
  project_id   INTEGER REFERENCES projects(id) ON DELETE SET NULL,
  service_id   INTEGER REFERENCES services(id) ON DELETE SET NULL,
  status       TEXT NOT NULL DEFAULT 'queued', -- queued|running|success|failed|canceled
  scope        TEXT NOT NULL DEFAULT 'service',-- service|project
  requested_by TEXT NOT NULL DEFAULT 'user',   -- user|scheduler
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  started_at   TIMESTAMP,
  finished_at  TIMESTAMP,
  exit_code    INTEGER,
  error        TEXT NOT NULL DEFAULT ''
);

CREATE TABLE job_logs (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  ts     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  stream TEXT NOT NULL DEFAULT 'stdout',       -- stdout|stderr|system
  line   TEXT NOT NULL
);

CREATE TABLE state_snapshots (
  id                     INTEGER PRIMARY KEY AUTOINCREMENT,
  service_id             INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  job_id                 INTEGER REFERENCES jobs(id) ON DELETE SET NULL,
  prev_repo              TEXT NOT NULL DEFAULT '',
  prev_digest            TEXT NOT NULL DEFAULT '',
  prev_image_id          TEXT NOT NULL DEFAULT '',
  prev_container_inspect TEXT NOT NULL DEFAULT '{}',
  compose_file_hash      TEXT NOT NULL DEFAULT '',
  compose_blob           TEXT,
  created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE service_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  service_id  INTEGER NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL,  -- detected|apply_started|succeeded|failed|rolled_back|dismissed
  ref_job_id  INTEGER REFERENCES jobs(id) ON DELETE SET NULL,
  from_digest TEXT NOT NULL DEFAULT '',
  to_digest   TEXT NOT NULL DEFAULT '',
  message     TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE registry_credentials (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  registry_host TEXT NOT NULL UNIQUE,
  username      TEXT NOT NULL DEFAULT '',
  secret        TEXT NOT NULL DEFAULT '',  -- AES-GCM sealed
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO hosts(name, type) VALUES ('local', 'local');

CREATE INDEX idx_services_project ON services(project_id);
CREATE INDEX idx_updates_service  ON updates(service_id);
CREATE INDEX idx_updates_status   ON updates(status);
CREATE INDEX idx_jobs_status      ON jobs(status);
CREATE INDEX idx_job_logs_job     ON job_logs(job_id);
CREATE INDEX idx_events_service   ON service_events(service_id);
