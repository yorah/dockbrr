-- tag_digest_cache.digest now stores the platform config digest (the image
-- identity used by the floating-tag reverse version-naming scan), not the
-- served manifest-list digest. The list digest differs per tag for multi-arch
-- images and never cross-matched, which is why floating-tag versions resolved
-- wrong. Wipe pre-upgrade rows so a stale served digest is never compared as a
-- config digest and cached as a permanent non-match; the table is a pure
-- rebuildable cache.
DELETE FROM tag_digest_cache;

-- Negative cache for resolveCurrentVersion: mark an image whose floating-tag
-- version has been resolved (matched a tag, fell back to a label, or
-- conclusively matched nothing) so the reverse scan is not re-run every detect
-- cycle for an unnameable digest. Defaults 0 so pre-upgrade rows re-resolve once.
ALTER TABLE images ADD COLUMN version_resolved BOOLEAN NOT NULL DEFAULT 0;
