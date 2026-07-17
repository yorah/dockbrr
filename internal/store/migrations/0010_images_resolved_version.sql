-- Reverse-resolved release version for an up-to-date floating tag (latest,
-- stable). The tag name carries no semver and many images ship no version
-- label, so detection reverse-looks the running digest up to a release tag and
-- caches it here (keyed by the image's digest). Lets the dashboard show
-- "v1.13.2" for a ":latest" service that has no update pending.
ALTER TABLE images ADD COLUMN resolved_version TEXT NOT NULL DEFAULT '';
