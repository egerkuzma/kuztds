**English** · [Русский](ARCHITECTURE.ru.md)

# KuzTDS architecture

Up to date as of 2026-09-30. Progress snapshot — `docs/STATUS.md`.

## Four binaries
- `cmd/engine` (:8080) — the hot path (traffic handling). A long-running process.
- `cmd/admin` (:8090) — REST API + embedded SPA (`internal/admin/web`, go:embed).
- `cmd/cron` — the background service (`internal/cron`): list and geo database
  updates, VirusTotal, disk monitoring, cleanup, Telegram. No port.
- `cmd/apiclient` (:9090) — client for a landing/donor page: collects visitor
  data → calls the engine `?api=` → applies the response.

Shared packages live in `internal/`.

## Request lifecycle in the engine

```
HTTP request
  │
  ├─ postback? (?pb=KEY&cid=&profit=) → store.RecordPostback, exit
  │
  ├─ realip middleware (XFF/CF only from trusted_proxies)
  │
  ├─ api mode? (?api=base64(JSON), checks KUZTDS_API_KEY)
  │     yes → input (ip/ua/ref/lang/uniq/key/domain/cf_country/pars/id) from request
  │
  ├─ ipindex.Lookup(ip, ip_blacklist)        → blacklisted: 403
  │
  ├─ group by id/alias (first path segment, or api.id); none → trash mode
  │
  ├─ anti-flood (Redis): N requests/IP per window
  │
  ├─ detect.Parse(ua) → device/OS/browser/brand
  ├─ geo.Resolve(ip) → country/city/region/time zone + ASN/organization ;
  │     CF-IPCountry only when the request came through a trusted proxy ;
  │     the group's `geo` decides which country source wins
  ├─ ipindex.Lookup(ip, wap) → operator
  │
  ├─ uniqueness: cookie | Redis SETNX
  │
  ├─ router.Select(group, visitor)           → pick a stream by data rules
  │     type "group" → take the target group and select again (≤ 3 hops);
  │     the event records the first forwarding stream in `via`
  │
  ├─ bot detection BY THE TOGGLES OF THE SELECTED STREAM (UA/referer/PTR/empty/
  │     ipv6/ua_blacklist/SE IP lists/save_ip) → bot_redirect (or skip)
  │
  ├─ separation (key→output, in-memory list) · remote (fetch [REMOTE]; spliced in AFTER macros) · chance ·
  │     distribution ||| (random/rotator/evenly) · api_mac
  │
  ├─ render: macros + redirect type (CURL — fetch+find/replace; api — JSON);
  │  the [REMOTE] body is spliced in AFTER macro expansion and never rescanned
  │
  ├─ save_keys / keys_se (collect keywords into files)
  │
  └─ logbuf.Push(event) → async batch into ClickHouse (response doesn't wait)
```

Important: bot detection runs AFTER stream selection — by the toggles of the
specific stream; bot_redirect serves bots a separate output.

## internal/ packages
| Package | Role |
|---------|------|
| `ipindex` | CIDR index O(log n) + list manager with hot-reload |
| `config` | group/stream model (data rules) + JSON loader with aliases, atomic swap on hot-reload |
| `seplist` | separation lists ("key;out") held in memory with hot-reload |
| `geo` | Resolver: `DB` — MaxMind-format City/Country + ASN databases read into memory and swapped atomically when the files change — or `Nop`; UTC-offset helpers |
| `cron` | the background jobs, their config (JSON), status file and "run now" queue |
| `atomicfile` | write to a temp file in the same directory, then rename |
| `detect` | device + OS/browser/brand (mileusna/useragent) + bots, signatures with hot-reload |
| `router` | stream selection (predicates): lang/country/…/os/browser/brand/asn/org/timezone/get/schedule/limit; `Why` names the rule that rejected a visitor |
| `render` | output macros + all redirect types |
| `fetch` | HTTP client with an in-memory TTL cache (CURL redirect, `[REMOTE]`) |
| `store` | ClickHouse (logs/postbacks/stats) + Redis (uniq/limit/firewall/rotate/sessions) |
| `logbuf` | async event buffer → batch insert into ClickHouse |
| `security` | argon2id, tokens/sessions, CSRF, constant-time |
| `server` | realip middleware (trusted proxies) |
| `admin` | HTTP handlers, file stores (groups/.dat/keys), IP lookup, visitor simulator, cron config, embedded SPA |

## Key principles
1. **State in process memory** (IP indexes, config, signatures, geo
   databases), refreshed in the background — routing decisions read no files.
2. **Hot path free of extra blocking I/O**: logs async; counters in Redis;
   external calls (CURL/remote/PTR) with timeouts.

   Three optional features are the exception and *do* touch the filesystem on
   every request that uses them: `save_keys`/`save_keys_se` append a line
   (`appendKey`), `save_ip` appends a crawler IP (`saveBotIP`, one write per IP
   per process), and the `[RANDLINE]`/`[RANDDFL]` macros read a file or a
   directory (`render/macros.go`). Enable them knowing they trade throughput for
   the feature; everything else — including separation lists since #17 — stays
   in memory.
3. **Rules are data** (a list of predicates evaluated in a loop).
4. **Atomic index swap** on hot-reload (`atomic.Pointer`).

## Configuration
- Groups config: JSON file `KUZTDS_GROUPS_FILE` (source of truth).
- The engine keeps groups in memory and re-reads the file when it changes,
  on the same `KUZTDS_RELOAD_INTERVAL` ticker as the `.dat` lists. No restart.
- The admin reads/writes the file on requests to `/api/groups` (live). The same
  file must be given to both the engine and the admin.
- Geo databases: `KUZTDS_GEO_DB` (City or Country) and `KUZTDS_ASN_DB`, in the
  MaxMind format. They are held in memory and re-read when the files change —
  which is how a download by the cron service goes live.
- Secrets/settings — via `KUZTDS_*` environment variables (see `docs/USAGE.md`).
  The exception is the cron config, which the admin panel edits: it holds the
  Telegram token and API keys, is written `0600`, and is git-ignored.

## The cron service
`cmd/cron` shares no connection with the engine or the admin — only files:

- **config** `KUZTDS_CRON_FILE` (JSON): written by the admin (`PUT /api/cron`),
  re-read by the service on every tick (5 s), validated before anything runs;
- **status** `<config>.status.json`: written by the service after every job —
  last run, result, next run, a heartbeat — and read by the admin;
- **run now** `<config>.run`: the admin appends a job name, the service
  consumes the file on its next tick;
- **output**: `.dat` lists in `KUZTDS_DATA_DIR`, databases at `KUZTDS_GEO_DB` /
  `KUZTDS_ASN_DB`, the groups file — all of which the engine hot-reloads.

Each job runs in its own goroutine, one instance at a time, so a slow
VirusTotal pass does not delay the disk check. The schedule survives a restart
(the status file holds the next run of each job). Every download is checked
before it replaces a working file: an IP source that yields no addresses is
refused, a database is opened and its type compared with the slot it is meant
for. Writes go through a temp file and a rename.

## Storage
- **ClickHouse**: `events` (logs) + `postbacks`. Partitions by date, TTL for
  auto-cleanup (migrations in `migrations/clickhouse`). The network columns
  (`asn`, `org`, `timezone`, `via`) were added later: the engine and the admin
  run the `ALTER … ADD COLUMN IF NOT EXISTS` on start and fall back to the old
  column set if it is refused, so an upgrade needs no manual migration and
  cannot cost events.
- **Redis**: uniq / limit / firewall / rotate (evenly) / admin sessions / login
  rate-limit.

## Security (details — `docs/SECURITY.md`)
realip with trusted proxies · argon2id + server-side sessions + CSRF ·
parameterized CH queries · JSON only · secrets out of VCS · output escaping.

## Observability (planned/partial)
`slog` structured logs and `/healthz`, which carries the log-loss counters in
`X-Events-Lost` / `X-Events-Lost-Detail` (`full` = the intake channel was full,
so the accumulator could not keep up; `queue` = the batch queue was full, so the
writer could not; `insert` = storage rejected the batch; `late` = pushed during
shutdown) so a failing ClickHouse does not look like a healthy one. The same numbers are logged on
exit. `/metrics` (Prometheus) and `pprof` — in TODO.
