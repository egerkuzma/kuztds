**English** · [Русский](ARCHITECTURE.ru.md)

# KuzTDS architecture

Up to date as of 2026-06-07. Progress snapshot — `docs/STATUS.md`.

## Three binaries
- `cmd/engine` (:8080) — the hot path (traffic handling). A long-running process.
- `cmd/admin` (:8090) — REST API + embedded SPA (`internal/admin/web`, go:embed).
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
  ├─ group by id/alias (the request path, or api.id); none → trash mode
  │
  ├─ anti-flood (Redis): N requests/IP per window
  │
  ├─ detect.Parse(ua) → device/OS/browser/brand ; geo.Resolve(ip) (+CF-IPCountry)
  ├─ ipindex.Lookup(ip, wap) → operator
  │
  ├─ uniqueness: cookie | Redis SETNX
  │
  ├─ router.Select(group, visitor)           → pick a stream by data rules
  │
  ├─ bot detection BY THE TOGGLES OF THE SELECTED STREAM (UA/referer/PTR/empty/
  │     ipv6/ua_blacklist/SE IP lists/save_ip) → bot_redirect (or skip)
  │
  ├─ separation (key→output from .dat) · remote ([REMOTE], cache) · chance ·
  │     distribution ||| (random/rotator/evenly) · api_mac
  │
  ├─ render: macros + redirect type (CURL — fetch+find/replace; api — JSON)
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
| `config` | group/stream model (data rules) + JSON loader with aliases |
| `geo` | Resolver: MMDB (MaxMind) / Nop |
| `detect` | device + OS/browser/brand (mileusna/useragent) + bots, signatures with hot-reload |
| `router` | stream selection (predicates), filters lang/country/.../os/browser/brand/schedule/limit |
| `render` | output macros + all redirect types |
| `fetch` | HTTP client with an in-memory TTL cache (CURL redirect, `[REMOTE]`) |
| `store` | ClickHouse (logs/postbacks/stats) + Redis (uniq/limit/firewall/rotate/sessions) |
| `logbuf` | async event buffer → batch insert into ClickHouse |
| `security` | argon2id, tokens/sessions, CSRF, constant-time |
| `server` | realip middleware (trusted proxies) |
| `admin` | HTTP handlers, file stores (groups/.dat/keys), embedded SPA |

## Key principles
1. **State in process memory** (IP indexes, config, signatures, geo), refreshed
   in the background — routing decisions read no files.
2. **Hot path free of extra blocking I/O**: logs async; counters in Redis;
   external calls (CURL/remote/PTR) with timeouts.

   Four optional features are the exception and *do* touch the filesystem on
   every request that uses them: `separation` re-reads and scans its `.dat`
   (`separationOut`), `save_keys`/`save_keys_se` append a line (`appendKey`),
   `save_ip` appends a crawler IP (`saveBotIP`, one write per IP per process),
   and the `[RANDLINE]`/`[RANDDFL]` macros read a file or a directory
   (`render/macros.go`). Enable them knowing they trade throughput for the
   feature; everything else stays in memory.
3. **Rules are data** (a list of predicates evaluated in a loop).
4. **Atomic index swap** on hot-reload (`atomic.Pointer`).

## Configuration
- Groups config: JSON file `KUZTDS_GROUPS_FILE` (source of truth).
- The engine caches groups at startup (after editing the file — restart it).
- The admin reads/writes the file on requests to `/api/groups` (live). The same
  file must be given to both the engine and the admin.
- Secrets/settings — via `KUZTDS_*` environment variables (see `docs/USAGE.md`).

## Storage
- **ClickHouse**: `events` (logs) + `postbacks`. Partitions by date, TTL for
  auto-cleanup (migrations in `migrations/clickhouse`).
- **Redis**: uniq / limit / firewall / rotate (evenly) / admin sessions / login
  rate-limit.

## Security (details — `docs/SECURITY.md`)
realip with trusted proxies · argon2id + server-side sessions + CSRF ·
parameterized CH queries · JSON only · secrets out of VCS · output escaping.

## Observability (planned/partial)
`slog` structured logs, `/healthz`. `/metrics` (Prometheus) and `pprof` — in TODO.
