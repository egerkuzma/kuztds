-- What the IP says about the network and the clock: AS number, its
-- organization, and the visitor's time zone (from the MaxMind-format
-- databases, see KUZTDS_GEO_DB / KUZTDS_ASN_DB).
--
-- The engine and the admin run the same ALTER on start, so an existing
-- installation is upgraded without anyone applying this file by hand; it is
-- here so that a fresh database is complete from its first second.
ALTER TABLE kuztds.events
    ADD COLUMN IF NOT EXISTS asn UInt32,
    ADD COLUMN IF NOT EXISTS org LowCardinality(String),
    ADD COLUMN IF NOT EXISTS timezone LowCardinality(String);
