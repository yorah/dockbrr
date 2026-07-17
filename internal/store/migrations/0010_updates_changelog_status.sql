-- Records why an update's changelog is empty. '' means resolved normally or
-- genuinely absent; 'rate_limited' means the GitHub Releases API throttled the
-- resolve attempt (set a GitHub token to raise the limit).
ALTER TABLE updates ADD COLUMN changelog_status TEXT NOT NULL DEFAULT '';
