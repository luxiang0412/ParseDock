-- PR-08: GPU admission. Workers report their free VRAM on every heartbeat so the
-- coordinator can refuse to hand a GPU job to a host with no room for the model.
-- -1 means "unknown" (no nvidia-smi, or an older worker); 0 means genuinely full.
ALTER TABLE workers ADD COLUMN free_vram_bytes INTEGER NOT NULL DEFAULT -1;
