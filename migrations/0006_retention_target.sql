-- PR-06: remember the intended final retention state so a 'deleting' job can be
-- retried after a crash/failed transaction without guessing between released/expired.
ALTER TABLE jobs ADD COLUMN retention_target TEXT NOT NULL DEFAULT '';
