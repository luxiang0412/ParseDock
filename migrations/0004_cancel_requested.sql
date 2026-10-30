-- PR-03: expose when a cancellation was requested, so clients and operators can
-- see the intent before the worker has stopped.
ALTER TABLE jobs ADD COLUMN cancel_requested_at TEXT;
