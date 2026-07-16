-- auto_named marks a standalone project whose container name Docker assigned
-- (adjective_surname). Drives the "Loose" grouping in the UI. Recomputed each
-- discovery reconcile, so a DEFAULT of 0 for existing rows is corrected on the
-- next sync.
ALTER TABLE projects ADD COLUMN auto_named INTEGER NOT NULL DEFAULT 0;
