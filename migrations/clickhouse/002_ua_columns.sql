-- Additive migration for existing installations:
-- User-Agent parsing columns. On a fresh DB they already exist from 001.
ALTER TABLE kuztds.events ADD COLUMN IF NOT EXISTS os          LowCardinality(String) AFTER se;
ALTER TABLE kuztds.events ADD COLUMN IF NOT EXISTS os_version  LowCardinality(String) AFTER os;
ALTER TABLE kuztds.events ADD COLUMN IF NOT EXISTS browser     LowCardinality(String) AFTER os_version;
ALTER TABLE kuztds.events ADD COLUMN IF NOT EXISTS browser_ver LowCardinality(String) AFTER browser;
ALTER TABLE kuztds.events ADD COLUMN IF NOT EXISTS brand       LowCardinality(String) AFTER browser_ver;
