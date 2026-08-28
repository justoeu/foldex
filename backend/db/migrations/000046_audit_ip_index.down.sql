-- Reversible for real: dropping the index restores exactly the shape 000044
-- left behind. Nothing reads it as a correctness dependency — the anomaly
-- queries return the same rows without it, only slower — so this down is a
-- plan change and never a data change.
DROP INDEX IF EXISTS audit_log_ip_time_idx;
