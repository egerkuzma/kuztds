-- Table names are qualified with the kuztds database: CH init scripts run in
-- the default DB context, so without the prefix tables would land elsewhere.
CREATE DATABASE IF NOT EXISTS kuztds;

-- Single events table. Partitioning by day + TTL = auto-cleanup.
CREATE TABLE IF NOT EXISTS kuztds.events
(
    ts          DateTime       DEFAULT now(),
    group_id    LowCardinality(String),
    group_name  LowCardinality(String),
    stream_name LowCardinality(String),
    out         String,
    keyword     String,
    redirect    LowCardinality(String),
    device      LowCardinality(String),
    operator    LowCardinality(String),
    country     LowCardinality(String),
    city        String,
    region      LowCardinality(String),
    lang        LowCardinality(String),
    uniq        UInt8,
    bot         LowCardinality(String),
    ip          String,
    referer     String,
    useragent   String,
    domain      String,
    page        String,
    se          LowCardinality(String),
    os          LowCardinality(String),
    os_version  LowCardinality(String),
    browser     LowCardinality(String),
    browser_ver LowCardinality(String),
    brand       LowCardinality(String),
    counter     UInt32,
    cid         String,
    postback    String
)
ENGINE = MergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (group_id, stream_name, ts)
TTL ts + INTERVAL 21 DAY;

-- Postbacks (long-term storage).
CREATE TABLE IF NOT EXISTS kuztds.postbacks
(
    ts          DateTime DEFAULT now(),
    domain      String,
    page        String,
    device      LowCardinality(String),
    operator    LowCardinality(String),
    country     LowCardinality(String),
    city        String,
    profit      Float64,
    group_name  LowCardinality(String),
    stream_name LowCardinality(String),
    cid         String
)
ENGINE = ReplacingMergeTree(ts)   -- idempotent by cid
PARTITION BY toYYYYMM(ts)
ORDER BY (cid)
TTL ts + INTERVAL 365 DAY;
