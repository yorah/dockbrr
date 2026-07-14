CREATE TABLE changelog_repo_cache (
  image_repo  TEXT PRIMARY KEY,
  owner       TEXT NOT NULL,
  name        TEXT NOT NULL,
  resolved_at INTEGER NOT NULL
);
