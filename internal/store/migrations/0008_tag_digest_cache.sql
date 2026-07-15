-- Permanent tag->digest cache for the floating-tag reverse version-naming scan.
-- Exact-semver release tags are immutable (a published vX.Y.Z never re-points),
-- so unlike image_remote_state this cache has no TTL: a hit is always valid.
CREATE TABLE tag_digest_cache (
  repo    TEXT NOT NULL,
  tag     TEXT NOT NULL,
  digest  TEXT NOT NULL,
  seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(repo, tag)
);
